package viewer

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const leagueJSON = `{"data":{"id":"espn:1","platform":"espn","sport":"nfl","season":2026,"name":"Office League",
 "teams":[{"id":"1","name":"Team Varun","owner":"v"},{"id":"2","name":"Alex's Aces","owner":"a"}],
 "scoring":{"pass_td":4,"sack":0},"scoringByPosition":{"DEF":{"sack":1}},
 "derivedStats":[{"key":"espn_pa_14_17","from":"pts_allow","min":14,"max":17},{"key":"espn_pass_yd_per_25","from":"pass_yd","min":0,"max":null,"step":25}],
 "unsupportedRules":["espn stat 93 (6 pts)"],"rosterSlots":["QB","DEF","BN"]},"meta":{"season":2026,"warnings":[]}}`

func matchupsJSON(week string) string {
	return `{"data":[{"week":` + week + `,
 "home":{"teamId":"1","points":22,"roster":{"teamId":"1",
   "starters":[
     {"slot":"QB","player":{"id":"4046","name":"Patrick Mahomes","position":"QB","nflTeam":"KC","platformIds":{}},"stats":{"pass_td":2,"pass_yd":250},"points":18,"pointsSource":"platform","pointsBreakdown":{"pass_td":8,"espn_pass_yd_per_25":10,"pass_int":-2}},
     {"slot":"DEF","player":{"id":"PIT","name":"Pittsburgh Steelers","position":"DEF","nflTeam":"PIT","platformIds":{}},"stats":{"sack":3,"pts_allow":14},"points":4,"pointsSource":"computed","pointsBreakdown":{"sack":3,"espn_pa_14_17":1}}],
   "bench":[{"slot":"BN","player":{"id":null,"name":"Rookie Guy","position":"RB","nflTeam":"","platformIds":{"espn":"9"}},"stats":null,"points":null}],
   "reserve":[]}},
 "away":{"teamId":"2","points":0,"roster":{"teamId":"2","starters":[],"bench":[],"reserve":[]}}}],
 "meta":{"season":2026,"week":` + week + `,"warnings":["unmapped_player: espn:9 (Rookie Guy)"]}}`
}

type fakeAPI struct {
	mu   sync.Mutex
	reqs []*url.URL
}

func (f *fakeAPI) urls() []*url.URL {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*url.URL(nil), f.reqs...)
}

func newFakeAPI(t *testing.T) (*fakeAPI, *httptest.Server) {
	t.Helper()
	f := &fakeAPI{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.reqs = append(f.reqs, r.URL)
		f.mu.Unlock()
		if r.Header.Get("X-API-Key") != "k" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":{"code":"unauthorized","message":"missing or invalid X-API-Key"}}`)
			return
		}
		switch r.URL.Path {
		case "/v1/nfl/leagues":
			io.WriteString(w, `{"data":[{"id":"espn:1","platform":"espn","season":2026,"name":"Office League"},{"id":"sleeper:2","platform":"sleeper","season":2026,"name":"Dynasty"}],"meta":{"season":2026,"warnings":[]}}`)
		case "/v1/nfl/leagues/espn:1":
			io.WriteString(w, leagueJSON)
		case "/v1/nfl/leagues/espn:1/matchups":
			week := r.URL.Query().Get("week")
			if week == "" {
				week = "3"
			}
			io.WriteString(w, matchupsJSON(week))
		case "/v1/nfl/leagues/espn:7":
			io.WriteString(w, strings.Replace(leagueJSON, `"id":"2","name":"Alex's Aces"`, `"id":"2","name":"<script>x</script>"`, 1))
		case "/v1/nfl/leagues/espn:7/matchups":
			io.WriteString(w, matchupsJSON("3"))
		case "/v1/nfl/leagues/espn:8":
			io.WriteString(w, leagueJSON)
		case "/v1/nfl/leagues/espn:8/matchups":
			io.WriteString(w, `{"data":[],"meta":{"season":2026,"week":3,"warnings":[]}}`)
		case "/v1/nfl/leagues/espn:500":
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, `{"error":{"code":"espn_auth_failed","message":"ESPN rejected the configured espn_s2/SWID cookies; rotate them in SSM"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":{"code":"not_found","message":"route not found"}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func get(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8081"+path, nil))
	return rec.Code, rec.Body.String()
}

func newTestHandler(t *testing.T) (*fakeAPI, http.Handler) {
	f, srv := newFakeAPI(t)
	return f, NewHandler(&Client{BaseURL: srv.URL, APIKey: "k", HTTP: &http.Client{Timeout: 5 * time.Second}})
}

func mustContain(t *testing.T, body string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(body, p) {
			t.Errorf("page missing %q", p)
		}
	}
}

func TestLeaguesPage(t *testing.T) {
	_, h := newTestHandler(t)
	code, body := get(t, h, "/")
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	mustContain(t, body, "Office League", `href="/league/espn:1"`, "Dynasty", `href="/league/sleeper:2"`, "ESPN", "Sleeper")
}

func TestLeaguePage(t *testing.T) {
	f, h := newTestHandler(t)
	code, body := get(t, h, "/league/espn:1")
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	mustContain(t, body,
		"Office League", "week 3",
		"Team Varun", "Alex&#39;s Aces", "22.00",
		"Patrick Mahomes", "18.00", "ESPN", "computed",
		"Passing TDs", "8.00", "Every 25 passing yards", "10.00",
		"Sacks", "14–17 points allowed",
		"Rookie Guy", "—",
		"unmapped_player: espn:9 (Rookie Guy)",
		"Scoring rules", "DEF", "espn stat 93 (6 pts)", "every 25", "14–17",
		`href="?week=2"`, `href="?week=4"`, "Refresh",
	)
	for _, u := range f.urls() {
		if u.Path == "/v1/nfl/leagues/espn:1/matchups" {
			if u.Query().Get("include") != "stats" || u.Query().Has("week") {
				t.Errorf("matchups query = %q, want include=stats and no week", u.RawQuery)
			}
		}
	}
}

func TestLeaguePageWeekParam(t *testing.T) {
	f, h := newTestHandler(t)
	code, body := get(t, h, "/league/espn:1?week=5")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "week 5", `href="?week=4"`, `href="?week=6"`)
	var sawWeek bool
	for _, u := range f.urls() {
		if u.Path == "/v1/nfl/leagues/espn:1/matchups" && u.Query().Get("week") == "5" {
			sawWeek = true
		}
	}
	if !sawWeek {
		t.Fatal("week=5 was not passed to the API")
	}
}

func TestLeaguePageWeekBounds(t *testing.T) {
	_, h := newTestHandler(t)
	_, first := get(t, h, "/league/espn:1?week=1")
	if strings.Contains(first, `href="?week=0"`) {
		t.Error("week 1 must not link to week 0")
	}
	_, last := get(t, h, "/league/espn:1?week=18")
	if strings.Contains(last, `href="?week=19"`) {
		t.Error("week 18 must not link to week 19")
	}
}

func TestInvalidWeek(t *testing.T) {
	for _, w := range []string{"0", "19", "abc", "-1"} {
		f, h := newTestHandler(t)
		code, body := get(t, h, "/league/espn:1?week="+w)
		if code != http.StatusBadRequest || !strings.Contains(body, "1 to 18") {
			t.Errorf("week=%s: status %d body %q", w, code, body)
		}
		if n := len(f.urls()); n != 0 {
			t.Errorf("week=%s: made %d API calls, want 0", w, n)
		}
	}
}

func TestLeaguePageNoMatchups(t *testing.T) {
	_, h := newTestHandler(t)
	code, body := get(t, h, "/league/espn:8")
	if code != 200 || !strings.Contains(body, "No matchups this week.") {
		t.Fatalf("status %d body %q", code, body)
	}
}

func TestLeaguePageEscapesNames(t *testing.T) {
	_, h := newTestHandler(t)
	_, body := get(t, h, "/league/espn:7")
	if strings.Contains(body, "<script>x</script>") || !strings.Contains(body, "&lt;script&gt;x&lt;/script&gt;") {
		t.Fatal("team names must be HTML-escaped")
	}
}

func TestAPIErrorRendered(t *testing.T) {
	_, h := newTestHandler(t)
	code, body := get(t, h, "/league/espn:500")
	if code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", code)
	}
	mustContain(t, body, "espn_auth_failed", "rotate them in SSM")
}

func TestAPIUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	h := NewHandler(&Client{BaseURL: base, APIKey: "k", HTTP: &http.Client{Timeout: time.Second}})
	code, body := get(t, h, "/")
	if code != http.StatusBadGateway || !strings.Contains(body, "API unreachable") {
		t.Fatalf("status %d body %q", code, body)
	}
}

func TestWrongKeyShowsUnauthorized(t *testing.T) {
	_, srv := newFakeAPI(t)
	h := NewHandler(&Client{BaseURL: srv.URL, APIKey: "wrong", HTTP: &http.Client{Timeout: 5 * time.Second}})
	code, body := get(t, h, "/")
	if code != http.StatusUnauthorized || !strings.Contains(body, "unauthorized") {
		t.Fatalf("status %d body %q", code, body)
	}
}

func TestLabel(t *testing.T) {
	for key, want := range map[string]string{
		"sack":                "Sacks",
		"pass_td":             "Passing TDs",
		"espn_pa_14_17":       "14–17 points allowed",
		"espn_pa_46p":         "46+ points allowed",
		"espn_pa_0":           "0 points allowed",
		"espn_ya_300_349":     "300–349 yards allowed",
		"pts_allow_14_20":     "14–20 points allowed",
		"espn_pass_yd_per_25": "Every 25 passing yards",
		"mystery_stat":        "mystery_stat",
	} {
		if got := Label(key); got != want {
			t.Errorf("Label(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestBreakdownRowsSortedWithTotal(t *testing.T) {
	rows, total := breakdownRows(map[string]float64{"sack": 1, "pass_int": -3, "pass_td": 2})
	var got []string
	for _, r := range rows {
		got = append(got, fmt.Sprintf("%s=%g", r.Label, r.Points))
	}
	want := []string{"Interceptions thrown=-3", "Passing TDs=2", "Sacks=1"}
	if strings.Join(got, ",") != strings.Join(want, ",") || total != 0 {
		t.Fatalf("rows = %v total %v, want %v total 0", got, total, want)
	}
}

func TestAPIKeyNotForwardedOnRedirect(t *testing.T) {
	var leaked sync.Mutex
	var sawKey bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Lock()
		defer leaked.Unlock()
		if r.Header.Get("X-API-Key") != "" {
			sawKey = true
		}
		io.WriteString(w, `{"data":[],"meta":{"season":2026,"warnings":[]}}`)
	}))
	defer other.Close()
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusFound)
	}))
	defer redirecting.Close()

	h := NewHandler(NewClient(redirecting.URL, "sekret-key-123", 5*time.Second))
	code, body := get(t, h, "/")
	leaked.Lock()
	defer leaked.Unlock()
	if sawKey {
		t.Fatal("X-API-Key was forwarded to the redirect target")
	}
	if code != http.StatusBadGateway || strings.Contains(body, "sekret-key-123") {
		t.Fatalf("status %d; a redirect should render as an API error without the key", code)
	}
}

func TestRejectsNonLoopbackHost(t *testing.T) {
	f, h := newTestHandler(t)
	for _, host := range []string{"evil.example", "evil.example:8081", "192.168.1.5:8081"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("Host %q: status %d, want 403", host, rec.Code)
		}
	}
	for _, host := range []string{"localhost:8081", "127.0.0.1:8081", "[::1]:8081"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("Host %q: status %d, want 200", host, rec.Code)
		}
	}
	if n := len(f.urls()); n != 3 {
		t.Errorf("API calls = %d, want 3 (only the loopback requests)", n)
	}
}

func TestLeaguePageVisuals(t *testing.T) {
	_, h := newTestHandler(t)
	code, body := get(t, h, "/league/espn:1")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body,
		`--bg:#0d1321`,
		`PM<img src="https://sleepercdn.com/content/nfl/players/thumb/4046.jpg" alt="" loading="lazy">`,
		`class="avatar def">PS<img src="https://sleepercdn.com/images/team_logos/nfl/pit.png"`,
		`RG<img src="https://a.espncdn.com/i/headshots/nfl/players/full/9.png"`,
		`class="score leading">22.00`,
		`class="score trailing">0.00`,
		`class="chip gain">Passing TDs<b>&#43;8.00</b>`, // html/template escapes "+"; browsers render it as +
		`class="chip loss">Interceptions thrown<b>−2.00</b>`,
		`class="row bench"`,
		`class="pts none">—`,
	)
}

func TestLeaguesPageBadges(t *testing.T) {
	_, h := newTestHandler(t)
	_, body := get(t, h, "/")
	mustContain(t, body, `class="badge espn">ESPN`, `class="badge sleeper">Sleeper`, `class="league-card" href="/league/espn:1"`)
	if strings.Index(body, "Office League") > strings.Index(body, "Dynasty") {
		t.Error("ESPN leagues should be listed before Sleeper leagues")
	}
}
