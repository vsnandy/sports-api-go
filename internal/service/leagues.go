package service

import (
	"context"
	"fmt"
	"log/slog"

	"golang.org/x/sync/errgroup"

	"github.com/vsnandy/sports-api-go/internal/domain"
	"github.com/vsnandy/sports-api-go/internal/scoring"
)

// ListLeagues returns the owner's leagues across platforms. A platform that fails
// becomes a warning unless every platform fails.
func (s *Service) ListLeagues(ctx context.Context, season int) ([]domain.LeagueSummary, domain.Meta, error) {
	season, _, err := s.seasonOrCurrent(ctx, season)
	if err != nil {
		return nil, domain.Meta{}, err
	}
	out := []domain.LeagueSummary{}
	var warnings []string
	var firstErr error
	for _, p := range s.order {
		ls, err := s.listFor(ctx, s.providers[p], season)
		if err != nil {
			slog.WarnContext(ctx, "list leagues failed", "platform", p, "err", err)
			warnings = append(warnings, fmt.Sprintf("%s_leagues_unavailable: %v", p, err))
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		out = append(out, ls...)
	}
	if len(warnings) == len(s.order) {
		return nil, domain.Meta{}, firstErr
	}
	return out, domain.Meta{Season: season, Warnings: warnings}, nil
}

func (s *Service) League(ctx context.Context, leagueID string, season int) (domain.League, domain.Meta, error) {
	_, _, league, err := s.resolveLeague(ctx, leagueID, season)
	if err != nil {
		return domain.League{}, domain.Meta{}, err
	}
	return league, domain.Meta{Season: league.Season}, nil
}

func (s *Service) Rosters(ctx context.Context, leagueID string, season int) ([]domain.Roster, domain.Meta, error) {
	prov, native, league, err := s.resolveLeague(ctx, leagueID, season)
	if err != nil {
		return nil, domain.Meta{}, err
	}
	refs, err := s.rosterCache.GetOrLoad(ctx, fmt.Sprintf("%s/%d", leagueID, league.Season), ttlRosters,
		func(ctx context.Context) ([]domain.RosterRef, error) {
			return prov.Rosters(ctx, native, league.Season, league.RosterSlots)
		})
	if err != nil {
		return nil, domain.Meta{}, err
	}
	var warnings []string
	out := make([]domain.Roster, 0, len(refs))
	for _, ref := range refs {
		r, err := s.resolveRoster(ctx, ref, &warnings)
		if err != nil {
			return nil, domain.Meta{}, err
		}
		out = append(out, r)
	}
	return out, domain.Meta{Season: league.Season, Warnings: warnings}, nil
}

// Matchups returns a week's matchups. With withStats, each rostered player gets that
// week's stat line and points under the league's scoring; a stats failure degrades
// to a warning.
func (s *Service) Matchups(ctx context.Context, leagueID string, season, week int, withStats bool) ([]domain.Matchup, domain.Meta, error) {
	prov, native, league, err := s.resolveLeague(ctx, leagueID, season)
	if err != nil {
		return nil, domain.Meta{}, err
	}
	st, err := s.state(ctx)
	if err != nil {
		return nil, domain.Meta{}, err
	}
	if week == 0 {
		if league.Season != st.Season {
			return nil, domain.Meta{}, &domain.InvalidParamError{Param: "week", Reason: "required for past seasons"}
		}
		week = max(st.Week, 1)
	}
	if week < 1 || week > 18 {
		return nil, domain.Meta{}, &domain.InvalidParamError{Param: "week", Reason: "must be between 1 and 18"}
	}

	var refs []domain.MatchupRef
	var lines map[string]domain.StatLine
	var statsErr error
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		var err error
		refs, err = s.matchupCache.GetOrLoad(gctx, fmt.Sprintf("%s/%d/%d", leagueID, league.Season, week), ttlMatchups,
			func(ctx context.Context) ([]domain.MatchupRef, error) {
				return prov.Matchups(ctx, native, league.Season, week, league.RosterSlots)
			})
		return err
	})
	if withStats {
		g.Go(func() error {
			lines, statsErr = s.weekStats(gctx, league.Season, week, st)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, domain.Meta{}, err
	}

	var warnings []string
	if withStats && statsErr != nil {
		slog.WarnContext(ctx, "week stats unavailable", "err", statsErr)
		warnings = append(warnings, fmt.Sprintf("stats_unavailable: %v", statsErr))
		lines = nil
	}
	out := make([]domain.Matchup, 0, len(refs))
	for _, m := range refs {
		home, err := s.resolveRoster(ctx, m.Home.Roster, &warnings)
		if err != nil {
			return nil, domain.Meta{}, err
		}
		away, err := s.resolveRoster(ctx, m.Away.Roster, &warnings)
		if err != nil {
			return nil, domain.Meta{}, err
		}
		if lines != nil {
			attachStats(home, lines, league.ScoringModel())
			attachStats(away, lines, league.ScoringModel())
		}
		out = append(out, domain.Matchup{
			Week: m.Week,
			Home: domain.MatchupSide{TeamID: m.Home.TeamID, Points: m.Home.Points, Roster: home},
			Away: domain.MatchupSide{TeamID: m.Away.TeamID, Points: m.Away.Points, Roster: away},
		})
	}
	return out, domain.Meta{Season: league.Season, Week: week, Warnings: warnings}, nil
}

func (s *Service) weekStats(ctx context.Context, season, week int, st domain.SeasonState) (map[string]domain.StatLine, error) {
	return s.weekStatCache.GetOrLoad(ctx, fmt.Sprintf("%d/%d", season, week), statsTTL(season, week, st),
		func(ctx context.Context) (map[string]domain.StatLine, error) {
			return s.stats.WeekStats(ctx, season, week)
		})
}

func (s *Service) resolveRoster(ctx context.Context, ref domain.RosterRef, warnings *[]string) (domain.Roster, error) {
	starters, err := s.resolveEntries(ctx, ref.Starters, warnings)
	if err != nil {
		return domain.Roster{}, err
	}
	bench, err := s.resolveEntries(ctx, ref.Bench, warnings)
	if err != nil {
		return domain.Roster{}, err
	}
	reserve, err := s.resolveEntries(ctx, ref.Reserve, warnings)
	if err != nil {
		return domain.Roster{}, err
	}
	return domain.Roster{TeamID: ref.TeamID, Starters: starters, Bench: bench, Reserve: reserve}, nil
}

// resolveEntries maps provider refs to players. Unmapped players are kept with a nil
// ID and their platform ID, and reported as a warning.
func (s *Service) resolveEntries(ctx context.Context, refs []domain.RosterEntryRef, warnings *[]string) ([]domain.RosterEntry, error) {
	out := make([]domain.RosterEntry, 0, len(refs))
	for _, r := range refs {
		p, ok, err := s.players.Resolve(ctx, r.Ref)
		if err != nil {
			return nil, err
		}
		if !ok {
			p = domain.Player{
				Name: r.Ref.Name, Position: r.Ref.Position, NFLTeam: r.Ref.NFLTeam,
				PlatformIDs: map[string]string{string(r.Ref.Platform): r.Ref.ID},
			}
			*warnings = append(*warnings, fmt.Sprintf("unmapped_player: %s:%s (%s)", r.Ref.Platform, r.Ref.ID, r.Ref.Name))
		}
		out = append(out, domain.RosterEntry{Slot: r.Slot, Player: p})
	}
	return out, nil
}

// attachStats fills Stats and Points in place for players with a stat line.
func attachStats(r domain.Roster, lines map[string]domain.StatLine, s domain.Scoring) {
	for _, entries := range [][]domain.RosterEntry{r.Starters, r.Bench, r.Reserve} {
		for i := range entries {
			e := &entries[i]
			if e.Player.ID == nil {
				continue
			}
			line, ok := lines[*e.Player.ID]
			if !ok {
				continue
			}
			pts := scoring.Points(line.Stats, s, e.Player.Position)
			e.Stats, e.Points = line.Stats, &pts
		}
	}
}
