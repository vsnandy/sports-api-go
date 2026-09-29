// Package scoreaudit compares the scoring engine against a platform's own per-player
// points and attributes each mismatch to the platform stat ID responsible.
package scoreaudit

import (
	"cmp"
	"fmt"
	"math"
	"slices"

	"github.com/vsnandy/sports-api-go/internal/domain"
	"github.com/vsnandy/sports-api-go/internal/scoring"
)

const tolerance = 0.01

// Sample is one player-week: the platform's points and the Sleeper stat line.
type Sample struct {
	League   string
	Week     int
	Name     string
	Mapped   bool               // false when the player has no Sleeper match (excluded)
	Position string             // Sleeper position, for position-specific rules
	Stats    map[string]float64 // Sleeper stat line; nil when the player has none
	Total    float64            // platform points
	ByStat   map[int]float64    // platform points per platform stat ID
}

// StatMismatch aggregates player-weeks whose points disagree for one platform stat.
// StatID 0 collects engine points from keys with no known platform stat ID.
type StatMismatch struct {
	StatID       int
	Count        int
	Expected     float64 // platform points in the first example
	Got          float64 // engine points in the first example
	Example      string  // "Name (league wk N)"
	ExampleStats map[string]float64
}

type Report struct {
	Compared   int
	Exact      int
	Unmapped   int
	Mismatches []StatMismatch // by Count desc, then StatID
}

func (r Report) ExactRate() float64 {
	if r.Compared == 0 {
		return 0
	}
	return float64(r.Exact) / float64(r.Compared)
}

// Pass reports whether at least threshold of compared player-weeks were exact.
func (r Report) Pass(threshold float64) bool {
	return r.Compared > 0 && r.ExactRate() >= threshold
}

// Compare scores every mapped sample with its league's scoring and compares it with
// the platform total, attributing differences per platform stat ID.
func Compare(samples []Sample, scorings map[string]domain.Scoring, statID func(key string) (int, bool)) Report {
	var r Report
	byID := map[int]*StatMismatch{}
	for _, s := range samples {
		if !s.Mapped {
			r.Unmapped++
			continue
		}
		r.Compared++
		sc := scorings[s.League]
		if math.Abs(scoring.Points(s.Stats, sc, s.Position)-s.Total) < tolerance {
			r.Exact++
			continue
		}
		ours := map[int]float64{}
		for k, p := range scoring.Breakdown(s.Stats, sc, s.Position) {
			id, ok := statID(k)
			if !ok {
				id = 0
			}
			ours[id] += p
		}
		ids := map[int]bool{}
		for id := range ours {
			ids[id] = true
		}
		for id := range s.ByStat {
			ids[id] = true
		}
		for id := range ids {
			if math.Abs(ours[id]-s.ByStat[id]) < tolerance {
				continue
			}
			m, ok := byID[id]
			if !ok {
				m = &StatMismatch{
					StatID: id, Expected: s.ByStat[id], Got: ours[id],
					Example: fmt.Sprintf("%s (%s wk %d)", s.Name, s.League, s.Week), ExampleStats: s.Stats,
				}
				byID[id] = m
			}
			m.Count++
		}
	}
	for _, m := range byID {
		r.Mismatches = append(r.Mismatches, *m)
	}
	slices.SortFunc(r.Mismatches, func(a, b StatMismatch) int {
		if c := cmp.Compare(b.Count, a.Count); c != 0 {
			return c
		}
		return cmp.Compare(a.StatID, b.StatID)
	})
	return r
}
