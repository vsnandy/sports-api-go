// Package service joins league data from providers with player identities, stats,
// and scoring. It owns caching and ownership checks.
package service

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/vsnandy/sports-api-go/internal/cache"
	"github.com/vsnandy/sports-api-go/internal/domain"
)

type LeagueProvider interface {
	Platform() domain.Platform
	ListLeagues(ctx context.Context, season int) ([]domain.LeagueSummary, error)
	League(ctx context.Context, nativeID string, season int) (domain.League, error)
	Rosters(ctx context.Context, nativeID string, season int, slots []string) ([]domain.RosterRef, error)
	Matchups(ctx context.Context, nativeID string, season, week int, slots []string) ([]domain.MatchupRef, error)
}

type StatsProvider interface {
	WeekStats(ctx context.Context, season, week int) (map[string]domain.StatLine, error)
	PlayerGamelog(ctx context.Context, playerID string, season int) ([]domain.StatLine, error)
}

type StateProvider interface {
	State(ctx context.Context) (domain.SeasonState, error)
}

type PlayerIndex interface {
	Get(ctx context.Context, id string) (domain.Player, bool, error)
	Resolve(ctx context.Context, ref domain.PlayerRef) (domain.Player, bool, error)
}

const (
	ttlState      = 5 * time.Minute
	ttlLeague     = time.Hour
	ttlRosters    = 5 * time.Minute
	ttlMatchups   = time.Minute
	ttlStatsFinal = 12 * time.Hour
	ttlStatsLive  = 5 * time.Minute
)

type Service struct {
	providers map[domain.Platform]LeagueProvider
	order     []domain.Platform
	stats     StatsProvider
	stateProv StateProvider
	players   PlayerIndex

	stateCache    *cache.Cache[domain.SeasonState]
	listCache     *cache.Cache[[]domain.LeagueSummary]
	leagueCache   *cache.Cache[domain.League]
	rosterCache   *cache.Cache[[]domain.RosterRef]
	matchupCache  *cache.Cache[[]domain.MatchupRef]
	weekStatCache *cache.Cache[map[string]domain.StatLine]
	gamelogCache  *cache.Cache[[]domain.StatLine]
}

func New(providers []LeagueProvider, stats StatsProvider, state StateProvider, players PlayerIndex, now func() time.Time) *Service {
	s := &Service{
		providers: map[domain.Platform]LeagueProvider{},
		stats:     stats, stateProv: state, players: players,
		stateCache:    cache.New[domain.SeasonState](now),
		listCache:     cache.New[[]domain.LeagueSummary](now),
		leagueCache:   cache.New[domain.League](now),
		rosterCache:   cache.New[[]domain.RosterRef](now),
		matchupCache:  cache.New[[]domain.MatchupRef](now),
		weekStatCache: cache.New[map[string]domain.StatLine](now),
		gamelogCache:  cache.New[[]domain.StatLine](now),
	}
	for _, p := range providers {
		s.providers[p.Platform()] = p
		s.order = append(s.order, p.Platform())
	}
	return s
}

func (s *Service) state(ctx context.Context) (domain.SeasonState, error) {
	return s.stateCache.GetOrLoad(ctx, "nfl", ttlState, s.stateProv.State)
}

func (s *Service) seasonOrCurrent(ctx context.Context, season int) (int, domain.SeasonState, error) {
	st, err := s.state(ctx)
	if err != nil {
		return 0, domain.SeasonState{}, err
	}
	if season == 0 {
		season = st.Season
	}
	return season, st, nil
}

func (s *Service) listFor(ctx context.Context, prov LeagueProvider, season int) ([]domain.LeagueSummary, error) {
	return s.listCache.GetOrLoad(ctx, fmt.Sprintf("%s/%d", prov.Platform(), season), ttlLeague,
		func(ctx context.Context) ([]domain.LeagueSummary, error) { return prov.ListLeagues(ctx, season) })
}

// resolveLeague validates leagueID, checks it belongs to the owner for season, and
// returns its provider, native ID, and settings.
func (s *Service) resolveLeague(ctx context.Context, leagueID string, season int) (LeagueProvider, string, domain.League, error) {
	platform, native, err := domain.ParseLeagueID(leagueID)
	if err != nil {
		return nil, "", domain.League{}, err
	}
	prov, ok := s.providers[platform]
	if !ok {
		return nil, "", domain.League{}, fmt.Errorf("league %s: %w", leagueID, domain.ErrNotFound)
	}
	season, _, err = s.seasonOrCurrent(ctx, season)
	if err != nil {
		return nil, "", domain.League{}, err
	}
	owned, err := s.listFor(ctx, prov, season)
	if err != nil {
		return nil, "", domain.League{}, err
	}
	if !slices.ContainsFunc(owned, func(l domain.LeagueSummary) bool { return l.ID == leagueID }) {
		return nil, "", domain.League{}, fmt.Errorf("league %s: %w", leagueID, domain.ErrNotFound)
	}
	league, err := s.leagueCache.GetOrLoad(ctx, fmt.Sprintf("%s/%d", leagueID, season), ttlLeague,
		func(ctx context.Context) (domain.League, error) { return prov.League(ctx, native, season) })
	return prov, native, league, err
}

// statsTTL keeps completed weeks for longer; the current and future weeks can still change.
func statsTTL(season, week int, st domain.SeasonState) time.Duration {
	if season < st.Season || (season == st.Season && week < st.Week) {
		return ttlStatsFinal
	}
	return ttlStatsLive
}
