package sleeper

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

type statJSON struct {
	PlayerID string             `json:"player_id"`
	Opponent string             `json:"opponent"`
	Stats    map[string]float64 `json:"stats"`
}

// WeekStats returns every player's regular-season stat line for one week, keyed by Sleeper ID.
func (c *Client) WeekStats(ctx context.Context, season, week int) (map[string]domain.StatLine, error) {
	var rows []statJSON
	if err := c.get(ctx, fmt.Sprintf("%s/stats/nfl/%d/%d?season_type=regular", c.statsBase, season, week), &rows); err != nil {
		return nil, err
	}
	out := make(map[string]domain.StatLine, len(rows))
	for _, r := range rows {
		if r.PlayerID == "" {
			continue
		}
		out[r.PlayerID] = domain.StatLine{PlayerID: r.PlayerID, Season: season, Week: week, Opponent: r.Opponent, Stats: r.Stats}
	}
	return out, nil
}

// PlayerGamelog returns one player's weekly regular-season stat lines, sorted by week.
// Bye weeks (null entries) are omitted.
func (c *Client) PlayerGamelog(ctx context.Context, playerID string, season int) ([]domain.StatLine, error) {
	var m map[string]*statJSON
	u := fmt.Sprintf("%s/stats/nfl/player/%s?season_type=regular&season=%d&grouping=week", c.statsBase, url.PathEscape(playerID), season)
	if err := c.get(ctx, u, &m); err != nil {
		return nil, err
	}
	out := make([]domain.StatLine, 0, len(m))
	for wk, r := range m {
		week, err := strconv.Atoi(wk)
		if r == nil || err != nil {
			continue
		}
		out = append(out, domain.StatLine{PlayerID: playerID, Season: season, Week: week, Opponent: r.Opponent, Stats: r.Stats})
	}
	slices.SortFunc(out, func(a, b domain.StatLine) int { return cmp.Compare(a.Week, b.Week) })
	return out, nil
}
