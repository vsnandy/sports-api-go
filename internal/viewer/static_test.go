package viewer

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newStaticFakeAPI(t *testing.T, leaguesJSON string, fail bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "k" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":{"code":"unauthorized","message":"bad key"}}`)
			return
		}
		if fail && strings.HasPrefix(r.URL.Path, "/v1/nfl/leagues/espn:1") {
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, `{"error":{"code":"upstream_error","message":"boom"}}`)
			return
		}
		switch {
		case r.URL.Path == "/v1/nfl/leagues":
			io.WriteString(w, leaguesJSON)
		case r.URL.Path == "/v1/nfl/leagues/espn:1":
			io.WriteString(w, leagueJSON)
		case r.URL.Path == "/v1/nfl/leagues/espn:1/matchups":
			week := r.URL.Query().Get("week")
			if week == "" {
				week = "3"
			}
			io.WriteString(w, matchupsJSON(week))
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":{"code":"not_found","message":"route not found"}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

const oneLeagueJSON = `{"data":[{"id":"espn:1","platform":"espn","season":2026,"name":"Office League"}],"meta":{"season":2026,"warnings":[]}}`
const noLeaguesJSON = `{"data":[],"meta":{"season":2026,"warnings":[]}}`

func TestRenderSiteWritesExpectedFiles(t *testing.T) {
	srv := newStaticFakeAPI(t, oneLeagueJSON, false)
	c := &Client{BaseURL: srv.URL, APIKey: "k", HTTP: &http.Client{Timeout: 5 * time.Second}}
	dir := t.TempDir()

	if err := RenderSite(context.Background(), c, dir, "/sports-api-go/"); err != nil {
		t.Fatalf("RenderSite: %v", err)
	}

	want := []string{
		"index.html",
		"rooting/index.html",
		"rooting/week-1.html",
		"rooting/week-18.html",
		"league/espn:1/index.html",
		"league/espn:1/week-1.html",
		"league/espn:1/week-18.html",
	}
	for _, p := range want {
		full := filepath.Join(dir, p)
		b, err := os.ReadFile(full)
		if err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		if strings.Contains(string(b), "k") == false { // sanity: file isn't empty
			continue
		}
		if strings.Contains(string(b), "X-API-Key") || strings.Contains(string(b), "\"k\"") {
			t.Errorf("%s: output must not contain the API key", p)
		}
	}

	leaguesHTML, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(leaguesHTML), `href="/sports-api-go/league/espn:1/index.html"`) {
		t.Errorf("leagues page should link to the static league page, got:\n%s", leaguesHTML)
	}
}

func TestRenderSiteNoLeagues(t *testing.T) {
	srv := newStaticFakeAPI(t, noLeaguesJSON, false)
	c := &Client{BaseURL: srv.URL, APIKey: "k", HTTP: &http.Client{Timeout: 5 * time.Second}}
	dir := t.TempDir()

	if err := RenderSite(context.Background(), c, dir, "/"); err != nil {
		t.Fatalf("RenderSite with zero leagues: %v", err)
	}
	for _, p := range []string{"index.html", "rooting/index.html"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}

const oneLeagueDegradedJSON = `{"data":[{"id":"espn:1","platform":"espn","season":2026,"name":"Office League"}],"meta":{"season":2026,"warnings":["espn_leagues_unavailable: espn auth failed"]}}`

func TestRenderSiteFailsWhenLeaguesListDegraded(t *testing.T) {
	srv := newStaticFakeAPI(t, oneLeagueDegradedJSON, false)
	c := &Client{BaseURL: srv.URL, APIKey: "k", HTTP: &http.Client{Timeout: 5 * time.Second}}
	dir := t.TempDir()

	err := RenderSite(context.Background(), c, dir, "/")
	if err == nil {
		t.Fatal("RenderSite should fail when the leagues list itself reports a provider unavailable, rather than silently publishing a site missing that provider's leagues")
	}
	if !strings.Contains(err.Error(), "espn_leagues_unavailable") {
		t.Errorf("error should name the warning, got: %v", err)
	}
}

func TestRenderSiteFailsOnUpstreamError(t *testing.T) {
	srv := newStaticFakeAPI(t, oneLeagueJSON, true)
	c := &Client{BaseURL: srv.URL, APIKey: "k", HTTP: &http.Client{Timeout: 5 * time.Second}}
	dir := t.TempDir()

	err := RenderSite(context.Background(), c, dir, "/")
	if err == nil {
		t.Fatal("RenderSite should fail when a league page returns a non-2xx status")
	}
	if !strings.Contains(err.Error(), "espn:1") {
		t.Errorf("error should name the failing page, got: %v", err)
	}
}
