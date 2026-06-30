package face

import (
	"context"
	"log/slog"
	"math/rand"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/AkagiYui/kenko-nvr/internal/database"
)

// clusterIterations is how many label-propagation rounds Chinese Whispers runs
// (it converges quickly; the loop also early-exits when stable).
const clusterIterations = 30

// Clusterer periodically re-groups tracks into identities with Chinese Whispers,
// cleaning up the over-splitting that incremental assignment can leave behind. It
// honours operator decisions: confirmed tracks are anchors that never move, and
// must/cannot links constrain the graph.
type Clusterer struct {
	DB  *database.DB
	Log *slog.Logger
}

// ReclusterResult summarises a pass.
type ReclusterResult struct {
	Tracks         int `json:"tracks"`
	Moved          int `json:"moved"`
	PersonsDeleted int `json:"personsDeleted"`
	PersonsAfter   int `json:"personsAfter"`
}

// Recluster runs one global re-clustering pass and applies the result.
func (c *Clusterer) Recluster(_ context.Context, cfg database.FaceConfig) (ReclusterResult, error) {
	var res ReclusterResult
	tracks, err := c.DB.FaceTracks.List(database.FaceTrackFilter{Limit: 1_000_000})
	if err != nil {
		return res, err
	}
	// Keep only tracks with a usable embedding.
	ts := tracks[:0]
	for _, t := range tracks {
		if len(t.Embedding) > 0 {
			ts = append(ts, t)
		}
	}
	tracks = ts
	res.Tracks = len(tracks)
	if len(tracks) < 2 {
		return res, nil
	}

	reps := make([][]float32, len(tracks))
	idx := make(map[string]int, len(tracks))
	for i, t := range tracks {
		reps[i] = normalize(t.Embedding)
		idx[t.ID] = i
	}

	// Constraints.
	links, _ := c.DB.PersonLinks.List()
	type pair struct{ a, b int }
	cannot := map[pair]bool{}
	var musts []pair
	for _, l := range links {
		ai, aok := idx[l.ATrack]
		bi, bok := idx[l.BTrack]
		if !aok || !bok || ai == bi {
			continue
		}
		if ai > bi {
			ai, bi = bi, ai
		}
		switch l.Kind {
		case database.LinkCannot:
			cannot[pair{ai, bi}] = true
		case database.LinkMust:
			musts = append(musts, pair{ai, bi})
		}
	}

	edgeThreshold := cfg.MatchThreshold
	if edgeThreshold <= 0 {
		edgeThreshold = 0.45
	}

	// Sparse weighted graph.
	type edge struct {
		to int
		w  float64
	}
	adj := make([][]edge, len(tracks))
	addEdge := func(a, b int, w float64) {
		adj[a] = append(adj[a], edge{b, w})
		adj[b] = append(adj[b], edge{a, w})
	}
	for i := 0; i < len(tracks); i++ {
		for j := i + 1; j < len(tracks); j++ {
			if cannot[pair{i, j}] {
				continue
			}
			cos := dot(reps[i], reps[j])
			if cos < edgeThreshold {
				continue
			}
			addEdge(i, j, cos)
		}
	}
	for _, m := range musts {
		addEdge(m.a, m.b, 1.0) // strong tie
	}

	// Labels: confirmed tracks are anchored to their person; others start in
	// their own singleton cluster.
	labels := make([]string, len(tracks))
	anchored := make([]bool, len(tracks))
	for i, t := range tracks {
		if t.Confirmed && t.PersonID != "" {
			labels[i] = "P:" + t.PersonID
			anchored[i] = true
		} else {
			labels[i] = "T:" + t.ID
		}
	}

	// Chinese Whispers: each non-anchored node adopts the highest-weighted label
	// among its neighbours, iterating to convergence. A seeded RNG keeps the order
	// (and thus the result) reproducible.
	rng := rand.New(rand.NewSource(1))
	order := rng.Perm(len(tracks))
	for it := 0; it < clusterIterations; it++ {
		rng.Shuffle(len(order), func(a, b int) { order[a], order[b] = order[b], order[a] })
		changed := false
		for _, i := range order {
			if anchored[i] || len(adj[i]) == 0 {
				continue
			}
			score := map[string]float64{}
			for _, e := range adj[i] {
				score[labels[e.to]] += e.w
			}
			best, bestW := labels[i], score[labels[i]]
			// Deterministic tie-break by label string.
			keys := make([]string, 0, len(score))
			for k := range score {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if score[k] > bestW {
					bestW, best = score[k], k
				}
			}
			if best != labels[i] {
				labels[i] = best
				changed = true
			}
		}
		if !changed {
			break
		}
	}

	// Map final labels to persons and apply, touching only unconfirmed tracks.
	groups := map[string][]int{}
	for i := range tracks {
		if !anchored[i] {
			groups[labels[i]] = append(groups[labels[i]], i)
		}
	}
	affected := map[string]bool{}
	for label, members := range groups {
		var target string
		if strings.HasPrefix(label, "P:") {
			target = label[2:] // joined a confirmed person's component
		} else {
			target = c.canonicalPerson(tracks, members)
		}
		for _, i := range members {
			t := tracks[i]
			if t.PersonID == target {
				continue
			}
			if err := c.DB.FaceTracks.UpdatePerson(t.ID, target); err != nil {
				return res, err
			}
			if err := c.DB.Faces.SetPersonByTrack(t.ID, target); err != nil {
				return res, err
			}
			affected[t.PersonID] = true
			affected[target] = true
			res.Moved++
		}
	}

	// Recount affected persons; rebuild their exemplars; delete those left empty.
	for p := range affected {
		if p == "" {
			continue
		}
		n, err := c.DB.Persons.Recount(p)
		if err != nil {
			return res, err
		}
		if n == 0 {
			if err := c.DB.Persons.Delete(p); err == nil {
				res.PersonsDeleted++
			}
			continue
		}
		if err := c.DB.Faces.RebuildExemplars(p, exemplarsPerPerson); err != nil {
			return res, err
		}
	}

	if persons, err := c.DB.Persons.List(database.PersonFilter{Limit: 1_000_000}); err == nil {
		res.PersonsAfter = len(persons)
	}
	if c.Log != nil {
		c.Log.Info("face: reclustered", "tracks", res.Tracks, "moved", res.Moved,
			"personsDeleted", res.PersonsDeleted, "personsAfter", res.PersonsAfter)
	}
	return res, nil
}

// canonicalPerson picks the person a cluster of unconfirmed tracks should
// collapse into: the existing person carrying the most faces among the members
// (deterministic tie-break), or a fresh person if none have one.
func (c *Clusterer) canonicalPerson(tracks []database.FaceTrack, members []int) string {
	weight := map[string]int{}
	for _, i := range members {
		if p := tracks[i].PersonID; p != "" {
			weight[p] += tracks[i].FaceCount + 1
		}
	}
	keys := make([]string, 0, len(weight))
	for k := range weight {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	best, bw := "", -1
	for _, k := range keys {
		if weight[k] > bw {
			bw, best = weight[k], k
		}
	}
	if best == "" {
		best = uuid.NewString()
		_ = c.DB.Persons.Create(database.Person{ID: best})
	}
	return best
}
