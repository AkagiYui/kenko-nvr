package recording

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/mpegts"
	mpegtscodecs "github.com/bluenviron/mediacommon/v2/pkg/formats/mpegts/codecs"

	"github.com/AkagiYui/kenko-nvr/internal/core"
)

// tsClockRate is the 90 kHz clock used by MPEG-TS timestamps.
const tsClockRate = 90000

// segNamePattern is FFmpeg's -strftime output template; segTimeLayout is the
// matching Go time layout for the timestamp core (between the "seg-" prefix and
// ".mp4" suffix) used to recover each segment's wall-clock start. The literal
// parts are stripped before parsing because ".mp4" contains a bare "4" that Go's
// time layout would otherwise read as a minute.
const (
	segNamePattern = "seg-%Y%m%d-%H%M%S.mp4"
	segTimeLayout  = "20060102-150405"
)

// parseSegmentTime recovers a segment's start time from its file name.
func parseSegmentTime(name string) (time.Time, error) {
	core := strings.TrimSuffix(strings.TrimPrefix(name, "seg-"), ".mp4")
	return time.ParseInLocation(segTimeLayout, core, time.Local)
}

// TranscodeAvailable reports whether the ffmpeg binary is on PATH.
func TranscodeAvailable() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

// TranscodeRecorder records by piping the live stream to FFmpeg as MPEG-TS and
// letting FFmpeg re-encode it into fixed-duration MP4 segments. Unlike the
// pure-Go stream-copy Recorder it costs CPU, but it normalises the codec (e.g.
// to browser-friendly H.264) and produces exact, keyframe-accurate segment
// lengths (FFmpeg forces a keyframe every SegmentDur). Completed segments are
// moved into the same template layout as the copy recorder and reported through
// the same Sink, so retention and S3 upload apply unchanged.
//
// Note: because the stream is piped (not opened by FFmpeg as a real-time
// source), -segment_atclocktime cannot anchor to the wall clock, so segments are
// SegmentDur long from the first one rather than landing exactly on :00/:30. The
// pure-Go copy Recorder, which rotates on the wall clock itself, gives exact
// clock alignment; prefer it (AlignToClock) when clock-aligned files matter more
// than re-encoding.
type TranscodeRecorder struct {
	CameraID   string
	CameraName string
	Root       string
	SegmentDur time.Duration
	Template   string
	VideoCodec string // "h264" (default) or "hevc"
	CRF        int
	Preset     string
	Sink       Sink
	Log        *slog.Logger

	workDir string
}

type tsTrack struct {
	core  *core.Track
	mts   *mpegts.Track
	h264  *h264.DTSExtractor
	h265  *h265.DTSExtractor
	audio bool
}

// Run pipes the stream to FFmpeg until ctx is cancelled or the stream ends.
func (r *TranscodeRecorder) Run(ctx context.Context, stream *core.Stream) error {
	if r.SegmentDur <= 0 {
		r.SegmentDur = 10 * time.Minute
	}

	byID, mtsTracks := buildTSTracks(stream.Tracks())
	if len(mtsTracks) == 0 {
		return fmt.Errorf("no transcodable tracks")
	}

	r.workDir = filepath.Join(r.Root, ".transcode", r.CameraID)
	if err := os.MkdirAll(r.workDir, 0o755); err != nil {
		return fmt.Errorf("creating transcode work dir: %w", err)
	}

	cmd := exec.Command("ffmpeg", r.ffmpegArgs()...) //nolint:gosec // args are built from config, not user input
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting ffmpeg: %w", err)
	}

	go r.logStderr(stderr)

	// Pump the live stream into FFmpeg as MPEG-TS. Closing stdin (on ctx cancel
	// or stream end) makes FFmpeg finalise the last segment and exit cleanly.
	go func() {
		defer stdin.Close()
		w := mpegts.NewWriter(stdin, mtsTracks)
		reader := stream.AddReader(1024)
		defer reader.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case u, ok := <-reader.Units():
				if !ok {
					return
				}
				if err := writeUnit(w, byID[u.TrackID], u); err != nil {
					return // FFmpeg exited / pipe broke
				}
			}
		}
	}()

	// Backstop: if FFmpeg won't exit within 15s of stdin closing, force it.
	done := make(chan struct{})
	go func() {
		select {
		case <-done:
		case <-ctx.Done():
			select {
			case <-time.After(15 * time.Second):
				_ = cmd.Process.Kill()
			case <-done:
			}
		}
	}()

	// FFmpeg writes one CSV line per finished segment to stdout. This blocks
	// until FFmpeg closes stdout (i.e. exits).
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		r.handleSegment(sc.Text())
	}

	err = cmd.Wait()
	close(done)
	if ctx.Err() != nil {
		return nil // normal shutdown
	}
	return err
}

func (r *TranscodeRecorder) ffmpegArgs() []string {
	vcodec := "libx264"
	if strings.EqualFold(r.VideoCodec, "hevc") || strings.EqualFold(r.VideoCodec, "h265") {
		vcodec = "libx265"
	}
	preset := r.Preset
	if preset == "" {
		preset = "fast"
	}
	crf := r.CRF
	if crf <= 0 {
		crf = 23
	}
	segSec := int(r.SegmentDur / time.Second)
	if segSec <= 0 {
		segSec = 600
	}

	args := []string{
		"-hide_banner", "-loglevel", "warning",
		"-fflags", "+genpts",
		"-f", "mpegts", "-i", "pipe:0",
		"-c:v", vcodec, "-preset", preset, "-crf", strconv.Itoa(crf),
	}
	if vcodec == "libx265" {
		args = append(args, "-tag:v", "hvc1")
	}
	args = append(args,
		"-c:a", "aac", "-b:a", "128k",
		"-f", "segment",
		"-segment_time", strconv.Itoa(segSec),
		"-segment_atclocktime", "1",
		"-strftime", "1",
		"-reset_timestamps", "1",
		"-segment_format", "mp4",
		"-movflags", "+faststart",
		"-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%d)", segSec),
		"-segment_list", "pipe:1",
		"-segment_list_type", "csv",
		filepath.Join(r.workDir, segNamePattern),
	)
	return args
}

// handleSegment processes one "filename,start,end" CSV line: it moves the
// finished segment into the template layout and reports it through the Sink.
func (r *TranscodeRecorder) handleSegment(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	fields := strings.Split(line, ",")
	srcPath := fields[0]
	if !filepath.IsAbs(srcPath) {
		srcPath = filepath.Join(r.workDir, filepath.Base(srcPath))
	}
	var durMS int64
	if len(fields) >= 3 {
		start, _ := strconv.ParseFloat(fields[1], 64)
		end, _ := strconv.ParseFloat(fields[2], 64)
		if end > start {
			durMS = int64((end - start) * 1000)
		}
	}

	startTime, err := parseSegmentTime(filepath.Base(srcPath))
	if err != nil {
		startTime = time.Now()
	}

	rel := RenderPath(r.Template, r.CameraID, r.CameraName, startTime)
	dst := filepath.Join(r.Root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		r.logf("transcode mkdir failed: %v", err)
		return
	}
	if err := os.Rename(srcPath, dst); err != nil {
		r.logf("transcode move failed: %v", err)
		return
	}

	var size int64
	if info, err := os.Stat(dst); err == nil {
		size = info.Size()
	}
	id, err := r.Sink.SegmentStarted(r.CameraID, rel, startTime)
	if err != nil {
		r.logf("registering transcoded segment: %v", err)
		return
	}
	if err := r.Sink.SegmentFinalized(id, startTime.Add(time.Duration(durMS)*time.Millisecond), durMS, size); err != nil {
		r.logf("finalizing transcoded segment: %v", err)
		return
	}
	if r.Log != nil {
		r.Log.Info("recording started", "camera", r.CameraID, "path", rel, "transcoded", true)
	}
}

func (r *TranscodeRecorder) logStderr(rc io.Reader) {
	sc := bufio.NewScanner(rc)
	for sc.Scan() {
		r.logf("ffmpeg: %s", sc.Text())
	}
}

func (r *TranscodeRecorder) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log.Debug(fmt.Sprintf(format, args...), "camera", r.CameraID)
	}
}

// buildTSTracks maps core tracks to MPEG-TS tracks and an id lookup.
func buildTSTracks(tracks []*core.Track) (map[int]*tsTrack, []*mpegts.Track) {
	byID := make(map[int]*tsTrack, len(tracks))
	var mtsTracks []*mpegts.Track
	for _, t := range tracks {
		var codec mpegtscodecs.Codec
		tt := &tsTrack{core: t}
		switch t.Codec {
		case core.CodecH264:
			codec = &mpegtscodecs.H264{}
			tt.h264 = h264.NewDTSExtractor()
		case core.CodecH265:
			codec = &mpegtscodecs.H265{}
			tt.h265 = h265.NewDTSExtractor()
		case core.CodecAAC:
			if t.AudioConfig == nil {
				continue
			}
			codec = &mpegtscodecs.MPEG4Audio{Config: *t.AudioConfig}
			tt.audio = true
		default:
			continue
		}
		tt.mts = &mpegts.Track{Codec: codec}
		byID[t.ID] = tt
		mtsTracks = append(mtsTracks, tt.mts)
	}
	return byID, mtsTracks
}

func writeUnit(w *mpegts.Writer, tt *tsTrack, u *core.Unit) error {
	if tt == nil {
		return nil
	}
	pts := rescale(u.PTS, int64(tt.core.ClockRate), tsClockRate)
	switch {
	case tt.h264 != nil:
		dts, err := tt.h264.Extract(u.AUs, u.PTS)
		if err != nil {
			return nil // decode order not established yet
		}
		return w.WriteH264(tt.mts, pts, rescale(dts, int64(tt.core.ClockRate), tsClockRate), u.AUs)
	case tt.h265 != nil:
		dts, err := tt.h265.Extract(u.AUs, u.PTS)
		if err != nil {
			return nil
		}
		return w.WriteH265(tt.mts, pts, rescale(dts, int64(tt.core.ClockRate), tsClockRate), u.AUs)
	default:
		return w.WriteMPEG4Audio(tt.mts, pts, u.AUs)
	}
}

// rescale converts a timestamp between clock rates without overflowing int64.
func rescale(v, from, to int64) int64 {
	if from == to || from == 0 {
		return v
	}
	return v/from*to + (v%from)*to/from
}

func procDone(cmd *exec.Cmd) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(ch)
	}()
	return ch
}
