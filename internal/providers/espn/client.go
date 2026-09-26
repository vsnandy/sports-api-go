// Package espn adapts the ESPN fantasy football v3 API to domain types. Private
// leagues require the espn_s2 and SWID cookies.
package espn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"golang.org/x/sync/errgroup"

	"github.com/vsnandy/sports-api-go/internal/domain"
	"github.com/vsnandy/sports-api-go/internal/providers/httpx"
)

const DefaultBase = "https://lm-api-reads.fantasy.espn.com/apis/v3/games/ffl"

type Client struct {
	http      *httpx.Client
	base      string
	cookie    string
	leagueIDs []string
}

func New(hc *httpx.Client, base, espnS2, swid string, leagueIDs []string) *Client {
	return &Client{http: hc, base: base, cookie: fmt.Sprintf("espn_s2=%s; SWID=%s", espnS2, swid), leagueIDs: leagueIDs}
}

func (c *Client) Platform() domain.Platform { return domain.PlatformESPN }

func (c *Client) fetch(ctx context.Context, nativeID string, season, week int, views ...string) (*leagueJSON, error) {
	q := url.Values{"view": views}
	if week > 0 {
		q.Set("scoringPeriodId", strconv.Itoa(week))
	}
	u := fmt.Sprintf("%s/seasons/%d/segments/0/leagues/%s?%s", c.base, season, nativeID, q.Encode())
	var l leagueJSON
	if err := c.http.GetJSON(ctx, u, http.Header{"Cookie": {c.cookie}}, &l); err != nil {
		return nil, classifyAuth(err)
	}
	return &l, nil
}

// classifyAuth turns ESPN's ways of rejecting cookies (401/403, or a 2xx HTML login
// page) into ErrESPNAuth.
func classifyAuth(err error) error {
	var ue *domain.UpstreamError
	if errors.As(err, &ue) && (ue.Status == http.StatusUnauthorized || ue.Status == http.StatusForbidden || ue.BadBody) {
		return fmt.Errorf("%w (%v)", domain.ErrESPNAuth, err)
	}
	return err
}

// ListLeagues returns the configured leagues that exist for season.
func (c *Client) ListLeagues(ctx context.Context, season int) ([]domain.LeagueSummary, error) {
	found := make([]*domain.LeagueSummary, len(c.leagueIDs))
	g, gctx := errgroup.WithContext(ctx)
	for i, id := range c.leagueIDs {
		g.Go(func() error {
			l, err := c.fetch(gctx, id, season, 0, "mSettings")
			if errors.Is(err, domain.ErrNotFound) {
				return nil // league did not exist that season
			}
			if err != nil {
				return err
			}
			found[i] = &domain.LeagueSummary{
				ID: domain.LeagueID(domain.PlatformESPN, id), Platform: domain.PlatformESPN,
				Season: season, Name: l.Settings.Name,
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	out := make([]domain.LeagueSummary, 0, len(found))
	for _, s := range found {
		if s != nil {
			out = append(out, *s)
		}
	}
	return out, nil
}
