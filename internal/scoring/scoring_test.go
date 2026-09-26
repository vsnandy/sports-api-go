package scoring

import (
	"testing"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

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
			if got := Points(tt.stats, rules); got != tt.want {
				t.Fatalf("Points = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPresets(t *testing.T) {
	wr := map[string]float64{"rec": 6, "rec_yd": 85, "rec_td": 1}
	for name, want := range map[string]float64{"ppr": 20.5, "half": 17.5, "std": 14.5} {
		rules, ok := Preset(name)
		if !ok {
			t.Fatalf("Preset(%q) missing", name)
		}
		if got := Points(wr, rules); got != want {
			t.Errorf("%s points = %v, want %v", name, got, want)
		}
	}
	if _, ok := Preset("bogus"); ok {
		t.Fatal("unknown preset should not resolve")
	}
	a, _ := Preset("ppr")
	a["rec"] = 99
	b, _ := Preset("ppr")
	if b["rec"] != 1 {
		t.Fatal("Preset must return a fresh copy")
	}
}
