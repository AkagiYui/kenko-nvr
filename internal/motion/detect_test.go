package motion

import (
	"testing"
	"time"
)

func TestChangeRatio(t *testing.T) {
	const n = 100
	a := make([]byte, n)
	b := make([]byte, n)
	for i := range a {
		a[i] = 100
		b[i] = 100
	}
	if r := changeRatio(a, b, 18); r != 0 {
		t.Errorf("identical frames: ratio = %v, want 0", r)
	}

	// Change half the pixels well beyond the threshold.
	for i := 0; i < n/2; i++ {
		b[i] = 200
	}
	if r := changeRatio(a, b, 18); r < 0.49 || r > 0.51 {
		t.Errorf("half-changed frames: ratio = %v, want ~0.5", r)
	}

	// Sub-threshold changes don't count.
	for i := range b {
		b[i] = a[i] + 5
	}
	if r := changeRatio(a, b, 18); r != 0 {
		t.Errorf("sub-threshold change: ratio = %v, want 0", r)
	}
}

func TestSensitivityToRatios(t *testing.T) {
	lowStart, lowEnd := sensitivityToRatios(0)
	highStart, highEnd := sensitivityToRatios(100)
	if !(highStart < lowStart) {
		t.Errorf("higher sensitivity should lower start threshold: low=%v high=%v", lowStart, highStart)
	}
	if lowEnd >= lowStart || highEnd >= highStart {
		t.Errorf("end ratio should be below start ratio")
	}
	// Clamping out-of-range values.
	if s, _ := sensitivityToRatios(999); s != highStart {
		t.Errorf("sensitivity should clamp to 100")
	}
}

func TestAnalyzerHysteresis(t *testing.T) {
	an := newAnalyzer(50, 2*time.Second)
	start := an.startRatio
	t0 := time.Unix(1_700_000_000, 0)

	// Below threshold: nothing happens.
	if s, e, _ := an.update(start/2, t0); s || e {
		t.Fatal("should not start below threshold")
	}

	// Cross the threshold: motion starts.
	if s, e, _ := an.update(start*1.5, t0.Add(time.Second)); !s || e {
		t.Fatalf("should start on threshold crossing (s=%v e=%v)", s, e)
	}

	// Still active, no new start, no end yet.
	if s, e, _ := an.update(start*2, t0.Add(2*time.Second)); s || e {
		t.Fatal("should remain active without re-firing start")
	}

	// Quiet, but within cooldown: no end.
	if _, e, _ := an.update(0, t0.Add(3*time.Second)); e {
		t.Fatal("should not end before cooldown elapses")
	}

	// Quiet past cooldown: motion ends, score is the observed peak.
	_, ended, score := an.update(0, t0.Add(6*time.Second))
	if !ended {
		t.Fatal("should end after cooldown")
	}
	if score < start*2-1e-9 {
		t.Errorf("end score should reflect peak ~%v, got %v", start*2, score)
	}

	// finish() on an inactive analyzer reports nothing.
	if e, _ := an.finish(); e {
		t.Error("finish on inactive analyzer should be a no-op")
	}
}

func TestAnalyzerFinishClosesActive(t *testing.T) {
	an := newAnalyzer(50, time.Second)
	t0 := time.Unix(1_700_000_000, 0)
	an.update(an.startRatio*2, t0)
	if ended, score := an.finish(); !ended || score <= 0 {
		t.Errorf("finish should close an active event with a positive score, got ended=%v score=%v", ended, score)
	}
}
