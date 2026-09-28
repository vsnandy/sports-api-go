package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

const key = "secret"

type fakeService struct {
	season, week int
	id, scoring  string
	withStats    bool
	err          error
	panicMsg     string
}

func (f *fakeService) ListLeagues(_ context.Context, season int) ([]domain.LeagueSummary, domain.Meta, error) {
	if f.panicMsg != "" {
		panic(f.panicMsg)
	}
	f.season = season
	return []domain.LeagueSummary{{ID: "espn:1", Platform: domain.PlatformESPN, Season: 2026, Name: "L"}}, domain.Meta{Season: 2026}, f.err
}

func (f *fakeService) League(_ context.Context, id string, season int) (domain.League, domain.Meta, error) {
	f.id, f.season = id, season
	return domain.League{ID: id}, domain.Meta{Season: 2026}, f.err
}

func (f *fakeService) Rosters(_ context.Context, id string, season int) ([]domain.Roster, domain.Meta, error) {
	f.id, f.season = id, season
	return []domain.Roster{}, domain.Meta{}, f.err
}

func (f *fakeService) Matchups(_ context.Context, id string, season, week int, withStats bool) ([]domain.Matchup, domain.Meta, error) {
	f.id, f.season, f.week, f.withStats = id, season, week, withStats
	return []domain.Matchup{}, domain.Meta{Season: 2026, Week: week}, f.err
}

func (f *fakeService) Player(_ context.Context, id string) (domain.Player, domain.Meta, error) {
	f.id = id
	return domain.Player{ID: &id}, domain.Meta{}, f.err
}

func (f *fakeService) Gamelog(_ context.Context, id string, season int, scoring string) ([]domain.GamelogEntry, domain.Meta, error) {
	f.id, f.season, f.scoring = id, season, scoring
	return []domain.GamelogEntry{}, domain.Meta{}, f.err
}

func do(h http.Handler, path, apiKey string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	return m
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	e, _ := decode(t, rec)["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func TestHealthzNoAuth(t *testing.T) {
	rec := do(NewHandler(&fakeService{}, key), "/healthz", "")
	if rec.Code != 200 || decode(t, rec)["status"] != "ok" {
		t.Fatalf("healthz = %d %s", rec.Code, rec.Body)
	}
}

func TestAuth(t *testing.T) {
	h := NewHandler(&fakeService{}, key)
	for _, k := range []string{"", "wrong"} {
		rec := do(h, "/v1/nfl/leagues", k)
		if rec.Code != 401 || errCode(t, rec) != "unauthorized" {
			t.Errorf("key %q: %d %s", k, rec.Code, rec.Body)
		}
	}
	if rec := do(h, "/v1/nfl/leagues", key); rec.Code != 200 {
		t.Errorf("valid key: %d %s", rec.Code, rec.Body)
	}
}

func TestEmptyConfiguredKeyRejectsEverything(t *testing.T) {
	h := NewHandler(&fakeService{}, "")
	req := httptest.NewRequest(http.MethodGet, "/v1/nfl/leagues", nil)
	req.Header.Set("X-API-Key", "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}

func TestEnvelope(t *testing.T) {
	rec := do(NewHandler(&fakeService{}, key), "/v1/nfl/leagues", key)
	body := decode(t, rec)
	data := body["data"].([]any)
	meta := body["meta"].(map[string]any)
	if data[0].(map[string]any)["id"] != "espn:1" || meta["season"] != float64(2026) {
		t.Fatalf("body = %v", body)
	}
	if w, ok := meta["warnings"].([]any); !ok || len(w) != 0 {
		t.Fatalf("warnings = %#v, want []", meta["warnings"])
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("content-type = %q", rec.Header().Get("Content-Type"))
	}
}

func TestParamValidation(t *testing.T) {
	h := NewHandler(&fakeService{}, key)
	for _, path := range []string{
		"/v1/nfl/leagues?season=abc",
		"/v1/nfl/leagues?season=1999",
		"/v1/nfl/leagues/espn:1/matchups?week=19",
		"/v1/nfl/leagues/espn:1/matchups?include=foo",
	} {
		if rec := do(h, path, key); rec.Code != 400 || errCode(t, rec) != "invalid_param" {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
}

func TestArgsPassedThrough(t *testing.T) {
	svc := &fakeService{}
	h := NewHandler(svc, key)
	do(h, "/v1/nfl/leagues/espn:1/matchups?week=3&season=2025&include=stats", key)
	if svc.id != "espn:1" || svc.week != 3 || svc.season != 2025 || !svc.withStats {
		t.Fatalf("matchups args = %+v", svc)
	}
	do(h, "/v1/nfl/players/6794/gamelog?scoring=ppr&season=2024", key)
	if svc.id != "6794" || svc.scoring != "ppr" || svc.season != 2024 {
		t.Fatalf("gamelog args = %+v", svc)
	}
	for _, path := range []string{"/v1/nfl/leagues/espn:1", "/v1/nfl/leagues/espn:1/rosters", "/v1/nfl/players/6794"} {
		if rec := do(h, path, key); rec.Code != 200 {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
}

func TestErrorMapping(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{&domain.InvalidParamError{Param: "week", Reason: "bad"}, 400, "invalid_param"},
		{fmt.Errorf("league x: %w", domain.ErrNotFound), 404, "not_found"},
		{fmt.Errorf("%w (upstream espn status 401)", domain.ErrESPNAuth), 502, "espn_auth_failed"},
		{fmt.Errorf("sleeper /x: %w", domain.ErrUpstreamTimeout), 504, "upstream_timeout"},
		{&domain.UpstreamError{Provider: "sleeper", Status: 500}, 502, "upstream_error"},
		{fmt.Errorf("sleeper /x: %w", context.Canceled), 500, "internal"},
		{errors.New("surprise"), 500, "internal"},
	}
	for _, tt := range tests {
		rec := do(NewHandler(&fakeService{err: tt.err}, key), "/v1/nfl/leagues/espn:1", key)
		if rec.Code != tt.status || errCode(t, rec) != tt.code {
			t.Errorf("%v: got %d %s", tt.err, rec.Code, rec.Body)
		}
	}
}

func TestClientDisconnectLogsAtWarnNotError(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(old)

	canceledErr := fmt.Errorf("sleeper /x: %w", context.Canceled)
	rec := do(NewHandler(&fakeService{err: canceledErr}, key), "/v1/nfl/leagues", key)
	if rec.Code != 500 || errCode(t, rec) != "internal" {
		t.Fatalf("status/code = %d %s, want 500 internal", rec.Code, errCode(t, rec))
	}

	var sawWarn, sawError bool
	dec := json.NewDecoder(&buf)
	for dec.More() {
		var line map[string]any
		if err := dec.Decode(&line); err != nil {
			break
		}
		if line["msg"] != "request failed" {
			continue
		}
		switch line["level"] {
		case "WARN":
			sawWarn = true
		case "ERROR":
			sawError = true
		}
	}
	if !sawWarn || sawError {
		t.Fatalf("want a WARN log for a canceled client, not ERROR (warn=%v error=%v, log=%s)", sawWarn, sawError, buf.String())
	}
}

func TestUnknownSportAndRoutes(t *testing.T) {
	h := NewHandler(&fakeService{}, key)
	for _, path := range []string{"/v1/nba/leagues", "/v1/nfl/nope", "/nope"} {
		if rec := do(h, path, key); rec.Code != 404 || errCode(t, rec) != "not_found" {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
}

func TestPanicRecovered(t *testing.T) {
	rec := do(NewHandler(&fakeService{panicMsg: "boom"}, key), "/v1/nfl/leagues", key)
	if rec.Code != 500 || errCode(t, rec) != "internal" || rec.Header().Get("X-Request-Id") == "" {
		t.Fatalf("panic response = %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
}

func TestRequestIDEchoed(t *testing.T) {
	rec := do(NewHandler(&fakeService{}, key), "/healthz", "", "X-Request-Id", "abc123")
	if rec.Header().Get("X-Request-Id") != "abc123" {
		t.Fatalf("X-Request-Id = %q", rec.Header().Get("X-Request-Id"))
	}
}
