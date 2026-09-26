package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

var ctx = context.Background()

func wantInvalid(t *testing.T, err error, param string) {
	t.Helper()
	var ip *domain.InvalidParamError
	if !errors.As(err, &ip) || ip.Param != param {
		t.Fatalf("err = %v, want InvalidParamError for %q", err, param)
	}
}

func TestListLeaguesMergesPlatforms(t *testing.T) {
	f := newFixture()
	ls, meta, err := f.svc.ListLeagues(ctx, 0)
	if err != nil || len(ls) != 2 || ls[0].ID != "espn:123456" || ls[1].ID != "sleeper:111" {
		t.Fatalf("ListLeagues = %+v, %v", ls, err)
	}
	if meta.Season != 2026 || len(meta.Warnings) != 0 {
		t.Fatalf("meta = %+v", meta)
	}
}

func TestListLeaguesDegradesWhenOnePlatformFails(t *testing.T) {
	f := newFixture()
	f.espn.listErr = fmt.Errorf("fetch: %w", domain.ErrESPNAuth)
	ls, meta, err := f.svc.ListLeagues(ctx, 0)
	if err != nil || len(ls) != 1 || ls[0].ID != "sleeper:111" {
		t.Fatalf("ListLeagues = %+v, %v", ls, err)
	}
	if len(meta.Warnings) != 1 || !strings.HasPrefix(meta.Warnings[0], "espn_leagues_unavailable:") {
		t.Fatalf("warnings = %v", meta.Warnings)
	}
}

func TestListLeaguesFailsWhenAllFail(t *testing.T) {
	f := newFixture()
	f.espn.listErr = domain.ErrESPNAuth
	f.sleeper.listErr = domain.ErrUpstreamTimeout
	if _, _, err := f.svc.ListLeagues(ctx, 0); !errors.Is(err, domain.ErrESPNAuth) {
		t.Fatalf("err = %v, want first failure", err)
	}
}

func TestLeagueInvalidIDDoesNotCallProviders(t *testing.T) {
	f := newFixture()
	_, _, err := f.svc.League(ctx, "espn:12/../3", 0)
	wantInvalid(t, err, "leagueId")
	if f.espn.Calls("list")+f.espn.Calls("league") != 0 {
		t.Fatal("invalid IDs must not reach providers")
	}
}

func TestLeagueNotOwned(t *testing.T) {
	f := newFixture()
	if _, _, err := f.svc.League(ctx, "espn:999", 0); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if f.espn.Calls("league") != 0 {
		t.Fatal("unowned league must not be fetched")
	}
}

func TestLeagueCached(t *testing.T) {
	f := newFixture()
	for range 2 {
		l, meta, err := f.svc.League(ctx, "espn:123456", 0)
		if err != nil || l.Name != "Office" || meta.Season != 2026 {
			t.Fatalf("League = %+v, %+v, %v", l, meta, err)
		}
	}
	if f.espn.Calls("league") != 1 || f.espn.Calls("list") != 1 {
		t.Fatalf("calls = %v, want one of each", f.espn.calls)
	}
}

func TestRostersResolvePlayers(t *testing.T) {
	f := newFixture()
	rs, meta, err := f.svc.Rosters(ctx, "espn:123456", 0)
	if err != nil || len(rs) != 1 {
		t.Fatalf("Rosters = %+v, %v", rs, err)
	}
	r := rs[0]
	if *r.Starters[0].Player.ID != "4046" || *r.Starters[1].Player.ID != "KC" || r.Starters[1].Slot != "DEF" {
		t.Fatalf("starters = %+v", r.Starters)
	}
	rookie := r.Bench[0].Player
	if rookie.ID != nil || rookie.Name != "Rookie Guy" || rookie.PlatformIDs["espn"] != "9999999" {
		t.Fatalf("unmapped player = %+v", rookie)
	}
	if r.Reserve == nil {
		t.Fatal("Reserve should be non-nil")
	}
	if len(meta.Warnings) != 1 || !strings.Contains(meta.Warnings[0], "unmapped_player: espn:9999999") {
		t.Fatalf("warnings = %v", meta.Warnings)
	}
}

func TestMatchupsWithStats(t *testing.T) {
	f := newFixture()
	ms, meta, err := f.svc.Matchups(ctx, "sleeper:111", 0, 0, true)
	if err != nil || len(ms) != 1 {
		t.Fatalf("Matchups = %+v, %v", ms, err)
	}
	if meta.Week != 3 || meta.Season != 2026 || len(meta.Warnings) != 0 {
		t.Fatalf("meta = %+v", meta)
	}
	home := ms[0].Home
	if home.Points != 112.34 || *home.Roster.Starters[0].Points != 18 || *home.Roster.Bench[0].Points != 20.5 {
		t.Fatalf("home = %+v", home)
	}
	if home.Roster.Starters[0].Stats["pass_td"] != 2 {
		t.Fatalf("stats not attached: %+v", home.Roster.Starters[0])
	}
}

func TestMatchupsWithoutStats(t *testing.T) {
	f := newFixture()
	ms, _, err := f.svc.Matchups(ctx, "sleeper:111", 0, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	e := ms[0].Home.Roster.Starters[0]
	if e.Stats != nil || e.Points != nil || f.stats.weekCalls != 0 {
		t.Fatalf("entry = %+v, weekCalls = %d", e, f.stats.weekCalls)
	}
}

func TestMatchupsStatsFailureDegrades(t *testing.T) {
	f := newFixture()
	f.stats.weekErr = &domain.UpstreamError{Provider: "sleeper", Status: 500}
	ms, meta, err := f.svc.Matchups(ctx, "sleeper:111", 0, 3, true)
	if err != nil {
		t.Fatalf("stats failure should not fail the request: %v", err)
	}
	if ms[0].Home.Roster.Starters[0].Points != nil {
		t.Fatal("points should be nil when stats are unavailable")
	}
	if len(meta.Warnings) != 1 || !strings.HasPrefix(meta.Warnings[0], "stats_unavailable:") {
		t.Fatalf("warnings = %v", meta.Warnings)
	}
}

func TestMatchupsPrimaryFailure(t *testing.T) {
	f := newFixture()
	f.sleeper.matchupsErr = domain.ErrUpstreamTimeout
	if _, _, err := f.svc.Matchups(ctx, "sleeper:111", 0, 3, true); !errors.Is(err, domain.ErrUpstreamTimeout) {
		t.Fatalf("err = %v, want timeout", err)
	}
}

func TestMatchupsWeekValidation(t *testing.T) {
	f := newFixture()
	_, _, err := f.svc.Matchups(ctx, "sleeper:111", 0, 19, false)
	wantInvalid(t, err, "week")
}

func TestMatchupsOffseasonDefaultsToWeek1(t *testing.T) {
	f := newFixture()
	f.state.st.Week = 0
	_, meta, err := f.svc.Matchups(ctx, "sleeper:111", 0, 0, false)
	if err != nil || meta.Week != 1 {
		t.Fatalf("meta = %+v, err = %v", meta, err)
	}
}

func TestMatchupsPastSeasonRequiresWeek(t *testing.T) {
	f := newFixture()
	f.sleeper.league.Season = 2025
	_, _, err := f.svc.Matchups(ctx, "sleeper:111", 2025, 0, false)
	wantInvalid(t, err, "week")
}

func TestMatchupsCachedWithinTTL(t *testing.T) {
	f := newFixture()
	for range 2 {
		if _, _, err := f.svc.Matchups(ctx, "sleeper:111", 0, 3, false); err != nil {
			t.Fatal(err)
		}
	}
	if f.sleeper.Calls("matchups") != 1 {
		t.Fatalf("matchups calls = %d, want 1", f.sleeper.Calls("matchups"))
	}
	f.now = f.now.Add(2 * time.Minute)
	f.svc.Matchups(ctx, "sleeper:111", 0, 3, false)
	if f.sleeper.Calls("matchups") != 2 {
		t.Fatal("matchups should refetch after the 1 minute TTL")
	}
}

func TestPlayer(t *testing.T) {
	f := newFixture()
	if p, _, err := f.svc.Player(ctx, "6794"); err != nil || p.Name != "Justin Jefferson" {
		t.Fatalf("Player = %+v, %v", p, err)
	}
	if _, _, err := f.svc.Player(ctx, "0000"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown player err = %v", err)
	}
	_, _, err := f.svc.Player(ctx, "../x")
	wantInvalid(t, err, "playerId")
}

func TestGamelogPreset(t *testing.T) {
	f := newFixture()
	es, meta, err := f.svc.Gamelog(ctx, "6794", 0, "ppr")
	if err != nil || len(es) != 2 || meta.Season != 2026 {
		t.Fatalf("Gamelog = %+v, %+v, %v", es, meta, err)
	}
	if *es[0].Points != 10.9 || *es[1].Points != 20.5 {
		t.Fatalf("points = %v, %v", *es[0].Points, *es[1].Points)
	}
}

func TestGamelogLeagueScoringAddsWarnings(t *testing.T) {
	f := newFixture()
	es, meta, err := f.svc.Gamelog(ctx, "6794", 0, "espn:123456")
	if err != nil {
		t.Fatal(err)
	}
	if *es[0].Points != 8.4 {
		t.Fatalf("week 1 points = %v, want 8.4", *es[0].Points)
	}
	if len(meta.Warnings) != 1 || meta.Warnings[0] != "unsupported_rule: espn stat 92 (1 pts)" {
		t.Fatalf("warnings = %v", meta.Warnings)
	}
}

func TestGamelogRawWhenNoScoring(t *testing.T) {
	f := newFixture()
	es, _, err := f.svc.Gamelog(ctx, "6794", 0, "")
	if err != nil || es[0].Points != nil || es[0].Stats["rec"] != 5 {
		t.Fatalf("Gamelog = %+v, %v", es, err)
	}
}

func TestGamelogBadScoring(t *testing.T) {
	f := newFixture()
	_, _, err := f.svc.Gamelog(ctx, "6794", 0, "bogus")
	wantInvalid(t, err, "scoring")
}

func TestGamelogUnknownPlayer(t *testing.T) {
	f := newFixture()
	if _, _, err := f.svc.Gamelog(ctx, "0000", 0, "ppr"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
