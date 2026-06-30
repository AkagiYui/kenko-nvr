package face

import (
	"sort"

	"github.com/google/uuid"

	"github.com/AkagiYui/kenko-nvr/internal/database"
)

// Track is a set of faces linked as one appearance within a recording.
type Track struct {
	ID    string
	Faces []database.Face
}

// TrackParams tunes the greedy tracker.
type TrackParams struct {
	// MaxGapMS is how long a track may go unseen before it is closed (sampling is
	// sparse, so this is generous).
	MaxGapMS int64
	// A detection links to a track when their embeddings are similar enough
	// (MinCos) OR their boxes overlap enough (MinIoU); ranking uses a blend.
	MinCos float64
	MinIoU float64
}

// DefaultTrackParams returns sensible defaults for ~2-4 fps sampling.
func DefaultTrackParams() TrackParams {
	return TrackParams{MaxGapMS: 4000, MinCos: 0.45, MinIoU: 0.30}
}

type liveTrack struct {
	id      string
	faces   []database.Face
	lastOff int64
	lastBox database.BBox
	sum     []float32 // running embedding sum (normalize for matching)
}

func (lt *liveTrack) matchRep() []float32 { return normalize(lt.sum) }

func (lt *liveTrack) add(f database.Face) {
	if lt.sum == nil {
		lt.sum = make([]float32, len(f.Embedding))
	}
	ne := normalize(f.Embedding)
	for i := range lt.sum {
		if i < len(ne) {
			lt.sum[i] += ne[i]
		}
	}
	lt.faces = append(lt.faces, f)
	lt.lastOff = f.OffsetMS
	lt.lastBox = f.BBox
}

// BuildTracks groups a recording's faces into tracks. It walks frames in time
// order and greedily links each frame's detections to the most similar still-open
// track (by embedding cosine blended with box IoU), gating on a similarity floor
// so unrelated faces start new tracks. Detections in the same frame can never
// join the same track.
func BuildTracks(faces []database.Face, p TrackParams) []Track {
	if len(faces) == 0 {
		return nil
	}
	// Order by in-file offset, then a stable tiebreak.
	sorted := make([]database.Face, len(faces))
	copy(sorted, faces)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].OffsetMS != sorted[j].OffsetMS {
			return sorted[i].OffsetMS < sorted[j].OffsetMS
		}
		return sorted[i].ID < sorted[j].ID
	})

	// Group into frames sharing the same sampled offset.
	var frames [][]database.Face
	for i := 0; i < len(sorted); {
		j := i
		for j < len(sorted) && sorted[j].OffsetMS == sorted[i].OffsetMS {
			j++
		}
		frames = append(frames, sorted[i:j])
		i = j
	}

	var open []*liveTrack
	var done []*liveTrack

	for _, frame := range frames {
		off := frame[0].OffsetMS
		// Close tracks idle beyond the gap.
		kept := open[:0]
		for _, lt := range open {
			if off-lt.lastOff > p.MaxGapMS {
				done = append(done, lt)
			} else {
				kept = append(kept, lt)
			}
		}
		open = kept

		// Candidate (detection, track) pairs above the gate, best first.
		type cand struct {
			di, ti int
			score  float64
		}
		var cands []cand
		for di, f := range frame {
			rep := normalize(f.Embedding)
			for ti, lt := range open {
				cos := dot(rep, lt.matchRep())
				ov := iou(f.BBox, lt.lastBox)
				if cos < p.MinCos && ov < p.MinIoU {
					continue
				}
				cands = append(cands, cand{di, ti, 0.6*cos + 0.4*ov})
			}
		}
		sort.Slice(cands, func(a, b int) bool { return cands[a].score > cands[b].score })

		usedDet := make([]bool, len(frame))
		usedTrk := make([]bool, len(open))
		for _, c := range cands {
			if usedDet[c.di] || usedTrk[c.ti] {
				continue
			}
			usedDet[c.di] = true
			usedTrk[c.ti] = true
			open[c.ti].add(frame[c.di])
		}
		// Unmatched detections open new tracks.
		for di, f := range frame {
			if usedDet[di] {
				continue
			}
			lt := &liveTrack{id: uuid.NewString()}
			lt.add(f)
			open = append(open, lt)
		}
	}

	all := append(done, open...)
	out := make([]Track, 0, len(all))
	for _, lt := range all {
		out = append(out, Track{ID: lt.id, Faces: lt.faces})
	}
	return out
}
