// Package scoring computes fantasy points from Sleeper-keyed stat lines.
package scoring

import (
	"maps"
	"math"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

// Breakdown returns the points each stat contributes (derived stats included),
// unrounded, omitting zero contributions. A stat's weight is the position's
// replacement rule if present, otherwise the base rule.
func Breakdown(stats map[string]float64, s domain.Scoring, position string) map[string]float64 {
	all := withDerived(stats, s.Derived)
	pos := s.ByPosition[position]
	out := map[string]float64{}
	for k, v := range all {
		w, ok := pos[k]
		if !ok {
			w, ok = s.Rules[k]
		}
		if ok && v*w != 0 {
			out[k] = v * w
		}
	}
	return out
}

// Points returns the sum of Breakdown rounded to 2 decimals.
func Points(stats map[string]float64, s domain.Scoring, position string) float64 {
	var total float64
	for _, p := range Breakdown(stats, s, position) {
		total += p
	}
	return math.Round(total*100) / 100
}

// withDerived returns stats plus a 1 for every derived stat whose raw value is in range.
// The caller's map is never modified.
func withDerived(stats map[string]float64, derived []domain.DerivedStat) map[string]float64 {
	if len(derived) == 0 {
		return stats
	}
	out := make(map[string]float64, len(stats)+len(derived))
	maps.Copy(out, stats)
	for _, d := range derived {
		v, ok := stats[d.From]
		if ok && v >= d.Min && (d.Max == nil || v <= *d.Max) {
			out[d.Key] = 1
		}
	}
	return out
}

// base is standard (non-PPR) scoring in Sleeper stat keys.
var base = domain.ScoringRules{
	"pass_yd": 0.04, "pass_td": 4, "pass_int": -2, "pass_2pt": 2,
	"rush_yd": 0.1, "rush_td": 6, "rush_2pt": 2,
	"rec_yd": 0.1, "rec_td": 6, "rec_2pt": 2,
	"fum_lost": -2,
	"fgm_0_19": 3, "fgm_20_29": 3, "fgm_30_39": 3, "fgm_40_49": 4, "fgm_50p": 5,
	"fgmiss": -1, "xpm": 1, "xpmiss": -1,
	"def_td": 6, "sack": 1, "int": 2, "fum_rec": 2, "safe": 2, "blk_kick": 2,
	"pts_allow_0": 10, "pts_allow_1_6": 7, "pts_allow_7_13": 4, "pts_allow_14_20": 1,
	"pts_allow_21_27": 0, "pts_allow_28_34": -1, "pts_allow_35p": -4,
}

// Preset returns a fresh copy of the named preset: "ppr", "half", or "std".
func Preset(name string) (domain.Scoring, bool) {
	var rec float64
	switch name {
	case "ppr":
		rec = 1
	case "half":
		rec = 0.5
	case "std":
		rec = 0
	default:
		return domain.Scoring{}, false
	}
	r := maps.Clone(base)
	r["rec"] = rec
	return domain.Scoring{Rules: r}, true
}
