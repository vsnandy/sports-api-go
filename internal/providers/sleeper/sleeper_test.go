package sleeper

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vsnandy/sports-api-go/internal/domain"
	"github.com/vsnandy/sports-api-go/internal/providers/httpx"
)

var slots = []string{"QB", "RB", "WR", "FLEX", "DEF", "BN", "BN"}

func newTestClient(t *testing.T) *Client {
	t.Helper()
	c, _ := newCountingClient(t, "varun")
	return c
}

// newCountingClient serves testdata and reports how many times each path was requested.
func newCountingClient(t *testing.T, username string) (*Client, func(path string) int) {
	t.Helper()
	routes := map[string]string{
		"/v1/state/nfl":                "state.json",
		"/v1/user/varun":               "user.json",
		"/v1/user/ghost":               "null.json",
		"/v1/user/u1/leagues/nfl/2026": "user_leagues.json",
		"/v1/league/111":               "league.json",
		"/v1/league/111/users":         "users.json",
		"/v1/league/111/rosters":       "rosters.json",
		"/v1/league/111/matchups/3":    "matchups.json",
		"/v1/league/444":               "league.json",
		"/v1/league/444/users":         "users.json",
		"/v1/league/444/rosters":       "rosters_coowned.json",
		"/v1/league/555":               "league.json",
		"/v1/league/555/users":         "users.json",
		"/v1/league/555/rosters":       "rosters_others.json",
		"/v1/players/nfl":              "players.json",
		"/stats/nfl/2026/3":            "week_stats.json",
		"/stats/nfl/player/6794":       "gamelog.json",
	}
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/stats/") && r.URL.Query().Get("season_type") != "regular" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join("testdata", f))
	}))
	t.Cleanup(srv.Close)
	count := func(path string) int {
		mu.Lock()
		defer mu.Unlock()
		return hits[path]
	}
	return New(httpx.New("sleeper", 2*time.Second), srv.URL+"/v1", srv.URL, username), count
}

func ids(entries []domain.RosterEntryRef) []string {
	out := []string{}
	for _, e := range entries {
		out = append(out, e.Slot+"="+e.Ref.ID)
	}
	return out
}

func TestState(t *testing.T) {
	st, err := newTestClient(t).State(context.Background())
	if err != nil || st != (domain.SeasonState{Season: 2026, Week: 3}) {
		t.Fatalf("State = %+v, %v", st, err)
	}
}

func TestListLeagues(t *testing.T) {
	ls, err := newTestClient(t).ListLeagues(context.Background(), 2026)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.LeagueSummary{
		{ID: "sleeper:111", Platform: domain.PlatformSleeper, Season: 2026, Name: "Dynasty Bros"},
		{ID: "sleeper:222", Platform: domain.PlatformSleeper, Season: 2026, Name: "Work League"},
	}
	if !reflect.DeepEqual(ls, want) {
		t.Fatalf("ListLeagues = %+v", ls)
	}
}

func TestLeague(t *testing.T) {
	l, err := newTestClient(t).League(context.Background(), "111", 0)
	if err != nil {
		t.Fatal(err)
	}
	if l.ID != "sleeper:111" || l.Name != "Dynasty Bros" || l.Season != 2026 || l.Sport != domain.SportNFL {
		t.Fatalf("League header = %+v", l)
	}
	wantTeams := []domain.Team{{ID: "1", Name: "Gridiron Gurus", Owner: "Varun", Mine: true}, {ID: "2", Name: "Sam", Owner: "Sam"}}
	if !reflect.DeepEqual(l.Teams, wantTeams) {
		t.Fatalf("Teams = %+v", l.Teams)
	}
	if l.Scoring["rec"] != 1 || !reflect.DeepEqual(l.RosterSlots, slots) {
		t.Fatalf("Scoring/RosterSlots = %v / %v", l.Scoring, l.RosterSlots)
	}
	if l.UnsupportedRules == nil || len(l.UnsupportedRules) != 0 {
		t.Fatalf("UnsupportedRules = %#v, want empty non-nil", l.UnsupportedRules)
	}
	if l.ScoringByPosition == nil || len(l.ScoringByPosition) != 0 {
		t.Errorf("ScoringByPosition = %#v, want empty non-nil", l.ScoringByPosition)
	}
	if l.DerivedStats == nil || len(l.DerivedStats) != 0 {
		t.Errorf("DerivedStats = %#v, want empty non-nil", l.DerivedStats)
	}
}

func TestLeagueNotFound(t *testing.T) {
	if _, err := newTestClient(t).League(context.Background(), "999", 0); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestRosters(t *testing.T) {
	rs, err := newTestClient(t).Rosters(context.Background(), "111", 2026, slots)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 || rs[0].TeamID != "1" || rs[1].TeamID != "2" {
		t.Fatalf("roster order = %+v", rs)
	}
	if got, want := ids(rs[0].Starters), []string{"QB=4984", "RB=4866", "WR=7564", "FLEX=9509", "DEF=SF"}; !reflect.DeepEqual(got, want) {
		t.Errorf("team 1 starters = %v, want %v", got, want)
	}
	if got, want := ids(rs[0].Bench), []string{"BN=1234"}; !reflect.DeepEqual(got, want) {
		t.Errorf("team 1 bench = %v, want %v", got, want)
	}
	if got, want := ids(rs[0].Reserve), []string{"IR=5678", "TAXI=8888"}; !reflect.DeepEqual(got, want) {
		t.Errorf("team 1 reserve = %v, want %v", got, want)
	}
	if got, want := ids(rs[1].Starters), []string{"QB=4046", "RB=4035", "WR=2133", "DEF=KC"}; !reflect.DeepEqual(got, want) {
		t.Errorf("team 2 starters (empty FLEX skipped) = %v, want %v", got, want)
	}
	if rs[1].Reserve == nil {
		t.Error("Reserve should be non-nil even when empty")
	}
	if rs[0].Starters[0].Ref.Platform != domain.PlatformSleeper {
		t.Error("refs should carry the sleeper platform")
	}
}

func TestMatchups(t *testing.T) {
	ms, err := newTestClient(t).Matchups(context.Background(), "111", 2026, 3, slots)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 {
		t.Fatalf("got %d matchups, want 1 (null matchup_id skipped)", len(ms))
	}
	m := ms[0]
	if m.Week != 3 || m.Home.TeamID != "1" || m.Home.Points != 112.34 || m.Away.TeamID != "2" || m.Away.Points != 98.5 {
		t.Fatalf("matchup = %+v", m)
	}
	if len(m.Home.Roster.Starters) != 5 || len(m.Home.Roster.Bench) != 1 {
		t.Fatalf("home roster = %+v", m.Home.Roster)
	}
}

func TestPlayers(t *testing.T) {
	ps, err := newTestClient(t).Players(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]domain.Player{}
	for _, p := range ps {
		byID[*p.ID] = p
	}
	if p := byID["4046"]; p.Name != "Patrick Mahomes" || p.PlatformIDs["espn"] != "3139477" || p.PlatformIDs["sleeper"] != "4046" {
		t.Errorf("numeric espn_id: %+v", p)
	}
	if p := byID["6794"]; p.PlatformIDs["espn"] != "4262921" {
		t.Errorf("string espn_id: %+v", p)
	}
	if p := byID["KC"]; p.Name != "Kansas City Chiefs" || p.Position != "DEF" {
		t.Errorf("defense: %+v", p)
	} else if _, ok := p.PlatformIDs["espn"]; ok {
		t.Errorf("null espn_id should be omitted: %+v", p.PlatformIDs)
	}
}

func TestWeekStats(t *testing.T) {
	lines, err := newTestClient(t).WeekStats(context.Background(), 2026, 3)
	if err != nil {
		t.Fatal(err)
	}
	l := lines["6794"]
	if len(lines) != 2 || l.Stats["rec"] != 6 || l.Opponent != "GB" || l.Week != 3 || l.Season != 2026 {
		t.Fatalf("WeekStats = %+v", lines)
	}
}

func TestPlayerGamelog(t *testing.T) {
	lines, err := newTestClient(t).PlayerGamelog(context.Background(), "6794", 2026)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].Week != 1 || lines[1].Week != 3 || lines[1].Stats["rec_td"] != 1 {
		t.Fatalf("gamelog = %+v (bye week should be skipped, sorted by week)", lines)
	}
}

func mineIDs(ts []domain.Team) []string {
	out := []string{}
	for _, t := range ts {
		if t.Mine {
			out = append(out, t.ID)
		}
	}
	return out
}

func TestLeagueMineViaCoOwner(t *testing.T) {
	l, err := newTestClient(t).League(context.Background(), "444", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := mineIDs(l.Teams); !reflect.DeepEqual(got, []string{"1"}) {
		t.Fatalf("mine teams = %v, want [1]", got)
	}
}

func TestLeagueMineNone(t *testing.T) {
	l, err := newTestClient(t).League(context.Background(), "555", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := mineIDs(l.Teams); len(got) != 0 {
		t.Fatalf("mine teams = %v, want none", got)
	}
}

func TestLeagueUnknownUserFails(t *testing.T) {
	c, _ := newCountingClient(t, "ghost")
	_, err := c.League(context.Background(), "111", 0)
	var ue *domain.UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v, want *domain.UpstreamError", err)
	}
}

func TestUserIDLookedUpOnce(t *testing.T) {
	c, hits := newCountingClient(t, "varun")
	ctx := context.Background()
	if _, err := c.League(ctx, "111", 0); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.League(ctx, "111", 0); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if _, err := c.ListLeagues(ctx, 2026); err != nil {
		t.Fatal(err)
	}
	if n := hits("/v1/user/varun"); n != 1 {
		t.Fatalf("user lookups = %d, want 1", n)
	}
}
