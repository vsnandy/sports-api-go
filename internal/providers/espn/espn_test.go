package espn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vsnandy/sports-api-go/internal/domain"
	"github.com/vsnandy/sports-api-go/internal/providers/httpx"
)

type recorder struct {
	mu      sync.Mutex
	queries []url.Values
}

func (r *recorder) last() url.Values {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.queries[len(r.queries)-1]
}

func newTestClient(t *testing.T, s2 string, leagueIDs ...string) (*Client, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.queries = append(rec.queries, r.URL.Query())
		rec.mu.Unlock()
		if r.Header.Get("Cookie") != "espn_s2=S2; SWID={SWID}" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/seasons/2026/segments/0/leagues/123456":
			http.ServeFile(w, r, "testdata/league.json")
		case "/seasons/2026/segments/0/leagues/401":
			w.WriteHeader(http.StatusUnauthorized)
		case "/seasons/2026/segments/0/leagues/777":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<html>please log in</html>")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return New(httpx.New("espn", 2*time.Second), srv.URL, s2, "{SWID}", leagueIDs), rec
}

func entryIDs(es []domain.RosterEntryRef) []string {
	out := []string{}
	for _, e := range es {
		out = append(out, e.Slot+"="+e.Ref.ID)
	}
	return out
}

func TestLeague(t *testing.T) {
	c, rec := newTestClient(t, "S2", "123456")
	l, err := c.League(context.Background(), "123456", 2026)
	if err != nil {
		t.Fatal(err)
	}
	if l.ID != "espn:123456" || l.Name != "Office League" || l.Season != 2026 || l.Platform != domain.PlatformESPN {
		t.Fatalf("header = %+v", l)
	}
	wantTeams := []domain.Team{{ID: "1", Name: "Team Varun", Owner: "varun"}, {ID: "2", Name: "Alex's Aces", Owner: "alex"}}
	if !reflect.DeepEqual(l.Teams, wantTeams) {
		t.Errorf("Teams = %+v", l.Teams)
	}
	wantSlots := []string{"QB", "RB", "RB", "WR", "WR", "TE", "FLEX", "DEF", "K", "BN", "BN", "BN", "BN", "BN", "BN", "IR"}
	if !reflect.DeepEqual(l.RosterSlots, wantSlots) {
		t.Errorf("RosterSlots = %v", l.RosterSlots)
	}
	wantRules := domain.ScoringRules{"pass_yd": 0.04, "pass_td": 4, "rec": 1, "fgm_0_19": 3, "fgm_20_29": 3, "fgm_30_39": 3}
	if !reflect.DeepEqual(l.Scoring, wantRules) {
		t.Errorf("Scoring = %v", l.Scoring)
	}
	wantUnsupported := []string{"espn stat 53 position overrides", "espn stat 92 (1 pts)"}
	if !reflect.DeepEqual(l.UnsupportedRules, wantUnsupported) {
		t.Errorf("UnsupportedRules = %v", l.UnsupportedRules)
	}
	if v := rec.last()["view"]; !slices.Contains(v, "mSettings") || !slices.Contains(v, "mTeam") {
		t.Errorf("views = %v", v)
	}
}

func TestRosters(t *testing.T) {
	c, _ := newTestClient(t, "S2")
	rs, err := c.Rosters(context.Background(), "123456", 2026, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 {
		t.Fatalf("got %d rosters", len(rs))
	}
	r := rs[0]
	if got, want := entryIDs(r.Starters), []string{"QB=3139477", "DEF=-16012"}; !reflect.DeepEqual(got, want) {
		t.Errorf("starters = %v, want %v (sorted by slot)", got, want)
	}
	if got, want := entryIDs(r.Bench), []string{"BN=4262921"}; !reflect.DeepEqual(got, want) {
		t.Errorf("bench = %v", got)
	}
	if got, want := entryIDs(r.Reserve), []string{"IR=9999999"}; !reflect.DeepEqual(got, want) {
		t.Errorf("reserve = %v", got)
	}
	dst := r.Starters[1].Ref
	if dst.Platform != domain.PlatformESPN || dst.Position != "DEF" || dst.NFLTeam != "KC" || dst.Name != "Chiefs D/ST" {
		t.Errorf("D/ST ref = %+v", dst)
	}
	if fa := r.Reserve[0].Ref; fa.NFLTeam != "" || fa.Position != "RB" {
		t.Errorf("free agent ref = %+v", fa)
	}
	if rs[1].Starters == nil || rs[1].Bench == nil || rs[1].Reserve == nil {
		t.Error("empty roster slices should be non-nil")
	}
}

func TestMatchups(t *testing.T) {
	c, rec := newTestClient(t, "S2")
	ms, err := c.Matchups(context.Background(), "123456", 2026, 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 {
		t.Fatalf("got %d matchups, want 1 (other weeks and byes skipped)", len(ms))
	}
	m := ms[0]
	if m.Week != 3 || m.Home.TeamID != "2" || m.Home.Points != 101.2 || m.Away.TeamID != "1" || m.Away.Points != 120.5 {
		t.Fatalf("matchup = %+v", m)
	}
	if got := entryIDs(m.Away.Roster.Starters); !reflect.DeepEqual(got, []string{"QB=3139477"}) {
		t.Errorf("away starters = %v", got)
	}
	q := rec.last()
	if q.Get("scoringPeriodId") != "3" || !slices.Contains(q["view"], "mMatchupScore") {
		t.Errorf("query = %v", q)
	}
}

func TestListLeaguesSkipsMissingSeasons(t *testing.T) {
	c, _ := newTestClient(t, "S2", "123456", "555")
	ls, err := c.ListLeagues(context.Background(), 2026)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.LeagueSummary{{ID: "espn:123456", Platform: domain.PlatformESPN, Season: 2026, Name: "Office League"}}
	if !reflect.DeepEqual(ls, want) {
		t.Fatalf("ListLeagues = %+v", ls)
	}
}

func TestAuthFailures(t *testing.T) {
	ctx := context.Background()
	good, _ := newTestClient(t, "S2")
	for _, id := range []string{"401", "777"} {
		if _, err := good.League(ctx, id, 2026); !errors.Is(err, domain.ErrESPNAuth) {
			t.Errorf("league %s: err = %v, want ErrESPNAuth", id, err)
		}
	}
	bad, _ := newTestClient(t, "WRONG", "123456")
	if _, err := bad.ListLeagues(ctx, 2026); !errors.Is(err, domain.ErrESPNAuth) {
		t.Errorf("wrong cookie: err = %v, want ErrESPNAuth", err)
	}
}

func TestSecretsNotInErrors(t *testing.T) {
	bad, _ := newTestClient(t, "SUPERSECRET", "123456")
	_, err := bad.ListLeagues(context.Background(), 2026)
	if err == nil || strings.Contains(err.Error(), "SUPERSECRET") {
		t.Fatalf("err = %v; must exist and must not include the cookie", err)
	}
}
