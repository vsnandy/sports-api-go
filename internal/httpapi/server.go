// Package httpapi exposes the service over HTTP: routing, auth, envelopes, and
// error mapping.
package httpapi

import (
	"context"
	"net/http"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

type Service interface {
	ListLeagues(ctx context.Context, season int) ([]domain.LeagueSummary, domain.Meta, error)
	League(ctx context.Context, leagueID string, season int) (domain.League, domain.Meta, error)
	Rosters(ctx context.Context, leagueID string, season int) ([]domain.Roster, domain.Meta, error)
	Matchups(ctx context.Context, leagueID string, season, week int, withStats bool) ([]domain.Matchup, domain.Meta, error)
	Player(ctx context.Context, playerID string) (domain.Player, domain.Meta, error)
	Gamelog(ctx context.Context, playerID string, season int, scoring string) ([]domain.GamelogEntry, domain.Meta, error)
}

func NewHandler(svc Service, apiKey string) http.Handler {
	h := handlers{svc: svc}

	api := http.NewServeMux()
	api.HandleFunc("GET /v1/{sport}/leagues", nflOnly(h.listLeagues))
	api.HandleFunc("GET /v1/{sport}/leagues/{leagueId}", nflOnly(h.league))
	api.HandleFunc("GET /v1/{sport}/leagues/{leagueId}/rosters", nflOnly(h.rosters))
	api.HandleFunc("GET /v1/{sport}/leagues/{leagueId}/matchups", nflOnly(h.matchups))
	api.HandleFunc("GET /v1/{sport}/players/{playerId}", nflOnly(h.player))
	api.HandleFunc("GET /v1/{sport}/players/{playerId}/gamelog", nflOnly(h.gamelog))
	api.HandleFunc("/", notFound)

	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	root.Handle("/v1/", requireAPIKey(apiKey, api))
	root.HandleFunc("/", notFound)

	return withRequestID(withLogging(withRecover(root)))
}
