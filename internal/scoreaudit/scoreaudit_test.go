package scoreaudit

import (
	"reflect"
	"testing"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

func TestCompare(t *testing.T) {
	seventeen := 17.0
	scorings := map[string]domain.Scoring{"espn:1": {
		Rules:      domain.ScoringRules{"sack": 0},
		ByPosition: map[string]domain.ScoringRules{"DEF": {"sack": 1, "espn_pa_14_17": 1}},
		Derived:    []domain.DerivedStat{{Key: "espn_pa_14_17", From: "pts_allow", Min: 14, Max: &seventeen}},
	}}
	ids := map[string]int{"sack": 99, "espn_pa_14_17": 92, "int": 95}
	statID := func(k string) (int, bool) { id, ok := ids[k]; return id, ok }

	samples := []Sample{
		{League: "espn:1", Week: 1, Name: "D1", Mapped: true, Position: "DEF",
			Stats: map[string]float64{"sack": 3, "pts_allow": 14}, Total: 4, ByStat: map[int]float64{99: 3, 92: 1}},
		{League: "espn:1", Week: 1, Name: "D2", Mapped: true, Position: "DEF",
			Stats: map[string]float64{"sack": 2, "int": 1, "pts_allow": 30}, Total: 4, ByStat: map[int]float64{99: 2, 95: 2}},
		{League: "espn:1", Week: 1, Name: "Nobody", Mapped: false, Total: 12, ByStat: map[int]float64{3: 12}},
		{League: "espn:1", Week: 2, Name: "Bye Guy", Mapped: true, Position: "WR", Total: 0, ByStat: map[int]float64{}},
	}
	r := Compare(samples, scorings, statID)

	if r.Compared != 3 || r.Exact != 2 || r.Unmapped != 1 {
		t.Fatalf("counts = compared %d exact %d unmapped %d", r.Compared, r.Exact, r.Unmapped)
	}
	want := []StatMismatch{{StatID: 95, Count: 1, Expected: 2, Got: 0, Example: "D2 (espn:1 wk 1)",
		ExampleStats: map[string]float64{"sack": 2, "int": 1, "pts_allow": 30}}}
	if !reflect.DeepEqual(r.Mismatches, want) {
		t.Fatalf("mismatches = %+v", r.Mismatches)
	}
	if !r.Pass(0.6) || r.Pass(0.98) {
		t.Fatalf("Pass: rate %.3f", r.ExactRate())
	}
}

func TestCompareAttributesEngineOnlyKeysToStatZero(t *testing.T) {
	scorings := map[string]domain.Scoring{"espn:1": {Rules: domain.ScoringRules{"bonus_x": 5}}}
	r := Compare([]Sample{{League: "espn:1", Week: 1, Name: "P", Mapped: true, Position: "QB",
		Stats: map[string]float64{"bonus_x": 1}, Total: 0, ByStat: map[int]float64{}}},
		scorings, func(string) (int, bool) { return 0, false })
	if len(r.Mismatches) != 1 || r.Mismatches[0].StatID != 0 || r.Mismatches[0].Got != 5 {
		t.Fatalf("mismatches = %+v", r.Mismatches)
	}
}

func TestEmptyReportFails(t *testing.T) {
	if (Report{}).Pass(0.98) {
		t.Fatal("a report with nothing compared must not pass")
	}
}
