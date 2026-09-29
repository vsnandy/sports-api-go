package main

import (
	"reflect"
	"testing"
)

func TestParseWeeks(t *testing.T) {
	ok := map[string][]int{"3": {3}, "1-3": {1, 2, 3}, "18": {18}}
	for in, want := range ok {
		got, err := parseWeeks(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("parseWeeks(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "0", "19", "3-1", "a", "1-", "1-2-3"} {
		if _, err := parseWeeks(bad); err == nil {
			t.Errorf("parseWeeks(%q) should fail", bad)
		}
	}
}

func TestLeagueIDs(t *testing.T) {
	got, err := leagueIDs([]string{"espn:123", "espn:456"}, "")
	if err != nil || !reflect.DeepEqual(got, []string{"123", "456"}) {
		t.Fatalf("flags: %v, %v", got, err)
	}
	got, err = leagueIDs(nil, " 789 ,111")
	if err != nil || !reflect.DeepEqual(got, []string{"789", "111"}) {
		t.Fatalf("env: %v, %v", got, err)
	}
	for _, bad := range [][]string{{"sleeper:1"}, {"espn:1/2"}} {
		if _, err := leagueIDs(bad, ""); err == nil {
			t.Errorf("leagueIDs(%v) should fail", bad)
		}
	}
	if _, err := leagueIDs(nil, ""); err == nil {
		t.Error("no leagues anywhere should fail")
	}
}
