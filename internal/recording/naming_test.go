package recording

import (
	"strings"
	"testing"
	"time"
)

func TestRenderPathTokens(t *testing.T) {
	tm := time.Date(2026, 6, 22, 4, 5, 6, 0, time.UTC)
	got := RenderPath("{camera}/{year}-{month}-{day}/{camera}_{hour}{minute}{second}.mp4", "id1", "Front Door", tm)
	want := "Front_Door/2026-06-22/Front_Door_040506.mp4"
	if got != want {
		t.Errorf("RenderPath = %q, want %q", got, want)
	}
}

func TestRenderPathDefaultsExtension(t *testing.T) {
	tm := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	got := RenderPath("{camera}/{unix}", "id", "cam", tm)
	if !strings.HasSuffix(got, ".mp4") {
		t.Errorf("expected .mp4 suffix, got %q", got)
	}
}

func TestRenderPathPreventsTraversal(t *testing.T) {
	tm := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	got := RenderPath("../../etc/{camera}.mp4", "id", "cam", tm)
	if strings.Contains(got, "..") || strings.HasPrefix(got, "/") {
		t.Errorf("path escaped root: %q", got)
	}
}

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"Front Door": "Front_Door",
		// path separators are stripped; remaining dots are harmless in a
		// single filename component (RenderPath also Cleans + confines).
		"cam/../etc":   "cam..etc",
		"..":           "unnamed",
		"":             "unnamed",
		"a*b?c:d":      "abcd",
		"日本語":          "unnamed",
		"valid-name_1": "valid-name_1",
	}
	for in, want := range cases {
		if got := sanitize(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}
