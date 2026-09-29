package espn

import (
	"reflect"
	"testing"
)

func TestESPNKeyForID(t *testing.T) {
	for id, want := range map[int]string{
		3:   "pass_yd",             // direct
		80:  "fgm_0_19",            // multi-key → first key
		96:  "fum_rec",             // multi-key → first key
		101: "def_st_td",           // shared key
		92:  "espn_pa_14_17",       // tier
		8:   "espn_pass_yd_per_25", // step
		999: "espn_999",            // unmapped
	} {
		if got := espnKeyForID(id); got != want {
			t.Errorf("espnKeyForID(%d) = %q, want %q", id, got, want)
		}
	}
}

func TestBreakdownFromApplied(t *testing.T) {
	got := breakdownFromApplied(map[string]float64{
		"95": 2, "99": 3, "101": 6, "102": 6, "999": 1.5, "3": 0, "bad": 4,
	})
	want := map[string]float64{"int": 2, "sack": 3, "def_st_td": 12, "espn_999": 1.5}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("breakdownFromApplied = %v, want %v (zeros dropped, shared keys summed, non-numeric IDs skipped)", got, want)
	}
}
