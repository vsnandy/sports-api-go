package scoring

import (
	"math"
	"testing"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

func flat(r domain.ScoringRules) domain.Scoring { return domain.Scoring{Rules: r} }

func upTo(v float64) *float64 { return &v }

func TestPoints(t *testing.T) {
	rules := domain.ScoringRules{"pass_yd": 0.04, "pass_td": 4, "pass_int": -2, "rush_yd": 0.1}
	tests := []struct {
		name  string
		stats map[string]float64
		want  float64
	}{
		{"qb line", map[string]float64{"pass_yd": 250, "pass_td": 2, "pass_int": 1, "rush_yd": 15}, 17.5},
		{"negative rushing", map[string]float64{"rush_yd": -5}, -0.5},
		{"unknown keys ignored", map[string]float64{"pass_att": 40, "pass_td": 1}, 4},
		{"rounds to cents", map[string]float64{"pass_yd": 263}, 10.52},
		{"empty", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Points(tt.stats, flat(rules), "QB"); got != tt.want {
				t.Fatalf("Points = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPresets(t *testing.T) {
	wr := map[string]float64{"rec": 6, "rec_yd": 85, "rec_td": 1}
	for name, want := range map[string]float64{"ppr": 20.5, "half": 17.5, "std": 14.5} {
		s, ok := Preset(name)
		if !ok {
			t.Fatalf("Preset(%q) missing", name)
		}
		if got := Points(wr, s, "WR"); got != want {
			t.Errorf("%s points = %v, want %v", name, got, want)
		}
	}
	if _, ok := Preset("bogus"); ok {
		t.Fatal("unknown preset should not resolve")
	}
	a, _ := Preset("ppr")
	a.Rules["rec"] = 99
	b, _ := Preset("ppr")
	if b.Rules["rec"] != 1 {
		t.Fatal("Preset must return a fresh copy")
	}
}

func TestTwoPointConversions(t *testing.T) {
	s, _ := Preset("std")
	stats := map[string]float64{"pass_2pt": 1, "rush_2pt": 1, "rec_2pt": 1}
	if got := Points(stats, s, "QB"); got != 6 {
		t.Fatalf("2pt conversions = %v, want 6", got)
	}
}

func TestByPositionReplacesBase(t *testing.T) {
	s := domain.Scoring{
		Rules:      domain.ScoringRules{"rec": 0.5, "sack": 0},
		ByPosition: map[string]domain.ScoringRules{"TE": {"rec": 1.5}, "DEF": {"sack": 1}},
	}
	line := map[string]float64{"rec": 4, "sack": 3}
	if got := Points(line, s, "TE"); got != 6 {
		t.Errorf("TE = %v, want 6 (rec replaced, sack base 0)", got)
	}
	if got := Points(line, s, "WR"); got != 2 {
		t.Errorf("WR = %v, want 2 (base rules only)", got)
	}
	if got := Points(line, s, "DEF"); got != 5 {
		t.Errorf("DEF = %v, want 5 (rec base 0.5×4 + sack override 1×3)", got)
	}
}

func TestDerivedStats(t *testing.T) {
	s := domain.Scoring{
		Rules: domain.ScoringRules{},
		ByPosition: map[string]domain.ScoringRules{"DEF": {
			"espn_pa_14_17": 1, "espn_pa_18_21": 0.5, "espn_pa_46p": -5,
		}},
		Derived: []domain.DerivedStat{
			{Key: "espn_pa_14_17", From: "pts_allow", Min: 14, Max: upTo(17)},
			{Key: "espn_pa_18_21", From: "pts_allow", Min: 18, Max: upTo(21)},
			{Key: "espn_pa_46p", From: "pts_allow", Min: 46},
		},
	}
	tests := []struct {
		name  string
		stats map[string]float64
		want  float64
	}{
		{"lower bound inclusive", map[string]float64{"pts_allow": 14}, 1},
		{"upper bound inclusive", map[string]float64{"pts_allow": 17}, 1},
		{"next tier starts at 18", map[string]float64{"pts_allow": 18}, 0.5},
		{"unbounded tier", map[string]float64{"pts_allow": 70}, -5},
		{"gap between tiers scores nothing", map[string]float64{"pts_allow": 30}, 0},
		{"no raw stat, no tier", map[string]float64{"sack": 2}, 0},
		{"nil stats", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Points(tt.stats, s, "DEF"); got != tt.want {
				t.Fatalf("Points = %v, want %v", got, tt.want)
			}
		})
	}
	if got := Points(map[string]float64{"pts_allow": 14}, s, "QB"); got != 0 {
		t.Errorf("tier rule is DEF-only; QB got %v", got)
	}
	in := map[string]float64{"pts_allow": 14}
	Points(in, s, "DEF")
	if _, leaked := in["espn_pa_14_17"]; leaked {
		t.Error("Points must not write derived stats into the caller's map")
	}
}

func TestBreakdownSumsToPoints(t *testing.T) {
	s := domain.Scoring{
		Rules:      domain.ScoringRules{"pass_yd": 0.04, "pass_td": 4},
		ByPosition: map[string]domain.ScoringRules{"DEF": {"sack": 1, "espn_pa_14_17": 1}},
		Derived:    []domain.DerivedStat{{Key: "espn_pa_14_17", From: "pts_allow", Min: 14, Max: upTo(17)}},
	}
	line := map[string]float64{"sack": 3, "pts_allow": 14, "pass_yd": 0}
	b := Breakdown(line, s, "DEF")
	if b["sack"] != 3 || b["espn_pa_14_17"] != 1 || len(b) != 2 {
		t.Fatalf("Breakdown = %v, want sack 3 and espn_pa_14_17 1 only", b)
	}
	var sum float64
	for _, v := range b {
		sum += v
	}
	if math.Round(sum*100)/100 != Points(line, s, "DEF") {
		t.Fatalf("Breakdown sum %v != Points %v", sum, Points(line, s, "DEF"))
	}
}
