package face

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/AkagiYui/kenko-nvr/internal/database"
)

// exemplarsPerPerson caps how many representative faces a person keeps for
// matching. Multiple diverse exemplars (vs a single centroid) absorb pose and
// lighting variation; matching takes the best over them.
const exemplarsPerPerson = 8

// Gallery groups a recording's faces into tracks and assigns each track to an
// identity: a track joins the nearest existing person when their cosine
// similarity clears the match threshold, otherwise it seeds a new (anonymous)
// person. It implements face.Assigner.
type Gallery struct {
	DB  *database.DB
	Log *slog.Logger
}

// AssignRecording tracks + assigns the faces of one recording. It is idempotent:
// a recording that already has tracks is left alone (so job retries are safe).
func (g *Gallery) AssignRecording(_ context.Context, recordingID string, cfg database.FaceConfig) error {
	if n, err := g.DB.FaceTracks.CountByRecording(recordingID); err != nil {
		return err
	} else if n > 0 {
		return nil
	}

	faces, err := g.DB.Faces.List(database.FaceFilter{RecordingID: recordingID, Limit: 1_000_000})
	if err != nil {
		return err
	}
	withEmb := faces[:0]
	for _, f := range faces {
		if len(f.Embedding) > 0 {
			withEmb = append(withEmb, f)
		}
	}
	if len(withEmb) == 0 {
		return nil
	}

	tracks := BuildTracks(withEmb, DefaultTrackParams())
	gal := loadGallery(g.DB)

	for _, tr := range tracks {
		agg := aggregateTrack(tr)
		candPerson, candSim := gal.best(agg.rep)

		personID := ""
		if candSim >= cfg.MatchThreshold {
			personID = candPerson
		}
		if personID == "" {
			personID = uuid.NewString()
			if err := g.DB.Persons.Create(database.Person{
				ID:        personID,
				FirstSeen: database.MS(agg.startTS),
				LastSeen:  database.MS(agg.endTS),
			}); err != nil {
				return err
			}
		}

		if err := g.DB.FaceTracks.Create(database.FaceTrack{
			ID:           tr.ID,
			RecordingID:  recordingID,
			CameraID:     agg.best.CameraID,
			PersonID:     personID,
			StartTS:      database.MS(agg.startTS),
			EndTS:        database.MS(agg.endTS),
			FaceCount:    len(tr.Faces),
			Quality:      agg.quality,
			Embedding:    agg.rep,
			Dim:          agg.dim,
			BestFaceID:   agg.best.ID,
			BestOffsetMS: agg.best.OffsetMS,
			CandPersonID: candPerson,
			CandSim:      candSim,
		}); err != nil {
			return err
		}

		ids := make([]string, len(tr.Faces))
		for i, f := range tr.Faces {
			ids[i] = f.ID
		}
		if err := g.DB.Faces.AssignTrackFaces(ids, tr.ID, personID); err != nil {
			return err
		}
		// The track's best face becomes a gallery exemplar (capped per person), and
		// is added to the in-memory gallery so later tracks in this same recording
		// can match a person first seen moments earlier.
		if err := g.DB.Faces.SetExemplar(agg.best.ID, true); err != nil {
			return err
		}
		if err := g.DB.Faces.PruneExemplars(personID, exemplarsPerPerson); err != nil {
			return err
		}
		gal.add(personID, normalize(agg.best.Embedding))
		if _, err := g.DB.Persons.Recount(personID); err != nil {
			return err
		}
	}

	if g.Log != nil {
		g.Log.Info("face: assigned recording", "recording", recordingID, "tracks", len(tracks))
	}
	return nil
}

// trackAgg is the aggregate of a track.
type trackAgg struct {
	rep            []float32
	dim            int
	best           database.Face
	quality        float64
	startTS, endTS time.Time
}

// aggregateTrack computes the quality-weighted representative embedding, the
// sharpest ("best") face, the mean quality and the time span of a track.
func aggregateTrack(tr Track) trackAgg {
	embs := make([][]float32, len(tr.Faces))
	weights := make([]float64, len(tr.Faces))
	best := tr.Faces[0]
	var qsum float64
	startTS, endTS := tr.Faces[0].Timestamp.Time, tr.Faces[0].Timestamp.Time
	for i, f := range tr.Faces {
		embs[i] = f.Embedding
		w := f.Quality
		if w <= 0 {
			w = f.DetScore
		}
		weights[i] = w
		qsum += f.Quality
		if f.Quality > best.Quality || (f.Quality == best.Quality && f.DetScore > best.DetScore) {
			best = f
		}
		if ts := f.Timestamp.Time; ts.Before(startTS) {
			startTS = ts
		} else if ts.After(endTS) {
			endTS = ts
		}
	}
	return trackAgg{
		rep:     weightedMean(embs, weights),
		dim:     tr.Faces[0].Dim,
		best:    best,
		quality: qsum / float64(len(tr.Faces)),
		startTS: startTS,
		endTS:   endTS,
	}
}

// gallery is an in-memory snapshot of every person's exemplar embeddings, used
// for nearest-neighbour matching during a job. Embeddings are L2-normalised, so
// the score is a plain dot product (cosine).
type gallery struct {
	embs map[string][][]float32
}

func loadGallery(db *database.DB) *gallery {
	g := &gallery{embs: make(map[string][][]float32)}
	faces, err := db.Faces.List(database.FaceFilter{OnlyExemplars: true, Limit: 1_000_000})
	if err != nil {
		return g
	}
	for _, f := range faces {
		if f.PersonID == "" || len(f.Embedding) == 0 {
			continue
		}
		g.embs[f.PersonID] = append(g.embs[f.PersonID], normalize(f.Embedding))
	}
	return g
}

// best returns the person whose nearest exemplar is most similar to rep, and
// that cosine similarity. Returns ("", -1) when the gallery is empty.
func (g *gallery) best(rep []float32) (string, float64) {
	bestID, bestSim := "", -1.0
	for id, list := range g.embs {
		for _, e := range list {
			if s := dot(rep, e); s > bestSim {
				bestSim, bestID = s, id
			}
		}
	}
	return bestID, bestSim
}

func (g *gallery) add(id string, e []float32) {
	g.embs[id] = append(g.embs[id], e)
}
