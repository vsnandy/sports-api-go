package viewer

import "strings"

var labels = map[string]string{
	"pass_yd": "Passing yards", "pass_td": "Passing TDs", "pass_int": "Interceptions thrown", "pass_2pt": "Passing 2-pt conversions",
	"rush_yd": "Rushing yards", "rush_td": "Rushing TDs", "rush_2pt": "Rushing 2-pt conversions",
	"rec": "Receptions", "rec_yd": "Receiving yards", "rec_td": "Receiving TDs", "rec_2pt": "Receiving 2-pt conversions",
	"fum_lost": "Fumbles lost",
	"fgm_0_19": "FG made 0–19", "fgm_20_29": "FG made 20–29", "fgm_30_39": "FG made 30–39", "fgm_40_49": "FG made 40–49",
	"fgm_50p": "FG made 50+", "fgm_50_59": "FG made 50–59", "fgm_60p": "FG made 60+",
	"fgmiss": "FG missed", "fgmiss_40_49": "FG missed 40–49", "xpm": "Extra points made", "xpmiss": "Extra points missed",
	"sack": "Sacks", "int": "Interceptions", "fum_rec": "Fumble recoveries", "def_st_fum_rec": "Special-teams fumble recoveries",
	"safe": "Safeties", "blk_kick": "Blocked kicks", "def_td": "Defensive TDs", "def_st_td": "Return TDs (D/ST)", "st_td": "Return TDs",
	"pts_allow": "Points allowed", "yds_allow": "Yards allowed",
	"espn_pass_yd_per_25": "Every 25 passing yards",
}

// Label returns a human-readable name for a stat key, falling back to the key itself.
func Label(key string) string {
	if l, ok := labels[key]; ok {
		return l
	}
	for prefix, unit := range map[string]string{"espn_pa_": "points allowed", "pts_allow_": "points allowed", "espn_ya_": "yards allowed"} {
		if r, ok := strings.CutPrefix(key, prefix); ok {
			return rangeLabel(r) + " " + unit
		}
	}
	return key
}

// rangeLabel turns "14_17" into "14–17", "46p" into "46+", and "0" into "0".
func rangeLabel(r string) string {
	if n, ok := strings.CutSuffix(r, "p"); ok {
		return n + "+"
	}
	return strings.ReplaceAll(r, "_", "–")
}
