package espn

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/vsnandy/sports-api-go/internal/domain"
	"github.com/vsnandy/sports-api-go/internal/scoring"
)

func loadItems(t *testing.T) []scoringItemJSON {
	t.Helper()
	raw, err := os.ReadFile("testdata/scoring_items.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		ScoringItems []scoringItemJSON `json:"scoringItems"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v.ScoringItems
}

func TestConvertScoringRealLeague(t *testing.T) {
	c := convertScoring(loadItems(t))

	for k, want := range map[string]float64{
		"pass_yd": 0.04, "pass_td": 4, "pass_int": -2, "rec": 0.5, "rush_td": 6,
		"fgm_0_19": 3, "fgm_40_49": 4, "xpm": 1, "sack": 0, "espn_pa_0": 0,
	} {
		if got, ok := c.rules[k]; !ok || got != want {
			t.Errorf("rules[%s] = %v (present %v), want %v", k, got, ok, want)
		}
	}
	wantDEF := domain.ScoringRules{
		"espn_pa_0": 5, "espn_pa_1_6": 4, "espn_pa_7_13": 3, "espn_pa_14_17": 1,
		"espn_pa_28_34": -1, "espn_pa_35_45": -3, "espn_pa_46p": -5,
		"int": 2, "fum_rec": 2, "blk_kick": 2, "safe": 2, "sack": 1,
		"espn_ya_0_99": 5, "espn_ya_100_199": 3, "espn_ya_200_299": 2, "espn_ya_350_399": -1,
		"espn_ya_400_449": -3, "espn_ya_450_499": -5, "espn_ya_500_549": -6, "espn_ya_550p": -7,
	}
	if !reflect.DeepEqual(c.byPosition, map[string]domain.ScoringRules{"DEF": wantDEF}) {
		t.Errorf("byPosition = %v", c.byPosition)
	}
	if len(c.derived) != 15 {
		t.Errorf("derived = %d stats, want 15 (only tiers present in the league)", len(c.derived))
	}
	wantUnsupported := []string{
		"espn stat 101 (6 pts)", "espn stat 102 (6 pts)", "espn stat 103 (6 pts)", "espn stat 104 (6 pts)",
		"espn stat 198 (5 pts)", "espn stat 201 (6 pts)", "espn stat 206 (2 pts)", "espn stat 209 (1 pts)",
		"espn stat 63 (6 pts)", "espn stat 93 (6 pts)",
	}
	if !reflect.DeepEqual(c.unsupported, wantUnsupported) {
		t.Errorf("unsupported = %v", c.unsupported)
	}
}

// The Steelers D/ST in week 2 scored 8 on ESPN: 3 sacks, 1 INT, 1 fumble recovery,
// 14 points allowed, 327 yards allowed (a tier this league doesn't score).
func TestConvertedScoringMatchesESPNForDST(t *testing.T) {
	c := convertScoring(loadItems(t))
	s := domain.Scoring{Rules: c.rules, ByPosition: c.byPosition, Derived: c.derived}
	pit := map[string]float64{"sack": 3, "int": 1, "fum_rec": 1, "pts_allow": 14, "yds_allow": 327, "int_ret_yd": 3}
	if got := scoring.Points(pit, s, "DEF"); got != 8 {
		t.Fatalf("PIT D/ST = %v, want 8", got)
	}
}

func TestTierTable(t *testing.T) {
	cases := []struct {
		id   int
		key  string
		from string
		min  float64
		max  float64 // -1 = unbounded
	}{
		{89, "espn_pa_0", "pts_allow", 0, 0}, {90, "espn_pa_1_6", "pts_allow", 1, 6},
		{91, "espn_pa_7_13", "pts_allow", 7, 13}, {92, "espn_pa_14_17", "pts_allow", 14, 17},
		{121, "espn_pa_18_21", "pts_allow", 18, 21}, {122, "espn_pa_22_27", "pts_allow", 22, 27},
		{123, "espn_pa_28_34", "pts_allow", 28, 34}, {124, "espn_pa_35_45", "pts_allow", 35, 45},
		{125, "espn_pa_46p", "pts_allow", 46, -1},
		{128, "espn_ya_0_99", "yds_allow", 0, 99}, {129, "espn_ya_100_199", "yds_allow", 100, 199},
		{130, "espn_ya_200_299", "yds_allow", 200, 299}, {131, "espn_ya_300_349", "yds_allow", 300, 349},
		{132, "espn_ya_350_399", "yds_allow", 350, 399}, {133, "espn_ya_400_449", "yds_allow", 400, 449},
		{134, "espn_ya_450_499", "yds_allow", 450, 499}, {135, "espn_ya_500_549", "yds_allow", 500, 549},
		{136, "espn_ya_550p", "yds_allow", 550, -1},
	}
	if len(tiers) != len(cases) {
		t.Fatalf("tiers has %d entries, want %d", len(tiers), len(cases))
	}
	for _, c := range cases {
		tr, ok := tiers[c.id]
		if !ok || tr.key != c.key || tr.from != c.from || tr.min != c.min {
			t.Errorf("tier %d = %+v, want %s %s min %v", c.id, tr, c.key, c.from, c.min)
			continue
		}
		if (c.max < 0) != (tr.max == nil) || (tr.max != nil && *tr.max != c.max) {
			t.Errorf("tier %d max = %v, want %v", c.id, tr.max, c.max)
		}
		if id, ok := ESPNStatIDForKey(c.key); !ok || id != c.id {
			t.Errorf("ESPNStatIDForKey(%s) = %d, %v", c.key, id, ok)
		}
	}
}

func TestConvertScoringUnknownOverrideSlot(t *testing.T) {
	c := convertScoring([]scoringItemJSON{
		{StatID: 53, Points: 1, PointsOverrides: map[string]float64{"23": 2, "6": 1.5}},
	})
	if c.byPosition["TE"]["rec"] != 1.5 {
		t.Errorf("TE override lost: %v", c.byPosition)
	}
	if !reflect.DeepEqual(c.unsupported, []string{"espn stat 53 override for slot 23"}) {
		t.Errorf("unsupported = %v", c.unsupported)
	}
}

func TestESPNStatIDForKey(t *testing.T) {
	for key, want := range map[string]int{"pass_yd": 3, "fgm_20_29": 80, "sack": 99, "pts_allow": 120, "yds_allow": 127} {
		if id, ok := ESPNStatIDForKey(key); !ok || id != want {
			t.Errorf("ESPNStatIDForKey(%s) = %d, %v; want %d", key, id, ok, want)
		}
	}
	if _, ok := ESPNStatIDForKey("pts_allow_14_20"); ok {
		t.Error("Sleeper bucket keys are no longer ESPN mappings")
	}
}
