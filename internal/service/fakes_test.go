package service

import (
	"context"
	"sync"
	"time"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

type fakeProvider struct {
	mu          sync.Mutex
	platform    domain.Platform
	leagues     []domain.LeagueSummary
	listErr     error
	league      domain.League
	rosters     []domain.RosterRef
	matchups    []domain.MatchupRef
	matchupsErr error
	calls       map[string]int
}

func (f *fakeProvider) count(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[name]++
}

func (f *fakeProvider) Calls(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[name]
}

func (f *fakeProvider) Platform() domain.Platform { return f.platform }

func (f *fakeProvider) ListLeagues(context.Context, int) ([]domain.LeagueSummary, error) {
	f.count("list")
	return f.leagues, f.listErr
}

func (f *fakeProvider) League(context.Context, string, int) (domain.League, error) {
	f.count("league")
	return f.league, nil
}

func (f *fakeProvider) Rosters(context.Context, string, int, []string) ([]domain.RosterRef, error) {
	f.count("rosters")
	return f.rosters, nil
}

func (f *fakeProvider) Matchups(context.Context, string, int, int, []string) ([]domain.MatchupRef, error) {
	f.count("matchups")
	return f.matchups, f.matchupsErr
}

type fakeStats struct {
	mu           sync.Mutex
	week         map[string]domain.StatLine
	weekErr      error
	gamelog      []domain.StatLine
	weekCalls    int
	gamelogCalls int
}

func (f *fakeStats) WeekStats(context.Context, int, int) (map[string]domain.StatLine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.weekCalls++
	return f.week, f.weekErr
}

func (f *fakeStats) PlayerGamelog(context.Context, string, int) ([]domain.StatLine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gamelogCalls++
	return f.gamelog, nil
}

type fakeState struct {
	mu    sync.Mutex
	st    domain.SeasonState
	calls int
}

func (f *fakeState) State(context.Context) (domain.SeasonState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.st, nil
}

type fakeIndex struct {
	mu       sync.Mutex
	byID     map[string]domain.Player
	byESPN   map[string]string
	getCalls int
}

func (f *fakeIndex) Get(_ context.Context, id string) (domain.Player, bool, error) {
	f.mu.Lock()
	f.getCalls++
	f.mu.Unlock()
	p, ok := f.byID[id]
	return p, ok, nil
}

func (f *fakeIndex) Resolve(ctx context.Context, ref domain.PlayerRef) (domain.Player, bool, error) {
	switch {
	case ref.Platform == domain.PlatformSleeper:
		return f.Get(ctx, ref.ID)
	case ref.Position == "DEF":
		return f.Get(ctx, ref.NFLTeam)
	}
	f.mu.Lock()
	id, ok := f.byESPN[ref.ID]
	f.mu.Unlock()
	if !ok {
		return domain.Player{}, false, nil
	}
	return f.Get(ctx, id)
}

func player(id, name, pos, team string, ids map[string]string) domain.Player {
	return domain.Player{ID: &id, Name: name, Position: pos, NFLTeam: team, PlatformIDs: ids}
}

func sleeperRef(slot, id string) domain.RosterEntryRef {
	return domain.RosterEntryRef{Slot: slot, Ref: domain.PlayerRef{Platform: domain.PlatformSleeper, ID: id}}
}

func espnRef(slot, id, name, pos, team string) domain.RosterEntryRef {
	return domain.RosterEntryRef{Slot: slot, Ref: domain.PlayerRef{Platform: domain.PlatformESPN, ID: id, Name: name, Position: pos, NFLTeam: team}}
}

func ptr(v float64) *float64 { return &v }

func withPlatformPoints(r domain.RosterEntryRef, pts float64) domain.RosterEntryRef {
	r.PlatformPoints = ptr(pts)
	return r
}

type fixture struct {
	svc     *Service
	sleeper *fakeProvider
	espn    *fakeProvider
	stats   *fakeStats
	state   *fakeState
	index   *fakeIndex
	now     time.Time
}

func newFixture() *fixture {
	f := &fixture{now: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}
	f.sleeper = &fakeProvider{
		platform: domain.PlatformSleeper,
		leagues:  []domain.LeagueSummary{{ID: "sleeper:111", Platform: domain.PlatformSleeper, Season: 2026, Name: "Dynasty"}},
		league: domain.League{
			ID: "sleeper:111", Platform: domain.PlatformSleeper, Sport: domain.SportNFL, Season: 2026, Name: "Dynasty",
			Scoring:          domain.ScoringRules{"pass_yd": 0.04, "pass_td": 4, "rec": 1, "rec_yd": 0.1, "rec_td": 6},
			UnsupportedRules: []string{}, RosterSlots: []string{"QB", "WR", "BN"},
		},
		matchups: []domain.MatchupRef{{
			Week: 3,
			Home: domain.MatchupSideRef{TeamID: "1", Points: 112.34, Roster: domain.RosterRef{
				TeamID:   "1",
				Starters: []domain.RosterEntryRef{sleeperRef("QB", "4046")},
				Bench:    []domain.RosterEntryRef{sleeperRef("BN", "6794")},
			}},
			Away: domain.MatchupSideRef{TeamID: "2", Points: 98.5, Roster: domain.RosterRef{TeamID: "2"}},
		}},
	}
	f.espn = &fakeProvider{
		platform: domain.PlatformESPN,
		leagues:  []domain.LeagueSummary{{ID: "espn:123456", Platform: domain.PlatformESPN, Season: 2026, Name: "Office"}},
		league: domain.League{
			ID: "espn:123456", Platform: domain.PlatformESPN, Sport: domain.SportNFL, Season: 2026, Name: "Office",
			Scoring:          domain.ScoringRules{"rec": 0.5, "rec_yd": 0.1},
			UnsupportedRules: []string{"espn stat 92 (1 pts)"},
		},
		rosters: []domain.RosterRef{{
			TeamID: "1",
			Starters: []domain.RosterEntryRef{
				espnRef("QB", "3139477", "Patrick Mahomes", "QB", "KC"),
				espnRef("DEF", "-16012", "Chiefs D/ST", "DEF", "KC"),
			},
			Bench: []domain.RosterEntryRef{espnRef("BN", "9999999", "Rookie Guy", "RB", "")},
		}},
	}
	f.stats = &fakeStats{
		week: map[string]domain.StatLine{
			"4046": {PlayerID: "4046", Season: 2026, Week: 3, Opponent: "ATL", Stats: map[string]float64{"pass_yd": 250, "pass_td": 2}},
			"6794": {PlayerID: "6794", Season: 2026, Week: 3, Opponent: "GB", Stats: map[string]float64{"rec": 6, "rec_yd": 85, "rec_td": 1}},
		},
		gamelog: []domain.StatLine{
			{PlayerID: "6794", Season: 2026, Week: 1, Opponent: "NYG", Stats: map[string]float64{"rec": 5, "rec_yd": 59}},
			{PlayerID: "6794", Season: 2026, Week: 3, Opponent: "GB", Stats: map[string]float64{"rec": 6, "rec_yd": 85, "rec_td": 1}},
		},
	}
	f.state = &fakeState{st: domain.SeasonState{Season: 2026, Week: 3}}
	f.index = &fakeIndex{
		byID: map[string]domain.Player{
			"4046": player("4046", "Patrick Mahomes", "QB", "KC", map[string]string{"sleeper": "4046", "espn": "3139477"}),
			"6794": player("6794", "Justin Jefferson", "WR", "MIN", map[string]string{"sleeper": "6794", "espn": "4262921"}),
			"KC":   player("KC", "Kansas City Chiefs", "DEF", "KC", map[string]string{"sleeper": "KC"}),
		},
		byESPN: map[string]string{"3139477": "4046", "4262921": "6794"},
	}
	f.svc = New([]LeagueProvider{f.espn, f.sleeper}, f.stats, f.state, f.index, func() time.Time { return f.now })
	return f
}

func withPlatformBreakdown(r domain.RosterEntryRef, b map[string]float64) domain.RosterEntryRef {
	r.PlatformBreakdown = b
	return r
}
