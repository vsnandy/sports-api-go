package httpapi

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

type handlers struct{ svc Service }

func nflOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if sport := r.PathValue("sport"); sport != string(domain.SportNFL) {
			writeError(w, r, fmt.Errorf("sport %q: %w", sport, domain.ErrNotFound))
			return
		}
		next(w, r)
	}
}

func notFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, fmt.Errorf("route %s: %w", r.URL.Path, domain.ErrNotFound))
}

func intParam(r *http.Request, name string, lo, hi int) (int, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		return 0, &domain.InvalidParamError{Param: name, Reason: fmt.Sprintf("must be an integer between %d and %d", lo, hi)}
	}
	return n, nil
}

func seasonParam(r *http.Request) (int, error) { return intParam(r, "season", 2000, 2100) }

func (h handlers) listLeagues(w http.ResponseWriter, r *http.Request) {
	season, err := seasonParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	data, meta, err := h.svc.ListLeagues(r.Context(), season)
	respond(w, r, data, meta, err)
}

func (h handlers) league(w http.ResponseWriter, r *http.Request) {
	season, err := seasonParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	data, meta, err := h.svc.League(r.Context(), r.PathValue("leagueId"), season)
	respond(w, r, data, meta, err)
}

func (h handlers) rosters(w http.ResponseWriter, r *http.Request) {
	season, err := seasonParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	data, meta, err := h.svc.Rosters(r.Context(), r.PathValue("leagueId"), season)
	respond(w, r, data, meta, err)
}

func (h handlers) matchups(w http.ResponseWriter, r *http.Request) {
	season, err := seasonParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	week, err := intParam(r, "week", 1, 18)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var withStats bool
	switch r.URL.Query().Get("include") {
	case "":
	case "stats":
		withStats = true
	default:
		writeError(w, r, &domain.InvalidParamError{Param: "include", Reason: `must be "stats" or omitted`})
		return
	}
	data, meta, err := h.svc.Matchups(r.Context(), r.PathValue("leagueId"), season, week, withStats)
	respond(w, r, data, meta, err)
}

func (h handlers) player(w http.ResponseWriter, r *http.Request) {
	data, meta, err := h.svc.Player(r.Context(), r.PathValue("playerId"))
	respond(w, r, data, meta, err)
}

func (h handlers) gamelog(w http.ResponseWriter, r *http.Request) {
	season, err := seasonParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	data, meta, err := h.svc.Gamelog(r.Context(), r.PathValue("playerId"), season, r.URL.Query().Get("scoring"))
	respond(w, r, data, meta, err)
}
