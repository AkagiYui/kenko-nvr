package face

import (
	"context"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/AkagiYui/kenko-nvr/internal/database"
)

// maxAttempts bounds how many times a failing job is retried before it sticks
// in the failed state.
const maxAttempts = 3

// Assigner does tracking + identity assignment for a recording's freshly
// extracted faces. It is implemented in phase 2; nil means "leave faces
// unassigned" (phase 1 behaviour).
type Assigner interface {
	AssignRecording(ctx context.Context, recordingID string, cfg database.FaceConfig) error
}

// Worker drains the face-job queue: for each finalized recording it samples
// frames, runs them through the sidecar, stores the detected faces, then (if an
// Assigner is set) groups them into identities.
type Worker struct {
	DB         *database.DB
	Root       string // recordings root dir
	FFmpegPath string // "" -> "ffmpeg"
	ConfigFn   func() database.FaceConfig
	Assigner   Assigner
	Log        *slog.Logger
}

// Run polls the queue every interval until ctx is cancelled.
func (w *Worker) Run(ctx context.Context, interval time.Duration) {
	if n, err := w.DB.FaceJobs.RequeueRunning(); err == nil && n > 0 && w.Log != nil {
		w.Log.Info("face: requeued interrupted jobs", "count", n)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.drain(ctx)
		}
	}
}

// drain processes runnable jobs until the queue is empty or analysis is disabled.
func (w *Worker) drain(ctx context.Context) {
	cfg := w.ConfigFn()
	if !cfg.Enabled || cfg.SidecarURL == "" {
		return
	}
	for {
		if ctx.Err() != nil {
			return
		}
		did, err := w.processOne(ctx, cfg)
		if err != nil {
			if w.Log != nil {
				w.Log.Error("face: worker error", "err", err)
			}
			return
		}
		if !did {
			return
		}
	}
}

// processOne claims and processes a single job. It returns whether a job was
// claimed (so the caller can keep draining).
func (w *Worker) processOne(ctx context.Context, cfg database.FaceConfig) (bool, error) {
	job, ok, err := w.DB.FaceJobs.ClaimNext(maxAttempts)
	if err != nil || !ok {
		return false, err
	}
	if err := w.process(ctx, job, cfg); err != nil {
		if w.Log != nil {
			w.Log.Warn("face: job failed", "recording", job.RecordingID, "attempt", job.Attempts, "err", err)
		}
		_ = w.DB.FaceJobs.Fail(job.ID, err.Error())
	}
	return true, nil
}

type rng struct{ startSec, durSec float64 }

// process analyses one recording end-to-end.
func (w *Worker) process(ctx context.Context, job database.FaceJob, cfg database.FaceConfig) error {
	rec, err := w.DB.Recordings.Get(job.RecordingID)
	if err != nil {
		// Recording vanished (deleted): nothing to do.
		return w.DB.FaceJobs.SetState(job.ID, database.FaceJobSkipped, "recording not found")
	}
	if rec.LocalRemoved {
		// Phase 1 reads local files only; S3-only recordings are handled later.
		return w.DB.FaceJobs.SetState(job.ID, database.FaceJobSkipped, "local file removed (S3 source not yet supported)")
	}
	path := filepath.Join(w.Root, filepath.FromSlash(rec.Path))
	if _, err := os.Stat(path); err != nil {
		return w.DB.FaceJobs.SetState(job.ID, database.FaceJobSkipped, "local file missing")
	}

	client := NewClient(cfg.SidecarURL)
	ffmpeg := w.FFmpegPath
	budget := cfg.MaxFramesPerJob
	if budget <= 0 {
		budget = 1800
	}
	batchSize := cfg.BatchSize
	if batchSize <= 0 {
		batchSize = 16
	}

	faceCount := 0
	for _, rg := range w.ranges(rec, cfg) {
		if budget <= 0 {
			break
		}
		frames, err := ExtractRange(ctx, ffmpeg, path, rg.startSec, rg.durSec, cfg.SampleFPS, budget, cfg.AnalyzeWidth)
		if err != nil {
			return err
		}
		for i := 0; i < len(frames); i += batchSize {
			end := min(i+batchSize, len(frames))
			batch := frames[i:end]
			jpegs := make([][]byte, len(batch))
			for k, fr := range batch {
				jpegs[k] = fr.JPEG
			}
			dets, model, dim, err := client.Analyze(ctx, jpegs, cfg.MinFaceSize)
			if err != nil {
				return err
			}
			n, err := w.storeBatch(rec, batch, dets, model, dim, cfg)
			if err != nil {
				return err
			}
			faceCount += n
		}
		budget -= len(frames)
	}

	// Phase 2 hook: group the recording's faces into identities.
	if w.Assigner != nil {
		if err := w.Assigner.AssignRecording(ctx, rec.ID, cfg); err != nil {
			if w.Log != nil {
				w.Log.Warn("face: assignment failed", "recording", rec.ID, "err", err)
			}
		}
	}

	if w.Log != nil {
		w.Log.Info("face: analysed recording", "recording", rec.ID, "camera", rec.CameraID, "faces", faceCount)
	}
	return w.DB.FaceJobs.Complete(job.ID, faceCount)
}

// storeBatch persists the detections of one analysed frame-batch, applying the
// detection/size/quality gates. Returns how many faces were stored.
func (w *Worker) storeBatch(rec database.Recording, batch []Frame, dets [][]Detection, model string, dim int, cfg database.FaceConfig) (int, error) {
	n := 0
	for k, frameDets := range dets {
		if k >= len(batch) {
			break
		}
		fr := batch[k]
		ts := rec.StartTime.Time.Add(time.Duration(fr.OffsetMS) * time.Millisecond)
		for _, d := range frameDets {
			if d.DetScore < cfg.DetThreshold {
				continue
			}
			fw := d.BBox[2] - d.BBox[0]
			fh := d.BBox[3] - d.BBox[1]
			if cfg.MinFaceSize > 0 && math.Min(fw, fh) < float64(cfg.MinFaceSize) {
				continue
			}
			q := detQuality(d, fw, fh)
			if cfg.MinQuality > 0 && q < cfg.MinQuality {
				continue
			}
			face := database.Face{
				RecordingID: rec.ID,
				CameraID:    rec.CameraID,
				Timestamp:   database.MS(ts),
				OffsetMS:    fr.OffsetMS,
				BBox:        database.BBox{X: d.BBox[0], Y: d.BBox[1], W: fw, H: fh},
				DetScore:    d.DetScore,
				Quality:     q,
				Embedding:   d.Embedding,
				Dim:         dim,
				Model:       model,
			}
			if err := w.DB.Faces.Create(face); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// ranges returns the (start,duration) windows of the recording to analyse. With
// motion gating on, it restricts to motion-event windows when any exist (a big
// CPU saving), else it analyses the whole file.
func (w *Worker) ranges(rec database.Recording, cfg database.FaceConfig) []rng {
	totalSec := float64(rec.DurationMS) / 1000.0
	whole := []rng{{startSec: 0, durSec: totalSec}}
	if !cfg.MotionGated || rec.StartTime.IsZero() {
		return whole
	}
	events, err := w.DB.Events.List(database.EventFilter{
		CameraIDs: []string{rec.CameraID},
		Types:     []database.EventType{database.EventMotion},
		From:      rec.StartTime.Time,
		To:        rec.EndTime.Time,
		Limit:     2000,
	})
	if err != nil || len(events) == 0 {
		return whole
	}

	const padSec = 2.0
	var ranges []rng
	for _, e := range events {
		startSec := e.StartTime.Time.Sub(rec.StartTime.Time).Seconds() - padSec
		endAbs := e.EndTime.Time
		if endAbs.IsZero() || endAbs.Before(e.StartTime.Time) {
			endAbs = e.StartTime.Time.Add(5 * time.Second)
		}
		endSec := endAbs.Sub(rec.StartTime.Time).Seconds() + padSec
		startSec = math.Max(0, startSec)
		if totalSec > 0 {
			endSec = math.Min(totalSec, endSec)
		}
		if endSec <= startSec {
			continue
		}
		ranges = append(ranges, rng{startSec: startSec, durSec: endSec - startSec})
	}
	return mergeRanges(ranges)
}

// mergeRanges coalesces overlapping/adjacent windows (assumes input ordered by
// start, which the events query guarantees).
func mergeRanges(in []rng) []rng {
	if len(in) == 0 {
		return in
	}
	out := []rng{in[0]}
	for _, r := range in[1:] {
		last := &out[len(out)-1]
		lastEnd := last.startSec + last.durSec
		if r.startSec <= lastEnd {
			if e := r.startSec + r.durSec; e > lastEnd {
				last.durSec = e - last.startSec
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

// detQuality folds detector score, face size, sharpness and head pose into a
// single [0,1] quality used to weight a face when aggregating a track and
// choosing exemplars. Detector score dominates; the rest modulate it.
func detQuality(d Detection, w, h float64) float64 {
	size := math.Min(w, h)
	sizeScore := clamp01(size / 100.0)         // saturates at ~100px
	sharpScore := clamp01(d.Sharpness / 120.0) // saturates
	poseScore := 1.0
	if len(d.Pose) >= 2 {
		yaw := math.Abs(d.Pose[0])
		pitch := math.Abs(d.Pose[1])
		poseScore = clamp01(1.0 - (yaw+pitch)/120.0)
	}
	return clamp01(d.DetScore) * (0.4 + 0.25*sizeScore + 0.2*sharpScore + 0.15*poseScore)
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
