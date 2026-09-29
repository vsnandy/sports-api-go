package service

import (
	"context"
	"fmt"

	"github.com/vsnandy/sports-api-go/internal/domain"
	"github.com/vsnandy/sports-api-go/internal/scoring"
)

func (s *Service) Player(ctx context.Context, playerID string) (domain.Player, domain.Meta, error) {
	p, err := s.player(ctx, playerID)
	return p, domain.Meta{}, err
}

func (s *Service) player(ctx context.Context, id string) (domain.Player, error) {
	if !domain.ValidPlayerID(id) {
		return domain.Player{}, &domain.InvalidParamError{Param: "playerId", Reason: "must be 1-12 letters or digits"}
	}
	p, ok, err := s.players.Get(ctx, id)
	if err != nil {
		return domain.Player{}, err
	}
	if !ok {
		return domain.Player{}, fmt.Errorf("player %s: %w", id, domain.ErrNotFound)
	}
	return p, nil
}

// Gamelog returns a player's weekly stat lines for season. scoringParam is "" (raw
// stats), a preset name, or a league ID whose scoring rules apply.
func (s *Service) Gamelog(ctx context.Context, playerID string, season int, scoringParam string) ([]domain.GamelogEntry, domain.Meta, error) {
	if !validScoringSyntax(scoringParam) {
		return nil, domain.Meta{}, &domain.InvalidParamError{Param: "scoring", Reason: "must be ppr, half, std, or a league id like espn:123456"}
	}
	p, err := s.player(ctx, playerID)
	if err != nil {
		return nil, domain.Meta{}, err
	}
	season, st, err := s.seasonOrCurrent(ctx, season)
	if err != nil {
		return nil, domain.Meta{}, err
	}
	sc, warnings, err := s.rulesFor(ctx, scoringParam, season)
	if err != nil {
		return nil, domain.Meta{}, err
	}
	ttl := ttlStatsLive
	if season < st.Season {
		ttl = ttlStatsFinal
	}
	lines, err := s.gamelogCache.GetOrLoad(ctx, fmt.Sprintf("%s/%d", playerID, season), ttl,
		func(ctx context.Context) ([]domain.StatLine, error) {
			return s.stats.PlayerGamelog(ctx, playerID, season)
		})
	if err != nil {
		return nil, domain.Meta{}, err
	}
	out := make([]domain.GamelogEntry, 0, len(lines))
	for _, l := range lines {
		e := domain.GamelogEntry{Season: l.Season, Week: l.Week, Opponent: l.Opponent, Stats: l.Stats}
		if sc != nil {
			pts := scoring.Points(l.Stats, *sc, p.Position)
			e.Points = &pts
		}
		out = append(out, e)
	}
	return out, domain.Meta{Season: season, Warnings: warnings}, nil
}

// validScoringSyntax reports whether param is a well-formed scoring value: empty,
// a preset name, or a syntactically valid league ID. It does not check that the
// league exists or is owned by the caller.
func validScoringSyntax(param string) bool {
	if param == "" {
		return true
	}
	if _, ok := scoring.Preset(param); ok {
		return true
	}
	_, _, err := domain.ParseLeagueID(param)
	return err == nil
}

func (s *Service) rulesFor(ctx context.Context, param string, season int) (*domain.Scoring, []string, error) {
	if param == "" {
		return nil, nil, nil
	}
	if sc, ok := scoring.Preset(param); ok {
		return &sc, nil, nil
	}
	_, _, league, err := s.resolveLeague(ctx, param, season)
	if err != nil {
		return nil, nil, err
	}
	var warnings []string
	for _, u := range league.UnsupportedRules {
		warnings = append(warnings, "unsupported_rule: "+u)
	}
	sc := league.ScoringModel()
	return &sc, warnings, nil
}
