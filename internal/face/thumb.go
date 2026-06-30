package face

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"

	"github.com/AkagiYui/kenko-nvr/internal/database"
	"github.com/AkagiYui/kenko-nvr/internal/storage"
)

// thumbMargin expands the face box by this fraction on each side, so the crop
// includes hair/chin context (nicer in the UI, and matches what a person looks
// like at a glance).
const thumbMargin = 0.35

// subImager is implemented by the concrete image types stdlib jpeg decodes to
// (*image.YCbCr, *image.RGBA), giving a zero-copy crop.
type subImager interface {
	SubImage(r image.Rectangle) image.Image
}

// cropFace crops the face box (with margin) out of a decoded frame and returns a
// JPEG. img is the already-decoded frame, shared across all faces in that frame.
func cropFace(img image.Image, b database.BBox) ([]byte, error) {
	si, ok := img.(subImager)
	if !ok {
		return nil, fmt.Errorf("image type %T does not support cropping", img)
	}
	bounds := img.Bounds()
	mx := b.W * thumbMargin
	my := b.H * thumbMargin
	x0 := clampInt(int(b.X-mx), bounds.Min.X, bounds.Max.X)
	y0 := clampInt(int(b.Y-my), bounds.Min.Y, bounds.Max.Y)
	x1 := clampInt(int(b.X+b.W+mx), bounds.Min.X, bounds.Max.X)
	y1 := clampInt(int(b.Y+b.H+my), bounds.Min.Y, bounds.Max.Y)
	if x1-x0 < 8 || y1-y0 < 8 {
		return nil, fmt.Errorf("crop too small")
	}
	crop := si.SubImage(image.Rect(x0, y0, x1, y1))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, crop, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeThumb writes a thumbnail under root at relPath, encrypting it with the
// KNV1 cipher when one is configured (biometric data at rest). The directory is
// sharded by the id prefix to keep any single directory small.
func writeThumb(root, relPath string, data []byte, cipher *storage.Cipher) error {
	abs := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	if cipher != nil {
		enc, err := cipher.EncryptAll(data)
		if err != nil {
			return err
		}
		data = enc
	}
	return os.WriteFile(abs, data, 0o644)
}

// thumbRelPath shards thumbnails by the first two id characters.
func thumbRelPath(id string) string {
	if len(id) >= 2 {
		return filepath.ToSlash(filepath.Join(id[:2], id+".jpg"))
	}
	return id + ".jpg"
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// decodeJPEG decodes a JPEG frame for cropping (nil image on failure).
func decodeJPEG(data []byte) image.Image {
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	return img
}
