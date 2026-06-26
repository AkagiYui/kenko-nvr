// Package motion provides lightweight motion detection over a live core.Stream.
//
// It avoids decoding video in Go (which would need CGO): a small FFmpeg process
// downsamples the stream to tiny grayscale frames, which are diffed in pure Go.
// The frame-diff analysis is isolated here so it can be unit-tested without
// FFmpeg or a camera.
package motion

import "time"

// changeRatio returns the fraction of pixels whose grayscale value changed by
// more than pixelDelta between two equally-sized frames. It is the core motion
// signal: a still scene yields ~0, movement yields a larger fraction.
func changeRatio(prev, cur []byte, pixelDelta int) float64 {
	n := len(cur)
	if len(prev) < n {
		n = len(prev)
	}
	if n == 0 {
		return 0
	}
	changed := 0
	for i := 0; i < n; i++ {
		d := int(prev[i]) - int(cur[i])
		if d < 0 {
			d = -d
		}
		if d > pixelDelta {
			changed++
		}
	}
	return float64(changed) / float64(n)
}

// sensitivityToRatios maps a 0–100 sensitivity to the start/end change-ratio
// thresholds. Higher sensitivity => lower threshold => smaller movement trips it.
func sensitivityToRatios(sensitivity int) (start, end float64) {
	if sensitivity < 0 {
		sensitivity = 0
	}
	if sensitivity > 100 {
		sensitivity = 100
	}
	const lo, hi = 0.004, 0.05 // start ratio at full / zero sensitivity
	start = lo + (1-float64(sensitivity)/100)*(hi-lo)
	end = start * 0.4
	return start, end
}

// analyzer turns a stream of per-frame change ratios into motion start/end
// events with hysteresis: motion starts when the ratio crosses startRatio and
// ends after the ratio stays below endRatio for the cooldown.
type analyzer struct {
	startRatio float64
	endRatio   float64
	cooldown   time.Duration

	active    bool
	lastAbove time.Time
	peak      float64
}

func newAnalyzer(sensitivity int, cooldown time.Duration) *analyzer {
	start, end := sensitivityToRatios(sensitivity)
	return &analyzer{startRatio: start, endRatio: end, cooldown: cooldown}
}

// update feeds one frame's change ratio at time now and reports any transition.
func (a *analyzer) update(ratio float64, now time.Time) (started, ended bool, score float64) {
	if ratio >= a.endRatio {
		a.lastAbove = now
	}
	switch {
	case !a.active:
		if ratio >= a.startRatio {
			a.active = true
			a.peak = ratio
			a.lastAbove = now
			return true, false, 0
		}
	default:
		if ratio > a.peak {
			a.peak = ratio
		}
		if now.Sub(a.lastAbove) >= a.cooldown {
			a.active = false
			score = a.peak
			a.peak = 0
			return false, true, score
		}
	}
	return false, false, 0
}

// finish reports a synthetic end event if motion was active when the stream
// stopped, so a dangling event is always closed.
func (a *analyzer) finish() (ended bool, score float64) {
	if a.active {
		a.active = false
		score = a.peak
		a.peak = 0
		return true, score
	}
	return false, 0
}
