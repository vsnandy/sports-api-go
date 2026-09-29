// Package sleeper adapts the Sleeper API (league data, players dump, stats) to domain types.
package sleeper

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/vsnandy/sports-api-go/internal/domain"
	"github.com/vsnandy/sports-api-go/internal/providers/httpx"
)

const (
	DefaultAPIBase   = "https://api.sleeper.app/v1"
	DefaultStatsBase = "https://api.sleeper.com"
)

type Client struct {
	http      *httpx.Client
	apiBase   string
	statsBase string
	username  string

	mu     sync.Mutex
	userID string // resolved from username on first successful lookup
}

func New(hc *httpx.Client, apiBase, statsBase, username string) *Client {
	return &Client{http: hc, apiBase: apiBase, statsBase: statsBase, username: username}
}

func (c *Client) Platform() domain.Platform { return domain.PlatformSleeper }

func (c *Client) get(ctx context.Context, url string, out any) error {
	return c.http.GetJSON(ctx, url, nil, out)
}

func badBody(err error) error {
	return &domain.UpstreamError{Provider: "sleeper", BadBody: true, Err: err}
}

func (c *Client) State(ctx context.Context) (domain.SeasonState, error) {
	var s struct {
		Season string `json:"season"`
		Week   int    `json:"week"`
	}
	if err := c.get(ctx, c.apiBase+"/state/nfl", &s); err != nil {
		return domain.SeasonState{}, err
	}
	season, err := strconv.Atoi(s.Season)
	if err != nil {
		return domain.SeasonState{}, badBody(err)
	}
	return domain.SeasonState{Season: season, Week: s.Week}, nil
}

type playerJSON struct {
	FullName  string     `json:"full_name"`
	FirstName string     `json:"first_name"`
	LastName  string     `json:"last_name"`
	Position  string     `json:"position"`
	Team      string     `json:"team"`
	ESPNID    flexString `json:"espn_id"`
}

// flexString accepts a JSON string, number, or null; the players dump mixes them.
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*f = ""
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = flexString(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = flexString(n.String())
	return nil
}

// Players fetches the full NFL players dump. Sleeper asks callers to do this at most daily.
func (c *Client) Players(ctx context.Context) ([]domain.Player, error) {
	var m map[string]playerJSON
	if err := c.get(ctx, c.apiBase+"/players/nfl", &m); err != nil {
		return nil, err
	}
	out := make([]domain.Player, 0, len(m))
	for id, p := range m {
		name := p.FullName
		if name == "" {
			name = strings.TrimSpace(p.FirstName + " " + p.LastName)
		}
		ids := map[string]string{"sleeper": id}
		if p.ESPNID != "" {
			ids["espn"] = string(p.ESPNID)
		}
		out = append(out, domain.Player{ID: &id, Name: name, Position: p.Position, NFLTeam: p.Team, PlatformIDs: ids})
	}
	slices.SortFunc(out, func(a, b domain.Player) int { return cmp.Compare(*a.ID, *b.ID) })
	return out, nil
}
