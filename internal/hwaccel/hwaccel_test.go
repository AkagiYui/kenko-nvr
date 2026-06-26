package hwaccel

import (
	"context"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestCandidatesSoftwareLast(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		c := candidates(goos)
		if len(c) == 0 {
			t.Fatalf("%s: no candidates", goos)
		}
		if last := c[len(c)-1]; last.vcodec != "libx264" || last.Hardware {
			t.Errorf("%s: last candidate = %q (hw=%v), want software libx264", goos, last.Name, last.Hardware)
		}
		if first := c[0]; goos != "" && !first.Hardware {
			t.Errorf("%s: first candidate %q is not hardware", goos, first.Name)
		}
	}
}

func TestCandidatesUnknownOSSoftwareOnly(t *testing.T) {
	c := candidates("plan9")
	if len(c) != 1 || c[0].vcodec != "libx264" {
		t.Fatalf("unknown OS should yield software only, got %d candidates", len(c))
	}
}

func TestCandidatesDarwinVideotoolbox(t *testing.T) {
	c := candidates("darwin")
	if c[0].Name != "h264_videotoolbox" || c[0].hwaccel != "videotoolbox" {
		t.Fatalf("darwin first candidate = %+v, want videotoolbox", c[0])
	}
}

func TestOrderForcesSoftware(t *testing.T) {
	for _, o := range []string{"none", "software", "libx264", "X264"} {
		got := order("linux", o)
		if len(got) != 1 || got[0].vcodec != "libx264" {
			t.Errorf("order(linux, %q) = %d candidates, want software only", o, len(got))
		}
	}
}

func TestOrderPrioritisesNamed(t *testing.T) {
	got := order("linux", "h264_vaapi")
	if got[0].Name != "h264_vaapi" {
		t.Fatalf("named encoder not first: %q", got[0].Name)
	}
	// libx264 must still be present as a fallback.
	if !slices.ContainsFunc(got, func(e *Encoder) bool { return e.vcodec == "libx264" }) {
		t.Error("software fallback dropped when prioritising a named encoder")
	}
}

func TestOrderUnknownNameFallsBackToAuto(t *testing.T) {
	got := order("linux", "h264_madeup")
	auto := candidates("linux")
	if len(got) != len(auto) {
		t.Fatalf("unknown name should fall back to full auto list: got %d want %d", len(got), len(auto))
	}
}

func TestVideoArgsSoftware(t *testing.T) {
	e := Software()
	args := strings.Join(e.VideoArgs(2000, 50), " ")
	for _, want := range []string{"-c:v libx264", "-g 50", "-b:v 2000k", "-maxrate 3000k", "-bufsize 4000k", "-tune zerolatency"} {
		if !strings.Contains(args, want) {
			t.Errorf("VideoArgs missing %q in %q", want, args)
		}
	}
}

func TestVideoArgsOmitsRateAndGOPWhenZero(t *testing.T) {
	args := strings.Join(Software().VideoArgs(0, 0), " ")
	if strings.Contains(args, "-b:v") || strings.Contains(args, "-g ") {
		t.Errorf("VideoArgs(0,0) should omit rate/gop control, got %q", args)
	}
}

func TestVAAPIArgsShape(t *testing.T) {
	var vaapi *Encoder
	for _, e := range candidates("linux") {
		if e.Name == "h264_vaapi" {
			vaapi = e
		}
	}
	if vaapi == nil {
		t.Fatal("no vaapi candidate on linux")
	}
	// Device init precedes -i; software decode adds no -hwaccel.
	in := strings.Join(vaapi.InputArgs(false), " ")
	if !strings.Contains(in, "-vaapi_device") || strings.Contains(in, "-hwaccel") {
		t.Errorf("vaapi InputArgs(false) = %q, want device init and no -hwaccel", in)
	}
	// Hardware decode appends -hwaccel vaapi.
	if in := strings.Join(vaapi.InputArgs(true), " "); !strings.Contains(in, "-hwaccel vaapi") {
		t.Errorf("vaapi InputArgs(true) = %q, want -hwaccel vaapi", in)
	}
	// Encode path uploads frames to a VAAPI surface.
	if v := strings.Join(vaapi.VideoArgs(2000, 50), " "); !strings.Contains(v, "format=nv12,hwupload") {
		t.Errorf("vaapi VideoArgs missing hwupload filter: %q", v)
	}
}

// TestDetectSoftware exercises the real ffmpeg smoke test for software, which
// must succeed wherever ffmpeg is installed. Skipped when ffmpeg is absent.
func TestDetectSoftware(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	enc := Detect(context.Background(), "none", nil)
	if enc == nil || enc.vcodec != "libx264" {
		t.Fatalf("Detect(none) = %v, want libx264", enc)
	}
}

// TestDetectAuto checks that auto-detection returns a working encoder on this
// machine (hardware if available, software otherwise).
func TestDetectAuto(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	enc := Detect(context.Background(), "auto", nil)
	if enc == nil {
		t.Fatal("Detect(auto) returned nil with ffmpeg present")
	}
	t.Logf("auto-detected encoder on %s: %s (hardware=%v)", runtime.GOOS, enc.Name, enc.Hardware)
}
