package face

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"github.com/AkagiYui/kenko-nvr/internal/database"
	"github.com/AkagiYui/kenko-nvr/internal/storage"
)

func TestThumbCropEncryptRoundTrip(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 120, 120))
	for y := 0; y < 120; y++ {
		for x := 0; x < 120; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 2), G: uint8(y * 2), B: 128, A: 255})
		}
	}

	jb, err := cropFace(img, database.BBox{X: 20, Y: 20, W: 50, H: 50})
	if err != nil {
		t.Fatalf("crop: %v", err)
	}
	if ci, err := jpeg.Decode(bytes.NewReader(jb)); err != nil || ci.Bounds().Dx() <= 0 {
		t.Fatalf("crop is not a valid jpeg: %v", err)
	}

	// Encrypted round-trip.
	key := make([]byte, 32)
	cipher, err := storage.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	rel := thumbRelPath("abcd1234")
	if err := writeThumb(dir, rel, jb, cipher); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	if !storage.IsEncryptedHeader(raw) {
		t.Fatal("encrypted thumb missing KNV1 header")
	}
	dec, err := cipher.DecryptAll(raw)
	if err != nil || !bytes.Equal(dec, jb) {
		t.Fatalf("decrypt mismatch: err=%v equal=%v", err, bytes.Equal(dec, jb))
	}

	// Plaintext path.
	rel2 := thumbRelPath("ef009999")
	if err := writeThumb(dir, rel2, jb, nil); err != nil {
		t.Fatal(err)
	}
	raw2, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel2)))
	if storage.IsEncryptedHeader(raw2) || !bytes.Equal(raw2, jb) {
		t.Fatal("plaintext thumb should be stored as-is")
	}
}
