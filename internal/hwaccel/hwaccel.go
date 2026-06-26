// Package hwaccel discovers, at startup, which FFmpeg H.264 encoder to use for
// live transcoding on the current machine — preferring a hardware encoder when
// one is actually usable, and always falling back to software libx264.
//
// Discovery is empirical, not assumed. Candidates are ordered by GOOS, then each
// is *smoke-tested* by asking FFmpeg to actually encode a few frames. The first
// candidate that exits cleanly is chosen. This catches the common trap where an
// encoder is compiled into FFmpeg but the hardware/driver is absent (e.g.
// h264_nvenc on a box with no NVIDIA GPU), which would otherwise surface only at
// the first real transcode. The deployer configures nothing: the same CGO-free
// binary adapts to macOS (VideoToolbox), Linux (NVENC/QSV/VAAPI/V4L2) and Windows
// (NVENC/QSV/AMF/MediaFoundation), and degrades to software anywhere else.
package hwaccel

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Encoder is a resolved FFmpeg encode recipe. It carries everything needed to
// build a live-transcode command: the encoder name, optional device
// initialisation and upload filter (VAAPI/QSV), and encoder-specific tuning.
// A recipe — not just a codec name — is stored because each encoder needs
// different flags (VAAPI needs a device + hwupload, NVENC needs preset/tune),
// and the very same recipe is used both to smoke-test and to run.
type Encoder struct {
	// Name is the probe/display name, e.g. "h264_videotoolbox".
	Name string
	// Hardware is true for a GPU/ASIC encoder, false for software libx264.
	Hardware bool

	vcodec   string   // FFmpeg -c:v value
	hwaccel  string   // decode -hwaccel value ("" = software decode)
	initArgs []string // device-init args placed before -i (VAAPI/QSV)
	filter   string   // -vf value (e.g. VAAPI "format=nv12,hwupload"), "" if none
	tune     []string // encoder-specific tuning placed right after -c:v
}

// VideoCodec returns the FFmpeg encoder name (-c:v value).
func (e *Encoder) VideoCodec() string { return e.vcodec }

// InputArgs returns the args that must precede -i: any device initialisation
// and, when hwDecode is true and the encoder has a matching decode accelerator,
// the -hwaccel flag. Live transcoding decodes in software by default (hwDecode
// false) because software decode + hardware encode is the most portable pairing
// and avoids fragile zero-copy surface-format plumbing; the expensive half —
// encoding — still runs on the GPU.
func (e *Encoder) InputArgs(hwDecode bool) []string {
	out := append([]string(nil), e.initArgs...)
	if hwDecode && e.hwaccel != "" {
		out = append(out, "-hwaccel", e.hwaccel)
	}
	return out
}

// VideoArgs returns the encode args for the given target bitrate (kbit/s) and
// GOP length (frames): the upload/format filter (if any), -c:v, encoder tuning
// and rate/GOP control. A bitrate <= 0 omits rate control; a gop <= 0 omits -g.
func (e *Encoder) VideoArgs(bitrateKbps, gop int) []string {
	var a []string
	if e.filter != "" {
		a = append(a, "-vf", e.filter)
	}
	a = append(a, "-c:v", e.vcodec)
	a = append(a, e.tune...)
	if gop > 0 {
		a = append(a, "-g", strconv.Itoa(gop))
	}
	if bitrateKbps > 0 {
		a = append(a,
			"-b:v", fmt.Sprintf("%dk", bitrateKbps),
			"-maxrate", fmt.Sprintf("%dk", bitrateKbps*3/2),
			"-bufsize", fmt.Sprintf("%dk", bitrateKbps*2),
		)
	}
	return a
}

// Software returns the always-available software (libx264) encoder — the
// terminal fallback when no hardware encoder works.
func Software() *Encoder {
	return &Encoder{
		Name:   "libx264",
		vcodec: "libx264",
		tune:   []string{"-preset", "veryfast", "-tune", "zerolatency", "-pix_fmt", "yuv420p"},
	}
}

// candidates returns the ordered encode recipes to try for an OS, hardware
// first and software (libx264) always last.
func candidates(goos string) []*Encoder {
	switch goos {
	case "darwin":
		return []*Encoder{
			{Name: "h264_videotoolbox", Hardware: true, vcodec: "h264_videotoolbox",
				hwaccel: "videotoolbox", tune: []string{"-realtime", "1", "-allow_sw", "1"}},
			Software(),
		}
	case "linux":
		return []*Encoder{
			{Name: "h264_nvenc", Hardware: true, vcodec: "h264_nvenc",
				hwaccel: "cuda", tune: []string{"-preset", "p4", "-tune", "ll"}},
			{Name: "h264_qsv", Hardware: true, vcodec: "h264_qsv",
				hwaccel: "qsv", tune: []string{"-preset", "veryfast"}},
			{Name: "h264_vaapi", Hardware: true, vcodec: "h264_vaapi",
				hwaccel: "vaapi", initArgs: []string{"-vaapi_device", "/dev/dri/renderD128"},
				filter: "format=nv12,hwupload"},
			{Name: "h264_v4l2m2m", Hardware: true, vcodec: "h264_v4l2m2m"},
			Software(),
		}
	case "windows":
		return []*Encoder{
			{Name: "h264_nvenc", Hardware: true, vcodec: "h264_nvenc",
				hwaccel: "cuda", tune: []string{"-preset", "p4", "-tune", "ll"}},
			{Name: "h264_qsv", Hardware: true, vcodec: "h264_qsv",
				hwaccel: "qsv", tune: []string{"-preset", "veryfast"}},
			{Name: "h264_amf", Hardware: true, vcodec: "h264_amf",
				tune: []string{"-usage", "lowlatency"}},
			{Name: "h264_mf", Hardware: true, vcodec: "h264_mf"},
			Software(),
		}
	default:
		return []*Encoder{Software()}
	}
}

// Detect probes the machine and returns the encoder to use for live transcode.
//
// override selects behaviour: "" or "auto" probes hardware then software; "none",
// "software" or "libx264" forces software; any other value names a specific
// encoder, which is tried first (with software kept as the fallback). It returns
// nil only when FFmpeg is not installed at all (live transcode then disabled).
func Detect(ctx context.Context, override string, log *slog.Logger) *Encoder {
	if !available() {
		if log != nil {
			log.Warn("ffmpeg not found on PATH; live transcode of non-H.264 cameras disabled")
		}
		return nil
	}

	cands := order(runtime.GOOS, override)
	for _, e := range cands {
		if err := smokeTest(ctx, e); err != nil {
			if log != nil {
				log.Debug("hwaccel candidate rejected", "encoder", e.Name, "err", err)
			}
			continue
		}
		if log != nil {
			log.Info("live transcode encoder selected", "encoder", e.Name, "hardware", e.Hardware)
		}
		return e
	}
	// Should be unreachable: software libx264 normally passes. Guard anyway.
	if log != nil {
		log.Warn("no working ffmpeg encoder found, including software; live transcode disabled")
	}
	return nil
}

// order resolves the override into the ordered candidate list to probe.
func order(goos, override string) []*Encoder {
	switch o := strings.ToLower(strings.TrimSpace(override)); o {
	case "", "auto":
		return candidates(goos)
	case "none", "software", "libx264", "x264":
		return []*Encoder{Software()}
	default:
		all := candidates(goos)
		var named *Encoder
		rest := make([]*Encoder, 0, len(all))
		for _, e := range all {
			if e.Name == o || e.vcodec == o {
				named = e
				continue
			}
			rest = append(rest, e)
		}
		if named == nil {
			// Unknown name: fall back to auto rather than failing hard.
			return all
		}
		return append([]*Encoder{named}, rest...)
	}
}

// smokeTest verifies an encoder by actually encoding a few generated frames to
// the null muxer. It uses the same device-init, filter and tuning as a real run
// (only the input differs: a synthetic source in system memory, which mirrors
// software-decode + hardware-encode), so a pass means the encode path works on
// this machine right now.
func smokeTest(parent context.Context, e *Encoder) error {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()

	args := []string{"-hide_banner", "-v", "error", "-nostdin"}
	args = append(args, e.initArgs...)
	args = append(args, "-f", "lavfi", "-i", "color=c=black:s=320x240:r=15")
	if e.filter != "" {
		args = append(args, "-vf", e.filter)
	}
	args = append(args, "-c:v", e.vcodec)
	args = append(args, e.tune...)
	args = append(args, "-b:v", "300k", "-frames:v", "5", "-f", "null", "-")

	cmd := exec.CommandContext(ctx, "ffmpeg", args...) //nolint:gosec // args are static, not user input
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, lastLine(stderr.Bytes()))
	}
	return nil
}

// available reports whether the ffmpeg binary is on PATH.
func available() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

func lastLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(s)
}
