// Package scoring computes fantasy points from Sleeper-keyed stat lines.
package scoring

import (
	"maps"
	"math"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

// Points returns Σ stats[k] × rules[k] over keys present in both, rounded to 2 decimals.
func Points(stats map[string]float64, rules domain.ScoringRules) float64 {
	var total float64
	for k, v := range stats {
		if w, ok := rules[k]; ok {
			total += v * w
		}
	}
	return math.Round(total*100) / 100
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
func Preset(name string) (domain.ScoringRules, bool) {
	var rec float64
	switch name {
	case "ppr":
		rec = 1
	case "half":
		rec = 0.5
	case "std":
		rec = 0
	default:
		return nil, false
	}
	r := maps.Clone(base)
	r["rec"] = rec
	return r, true
}
