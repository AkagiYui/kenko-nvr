package face

import (
	"math"

	"github.com/AkagiYui/kenko-nvr/internal/database"
)

// dot is the dot product of two equal-length vectors. For L2-normalised
// embeddings this equals cosine similarity.
func dot(a, b []float32) float64 {
	n := min(len(a), len(b))
	var s float64
	for i := 0; i < n; i++ {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// normalize returns an L2-normalised copy of v (a zero vector is returned as-is).
func normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		out := make([]float32, len(v))
		copy(out, v)
		return out
	}
	inv := 1.0 / math.Sqrt(sum)
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) * inv)
	}
	return out
}

// weightedMean returns the L2-normalised, weight-averaged embedding. Embeddings
// are assumed unit-norm; weighting by face quality makes a sharp, frontal,
// large face count more toward the representative than a blurry profile.
func weightedMean(embs [][]float32, weights []float64) []float32 {
	if len(embs) == 0 {
		return nil
	}
	dim := len(embs[0])
	acc := make([]float64, dim)
	for i, e := range embs {
		w := weights[i]
		if w <= 0 {
			w = 1e-3
		}
		for j := 0; j < dim && j < len(e); j++ {
			acc[j] += w * float64(e[j])
		}
	}
	out := make([]float32, dim)
	for j := range acc {
		out[j] = float32(acc[j])
	}
	return normalize(out)
}

// iou is the intersection-over-union of two boxes.
func iou(a, b database.BBox) float64 {
	ax2, ay2 := a.X+a.W, a.Y+a.H
	bx2, by2 := b.X+b.W, b.Y+b.H
	ix1, iy1 := math.Max(a.X, b.X), math.Max(a.Y, b.Y)
	ix2, iy2 := math.Min(ax2, bx2), math.Min(ay2, by2)
	iw, ih := ix2-ix1, iy2-iy1
	if iw <= 0 || ih <= 0 {
		return 0
	}
	inter := iw * ih
	union := a.W*a.H + b.W*b.H - inter
	if union <= 0 {
		return 0
	}
	return inter / union
}
