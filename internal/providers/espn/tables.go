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
	74: {"fgm_50p"}, 77: {"fgm_40_49"}, 80: {"fgm_0_19", "fgm_20_29", "fgm_30_39"},
	85: {"fgmiss"}, 86: {"xpm"}, 88: {"xpmiss"},
	89: {"pts_allow_0"}, 90: {"pts_allow_1_6"}, 91: {"pts_allow_7_13"},
	95: {"int"}, 96: {"fum_rec"}, 97: {"blk_kick"}, 98: {"safe"}, 99: {"sack"},
}
