package viewer

import (
	"testing"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

func sid(s string) *string { return &s }

func TestPlayerImage(t *testing.T) {
	cases := []struct {
		name string
		p    domain.Player
		want string
	}{
		{"sleeper id", domain.Player{ID: sid("4046"), Position: "QB"}, "https://sleepercdn.com/content/nfl/players/thumb/4046.jpg"},
		{"DEF uses team logo", domain.Player{ID: sid("PIT"), Position: "DEF", NFLTeam: "PIT"}, "https://sleepercdn.com/images/team_logos/nfl/pit.png"},
		{"DEF falls back to ID", domain.Player{ID: sid("KC"), Position: "DEF"}, "https://sleepercdn.com/images/team_logos/nfl/kc.png"},
		{"unmapped ESPN D/ST", domain.Player{Position: "DEF", NFLTeam: "MIN", PlatformIDs: map[string]string{"espn": "-16016"}}, "https://sleepercdn.com/images/team_logos/nfl/min.png"},
		{"unmapped ESPN player", domain.Player{Position: "RB", PlatformIDs: map[string]string{"espn": "4685720"}}, "https://a.espncdn.com/i/headshots/nfl/players/full/4685720.png"},
		{"hostile ids", domain.Player{ID: sid("1/../2"), Position: "WR", PlatformIDs: map[string]string{"espn": "12 3"}}, ""},
		{"DEF with bad team", domain.Player{Position: "DEF", NFLTeam: "K C"}, ""},
		{"nothing", domain.Player{Position: "WR"}, ""},
	}
	for _, c := range cases {
		if got := playerImage(c.p); got != c.want {
			t.Errorf("%s: playerImage = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestInitials(t *testing.T) {
	for in, want := range map[string]string{
		"Patrick Mahomes": "PM",
		"A.J. Brown":      "AB",
		"Steelers D/ST":   "SD",
		"Pelé":            "P",
		"  ":              "?",
		"":                "?",
		"ja'marr chase":   "JC",
	} {
		if got := Initials(in); got != want {
			t.Errorf("Initials(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildLeagueViewFlags(t *testing.T) {
	pts := func(v float64) *float64 { return &v }
	lg := domain.League{Teams: []domain.Team{{ID: "1", Name: "A"}, {ID: "2", Name: "B"}}}
	ms := []domain.Matchup{
		{Home: domain.MatchupSide{TeamID: "1", Points: 22, Roster: domain.Roster{
			Starters: []domain.RosterEntry{{Slot: "DEF", Player: domain.Player{ID: sid("PIT"), Name: "Pittsburgh Steelers", Position: "DEF", NFLTeam: "PIT"},
				Points: pts(4), PointsBreakdown: map[string]float64{"sack": 3, "pass_int": -1}}},
			Bench: []domain.RosterEntry{{Slot: "BN", Player: domain.Player{Name: "Rookie Guy", Position: "RB"}}},
		}}, Away: domain.MatchupSide{TeamID: "2", Points: 0}},
		{Home: domain.MatchupSide{TeamID: "1", Points: 10}, Away: domain.MatchupSide{TeamID: "2", Points: 10}},
	}
	v := buildLeagueView(lg, ms, Meta{Week: 3})
	m := v.Matchups[0]
	if !m.Home.Leading || m.Away.Leading {
		t.Errorf("22 vs 0: leading = %v/%v, want true/false", m.Home.Leading, m.Away.Leading)
	}
	if tie := v.Matchups[1]; !tie.Home.Leading || !tie.Away.Leading {
		t.Errorf("tie: leading = %v/%v, want both", tie.Home.Leading, tie.Away.Leading)
	}
	def := m.Home.Starters[0]
	if !def.IsDEF || def.Bench || def.Initials != "PS" || def.Image != "https://sleepercdn.com/images/team_logos/nfl/pit.png" {
		t.Errorf("DEF row = %+v", def)
	}
	if !def.Breakdown[0].Gain || def.Breakdown[1].Gain {
		t.Errorf("gain flags = %+v", def.Breakdown)
	}
	if b := m.Home.Bench[0]; !b.Bench || b.Initials != "RG" || b.Image != "" {
		t.Errorf("bench row = %+v", b)
	}
}
