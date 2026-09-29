package viewer

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

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

const rootingLeagues = `{"data":[{"id":"espn:1","platform":"espn","season":2026,"name":"Office League"},{"id":"sleeper:2","platform":"sleeper","season":2026,"name":"Dynasty"},{"id":"espn:9","platform":"espn","season":2026,"name":"Broken"}],"meta":{"season":2026,"warnings":[]}}`

func rootingPlayerJSON(id, name string, pts float64) string {
	return fmt.Sprintf(`{"slot":"QB","player":{"id":%q,"name":%q,"position":"QB","nflTeam":"KC","platformIds":{}},"stats":{},"points":%g}`, id, name, pts)
}

var rootingRoutes = map[string]string{
	"/v1/nfl/leagues":        rootingLeagues,
	"/v1/nfl/leagues/espn:1": `{"data":{"id":"espn:1","name":"Office League","teams":[{"id":"1","name":"Team Varun","owner":"v","mine":true},{"id":"2","name":"Alex","owner":"a"}]},"meta":{"warnings":[]}}`,
	"/v1/nfl/leagues/espn:1/matchups": `{"data":[{"week":WEEK,"home":{"teamId":"1","points":18,"roster":{"teamId":"1","starters":[` + rootingPlayerJSON("4046", "Patrick Mahomes", 18) + `],"bench":[],"reserve":[]}},
 "away":{"teamId":"2","points":7,"roster":{"teamId":"2","starters":[` + rootingPlayerJSON("1466", "Travis Kelce", 7) + `],"bench":[],"reserve":[]}}}],"meta":{"season":2026,"week":WEEK,"warnings":[]}}`,
	"/v1/nfl/leagues/sleeper:2": `{"data":{"id":"sleeper:2","name":"Dynasty","teams":[{"id":"5","name":"Mine","owner":"v","mine":true},{"id":"6","name":"Sam","owner":"s"}]},"meta":{"warnings":[]}}`,
	"/v1/nfl/leagues/sleeper:2/matchups": `{"data":[{"week":WEEK,"home":{"teamId":"6","points":9.5,"roster":{"teamId":"6","starters":[` + rootingPlayerJSON("1466", "Travis Kelce", 9.5) + `],"bench":[],"reserve":[]}},
 "away":{"teamId":"5","points":20,"roster":{"teamId":"5","starters":[` + rootingPlayerJSON("4046", "Patrick Mahomes", 20) + `],"bench":[],"reserve":[]}}}],"meta":{"season":2026,"week":WEEK,"warnings":[]}}`,
}

// newRootingHandler serves routes (WEEK replaced by ?week= or 3); any other path is a 502.
// Matchups without include=stats are rejected, since points need stats.
func newRootingHandler(t *testing.T, routes map[string]string) http.Handler {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, `{"error":{"code":"upstream_error","message":"upstream down"}}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/matchups") && r.URL.Query().Get("include") != "stats" {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"code":"invalid_param","message":"include=stats missing"}}`)
			return
		}
		week := r.URL.Query().Get("week")
		if week == "" {
			week = "3"
		}
		io.WriteString(w, strings.ReplaceAll(body, "WEEK", week))
	}))
	t.Cleanup(srv.Close)
	return NewHandler(&Client{BaseURL: srv.URL, APIKey: "k", HTTP: &http.Client{Timeout: 5 * time.Second}})
}

func TestRootingPage(t *testing.T) {
	code, body := get(t, newRootingHandler(t, rootingRoutes), "/rooting")
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	mustContain(t, body,
		"<h1>Rooting guide</h1>",
		"2026 · week 3 · 2 leagues counted",
		`<div class="warn">Broken: upstream down</div>`,
		`class="net pos">&#43;2<small>2 for · 0 against</small>`,
		`class="net neg">−2<small>0 for · 2 against</small>`,
		`class="chip for" href="/league/espn:1?week=3">Office League<b>18.00</b>`,
		`class="chip against" href="/league/sleeper:2?week=3">Dynasty<b>9.50</b>`,
		`PM<img src="https://sleepercdn.com/content/nfl/players/thumb/4046.jpg"`,
		`href="?week=2"`, `href="?week=4"`,
	)
	if strings.Index(body, "Patrick Mahomes") > strings.Index(body, "Travis Kelce") {
		t.Error("equal |net| and appearances should sort by name")
	}
}

func TestRootingPassesWeek(t *testing.T) {
	_, body := get(t, newRootingHandler(t, rootingRoutes), "/rooting?week=2")
	mustContain(t, body, "2026 · week 2 · 2 leagues counted", `href="/league/espn:1?week=2"`)
}

func TestRootingInvalidWeek(t *testing.T) {
	code, body := get(t, newRootingHandler(t, rootingRoutes), "/rooting?week=0")
	if code != http.StatusBadRequest {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "Invalid week", "week must be a number from 1 to 18")
}

func TestRootingAllLeaguesFail(t *testing.T) {
	routes := map[string]string{"/v1/nfl/leagues": `{"data":[{"id":"espn:9","platform":"espn","season":2026,"name":"Broken"}],"meta":{"season":2026,"warnings":[]}}`}
	code, body := get(t, newRootingHandler(t, routes), "/rooting")
	if code != http.StatusBadGateway {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "upstream down")
}

func TestRootingNoSharedPlayers(t *testing.T) {
	routes := map[string]string{
		"/v1/nfl/leagues":                 `{"data":[{"id":"espn:1","platform":"espn","season":2026,"name":"Office League"}],"meta":{"season":2026,"warnings":[]}}`,
		"/v1/nfl/leagues/espn:1":          rootingRoutes["/v1/nfl/leagues/espn:1"],
		"/v1/nfl/leagues/espn:1/matchups": rootingRoutes["/v1/nfl/leagues/espn:1/matchups"],
	}
	code, body := get(t, newRootingHandler(t, routes), "/rooting")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "No shared players this week.", "1 leagues counted")
}

func TestRootingBeforeRedeploy(t *testing.T) {
	_, h := newTestHandler(t) // its leagues carry no mine flag; sleeper:2 is a 404
	code, body := get(t, h, "/rooting")
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	mustContain(t, body, "Office League: couldn&#39;t find your team", "Dynasty: route not found", "No shared players this week.")
}

func TestLeaguesPageLinksRooting(t *testing.T) {
	_, h := newTestHandler(t)
	_, body := get(t, h, "/")
	mustContain(t, body, `href="/rooting"`, "Rooting guide")
}
