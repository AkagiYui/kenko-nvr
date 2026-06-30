package face

import (
	"bufio"
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
	"time"
)

func makeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 90, A: 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestSplitMJPEG(t *testing.T) {
	a := makeJPEG(t, 40, 40)
	b := makeJPEG(t, 56, 24)
	stream := append(append([]byte{}, a...), b...)

	sc := bufio.NewScanner(bytes.NewReader(stream))
	sc.Buffer(make([]byte, 0, 1024), 1<<20)
	sc.Split(splitMJPEG)
	var frames [][]byte
	for sc.Scan() {
		frames = append(frames, append([]byte(nil), sc.Bytes()...))
	}
	if len(frames) != 2 {
		t.Fatalf("want 2 frames, got %d", len(frames))
	}
	for i, f := range frames {
		if _, err := jpeg.Decode(bytes.NewReader(f)); err != nil {
			t.Fatalf("frame %d not a valid jpeg: %v", i, err)
		}
		if f[len(f)-2] != 0xFF || f[len(f)-1] != 0xD9 {
			t.Errorf("frame %d does not end at EOI", i)
		}
	}
}

func TestPresenceDebounce(t *testing.T) {
	p := &presence{cooldown: 2 * time.Second}
	t0 := time.Unix(1000, 0)

	if s, e, _ := p.update(true, 0.9, t0); !s || e {
		t.Fatal("first face should start an event")
	}
	if s, e, _ := p.update(true, 0.8, t0.Add(time.Second)); s || e {
		t.Fatal("continued presence should not re-fire")
	}
	// Face gone but still within cooldown of the last sighting (t0+1s).
	if s, e, _ := p.update(false, 0, t0.Add(2*time.Second)); s || e {
		t.Fatal("within cooldown should not end")
	}
	// Cooldown elapsed since last sighting -> end, reporting the best score.
	s, e, sc := p.update(false, 0, t0.Add(4*time.Second))
	if s || !e || sc != 0.9 {
		t.Fatalf("should end with best score 0.9: started=%v ended=%v score=%v", s, e, sc)
	}
}
