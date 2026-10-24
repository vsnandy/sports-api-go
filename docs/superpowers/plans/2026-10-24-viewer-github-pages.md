# Publish the League Viewer to GitHub Pages Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish a read-only, periodically-refreshed static copy of the league viewer (leagues list, per-league matchups, rooting guide) to GitHub Pages, without ever putting `API_KEY` in the browser or in the published output.

**Architecture:** The existing `internal/viewer` handlers and templates render the same HTML either way; a new `linker` abstraction is the only thing that changes between modes — it decides whether a page's internal links are live-server query strings (`?week=5`) or real static file paths (`week-5.html`). A new static-export driver drives the existing handler in-process (via `httptest`, no network listener) for every known page (leagues list, each league x weeks 1–18, rooting guide x weeks 1–18 — the week range is fixed, so this is a fully enumerable crawl, not link discovery) and writes the output to disk. A new GitHub Actions workflow runs that driver on a schedule and publishes the result with `actions/deploy-pages`.

**Refinement from the spec:** the spec's §2.1 described the static mode's base URL as an absolute URL written into a `<base href>` tag. This plan instead has `staticLinker` bake a configurable **path prefix** (e.g. `/sports-api-go/`) directly into every href it builds (`/sports-api-go/league/espn:1/index.html`), and drops the `<base>` tag entirely. Same outcome (pages resolve correctly under the GitHub Pages project-site subpath), fewer moving parts — no `head`/`foot` template changes, no new field threaded through three template call sites. The env var is `SITE_BASE_PATH`, not `SITE_BASE_URL`.

**Tech Stack:** Go 1.26 stdlib only (`net/http/httptest`, `net/url`, `strconv`) — no new dependencies. GitHub Actions (`actions/configure-pages`, `actions/upload-pages-artifact`, `actions/deploy-pages`).

**Spec:** `docs/superpowers/specs/2026-10-24-viewer-github-pages-design.md`

## Global Constraints

- Weeks are always 1–18, independent of season (`weekNav`, unchanged) — the static crawl enumerates exactly this range.
- `API_KEY` must never reach the browser and must never appear in any file written to the static output directory.
- Every request into the viewer handler — including the static exporter's in-process ones — must still pass the `loopbackOnly` host check; the exporter sets `Host: "127.0.0.1"` on every request it builds.
- No new third-party Go dependencies.
- `cmd/viewer`'s existing behavior (`make viewer`, `NewHandler`, live hrefs) must not change — verified by the existing test suite continuing to pass (with the href-string updates in Task 2, which reflect an intentional, uniform "always absolute path" policy, not a behavior regression).
- GitHub Actions workflow permissions: `contents: read`, `id-token: write`, `pages: write`. Triggers: `schedule` (every 30 minutes), `workflow_dispatch`, `push` to `main`.

## Review Focus

- **League IDs with `:` in them (e.g. `espn:1`).** `url.PathEscape` does not escape `:` (it's a valid `pchar`), so both the href and the on-disk directory name are literally `league/espn:1/...` in both modes — a person clicking a league link must land on a file that actually exists at that exact path. Covered in Task 1 (linker tests) and Task 3 (`RenderSite` writes a real `espn:1` directory).
- **One league's API call failing during static export.** The live viewer already degrades gracefully per-page; a static build must not do the same thing at build time — a partial failure should fail the whole export rather than silently publish a site missing pages or containing error-page HTML in place of real content. Covered in Task 3.
- **`SITE_BASE_PATH` set without a trailing (or leading) slash.** An operator typo here (`/sports-api-go` instead of `/sports-api-go/`) must not silently produce a malformed href like `/sports-api-goleague/...`. Covered in Task 4 (`loadConfig` normalizes it).
- **Zero leagues configured.** `RenderSite` must still produce a valid `index.html` and a valid (empty-state) rooting guide rather than erroring, since an empty-but-valid site is better than a failed build. Covered in Task 3.
- **The week `<select>` losing its `<form>` wrapper.** Removing the `<form>`/`Go` button (Task 2) must not silently drop the ability to jump to an arbitrary week — the `onchange` handler must actually navigate. Covered in Task 2's manual check and by the existing `mustContain` assertions on the rendered `<option>` hrefs.

---

## Task 1: Href Linker Abstraction

**Files:**
- Create: `internal/viewer/links.go`
- Test: `internal/viewer/links_test.go`

**Interfaces:**
- Produces: `type linker interface { leagues() string; league(id string, week int) string; rooting(week int) string }`, `type liveLinker struct{}`, `type staticLinker struct{ base string }` (all implement `linker`), `type weekOption struct { Week int; Href string }`, `func weekLinks(week int, linkFor func(int) string) (prev, next int, prevHref, nextHref, refreshHref string, weeks []weekOption)`.
- Consumes: nothing from other tasks (this is the foundation task).

- [ ] **Step 1: Write the failing tests**

Create `internal/viewer/links_test.go`:

```go
package viewer

import (
	"reflect"
	"testing"
)

func TestLiveLinker(t *testing.T) {
	var l liveLinker
	if got := l.leagues(); got != "/" {
		t.Errorf("leagues() = %q, want /", got)
	}
	cases := []struct {
		id   string
		week int
		want string
	}{
		{"espn:1", 0, "/league/espn:1"},
		{"espn:1", 5, "/league/espn:1?week=5"},
		{"sleeper:2", 1, "/league/sleeper:2?week=1"},
	}
	for _, c := range cases {
		if got := l.league(c.id, c.week); got != c.want {
			t.Errorf("league(%q, %d) = %q, want %q", c.id, c.week, got, c.want)
		}
	}
	if got := l.rooting(0); got != "/rooting" {
		t.Errorf("rooting(0) = %q, want /rooting", got)
	}
	if got := l.rooting(7); got != "/rooting?week=7" {
		t.Errorf("rooting(7) = %q, want /rooting?week=7", got)
	}
}

func TestStaticLinker(t *testing.T) {
	l := staticLinker{base: "/sports-api-go/"}
	if got := l.leagues(); got != "/sports-api-go/index.html" {
		t.Errorf("leagues() = %q", got)
	}
	cases := []struct {
		id   string
		week int
		want string
	}{
		{"espn:1", 0, "/sports-api-go/league/espn:1/index.html"},
		{"espn:1", 5, "/sports-api-go/league/espn:1/week-5.html"},
		{"sleeper:2", 18, "/sports-api-go/league/sleeper:2/week-18.html"},
	}
	for _, c := range cases {
		if got := l.league(c.id, c.week); got != c.want {
			t.Errorf("league(%q, %d) = %q, want %q", c.id, c.week, got, c.want)
		}
	}
	if got := l.rooting(0); got != "/sports-api-go/rooting/index.html" {
		t.Errorf("rooting(0) = %q", got)
	}
	if got := l.rooting(3); got != "/sports-api-go/rooting/week-3.html" {
		t.Errorf("rooting(3) = %q", got)
	}
}

func TestStaticLinkerRootBase(t *testing.T) {
	l := staticLinker{base: "/"}
	if got := l.league("espn:1", 2); got != "/league/espn:1/week-2.html" {
		t.Errorf("league() = %q", got)
	}
}

func TestWeekLinks(t *testing.T) {
	linkFor := func(w int) string { return "W" + itoaForTest(w) }
	prev, next, prevHref, nextHref, refreshHref, weeks := weekLinks(1, linkFor)
	if prev != 0 || prevHref != "" {
		t.Errorf("week 1: prev=%d prevHref=%q, want 0 and empty", prev, prevHref)
	}
	if next != 2 || nextHref != "W2" {
		t.Errorf("week 1: next=%d nextHref=%q, want 2 and W2", next, nextHref)
	}
	if refreshHref != "W1" {
		t.Errorf("refreshHref = %q, want W1", refreshHref)
	}
	if len(weeks) != 18 || weeks[0] != (weekOption{1, "W1"}) || weeks[17] != (weekOption{18, "W18"}) {
		t.Errorf("weeks = %+v", weeks)
	}

	prev, next, _, nextHref, _, _ = weekLinks(18, linkFor)
	if prev != 17 || next != 0 || nextHref != "" {
		t.Errorf("week 18: prev=%d next=%d nextHref=%q, want 17, 0, empty", prev, next, nextHref)
	}
}

func itoaForTest(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/viewer/... -run 'TestLiveLinker|TestStaticLinker|TestWeekLinks' -v`
Expected: FAIL — `liveLinker`, `staticLinker`, `weekOption`, `weekLinks` are undefined.

- [ ] **Step 3: Implement `internal/viewer/links.go`**

```go
package viewer

import (
	"net/url"
	"strconv"
)

// linker builds the hrefs a page uses to link to itself (week nav) or to other pages
// (the leagues list, a league page). liveLinker is what cmd/viewer uses today;
// staticLinker is used only when exporting the site to static files for GitHub Pages.
type linker interface {
	leagues() string
	league(id string, week int) string // week 0 means the current/default week
	rooting(week int) string           // week 0 means the current/default week
}

// liveLinker reproduces the handler's existing live-server URLs unchanged.
type liveLinker struct{}

func (liveLinker) leagues() string { return "/" }

func (liveLinker) league(id string, week int) string {
	p := "/league/" + url.PathEscape(id)
	if week == 0 {
		return p
	}
	return p + "?week=" + strconv.Itoa(week)
}

func (liveLinker) rooting(week int) string {
	if week == 0 {
		return "/rooting"
	}
	return "/rooting?week=" + strconv.Itoa(week)
}

// staticLinker builds hrefs as real file paths under base, for a statically exported
// copy of the site (base must start and end with "/", e.g. "/sports-api-go/" or "/").
type staticLinker struct{ base string }

func (l staticLinker) leagues() string { return l.base + "index.html" }

func (l staticLinker) league(id string, week int) string {
	p := l.base + "league/" + url.PathEscape(id) + "/"
	if week == 0 {
		return p + "index.html"
	}
	return p + "week-" + strconv.Itoa(week) + ".html"
}

func (l staticLinker) rooting(week int) string {
	if week == 0 {
		return l.base + "rooting/index.html"
	}
	return l.base + "rooting/week-" + strconv.Itoa(week) + ".html"
}

// weekOption is one entry in a week <select>: its display number and the href that
// week's page lives at under the active linker.
type weekOption struct {
	Week int
	Href string
}

// weekLinks wraps weekNav with hrefs built by linkFor, so both leagueView and
// rootingView can share one implementation of the week-nav row.
func weekLinks(week int, linkFor func(int) string) (prev, next int, prevHref, nextHref, refreshHref string, weeks []weekOption) {
	prev, next, ws := weekNav(week)
	if prev > 0 {
		prevHref = linkFor(prev)
	}
	if next > 0 {
		nextHref = linkFor(next)
	}
	refreshHref = linkFor(week)
	weeks = make([]weekOption, len(ws))
	for i, w := range ws {
		weeks[i] = weekOption{Week: w, Href: linkFor(w)}
	}
	return
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/viewer/... -run 'TestLiveLinker|TestStaticLinker|TestWeekLinks' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/viewer/links.go internal/viewer/links_test.go
git commit -m "feat(viewer): add linker abstraction for live vs. static hrefs

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

## Task 2: Wire the Linker Through Handlers, Views, and Templates

**Files:**
- Modify: `internal/viewer/handlers.go`
- Modify: `internal/viewer/view.go`
- Modify: `internal/viewer/rooting.go`
- Modify: `internal/viewer/templates/league.html`
- Modify: `internal/viewer/templates/rooting.html`
- Modify: `internal/viewer/viewer_test.go`
- Modify: `internal/viewer/view_test.go`
- Modify: `internal/viewer/rooting_test.go`

**Interfaces:**
- Consumes: `linker`, `liveLinker`, `staticLinker`, `weekOption`, `weekLinks` from Task 1.
- Produces: `func NewHandler(c *Client) http.Handler` (unchanged signature, now backed by `liveLinker`), `func NewStaticHandler(c *Client, basePath string) http.Handler` (new — Task 3/4 call this), `func buildLeagueView(lg domain.League, ms []domain.Matchup, meta Meta, links linker) leagueView`, `func buildRooting(season, reqWeek int, lws []leagueWeek, warnings []string, links linker) rootingView`.

This task makes hrefs uniformly absolute (`/league/espn:1?week=5` instead of the old bare `?week=5`) everywhere, including a league page's own week nav — today only the rooting guide's cross-league chips were absolute. That's why several existing test assertions change below; it's a deliberate, uniform policy, not a behavior regression (same destination, same page).

- [ ] **Step 1: Update the existing tests to the new call signatures and href strings**

In `internal/viewer/view_test.go`, change the `buildLeagueView` call in `TestBuildLeagueViewFlags`:

```go
	v := buildLeagueView(lg, ms, Meta{Week: 3}, liveLinker{})
```

In `internal/viewer/rooting_test.go`, change all three `buildRooting` calls to pass `liveLinker{}` as the fifth argument:

```go
	v := buildRooting(2026, 0, lws, []string{"upstream note"}, liveLinker{})
```

(in `TestBuildRooting`), and:

```go
	v := buildRooting(2026, 0, lws, nil, liveLinker{})
```

(in `TestBuildRootingWeekFromFirstLoaded`), and:

```go
	v := buildRooting(2026, 2, lws, nil, liveLinker{})
```

(in `TestBuildRootingRequestedWeek`).

Still in `internal/viewer/rooting_test.go`, update `TestRootingPage`'s nav assertions (its own week prev/next are now absolute paths, not bare query strings):

```go
		`href="/rooting?week=2"`, `href="/rooting?week=4"`,
```

(replacing the old `` `href="?week=2"`, `href="?week=4"` ``).

In `internal/viewer/viewer_test.go`, update `TestLeaguePage`'s nav assertions:

```go
		`href="/league/espn:1?week=2"`, `href="/league/espn:1?week=4"`, "Refresh",
```

(replacing the old `` `href="?week=2"`, `href="?week=4"`, "Refresh" ``).

Update `TestLeaguePageWeekParam`:

```go
	mustContain(t, body, "week 5", `href="/league/espn:1?week=4"`, `href="/league/espn:1?week=6"`)
```

Update `TestLeaguePageWeekBounds`:

```go
	_, first := get(t, h, "/league/espn:1?week=1")
	if strings.Contains(first, `href="/league/espn:1?week=0"`) {
		t.Error("week 1 must not link to week 0")
	}
	_, last := get(t, h, "/league/espn:1?week=18")
	if strings.Contains(last, `href="/league/espn:1?week=19"`) {
		t.Error("week 18 must not link to week 19")
	}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/viewer/... -v`
Expected: FAIL — compile errors (`buildLeagueView`/`buildRooting` called with the wrong number of arguments) and/or assertion failures on the href strings, since production code hasn't changed yet.

- [ ] **Step 3: Add the `links` field and `NewStaticHandler` in `internal/viewer/handlers.go`**

Change the `server` struct and `NewHandler`:

```go
type server struct {
	c     *Client
	links linker
	tmpl  *template.Template
}

// NewHandler serves the leagues list at /, one league at /league/{id}, and the rooting guide at /rooting.
func NewHandler(c *Client) http.Handler { return newHandler(c, liveLinker{}) }

// NewStaticHandler is NewHandler, but every page's internal links are file paths under
// basePath (e.g. "/sports-api-go/") instead of live-server query strings, for exporting
// the site to static files. basePath must start and end with "/"; "/" means site root.
func NewStaticHandler(c *Client, basePath string) http.Handler {
	return newHandler(c, staticLinker{base: basePath})
}

func newHandler(c *Client, links linker) http.Handler {
	s := &server{c: c, links: links, tmpl: template.Must(template.New("").Funcs(funcs).ParseFS(templateFS, "templates/*.html"))}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.leagues)
	mux.HandleFunc("GET /league/{id}", s.league)
	mux.HandleFunc("GET /rooting", s.rooting)
	return loopbackOnly(mux)
}
```

In `(s *server) leagues`, change the href line:

```go
		link := leagueLink{Name: l.Name, Href: s.links.league(l.ID, 0), Platform: string(l.Platform)}
```

In `(s *server) league`, pass the linker through:

```go
	s.render(w, http.StatusOK, "league.html", buildLeagueView(lg, ms, meta, s.links))
```

In `(s *server) rooting`, pass the linker through:

```go
	s.render(w, http.StatusOK, "rooting.html", buildRooting(meta.Season, week, lws, meta.Warnings, s.links))
```

- [ ] **Step 4: Update `internal/viewer/view.go`**

Add `PrevHref`, `NextHref`, `RefreshHref` and change `Weeks` to `[]weekOption` on `leagueView`:

```go
type leagueView struct {
	ID, Name                        string
	Season, Week, Prev, Next        int
	PrevHref, NextHref, RefreshHref string
	Weeks                            []weekOption
	Warnings                        []string
	Matchups                        []matchupView
	Rules                           []ruleView
	ByPosition                      []positionRules
	Derived                         []derivedView
	Unsupported                     []string
}
```

Change `buildLeagueView`'s signature and week-nav line:

```go
func buildLeagueView(lg domain.League, ms []domain.Matchup, meta Meta, links linker) leagueView {
	names := map[string]string{}
	for _, t := range lg.Teams {
		names[t.ID] = t.Name
	}
	v := leagueView{ID: lg.ID, Name: lg.Name, Season: meta.Season, Week: meta.Week, Warnings: meta.Warnings, Unsupported: lg.UnsupportedRules}
	v.Prev, v.Next, v.PrevHref, v.NextHref, v.RefreshHref, v.Weeks = weekLinks(v.Week, func(w int) string { return links.league(lg.ID, w) })
	for _, m := range ms {
```

(the rest of `buildLeagueView` is unchanged).

- [ ] **Step 5: Update `internal/viewer/rooting.go`**

Add the same three href fields and change `Weeks` on `rootingView`:

```go
type rootingView struct {
	Season, Week, Prev, Next        int
	PrevHref, NextHref, RefreshHref string
	Weeks                            []weekOption
	Warnings, NotPlaying             []string
	Counted                         int
	Players                         []rootingPlayer
}
```

Change `buildRooting`'s signature, its week-nav line, and the cross-league href line:

```go
func buildRooting(season, reqWeek int, lws []leagueWeek, warnings []string, links linker) rootingView {
	v := rootingView{Season: season, Week: reqWeek, Warnings: slices.Clone(warnings)}
	if v.Week == 0 {
		for _, lw := range lws {
			if lw.Err == nil {
				v.Week = lw.Week
				break
			}
		}
	}
	v.Prev, v.Next, v.PrevHref, v.NextHref, v.RefreshHref, v.Weeks = weekLinks(v.Week, links.rooting)
	byKey := map[string]*rootingPlayer{}
	for _, lw := range lws {
```

(leave the body unchanged down to the href line, then):

```go
		v.Counted++
		href := links.league(lw.ID, v.Week)
		add := func(s domain.MatchupSide, isFor bool) {
```

Remove the now-unused `"net/url"` import from `internal/viewer/rooting.go` (it was only used by the line just replaced) — its import block becomes:

```go
import (
	"cmp"
	"errors"
	"fmt"
	"slices"

	"github.com/vsnandy/sports-api-go/internal/domain"
)
```

- [ ] **Step 6: Update `internal/viewer/templates/league.html`**

Replace the nav row (lines 6–11):

```html
<div class="nav">
{{if .Prev}}<a href="{{.PrevHref}}">‹ Week {{.Prev}}</a>{{end}}
<select onchange="location.href=this.value">{{range .Weeks}}<option value="{{.Href}}"{{if eq .Week $.Week}} selected{{end}}>Week {{.Week}}</option>{{end}}</select>
{{if .Next}}<a href="{{.NextHref}}">Week {{.Next}} ›</a>{{end}}
<a href="{{.RefreshHref}}">⟳ Refresh</a>
</div>
```

- [ ] **Step 7: Update `internal/viewer/templates/rooting.html`**

Replace the nav row (lines 6–11) the same way:

```html
<div class="nav">
{{if .Prev}}<a href="{{.PrevHref}}">‹ Week {{.Prev}}</a>{{end}}
<select onchange="location.href=this.value">{{range .Weeks}}<option value="{{.Href}}"{{if eq .Week $.Week}} selected{{end}}>Week {{.Week}}</option>{{end}}</select>
{{if .Next}}<a href="{{.NextHref}}">Week {{.Next}} ›</a>{{end}}
<a href="{{.RefreshHref}}">⟳ Refresh</a>
</div>
```

- [ ] **Step 8: Run the tests to verify they pass**

Run: `go test ./internal/viewer/... -v`
Expected: PASS — all existing tests green with their updated href strings, plus Task 1's new tests still green.

Run: `gofmt -l internal/viewer` — expected empty output (format any file it lists with `gofmt -w`).

- [ ] **Step 9: Manual check**

```bash
make viewer-env   # if you don't already have .env
make viewer
```

Open `http://127.0.0.1:8081`, open a league, and confirm the week `<select>` actually navigates on change (no `Go` button needed anymore), and `‹ Week`/`Week ›`/`⟳ Refresh` still work.

- [ ] **Step 10: Commit**

```bash
git add internal/viewer/handlers.go internal/viewer/view.go internal/viewer/rooting.go \
        internal/viewer/templates/league.html internal/viewer/templates/rooting.html \
        internal/viewer/viewer_test.go internal/viewer/view_test.go internal/viewer/rooting_test.go
git commit -m "feat(viewer): thread the linker through handlers, views, and templates

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

## Task 3: Static Export Driver

**Files:**
- Create: `internal/viewer/static.go`
- Test: `internal/viewer/static_test.go`

**Interfaces:**
- Consumes: `NewStaticHandler(c *Client, basePath string) http.Handler` and `linker` from Tasks 1–2; `Client` from `internal/viewer/client.go` (existing).
- Produces: `func RenderSite(ctx context.Context, c *Client, outDir, basePath string) error` (Task 4 calls this).

**Note on the fake API used by the test below:** this mirrors the `newFakeAPI` pattern already used in `viewer_test.go`, but as a small standalone fake scoped to this test file (not reusing `newFakeAPI` itself, to keep this test independent of `viewer_test.go`'s fixture).

- [ ] **Step 1: Write the failing tests**

Create `internal/viewer/static_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/viewer/... -run TestRenderSite -v`
Expected: FAIL — `RenderSite` is undefined.

- [ ] **Step 3: Implement `internal/viewer/static.go`**

```go
package viewer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
)

// RenderSite renders every page of the viewer to static HTML files under outDir, for
// publishing to GitHub Pages. basePath is passed to NewStaticHandler (see its doc).
// It fails fast on the first page that doesn't render with a 2xx status, rather than
// publishing a partial or broken site.
func RenderSite(ctx context.Context, c *Client, outDir, basePath string) error {
	var ls []struct {
		ID string `json:"id"`
	}
	if _, err := c.Get(ctx, "/v1/nfl/leagues", nil, &ls); err != nil {
		return fmt.Errorf("listing leagues: %w", err)
	}

	h := NewStaticHandler(c, basePath)
	links := staticLinker{base: basePath}

	type page struct{ urlPath, outPath string }
	var pages []page
	pages = append(pages, page{"/", links.leagues()})
	for w := 0; w <= 18; w++ {
		pages = append(pages, page{urlPath: rootingURLPath(w), outPath: links.rooting(w)})
	}
	for _, l := range ls {
		for w := 0; w <= 18; w++ {
			pages = append(pages, page{urlPath: leagueURLPath(l.ID, w), outPath: links.league(l.ID, w)})
		}
	}

	for _, p := range pages {
		if err := renderPage(ctx, h, outDir, p.urlPath, p.outPath); err != nil {
			return err
		}
	}
	return nil
}

func leagueURLPath(id string, week int) string {
	p := "/league/" + id
	if week == 0 {
		return p
	}
	return p + "?week=" + strconv.Itoa(week)
}

func rootingURLPath(week int) string {
	if week == 0 {
		return "/rooting"
	}
	return "/rooting?week=" + strconv.Itoa(week)
}

// renderPage serves one page in-process and writes its body to outDir/outPath. It fails
// if the response isn't 2xx, naming the page's URL path so a build failure is traceable.
func renderPage(ctx context.Context, h http.Handler, outDir, urlPath, outPath string) error {
	req := httptest.NewRequest(http.MethodGet, urlPath, nil).WithContext(ctx)
	req.Host = "127.0.0.1"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code < 200 || rec.Code >= 300 {
		return fmt.Errorf("rendering %s: status %d: %s", urlPath, rec.Code, rec.Body.String())
	}
	full := filepath.Join(outDir, outPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("creating directory for %s: %w", outPath, err)
	}
	if err := os.WriteFile(full, rec.Body.Bytes(), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", outPath, err)
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/viewer/... -v`
Expected: PASS — every test in the package, including Tasks 1–2's tests.

Run: `gofmt -l internal/viewer` — expected empty output.

- [ ] **Step 5: Commit**

```bash
git add internal/viewer/static.go internal/viewer/static_test.go
git commit -m "feat(viewer): add RenderSite static export driver

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

## Task 4: `cmd/viewer-static` and Local Build Target

**Files:**
- Create: `cmd/viewer-static/main.go`
- Test: `cmd/viewer-static/main_test.go`
- Modify: `Makefile`

**Interfaces:**
- Consumes: `viewer.RenderSite(ctx, c, outDir, basePath string) error` and `viewer.NewClient(baseURL, apiKey string, timeout time.Duration) *Client` (Task 3 / existing).
- Produces: a runnable binary; no other task depends on this one.

- [ ] **Step 1: Write the failing test**

Create `cmd/viewer-static/main_test.go`:

```go
package main

import (
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	c, err := loadConfig(env(map[string]string{
		"API_URL": " https://x.example ", "API_KEY": "k", "OUT_DIR": "dist/pages",
	}))
	if err != nil || c.apiURL != "https://x.example" || c.apiKey != "k" || c.outDir != "dist/pages" || c.basePath != "/" {
		t.Fatalf("config = %+v, %v, want basePath defaulted to /", c, err)
	}

	for name, m := range map[string]map[string]string{
		"API_URL": {"API_KEY": "k", "OUT_DIR": "d"},
		"API_KEY": {"API_URL": "https://x.example", "OUT_DIR": "d"},
		"OUT_DIR": {"API_URL": "https://x.example", "API_KEY": "k"},
	} {
		if _, err := loadConfig(env(m)); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("missing %s: err = %v", name, err)
		}
	}

	for in, want := range map[string]string{
		"/sports-api-go":  "/sports-api-go/",
		"/sports-api-go/": "/sports-api-go/",
		"sports-api-go/":  "/sports-api-go/",
		"":                "/",
		"/":                "/",
	} {
		c, err := loadConfig(env(map[string]string{
			"API_URL": "https://x.example", "API_KEY": "k", "OUT_DIR": "d", "SITE_BASE_PATH": in,
		}))
		if err != nil || c.basePath != want {
			t.Errorf("SITE_BASE_PATH=%q: basePath = %q, err = %v, want %q", in, c.basePath, err, want)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/viewer-static/... -v`
Expected: FAIL — package `cmd/viewer-static` / `loadConfig` doesn't exist yet.

- [ ] **Step 3: Implement `cmd/viewer-static/main.go`**

```go
// Command viewer-static renders the league viewer to static HTML for GitHub Pages,
// calling the deployed API with API_KEY server-side; the rendered output never
// contains the API key.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/vsnandy/sports-api-go/internal/viewer"
)

type config struct {
	apiURL, apiKey, outDir, basePath string
}

func loadConfig(getenv func(string) string) (config, error) {
	c := config{
		apiURL: strings.TrimSpace(getenv("API_URL")),
		apiKey: getenv("API_KEY"),
		outDir: strings.TrimSpace(getenv("OUT_DIR")),
	}
	if c.apiURL == "" {
		return config{}, errors.New("API_URL is required")
	}
	if c.apiKey == "" {
		return config{}, errors.New("API_KEY is required")
	}
	if c.outDir == "" {
		return config{}, errors.New("OUT_DIR is required")
	}
	c.basePath = normalizeBasePath(getenv("SITE_BASE_PATH"))
	return c, nil
}

// normalizeBasePath ensures a leading and trailing "/", so an operator typo (a missing
// slash either side) can't produce a malformed href like "/sports-api-goleague/...".
func normalizeBasePath(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

func main() {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "viewer-static:", err)
		os.Exit(2)
	}
	c := viewer.NewClient(cfg.apiURL, cfg.apiKey, 20*time.Second)
	if err := viewer.RenderSite(context.Background(), c, cfg.outDir, cfg.basePath); err != nil {
		fmt.Fprintln(os.Stderr, "viewer-static:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote static site to %s\n", cfg.outDir)
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/viewer-static/... -v`
Expected: PASS

- [ ] **Step 5: Add a `viewer-static` Makefile target**

In `Makefile`, update the `.PHONY` line to add `viewer-static`:

```makefile
.PHONY: test run build deploy smoke tf-init scoreaudit viewer viewer-env viewer-static
```

Add a new target after `viewer-env`:

```makefile
# Renders the viewer to dist/pages using the same .env as `make viewer`.
viewer-static:
	@test -f .env || { echo "no .env: run 'make viewer-env' (or copy .env.example)"; exit 1; }
	set -a && . ./.env && set +a && OUT_DIR=dist/pages go run ./cmd/viewer-static
```

- [ ] **Step 6: Manual check**

```bash
make viewer-static
open dist/pages/index.html   # macOS; use `xdg-open` on Linux
```

Click through to a league page and the rooting guide, and confirm the week links load real files (check the browser's address bar shows a `.html` path, not a `?week=` query).

- [ ] **Step 7: Commit**

```bash
git add cmd/viewer-static/main.go cmd/viewer-static/main_test.go Makefile
git commit -m "feat(viewer): add cmd/viewer-static and a local build target

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

## Task 5: GitHub Actions Workflow and Documentation

**Files:**
- Create: `.github/workflows/pages.yml`
- Modify: `README.md`

**Interfaces:**
- Consumes: `cmd/viewer-static` (Task 4); the existing `AWS_DEPLOY_ROLE_ARN`, `AWS_REGION`, `SSM_PREFIX` conventions from `.github/workflows/deploy.yml`; `scripts/check-vars.sh` (existing).
- Produces: nothing further downstream — this is the last task.

This task has no Go code, so there's no red/green test cycle; instead each step is verified by a `workflow_dispatch` run after the one-time repo setup (documented in Step 4), since GitHub Actions workflows can't be unit-tested locally.

- [ ] **Step 1: Write `.github/workflows/pages.yml`**

```yaml
name: pages

on:
  schedule:
    - cron: "*/30 * * * *"
  workflow_dispatch:
  push:
    branches: [main]

permissions:
  contents: read

env:
  AWS_REGION: ${{ vars.AWS_REGION }}
  SSM_PREFIX: "/sports-api/"
  SITE_BASE_PATH: ${{ vars.SITE_BASE_PATH }}

concurrency:
  group: pages
  cancel-in-progress: true

jobs:
  build-and-deploy:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      id-token: write
      pages: write
    environment:
      name: github-pages
      url: ${{ steps.deployment.outputs.page_url }}
    steps:
      - uses: actions/checkout@v4
      - name: Check repository variables
        env:
          API_URL: ${{ vars.API_URL }}
        run: ./scripts/check-vars.sh AWS_REGION API_URL
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - uses: aws-actions/configure-aws-credentials@v4
        with:
          role-to-assume: ${{ vars.AWS_DEPLOY_ROLE_ARN }}
          aws-region: ${{ vars.AWS_REGION }}
      - name: Render static site
        env:
          API_URL: ${{ vars.API_URL }}
        run: |
          API_KEY=$(aws ssm get-parameter --name "${SSM_PREFIX}api-key" --with-decryption --query Parameter.Value --output text)
          echo "::add-mask::$API_KEY"
          OUT_DIR=dist/pages API_KEY="$API_KEY" go run ./cmd/viewer-static
      - uses: actions/configure-pages@v5
      - uses: actions/upload-pages-artifact@v3
        with:
          path: dist/pages
      - uses: actions/deploy-pages@v4
        id: deployment
```

- [ ] **Step 2: Verify the workflow file is valid YAML**

Run: `python3 -c "import yaml, sys; yaml.safe_load(open('.github/workflows/pages.yml'))" && echo OK`
Expected: `OK` (if `python3`/`pyyaml` isn't available, open the file in a YAML-aware editor and confirm there are no red squiggles instead).

- [ ] **Step 3: Add a "Publishing to GitHub Pages" section to `README.md`**

Add this section after the existing "## League viewer" section (right before "## Run locally"):

```markdown
## Publishing to GitHub Pages

A read-only copy of the viewer — leagues, matchups, and the rooting guide — publishes to
GitHub Pages so leaguemates can view it without running anything locally. It's a static
snapshot rebuilt every 30 minutes (`.github/workflows/pages.yml`), not a live view: `API_KEY`
never leaves GitHub Actions, and the published pages contain no key and make no API calls
from the browser.

```bash
make viewer-static   # renders to dist/pages using the same .env as `make viewer`
```

### One-time setup

1. Repo Settings → Pages → Source: **GitHub Actions**.
2. Add a repository variable `SITE_BASE_PATH` — the path GitHub Pages serves this repo
   under, e.g. `/sports-api-go/` for `https://<user>.github.io/sports-api-go/`, or `/` for a
   user/org page or custom domain at the root.
3. Add a repository variable `API_URL` (same value as `make viewer-env` would write to
   `.env` — `terraform -chdir=deploy/terraform output -raw api_url`), if it isn't already
   set for another workflow.
4. The workflow reuses `AWS_DEPLOY_ROLE_ARN` (already set up for `deploy.yml`) to read
   `API_KEY` from SSM; no additional AWS setup is needed.
```

- [ ] **Step 4: Commit**

```bash
git add .github/workflows/pages.yml README.md
git commit -m "ci: publish the viewer to GitHub Pages on a schedule

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

- [ ] **Step 5: One-time manual setup and first run (after this plan is merged)**

- In the repo's GitHub Settings → Pages, set Source to "GitHub Actions".
- Add the `SITE_BASE_PATH` and (if not already present) `API_URL` repository variables as described in the README section above.
- Push this branch (or merge it to `main`), then run the `pages` workflow manually once via Actions → pages → Run workflow, and confirm the Pages URL in the run's summary loads the leagues list, a league page, and the rooting guide, and that week navigation and the rooting guide's league links resolve to real pages (no 404s).
