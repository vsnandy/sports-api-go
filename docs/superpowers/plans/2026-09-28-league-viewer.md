# League Viewer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A local-only, server-rendered Go web view of the owner's leagues (matchups, per-player points with per-stat breakdown, league scoring rules), plus the `pointsBreakdown` API field it needs.

**Architecture:** The API adds `pointsBreakdown` to matchup roster entries (ESPN's per-stat points translated to stat keys, or the engine's `Breakdown`). A new `internal/viewer` package calls the deployed API with `X-API-Key` server-side and renders `html/template` pages using `<details>` for expansion (no JavaScript). `cmd/viewer` wires it on `127.0.0.1:8081` with config from `.env`.

**Tech Stack:** Go stdlib only (`net/http`, `html/template`, `embed`, `httptest`).

**Spec:** `docs/superpowers/specs/2026-09-28-league-viewer-design.md`

## Global Constraints

- No new dependencies; no JavaScript in pages.
- `pointsBreakdown` (`json:"pointsBreakdown,omitempty"`) is set only together with `points`; values: ESPN `appliedStats` translated via `espnKeyForID` (tier key → step key → first `statKeys` entry → `espn_<id>`), zero values omitted, same-key values summed; computed values are `scoring.Breakdown` rounded to 2 decimals. No other API response change.
- Viewer listens on `127.0.0.1:8081` only; reads `API_URL` and `API_KEY` from the environment (`make viewer` loads `.env`); the key is sent only as the `X-API-Key` header from the Go process.
- Routes: `GET /` (leagues), `GET /league/{id}` with optional `week` (1–18). Invalid `week` → 400 with no API call.
- API errors render status, code and message (status mirrored; < 400 → 502); transport errors render "API unreachable" with 502.
- Points shown with 2 decimals; null points shown as `—`; source tag `ESPN` for `pointsSource: "platform"`, `computed` for `"computed"`.
- Tests make no network calls outside `httptest`.

## Review Focus

1. **Team or player names containing HTML/quotes (e.g. `Alex's Aces`, `<script>`)** → rendered escaped, never as markup. (Task 2: `TestLeaguePage` asserts `Alex&#39;s Aces`; `TestLeaguePageEscapesNames`.)
2. **A week with no matchups (bye week league, offseason)** → page renders with "No matchups this week." instead of an empty or broken page. (Task 2: `TestLeaguePageNoMatchups`.)
3. **Players with null points (unmapped, no stats)** → row shows `—` and no breakdown, no template error. (Task 2: `TestLeaguePage` Rookie Guy assertions.)
4. **Two ESPN stat IDs sharing a key in one row (101 and 102 → `def_st_td`)** → breakdown sums them so it still adds up to ESPN's total. (Task 1: `TestBreakdownFromApplied`.)
5. **Expired ESPN cookies (`espn_auth_failed`)** → the league page shows that code and the "rotate them in SSM" message rather than a blank page. (Task 2: `TestAPIErrorRendered`.)

---

## File Map

```
internal/domain/domain.go                       # MODIFY: RosterEntryRef.PlatformBreakdown, RosterEntry.PointsBreakdown
internal/providers/espn/tables.go               # MODIFY: espnKeyForID
internal/providers/espn/league.go               # MODIFY: breakdownFromApplied, toRoster sets PlatformBreakdown
internal/providers/espn/breakdown_test.go       # CREATE
internal/providers/espn/espn_test.go            # MODIFY: TestMatchupsPlatformPoints asserts breakdown
internal/service/leagues.go                     # MODIFY: attachPoints sets PointsBreakdown
internal/service/service_test.go                # MODIFY
docs/superpowers/specs/2026-09-28-league-viewer-design.md  # MODIFY: omitempty wording
internal/viewer/client.go                       # CREATE
internal/viewer/labels.go                       # CREATE
internal/viewer/view.go                         # CREATE: view models + builders
internal/viewer/handlers.go                     # CREATE
internal/viewer/templates/base.html             # CREATE
internal/viewer/templates/leagues.html          # CREATE
internal/viewer/templates/league.html           # CREATE
internal/viewer/templates/error.html            # CREATE
internal/viewer/viewer_test.go                  # CREATE
cmd/viewer/main.go                              # CREATE
cmd/viewer/main_test.go                         # CREATE
.env.example                                    # CREATE
Makefile                                        # MODIFY: viewer target
README.md                                       # MODIFY: League viewer section
```

---

### Task 1: `pointsBreakdown` in the API

**Files:**
- Modify: `internal/domain/domain.go`, `internal/providers/espn/tables.go`, `internal/providers/espn/league.go`, `internal/providers/espn/espn_test.go`, `internal/service/leagues.go`, `internal/service/service_test.go`, `docs/superpowers/specs/2026-09-28-league-viewer-design.md`
- Create: `internal/providers/espn/breakdown_test.go`

**Interfaces:**
- Consumes: existing `tiers`, `steps`, `statKeys`, `statRowJSON.AppliedStats`, `scoring.Breakdown`, `attachPoints`.
- Produces: `domain.RosterEntryRef.PlatformBreakdown map[string]float64`; `domain.RosterEntry.PointsBreakdown map[string]float64` (`json:"pointsBreakdown,omitempty"`); unexported `espnKeyForID(id int) string`, `breakdownFromApplied(applied map[string]float64) map[string]float64` in package `espn`.

- [ ] **Step 1: Write the failing tests**

`internal/providers/espn/breakdown_test.go`:

```go
package espn

import (
	"reflect"
	"testing"
)

func TestESPNKeyForID(t *testing.T) {
	for id, want := range map[int]string{
		3:   "pass_yd",             // direct
		80:  "fgm_0_19",            // multi-key → first key
		96:  "fum_rec",             // multi-key → first key
		101: "def_st_td",           // shared key
		92:  "espn_pa_14_17",       // tier
		8:   "espn_pass_yd_per_25", // step
		999: "espn_999",            // unmapped
	} {
		if got := espnKeyForID(id); got != want {
			t.Errorf("espnKeyForID(%d) = %q, want %q", id, got, want)
		}
	}
}

func TestBreakdownFromApplied(t *testing.T) {
	got := breakdownFromApplied(map[string]float64{
		"95": 2, "99": 3, "101": 6, "102": 6, "999": 1.5, "3": 0, "bad": 4,
	})
	want := map[string]float64{"int": 2, "sack": 3, "def_st_td": 12, "espn_999": 1.5}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("breakdownFromApplied = %v, want %v (zeros dropped, shared keys summed, non-numeric IDs skipped)", got, want)
	}
}
```

In `internal/providers/espn/espn_test.go`, inside `TestMatchupsPlatformPoints`, after the existing QB `PlatformPoints` check, add:

```go
	wantBreakdown := map[string]float64{"pass_yd": 10, "pass_td": 12, "pass_int": -2, "rush_yd": 4.5}
	if !reflect.DeepEqual(qb.PlatformBreakdown, wantBreakdown) {
		t.Fatalf("QB PlatformBreakdown = %v, want %v", qb.PlatformBreakdown, wantBreakdown)
	}
	if away.Bench[0].PlatformBreakdown != nil {
		t.Fatal("no actual row → no breakdown")
	}
```

In `internal/service/service_test.go`, in `espnMatchups()`, give the Mahomes ref a breakdown by changing its line to:

```go
				withPlatformBreakdown(withPlatformPoints(espnRef("QB", "3139477", "Patrick Mahomes", "QB", "KC"), 24.5), map[string]float64{"pass_yd": 12.5, "pass_td": 12}),
```

append to `internal/service/fakes_test.go`:

```go
func withPlatformBreakdown(r domain.RosterEntryRef, b map[string]float64) domain.RosterEntryRef {
	r.PlatformBreakdown = b
	return r
}
```

and in `TestMatchupsPlatformPoints` after the existing assertions add:

```go
	if !reflect.DeepEqual(st[0].PointsBreakdown, map[string]float64{"pass_yd": 12.5, "pass_td": 12}) {
		t.Errorf("platform breakdown = %v", st[0].PointsBreakdown)
	}
	// Computed: rec 6×0.5 + rec_yd 85×0.1 under the ESPN league's flat rules.
	if !reflect.DeepEqual(st[2].PointsBreakdown, map[string]float64{"rec": 3, "rec_yd": 8.5}) {
		t.Errorf("computed breakdown = %v", st[2].PointsBreakdown)
	}
	if st[1].PointsBreakdown != nil {
		t.Errorf("platform points without a breakdown should leave it nil: %v", st[1].PointsBreakdown)
	}
```

and in `TestMatchupsPlatformPointsSurviveStatsFailure` add:

```go
	if st[2].PointsBreakdown != nil {
		t.Errorf("no points → no breakdown: %v", st[2].PointsBreakdown)
	}
```

(Add `"reflect"` to `service_test.go` imports if it is not already there.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/providers/espn/ ./internal/service/`
Expected: FAIL — `undefined: espnKeyForID`, `qb.PlatformBreakdown undefined`, `st[0].PointsBreakdown undefined`.

- [ ] **Step 3: Implement**

`internal/domain/domain.go` — add to `RosterEntryRef` after `PlatformPoints`:

```go
	// PlatformBreakdown is the platform's own points per stat key for that week.
	PlatformBreakdown map[string]float64
```

and to `RosterEntry` after `PointsSource`:

```go
	PointsBreakdown map[string]float64 `json:"pointsBreakdown,omitempty"` // points per stat key when Points is set
```

`internal/providers/espn/tables.go` — append:

```go
// espnKeyForID returns the stat key an ESPN stat ID's points are reported under:
// its tier key, step key, or first mapped Sleeper key; unmapped IDs become "espn_<id>".
func espnKeyForID(id int) string {
	if t, ok := tiers[id]; ok {
		return t.key
	}
	if s, ok := steps[id]; ok {
		return s.key
	}
	if keys, ok := statKeys[id]; ok && len(keys) > 0 {
		return keys[0]
	}
	return fmt.Sprintf("espn_%d", id)
}
```

`internal/providers/espn/league.go` — append:

```go
// breakdownFromApplied converts ESPN appliedStats (stat ID → points) into points per
// stat key, dropping zeros and summing IDs that share a key.
func breakdownFromApplied(applied map[string]float64) map[string]float64 {
	out := map[string]float64{}
	for k, v := range applied {
		id, err := strconv.Atoi(k)
		if err != nil || v == 0 {
			continue
		}
		out[espnKeyForID(id)] += v
	}
	return out
}
```

and in `toRoster`, inside `if row, ok := e.actualRow(week); ok {`, after setting `ref.PlatformPoints`:

```go
				ref.PlatformBreakdown = breakdownFromApplied(row.AppliedStats)
```

`internal/service/leagues.go` — in `attachPoints`, replace the two `switch` cases with:

```go
			case i < len(g.refs) && g.refs[i].PlatformPoints != nil:
				pts := *g.refs[i].PlatformPoints
				e.Points, e.PointsSource = &pts, "platform"
				e.PointsBreakdown = maps.Clone(g.refs[i].PlatformBreakdown)
			case hasLine:
				pts := scoring.Points(line.Stats, s, e.Player.Position)
				e.Points, e.PointsSource = &pts, "computed"
				b := scoring.Breakdown(line.Stats, s, e.Player.Position)
				for k, v := range b {
					b[k] = math.Round(v*100) / 100
				}
				e.PointsBreakdown = b
```

(add `"maps"` and `"math"` imports if missing).

In the spec §2, replace "Present exactly when `points` is non-null (same rule as `pointsSource`); omitted otherwise." with "Set only when `points` is non-null (same rule as `pointsSource`); omitted when null or when no stat contributed (e.g. 0 points)."

- [ ] **Step 4: Run the tests to verify they pass**

Run: `gofmt -l . ; go vet ./... && go test -race ./...`
Expected: no gofmt output; all packages `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/domain internal/providers/espn internal/service docs/superpowers/specs/2026-09-28-league-viewer-design.md
git commit -m "feat: add per-stat pointsBreakdown to matchup players"
```

---

### Task 2: Viewer package

**Files:**
- Create: `internal/viewer/client.go`, `internal/viewer/labels.go`, `internal/viewer/view.go`, `internal/viewer/handlers.go`, `internal/viewer/templates/base.html`, `internal/viewer/templates/leagues.html`, `internal/viewer/templates/league.html`, `internal/viewer/templates/error.html`, `internal/viewer/viewer_test.go`

**Interfaces:**
- Consumes: `domain.LeagueSummary`, `domain.League`, `domain.Matchup`, `domain.RosterEntry` (incl. `PointsBreakdown`, Task 1) — decoded from the API's JSON.
- Produces: `viewer.Client{BaseURL, APIKey string; HTTP *http.Client}`, `(*Client).Get(ctx, path string, q url.Values, out any) (Meta, error)`, `viewer.APIError{Status int; Code, Message string}`, `viewer.Meta{Season, Week int; Warnings []string}`, `viewer.Label(key string) string`, `viewer.NewHandler(c *Client) http.Handler`.

- [ ] **Step 1: Write the failing tests**

`internal/viewer/viewer_test.go`:

```go
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
     {"slot":"QB","player":{"id":"4046","name":"Patrick Mahomes","position":"QB","nflTeam":"KC","platformIds":{}},"stats":{"pass_td":2,"pass_yd":250},"points":18,"pointsSource":"platform","pointsBreakdown":{"pass_td":8,"espn_pass_yd_per_25":10}},
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
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/viewer/`
Expected: FAIL — `no non-test Go files` / `undefined: NewHandler`.

- [ ] **Step 3: Implement the client and labels**

`internal/viewer/client.go`:

```go
// Package viewer renders a local, read-only web view of the owner's leagues by
// calling the sports API server-side (the API key never reaches the browser).
package viewer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// APIError is a non-2xx API response, decoded from its error envelope.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("API %d %s: %s", e.Status, e.Code, e.Message)
}

type Meta struct {
	Season   int      `json:"season"`
	Week     int      `json:"week"`
	Warnings []string `json:"warnings"`
}

// Get fetches path with query q and decodes the envelope's data into out.
func (c *Client) Get(ctx context.Context, path string, q url.Values, out any) (Meta, error) {
	u := strings.TrimRight(c.BaseURL, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Meta{}, err
	}
	req.Header.Set("X-API-Key", c.APIKey)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Meta{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return Meta{}, &APIError{Status: resp.StatusCode, Code: e.Error.Code, Message: e.Error.Message}
	}
	var env struct {
		Data json.RawMessage `json:"data"`
		Meta Meta            `json:"meta"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return Meta{}, fmt.Errorf("decoding %s: %w", path, err)
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return Meta{}, fmt.Errorf("decoding %s data: %w", path, err)
	}
	return env.Meta, nil
}
```

`internal/viewer/labels.go`:

```go
package viewer

import "strings"

var labels = map[string]string{
	"pass_yd": "Passing yards", "pass_td": "Passing TDs", "pass_int": "Interceptions thrown", "pass_2pt": "Passing 2-pt conversions",
	"rush_yd": "Rushing yards", "rush_td": "Rushing TDs", "rush_2pt": "Rushing 2-pt conversions",
	"rec": "Receptions", "rec_yd": "Receiving yards", "rec_td": "Receiving TDs", "rec_2pt": "Receiving 2-pt conversions",
	"fum_lost": "Fumbles lost",
	"fgm_0_19": "FG made 0–19", "fgm_20_29": "FG made 20–29", "fgm_30_39": "FG made 30–39", "fgm_40_49": "FG made 40–49",
	"fgm_50p": "FG made 50+", "fgm_50_59": "FG made 50–59", "fgm_60p": "FG made 60+",
	"fgmiss": "FG missed", "fgmiss_40_49": "FG missed 40–49", "xpm": "Extra points made", "xpmiss": "Extra points missed",
	"sack": "Sacks", "int": "Interceptions", "fum_rec": "Fumble recoveries", "def_st_fum_rec": "Special-teams fumble recoveries",
	"safe": "Safeties", "blk_kick": "Blocked kicks", "def_td": "Defensive TDs", "def_st_td": "Return TDs (D/ST)", "st_td": "Return TDs",
	"pts_allow": "Points allowed", "yds_allow": "Yards allowed",
	"espn_pass_yd_per_25": "Every 25 passing yards",
}

// Label returns a human-readable name for a stat key, falling back to the key itself.
func Label(key string) string {
	if l, ok := labels[key]; ok {
		return l
	}
	for prefix, unit := range map[string]string{"espn_pa_": "points allowed", "pts_allow_": "points allowed", "espn_ya_": "yards allowed"} {
		if r, ok := strings.CutPrefix(key, prefix); ok {
			return rangeLabel(r) + " " + unit
		}
	}
	return key
}

// rangeLabel turns "14_17" into "14–17", "46p" into "46+", and "0" into "0".
func rangeLabel(r string) string {
	if n, ok := strings.CutSuffix(r, "p"); ok {
		return n + "+"
	}
	return strings.ReplaceAll(r, "_", "–")
}
```

- [ ] **Step 4: Implement the view models**

`internal/viewer/view.go`:

```go
package viewer

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

type statRow struct {
	Label  string
	Points float64
}

type playerView struct {
	Slot, Name, NFLTeam string
	Points              *float64
	Source              string // "ESPN", "computed", or ""
	Breakdown           []statRow
	Total               float64
	Stats               string
}

type sideView struct {
	Name     string
	Points   float64
	Starters []playerView
	Bench    []playerView
}

type matchupView struct{ Home, Away sideView }

type ruleView struct {
	Label string
	Value float64
}

type positionRules struct {
	Position string
	Rules    []ruleView
}

type derivedView struct{ Label, From, Range string }

type leagueView struct {
	ID, Name                string
	Season, Week, Prev, Next int
	Weeks                   []int
	Warnings                []string
	Matchups                []matchupView
	Rules                   []ruleView
	ByPosition              []positionRules
	Derived                 []derivedView
	Unsupported             []string
}

func buildLeagueView(lg domain.League, ms []domain.Matchup, meta Meta) leagueView {
	names := map[string]string{}
	for _, t := range lg.Teams {
		names[t.ID] = t.Name
	}
	v := leagueView{ID: lg.ID, Name: lg.Name, Season: meta.Season, Week: meta.Week, Warnings: meta.Warnings, Unsupported: lg.UnsupportedRules}
	if v.Week > 1 {
		v.Prev = v.Week - 1
	}
	if v.Week < 18 {
		v.Next = v.Week + 1
	}
	for w := 1; w <= 18; w++ {
		v.Weeks = append(v.Weeks, w)
	}
	for _, m := range ms {
		v.Matchups = append(v.Matchups, matchupView{Home: buildSide(m.Home, names), Away: buildSide(m.Away, names)})
	}
	v.Rules = rules(lg.Scoring)
	positions := make([]string, 0, len(lg.ScoringByPosition))
	for p := range lg.ScoringByPosition {
		positions = append(positions, p)
	}
	sort.Strings(positions)
	for _, p := range positions {
		v.ByPosition = append(v.ByPosition, positionRules{Position: p, Rules: rules(lg.ScoringByPosition[p])})
	}
	for _, d := range lg.DerivedStats {
		v.Derived = append(v.Derived, derivedView{Label: Label(d.Key), From: Label(d.From), Range: rangeText(d)})
	}
	return v
}

func buildSide(s domain.MatchupSide, names map[string]string) sideView {
	name := names[s.TeamID]
	if name == "" {
		name = "Team " + s.TeamID
	}
	sv := sideView{Name: name, Points: s.Points}
	for _, e := range s.Roster.Starters {
		sv.Starters = append(sv.Starters, buildPlayer(e))
	}
	for _, e := range append(slices.Clone(s.Roster.Bench), s.Roster.Reserve...) {
		sv.Bench = append(sv.Bench, buildPlayer(e))
	}
	return sv
}

func buildPlayer(e domain.RosterEntry) playerView {
	pv := playerView{Slot: e.Slot, Name: e.Player.Name, NFLTeam: e.Player.NFLTeam, Points: e.Points}
	switch e.PointsSource {
	case "platform":
		pv.Source = "ESPN"
	case "computed":
		pv.Source = "computed"
	}
	pv.Breakdown, pv.Total = breakdownRows(e.PointsBreakdown)
	keys := make([]string, 0, len(e.Stats))
	for k := range e.Stats {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %g", k, e.Stats[k]))
	}
	pv.Stats = strings.Join(parts, " · ")
	return pv
}

// breakdownRows labels a breakdown, sorts by absolute points (then label), and totals it.
func breakdownRows(b map[string]float64) ([]statRow, float64) {
	rows := make([]statRow, 0, len(b))
	var total float64
	for k, v := range b {
		rows = append(rows, statRow{Label: Label(k), Points: v})
		total += v
	}
	slices.SortFunc(rows, func(a, b statRow) int {
		if c := cmp.Compare(math.Abs(b.Points), math.Abs(a.Points)); c != 0 {
			return c
		}
		return cmp.Compare(a.Label, b.Label)
	})
	return rows, math.Round(total*100) / 100
}

// rules lists non-zero rules by label.
func rules(r domain.ScoringRules) []ruleView {
	out := []ruleView{}
	for k, v := range r {
		if v != 0 {
			out = append(out, ruleView{Label: Label(k), Value: v})
		}
	}
	slices.SortFunc(out, func(a, b ruleView) int { return cmp.Compare(a.Label, b.Label) })
	return out
}

func rangeText(d domain.DerivedStat) string {
	switch {
	case d.Step > 0:
		return fmt.Sprintf("every %g", d.Step)
	case d.Max == nil:
		return fmt.Sprintf("%g+", d.Min)
	case *d.Max == d.Min:
		return fmt.Sprintf("%g", d.Min)
	default:
		return fmt.Sprintf("%g–%g", d.Min, *d.Max)
	}
}
```

- [ ] **Step 5: Implement handlers and templates**

`internal/viewer/handlers.go`:

```go
package viewer

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

//go:embed templates/*.html
var templateFS embed.FS

var funcs = template.FuncMap{
	"pts": func(v any) string {
		switch x := v.(type) {
		case float64:
			return fmt.Sprintf("%.2f", x)
		case *float64:
			if x == nil {
				return "—"
			}
			return fmt.Sprintf("%.2f", *x)
		}
		return ""
	},
	"num": func(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) },
}

type server struct {
	c    *Client
	tmpl *template.Template
}

// NewHandler serves the leagues list at / and one league at /league/{id}.
func NewHandler(c *Client) http.Handler {
	s := &server{c: c, tmpl: template.Must(template.New("").Funcs(funcs).ParseFS(templateFS, "templates/*.html"))}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.leagues)
	mux.HandleFunc("GET /league/{id}", s.league)
	return mux
}

type leagueLink struct{ Name, Href string }

type leaguesView struct {
	Season        int
	Warnings      []string
	ESPN, Sleeper []leagueLink
}

type errorView struct{ Title, Code, Message string }

func (s *server) leagues(w http.ResponseWriter, r *http.Request) {
	var ls []domain.LeagueSummary
	meta, err := s.c.Get(r.Context(), "/v1/nfl/leagues", nil, &ls)
	if err != nil {
		s.fail(w, err)
		return
	}
	v := leaguesView{Season: meta.Season, Warnings: meta.Warnings}
	for _, l := range ls {
		link := leagueLink{Name: l.Name, Href: "/league/" + url.PathEscape(l.ID)}
		if l.Platform == domain.PlatformESPN {
			v.ESPN = append(v.ESPN, link)
		} else {
			v.Sleeper = append(v.Sleeper, link)
		}
	}
	s.render(w, http.StatusOK, "leagues.html", v)
}

func (s *server) league(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	q := url.Values{"include": {"stats"}}
	if wk := r.URL.Query().Get("week"); wk != "" {
		n, err := strconv.Atoi(wk)
		if err != nil || n < 1 || n > 18 {
			s.render(w, http.StatusBadRequest, "error.html", errorView{Title: "Invalid week", Message: "week must be a number from 1 to 18"})
			return
		}
		q.Set("week", wk)
	}
	base := "/v1/nfl/leagues/" + url.PathEscape(id)
	var lg domain.League
	if _, err := s.c.Get(r.Context(), base, nil, &lg); err != nil {
		s.fail(w, err)
		return
	}
	var ms []domain.Matchup
	meta, err := s.c.Get(r.Context(), base+"/matchups", q, &ms)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, http.StatusOK, "league.html", buildLeagueView(lg, ms, meta))
}

func (s *server) fail(w http.ResponseWriter, err error) {
	var ae *APIError
	if errors.As(err, &ae) {
		status := ae.Status
		if status < 400 {
			status = http.StatusBadGateway
		}
		s.render(w, status, "error.html", errorView{Title: fmt.Sprintf("API error %d", ae.Status), Code: ae.Code, Message: ae.Message})
		return
	}
	s.render(w, http.StatusBadGateway, "error.html", errorView{Title: "API unreachable", Message: err.Error()})
}

// render executes into a buffer first so a template error never sends a half page.
func (s *server) render(w http.ResponseWriter, status int, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}
```

`internal/viewer/templates/base.html`:

```html
{{define "head"}}<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.}}</title>
<style>
body{font:14px/1.45 system-ui,sans-serif;margin:1.5rem auto;max-width:72rem;padding:0 1rem;color:#1d1d1f;background:#fff}
a{color:#0b57d0} h1{font-size:1.4rem;margin:.2rem 0} h2{font-size:1.1rem} h3{font-size:1rem;margin:.6rem 0 .3rem}
.muted{color:#666;font-size:12px}
.warn{background:#fff4e5;border:1px solid #f0c36d;padding:.4rem .7rem;margin:.4rem 0}
.err{background:#fdecea;border:1px solid #f5a8a0;padding:.6rem .8rem}
details{border:1px solid #ddd;border-radius:6px;margin:.4rem 0;padding:.3rem .6rem}
td details{border:none;margin:0;padding:0}
summary{cursor:pointer}
.matchup>summary{font-weight:600}
.sides{display:grid;grid-template-columns:1fr 1fr;gap:1rem}
table{border-collapse:collapse;width:100%} td,th{padding:.2rem .4rem;border-bottom:1px solid #eee;text-align:left;vertical-align:top}
.num{text-align:right;font-variant-numeric:tabular-nums}
.tag{font-size:11px;padding:0 .3rem;border-radius:3px;background:#e8eefc}
.tag.computed{background:#e7f5e9}
nav form{display:inline}
</style></head><body>{{end}}
{{define "foot"}}</body></html>{{end}}
```

`internal/viewer/templates/leagues.html`:

```html
{{template "head" "Leagues"}}
<h1>Leagues {{.Season}}</h1>
{{range .Warnings}}<div class="warn">{{.}}</div>{{end}}
{{if .ESPN}}<h2>ESPN</h2><ul>{{range .ESPN}}<li><a href="{{.Href}}">{{.Name}}</a></li>{{end}}</ul>{{end}}
{{if .Sleeper}}<h2>Sleeper</h2><ul>{{range .Sleeper}}<li><a href="{{.Href}}">{{.Name}}</a></li>{{end}}</ul>{{end}}
{{template "foot"}}
```

`internal/viewer/templates/league.html`:

```html
{{template "head" .Name}}
<p><a href="/">← All leagues</a></p>
<h1>{{.Name}}</h1>
<p class="muted">{{.Season}} · week {{.Week}}</p>
<nav>
{{if .Prev}}<a href="?week={{.Prev}}">← Week {{.Prev}}</a>{{end}}
<form method="get"><select name="week">{{range .Weeks}}<option value="{{.}}"{{if eq . $.Week}} selected{{end}}>Week {{.}}</option>{{end}}</select> <button>Go</button></form>
{{if .Next}}<a href="?week={{.Next}}">Week {{.Next}} →</a>{{end}}
· <a href="?week={{.Week}}">Refresh</a>
</nav>
{{range .Warnings}}<div class="warn">{{.}}</div>{{end}}
{{if not .Matchups}}<p>No matchups this week.</p>{{end}}
{{range .Matchups}}
<details class="matchup"><summary>{{.Home.Name}} {{pts .Home.Points}} — {{pts .Away.Points}} {{.Away.Name}}</summary>
<div class="sides">{{template "side" .Home}}{{template "side" .Away}}</div>
</details>
{{end}}
<details><summary>Scoring rules</summary>
<h3>Base</h3>
<table>{{range .Rules}}<tr><td>{{.Label}}</td><td class="num">{{num .Value}}</td></tr>{{end}}</table>
{{range .ByPosition}}<h3>{{.Position}}</h3>
<table>{{range .Rules}}<tr><td>{{.Label}}</td><td class="num">{{num .Value}}</td></tr>{{end}}</table>{{end}}
{{if .Derived}}<h3>Computed stats</h3>
<table>{{range .Derived}}<tr><td>{{.Label}}</td><td>{{.From}}</td><td>{{.Range}}</td></tr>{{end}}</table>{{end}}
{{if .Unsupported}}<h3>Unsupported</h3><ul>{{range .Unsupported}}<li>{{.}}</li>{{end}}</ul>{{end}}
</details>
{{template "foot"}}

{{define "side"}}<div>
<h3>{{.Name}} <span class="muted">{{pts .Points}}</span></h3>
<table>
<tr><th>Slot</th><th>Player</th><th>Team</th><th class="num">Pts</th><th></th></tr>
{{range .Starters}}{{template "player" .}}{{end}}
{{if .Bench}}<tr><th colspan="5">Bench</th></tr>{{range .Bench}}{{template "player" .}}{{end}}{{end}}
</table>
</div>{{end}}

{{define "player"}}<tr>
<td>{{.Slot}}</td>
<td>{{if .Breakdown}}<details><summary>{{.Name}}</summary>
<table>{{range .Breakdown}}<tr><td>{{.Label}}</td><td class="num">{{pts .Points}}</td></tr>{{end}}
<tr><th>Total</th><th class="num">{{pts .Total}}</th></tr></table>
{{if .Stats}}<p class="muted">{{.Stats}}</p>{{end}}
</details>{{else}}{{.Name}}{{end}}</td>
<td>{{.NFLTeam}}</td>
<td class="num">{{pts .Points}}</td>
<td>{{if .Source}}<span class="tag {{.Source}}">{{.Source}}</span>{{end}}</td>
</tr>{{end}}
```

`internal/viewer/templates/error.html`:

```html
{{template "head" .Title}}
<p><a href="/">← All leagues</a></p>
<div class="err"><h1>{{.Title}}</h1>
{{if .Code}}<p><code>{{.Code}}</code></p>{{end}}
<p>{{.Message}}</p></div>
{{template "foot"}}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `gofmt -l . ; go vet ./... && go test -race ./internal/viewer/`
Expected: no gofmt output; `ok`. If `html/template` percent-encodes the `:` in `href="/league/espn:1"`, the leagues test will fail: report it (DONE_WITH_CONCERNS) with the rendered href rather than weakening the assertion — the link must still route to `/league/{id}` correctly.

- [ ] **Step 7: Commit**

```bash
git add internal/viewer
git commit -m "feat: add server-rendered league viewer package"
```

---

### Task 3: `cmd/viewer`, Makefile, docs

**Files:**
- Create: `cmd/viewer/main.go`, `cmd/viewer/main_test.go`, `.env.example`
- Modify: `Makefile`, `README.md`

**Interfaces:**
- Consumes: `viewer.Client`, `viewer.NewHandler` (Task 2).
- Produces: `make viewer`; `loadConfig(getenv func(string) string) (config, error)` in `cmd/viewer`.

- [ ] **Step 1: Write the failing test**

`cmd/viewer/main_test.go`:

```go
package main

import (
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	c, err := loadConfig(env(map[string]string{"API_URL": " https://x.example ", "API_KEY": "k"}))
	if err != nil || c.apiURL != "https://x.example" || c.apiKey != "k" {
		t.Fatalf("config = %+v, %v", c, err)
	}
	for name, m := range map[string]map[string]string{
		"API_URL": {"API_KEY": "k"},
		"API_KEY": {"API_URL": "https://x.example"},
	} {
		if _, err := loadConfig(env(m)); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("missing %s: err = %v", name, err)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/viewer/`
Expected: FAIL — `no non-test Go files` / `undefined: loadConfig`.

- [ ] **Step 3: Implement**

`cmd/viewer/main.go`:

```go
// Command viewer serves a local, read-only view of the owner's leagues at
// http://127.0.0.1:8081, calling the deployed API with API_KEY server-side.
package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/vsnandy/sports-api-go/internal/viewer"
)

const addr = "127.0.0.1:8081"

type config struct{ apiURL, apiKey string }

func loadConfig(getenv func(string) string) (config, error) {
	c := config{apiURL: strings.TrimSpace(getenv("API_URL")), apiKey: getenv("API_KEY")}
	if c.apiURL == "" {
		return config{}, errors.New("API_URL is required (see .env.example)")
	}
	if c.apiKey == "" {
		return config{}, errors.New("API_KEY is required (see .env.example)")
	}
	return c, nil
}

func main() {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "viewer:", err)
		os.Exit(2)
	}
	c := &viewer.Client{BaseURL: cfg.apiURL, APIKey: cfg.apiKey, HTTP: &http.Client{Timeout: 20 * time.Second}}
	fmt.Printf("league viewer on http://%s\n", addr)
	if err := http.ListenAndServe(addr, viewer.NewHandler(c)); err != nil {
		fmt.Fprintln(os.Stderr, "viewer:", err)
		os.Exit(1)
	}
}
```

`.env.example`:

```bash
# Copy to .env (git-ignored) for `make viewer`.
# API_URL: terraform -chdir=deploy/terraform output -raw api_url
API_URL=https://<api-id>.execute-api.us-east-1.amazonaws.com
# API_KEY: aws ssm get-parameter --name /sports-api/api-key --with-decryption --query Parameter.Value --output text
API_KEY=<api key>
```

`Makefile`: add `viewer` to `.PHONY` and this target (recipe lines start with a TAB):

```make
viewer:
	@test -f .env || { echo "create .env from .env.example first"; exit 1; }
	set -a && . ./.env && set +a && go run ./cmd/viewer
```

`README.md`: add after the "Scoring and points" section:

````markdown
## League viewer

A local, read-only page for your leagues: matchups by week, each player's points and how they were earned, and each league's scoring rules.

```bash
cp .env.example .env   # fill in API_URL and API_KEY (commands in the file)
make viewer            # then open http://127.0.0.1:8081
```

It listens on 127.0.0.1 only and calls the API from the Go process, so the API key never reaches the browser.
````

- [ ] **Step 4: Run the tests to verify they pass**

Run: `gofmt -l . ; go vet ./... && go test -race ./... && go build ./cmd/viewer && rm -f viewer`
Expected: clean; all `ok`; build succeeds.

- [ ] **Step 5: Commit**

```bash
git add cmd/viewer .env.example Makefile README.md
git commit -m "feat: add cmd/viewer and make viewer"
```

- [ ] **Step 6 (controller, after the API change is deployed): Manual check**

With the Task 1 API change deployed, run the viewer against the live API (`API_URL`/`API_KEY` in the environment, key from SSM, never printed), open `/` and each of the 7 leagues for weeks 1–3, and confirm: team totals equal starter sums, breakdowns total each player's points, ESPN/computed tags, scoring rules render. Before deploy, the pages still render but without breakdowns.
