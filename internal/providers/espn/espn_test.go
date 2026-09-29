package espn

import (
	"context"
	"encoding/json"
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
		if r.Header.Get("Cookie") != "espn_s2=S2; SWID=aaa" {
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
		case "/seasons/2026/segments/0/leagues/888":
			fmt.Fprint(w, `{"settings": "oops"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return New(httpx.New("espn", 2*time.Second), srv.URL, s2, "aaa", leagueIDs), rec
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
	wantTeams := []domain.Team{{ID: "1", Name: "Team Varun", Owner: "varun", Mine: true}, {ID: "2", Name: "Alex's Aces", Owner: "alex"}}
	if !reflect.DeepEqual(l.Teams, wantTeams) {
		t.Errorf("Teams = %+v", l.Teams)
	}
	wantSlots := []string{"QB", "RB", "RB", "WR", "WR", "TE", "FLEX", "DEF", "K", "BN", "BN", "BN", "BN", "BN", "BN", "IR"}
	if !reflect.DeepEqual(l.RosterSlots, wantSlots) {
		t.Errorf("RosterSlots = %v", l.RosterSlots)
	}
	wantRules := domain.ScoringRules{"pass_yd": 0.04, "pass_td": 4, "rec": 1, "fgm_0_19": 3, "fgm_20_29": 3, "fgm_30_39": 3, "espn_pa_14_17": 1}
	if !reflect.DeepEqual(l.Scoring, wantRules) {
		t.Errorf("Scoring = %v", l.Scoring)
	}
	if !reflect.DeepEqual(l.ScoringByPosition, map[string]domain.ScoringRules{"TE": {"rec": 1.5}}) {
		t.Errorf("ScoringByPosition = %v", l.ScoringByPosition)
	}
	seventeen := 17.0
	wantDerived := []domain.DerivedStat{{Key: "espn_pa_14_17", From: "pts_allow", Min: 14, Max: &seventeen}}
	if !reflect.DeepEqual(l.DerivedStats, wantDerived) {
		t.Errorf("DerivedStats = %+v", l.DerivedStats)
	}
	if l.UnsupportedRules == nil || len(l.UnsupportedRules) != 0 {
		t.Errorf("UnsupportedRules = %#v, want empty non-nil", l.UnsupportedRules)
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
	// Home is a finalized side (totalPoints); away is ESPN's shape for a period that is not
	// final yet: totalPoints 0 with the running score in totalPointsLive.
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

func TestWrongShapeBodyIsNotAuthFailure(t *testing.T) {
	c, _ := newTestClient(t, "S2")
	_, err := c.League(context.Background(), "888", 2026)
	if err == nil {
		t.Fatal("want an error for the malformed body")
	}
	if errors.Is(err, domain.ErrESPNAuth) {
		t.Fatalf("err = %v, want a decode error, not ErrESPNAuth", err)
	}
}

func TestSecretsNotInErrors(t *testing.T) {
	bad, _ := newTestClient(t, "SUPERSECRET", "123456")
	_, err := bad.ListLeagues(context.Background(), 2026)
	if err == nil || strings.Contains(err.Error(), "SUPERSECRET") {
		t.Fatalf("err = %v; must exist and must not include the cookie", err)
	}
}

func TestMatchupsPlatformPoints(t *testing.T) {
	c, _ := newTestClient(t, "S2")
	ms, err := c.Matchups(context.Background(), "123456", 2026, 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	away := ms[0].Away.Roster
	qb := away.Starters[0]
	if qb.PlatformPoints == nil || *qb.PlatformPoints != 24.5 {
		t.Fatalf("QB PlatformPoints = %v, want 24.5 from the actual week-3 row (not projection 99.9, week 2, or season)", qb.PlatformPoints)
	}
	wantBreakdown := map[string]float64{"pass_yd": 10, "pass_td": 12, "pass_int": -2, "rush_yd": 4.5}
	if !reflect.DeepEqual(qb.PlatformBreakdown, wantBreakdown) {
		t.Fatalf("QB PlatformBreakdown = %v, want %v", qb.PlatformBreakdown, wantBreakdown)
	}
	if away.Bench[0].PlatformBreakdown != nil {
		t.Fatal("no actual row → no breakdown")
	}
	if len(away.Bench) != 1 || away.Bench[0].PlatformPoints != nil {
		t.Fatalf("bench entry with only a projection row must have nil PlatformPoints: %+v", away.Bench)
	}
}

func TestRostersHaveNoPlatformPoints(t *testing.T) {
	c, _ := newTestClient(t, "S2")
	rs, err := c.Rosters(context.Background(), "123456", 2026, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range rs[0].Starters {
		if e.PlatformPoints != nil {
			t.Fatalf("rosters have no week; PlatformPoints = %v", *e.PlatformPoints)
		}
	}
}

func TestWeekPlayerPoints(t *testing.T) {
	c, _ := newTestClient(t, "S2")
	pts, err := c.WeekPlayerPoints(context.Background(), "123456", 2026, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 1 {
		t.Fatalf("got %d players, want 1 (only entries with an actual week-3 row)", len(pts))
	}
	p := pts[0]
	if p.Ref.ID != "3139477" || p.Ref.Name != "Patrick Mahomes" || p.Ref.Position != "QB" || p.Total != 24.5 {
		t.Fatalf("player = %+v", p)
	}
	if !reflect.DeepEqual(p.ByStat, map[int]float64{3: 10, 4: 12, 20: -2, 24: 4.5}) {
		t.Fatalf("ByStat = %v", p.ByStat)
	}
}

func TestNormSWID(t *testing.T) {
	for _, s := range []string{"{AAA-1}", "aaa-1", " {aAa-1} ", "AAA-1"} {
		if got := normSWID(s); got != "aaa-1" {
			t.Errorf("normSWID(%q) = %q, want aaa-1", s, got)
		}
	}
}

func TestLeagueDoesNotExposeSWID(t *testing.T) {
	c, _ := newTestClient(t, "S2", "123456")
	l, err := c.League(context.Background(), "123456", 2026)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(b)), "aaa") {
		t.Errorf("league JSON contains the SWID: %s", b)
	}
	if !strings.Contains(string(b), `"mine":true`) {
		t.Errorf("league JSON missing mine flag: %s", b)
	}
}
