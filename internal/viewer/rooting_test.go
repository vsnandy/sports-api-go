package viewer

import (
	"reflect"
	"testing"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

func pf(v float64) *float64 { return &v }

func starter(id *string, name string, ids map[string]string, pts float64) domain.RosterEntry {
	return domain.RosterEntry{Slot: "QB", Player: domain.Player{ID: id, Name: name, Position: "QB", NFLTeam: "KC", PlatformIDs: ids}, Points: pf(pts)}
}

func side(team string, starters, bench []domain.RosterEntry) domain.MatchupSide {
	return domain.MatchupSide{TeamID: team, Roster: domain.Roster{TeamID: team, Starters: starters, Bench: bench}}
}

func mkLeague(id, name string, p domain.Platform, week int, mine string, ms ...domain.Matchup) leagueWeek {
	teams := []domain.Team{{ID: "1"}, {ID: "2"}, {ID: "5"}, {ID: "6"}, {ID: "9"}}
	for i := range teams {
		teams[i].Mine = teams[i].ID == mine
	}
	return leagueWeek{ID: id, Name: name, Platform: p, Week: week, League: domain.League{Teams: teams}, Matchups: ms}
}

func TestBuildRooting(t *testing.T) {
	mahomes := func(pts float64) domain.RosterEntry { return starter(sid("4046"), "Patrick Mahomes", nil, pts) }
	kelce := func(pts float64) domain.RosterEntry { return starter(sid("1466"), "Travis Kelce", nil, pts) }
	chase := starter(sid("7564"), "Ja'Marr Chase", nil, 11)
	jj := func(pts float64) domain.RosterEntry {
		return starter(nil, "Justin Jefferson", map[string]string{"espn": "4262921"}, pts)
	}
	ghost := starter(nil, "Empty", map[string]string{}, 0)

	lws := []leagueWeek{
		mkLeague("espn:1", "A", domain.PlatformESPN, 3, "1",
			domain.Matchup{Home: side("1", []domain.RosterEntry{mahomes(18), jj(20), ghost}, []domain.RosterEntry{chase}), Away: side("2", []domain.RosterEntry{kelce(7), ghost}, nil)}),
		mkLeague("sleeper:2", "B", domain.PlatformSleeper, 3, "5",
			domain.Matchup{Home: side("6", []domain.RosterEntry{kelce(9), chase}, nil), Away: side("5", []domain.RosterEntry{mahomes(19)}, nil)}),
		mkLeague("espn:3", "C", domain.PlatformESPN, 3, "1",
			domain.Matchup{Home: side("1", []domain.RosterEntry{jj(21)}, nil), Away: side("2", []domain.RosterEntry{mahomes(17)}, nil)}),
		mkLeague("espn:4", "D", domain.PlatformESPN, 3, "",
			domain.Matchup{Home: side("1", []domain.RosterEntry{mahomes(18)}, nil), Away: side("2", nil, nil)}),
		mkLeague("sleeper:5", "E", domain.PlatformSleeper, 3, "9",
			domain.Matchup{Home: side("1", []domain.RosterEntry{mahomes(18)}, nil), Away: side("2", nil, nil)}),
		{ID: "espn:6", Name: "F", Platform: domain.PlatformESPN, Err: &APIError{Status: 502, Code: "upstream_error", Message: "boom"}},
		mkLeague("espn:7", "G", domain.PlatformESPN, 4, "1",
			domain.Matchup{Home: side("1", []domain.RosterEntry{kelce(3)}, nil), Away: side("2", nil, nil)}),
	}
	v := buildRooting(2026, 0, lws, []string{"upstream note"})

	if v.Season != 2026 || v.Week != 3 || v.Prev != 2 || v.Next != 4 || len(v.Weeks) != 18 {
		t.Fatalf("header = season %d week %d prev %d next %d weeks %d", v.Season, v.Week, v.Prev, v.Next, len(v.Weeks))
	}
	wantWarn := []string{"upstream note", "D: couldn't find your team", "F: boom", "G: returned week 4"}
	if !reflect.DeepEqual(v.Warnings, wantWarn) {
		t.Errorf("warnings = %q, want %q", v.Warnings, wantWarn)
	}
	if !reflect.DeepEqual(v.NotPlaying, []string{"E"}) || v.Counted != 3 {
		t.Errorf("notPlaying = %v counted = %d, want [E] 3", v.NotPlaying, v.Counted)
	}
	type row struct {
		Name              string
		For, Against, Net int
		Apps              int
	}
	var got []row
	for _, p := range v.Players {
		got = append(got, row{p.Name, p.For, p.Against, p.Net, len(p.Apps)})
	}
	want := []row{
		{"Justin Jefferson", 2, 0, 2, 2}, // unmapped: merged by espn:4262921 across A and C
		{"Travis Kelce", 0, 2, -2, 2},
		{"Patrick Mahomes", 2, 1, 1, 3},
	} // Chase: 1 starter appearance (bench in A ignored) → excluded; "Empty" has no key → skipped
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("players = %+v, want %+v", got, want)
	}
	m := v.Players[2]
	wantApps := []appearance{
		{League: "A", Href: "/league/espn:1?week=3", For: true, Points: pf(18)},
		{League: "B", Href: "/league/sleeper:2?week=3", For: true, Points: pf(19)},
		{League: "C", Href: "/league/espn:3?week=3", For: false, Points: pf(17)},
	}
	if !reflect.DeepEqual(m.Apps, wantApps) {
		t.Errorf("Mahomes apps = %+v", m.Apps)
	}
	if m.Image != "https://sleepercdn.com/content/nfl/players/thumb/4046.jpg" || m.Initials != "PM" || m.NFLTeam != "KC" {
		t.Errorf("Mahomes view = %+v", m)
	}
}

func TestBuildRootingWeekFromFirstLoaded(t *testing.T) {
	lws := []leagueWeek{
		{ID: "espn:6", Name: "F", Err: &APIError{Status: 502, Message: "boom"}},
		mkLeague("espn:1", "A", domain.PlatformESPN, 5, "1", domain.Matchup{Home: side("1", nil, nil), Away: side("2", nil, nil)}),
	}
	v := buildRooting(2026, 0, lws, nil)
	if v.Week != 5 || v.Counted != 1 {
		t.Fatalf("week = %d counted = %d, want 5 and 1", v.Week, v.Counted)
	}
}

func TestBuildRootingRequestedWeek(t *testing.T) {
	lws := []leagueWeek{mkLeague("espn:1", "A", domain.PlatformESPN, 2, "1", domain.Matchup{Home: side("1", nil, nil), Away: side("2", nil, nil)})}
	v := buildRooting(2026, 2, lws, nil)
	if v.Week != 2 || v.Counted != 1 || len(v.Warnings) != 0 || v.Players != nil {
		t.Fatalf("view = %+v", v)
	}
}

func TestWeekNav(t *testing.T) {
	if p, n, w := weekNav(1); p != 0 || n != 2 || len(w) != 18 || w[0] != 1 || w[17] != 18 {
		t.Errorf("weekNav(1) = %d %d %v", p, n, w)
	}
	if p, n, _ := weekNav(18); p != 17 || n != 0 {
		t.Errorf("weekNav(18) = %d %d", p, n)
	}
}
