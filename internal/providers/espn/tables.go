package espn

import (
	"fmt"
	"slices"
)

// Lineup slot IDs → Sleeper-style slot names.
var slotNames = map[int]string{
	0: "QB", 2: "RB", 3: "WRRB_FLEX", 4: "WR", 5: "REC_FLEX", 6: "TE", 7: "SUPER_FLEX",
	16: "DEF", 17: "K", 20: "BN", 21: "IR", 23: "FLEX",
}

// slotOrder is the display order for slots; unknown slots sort after these.
var slotOrder = []int{0, 2, 3, 4, 5, 6, 23, 7, 16, 17, 20, 21}

func slotName(id int) string {
	if n, ok := slotNames[id]; ok {
		return n
	}
	return fmt.Sprintf("SLOT_%d", id)
}

func slotRank(id int) int {
	if i := slices.Index(slotOrder, id); i >= 0 {
		return i
	}
	return 100 + id
}

var positions = map[int]string{1: "QB", 2: "RB", 3: "WR", 4: "TE", 5: "K", 16: "DEF"}

// proTeams maps ESPN pro team IDs to Sleeper team abbreviations. 0 (free agent) is absent.
var proTeams = map[int]string{
	1: "ATL", 2: "BUF", 3: "CHI", 4: "CIN", 5: "CLE", 6: "DAL", 7: "DEN", 8: "DET",
	9: "GB", 10: "TEN", 11: "IND", 12: "KC", 13: "LV", 14: "LAR", 15: "MIA", 16: "MIN",
	17: "NE", 18: "NO", 19: "NYG", 20: "NYJ", 21: "PHI", 22: "ARI", 23: "PIT", 24: "LAC",
	25: "SF", 26: "SEA", 27: "TB", 28: "WAS", 29: "CAR", 30: "JAX", 33: "BAL", 34: "HOU",
}

// statKeys maps ESPN scoring stat IDs to Sleeper stat keys, following the community
// mapping in cwendt94/espn-api (PLAYER_STATS_MAP). IDs not listed here surface as
// unsupported rules rather than guesses. ESPN's "FG under 40" (80) covers three Sleeper keys.
var statKeys = map[int][]string{
	3: {"pass_yd"}, 4: {"pass_td"}, 19: {"pass_2pt"}, 20: {"pass_int"},
	24: {"rush_yd"}, 25: {"rush_td"}, 26: {"rush_2pt"},
	42: {"rec_yd"}, 43: {"rec_td"}, 44: {"rec_2pt"}, 53: {"rec"},
	72: {"fum_lost"},
	74: {"fgm_50p"}, 77: {"fgm_40_49"}, 79: {"fgmiss_40_49"}, 80: {"fgm_0_19", "fgm_20_29", "fgm_30_39"},
	85: {"fgmiss"}, 86: {"xpm"}, 88: {"xpmiss"},
	95: {"int"}, 96: {"fum_rec", "def_st_fum_rec"}, 97: {"blk_kick"}, 98: {"safe"}, 99: {"sack"},
	// 101/102 (kick/punt return TD) apply to every player: individual returners carry
	// Sleeper's st_td, team D/ST lines carry def_st_td (never both on the same line).
	101: {"def_st_td", "st_td"}, 102: {"def_st_td", "st_td"},
	103: {"def_td"}, 104: {"def_td"},
	120: {"pts_allow"}, 127: {"yds_allow"},
	198: {"fgm_50_59"},
	201: {"fgm_60p"},
}

// tier is an ESPN stat that scores a range of a raw stat, as a derived indicator.
type tier struct {
	key  string
	from string
	min  float64
	max  *float64 // nil = unbounded
}

func upTo(v float64) *float64 { return &v }

// tiers maps ESPN points-allowed and yards-allowed stat IDs to inclusive ranges of
// Sleeper's raw pts_allow / yds_allow.
var tiers = map[int]tier{
	89:  {"espn_pa_0", "pts_allow", 0, upTo(0)},
	90:  {"espn_pa_1_6", "pts_allow", 1, upTo(6)},
	91:  {"espn_pa_7_13", "pts_allow", 7, upTo(13)},
	92:  {"espn_pa_14_17", "pts_allow", 14, upTo(17)},
	121: {"espn_pa_18_21", "pts_allow", 18, upTo(21)},
	122: {"espn_pa_22_27", "pts_allow", 22, upTo(27)},
	123: {"espn_pa_28_34", "pts_allow", 28, upTo(34)},
	124: {"espn_pa_35_45", "pts_allow", 35, upTo(45)},
	125: {"espn_pa_46p", "pts_allow", 46, nil},
	128: {"espn_ya_0_99", "yds_allow", 0, upTo(99)},
	129: {"espn_ya_100_199", "yds_allow", 100, upTo(199)},
	130: {"espn_ya_200_299", "yds_allow", 200, upTo(299)},
	131: {"espn_ya_300_349", "yds_allow", 300, upTo(349)},
	132: {"espn_ya_350_399", "yds_allow", 350, upTo(399)},
	133: {"espn_ya_400_449", "yds_allow", 400, upTo(449)},
	134: {"espn_ya_450_499", "yds_allow", 450, upTo(499)},
	135: {"espn_ya_500_549", "yds_allow", 500, upTo(549)},
	136: {"espn_ya_550p", "yds_allow", 550, nil},
}

// stepStat is an ESPN stat that scores every Step units of a raw stat.
type stepStat struct {
	key  string
	from string
	step float64
}

var steps = map[int]stepStat{
	8: {"espn_pass_yd_per_25", "pass_yd", 25},
}

// overrideSlots maps pointsOverrides keys (lineup slot IDs) to Sleeper positions.
var overrideSlots = map[string]string{"0": "QB", "2": "RB", "4": "WR", "6": "TE", "16": "DEF", "17": "K"}

// ESPNStatIDForKey returns the lowest ESPN stat ID a Sleeper or derived stat key came
// from. Several IDs can share a key (e.g. 101 and 102 both feed def_st_td), so the
// result must be deterministic.
func ESPNStatIDForKey(key string) (int, bool) {
	best := -1
	consider := func(id int) {
		if best == -1 || id < best {
			best = id
		}
	}
	for id, t := range tiers {
		if t.key == key {
			consider(id)
		}
	}
	for id, s := range steps {
		if s.key == key {
			consider(id)
		}
	}
	for id, keys := range statKeys {
		if slices.Contains(keys, key) {
			consider(id)
		}
	}
	if best == -1 {
		return 0, false
	}
	return best, true
}

// espnKeyForID returns the stat key an ESPN stat ID's points are reported under:
// its tier key, step key, or first mapped Sleeper key; unmapped IDs become "espn_<id>".
func espnKeyForID(id int) string {
	if t, ok := tiers[id]; ok {
		return t.key
	}
	if s, ok := steps[id]; ok {
		return s.key
	}
	if keys, ok := statKeys[id]; ok && len(keys) > 0 {
		return keys[0]
	}
	return fmt.Sprintf("espn_%d", id)
}
