# Publishing the League Viewer to GitHub Pages

Date: 2026-10-24
Status: Approved design, pending spec review
Builds on: `docs/superpowers/specs/2026-09-28-league-viewer-design.md`,
`docs/superpowers/specs/2026-09-28-rooting-guide-design.md`

## 1. Goal

Publish a read-only copy of the league viewer (leagues list, per-league matchups, rooting
guide) to GitHub Pages so leaguemates can view it over the internet, without exposing
`API_KEY` to the browser.

**Decisions (owner):** audience is leaguemates (public URL, not just personal
convenience); periodic refresh is acceptable — the page does not need to update live
during games. This rules out a client-side SPA calling the API directly (would require a
new public, keyless/rate-limited endpoint) in favor of rebuilding static HTML on a
schedule and keeping `API_KEY` in GitHub Actions only.

**Out of scope:** live/real-time updates during games, a public API endpoint, auth/access
control on the published site (anyone with the URL can view it, same as any GitHub Pages
site), season selection (the existing viewer has none either — it always shows the API's
current season).

## 2. Href Abstraction

Three places currently bake live-server URLs into rendered output, which breaks once pages
are static files instead of a running server resolving query strings:

- `handlers.go`'s `leagues()` builds `leagueLink.Href` as `/league/{id}`.
- `league.html` and `rooting.html` render week nav (`‹ Week N-1`, the week `<select>`,
  `Week N+1 ›`, `⟳ Refresh`) as `?week=N`, resolved against the current URL.
- `rooting.go`'s `buildRooting` builds each league chip's `Href` as
  `/league/{id}?week=N`.

A static host serves one file per path and ignores the query string, so `?week=5` and
`?week=6` would resolve to the same file. All three need real, distinct file paths when
exporting statically, while the existing local server (`cmd/viewer`, `make viewer`) must
keep working exactly as it does today.

### 2.1 `linker` interface

New type in `internal/viewer` (e.g. `links.go`):

```go
type linker interface {
    leagues() string                // leagues list page
    league(id string, week int) string // week 0 = current/default
    rooting(week int) string           // week 0 = current/default
}
```

- `liveLinker`: today's behavior, byte-for-byte — `/`, `/league/{id}`, `/league/{id}?week=N`,
  `/rooting`, `/rooting?week=N`.
- `staticLinker{base string}`: file paths relative to `base` —
  `index.html`, `league/{id}/index.html`, `league/{id}/week-N.html`,
  `rooting/index.html`, `rooting/week-N.html`. `base` is an absolute URL
  (e.g. `https://vsnandy.github.io/sports-api-go/`), written once into a `<base href>` tag
  in `base.html` so every relative href resolves correctly regardless of a page's nesting
  depth.

`server` (in `handlers.go`) gains a `links linker` field, defaulted to `liveLinker{}` by
`NewHandler`. `buildLeagueView`, `buildRooting`, and `leagues()` take the `linker` and call
it instead of formatting URLs themselves.

### 2.2 Template changes

- `leagues.html`: unchanged markup; `.Href` values now come from the linker.
- `league.html` / `rooting.html`: replace the three hardcoded `?week={{.N}}` hrefs with
  precomputed fields on the view (`PrevHref`, `NextHref`, `RefreshHref`), and replace the
  `<form method="get"><select>...<button>Go</button></form>` with a single
  `<select onchange="location.href=this.value">` whose `<option value>` is each week's
  linker-built href. This removes the form/button in both modes (one less server round
  trip locally, and no query-string submission to break statically) and is the only markup
  change — no visual/CSS change.

### 2.3 `NewHandler` signature

```go
func NewHandler(c *Client) http.Handler              // unchanged; liveLinker
func NewStaticHandler(c *Client, base string) http.Handler // new; staticLinker
```

`cmd/viewer` keeps calling `NewHandler` — no behavior change for `make viewer`.

## 3. Static Export

### 3.1 `internal/viewer/static.go`

```go
func RenderSite(ctx context.Context, c *Client, outDir, baseURL string) error
```

- Calls `c.Get(ctx, "/v1/nfl/leagues", nil, &ls)` directly to enumerate league IDs — no
  HTML parsing.
- Builds `h := NewStaticHandler(c, baseURL)`.
- For each of the following, builds an `httptest.NewRequest(http.MethodGet, path, nil)`
  with `req.Host` forced to `"127.0.0.1"` (so `loopbackOnly` passes the same way it does for
  a real local request), calls `h.ServeHTTP(rec, req)`, and writes `rec.Body.Bytes()` to
  `outDir/<path from the linker>`:
  - `/` → `index.html`
  - `/rooting` and `/rooting?week=N` for `N` in 1–18 → `rooting/index.html`,
    `rooting/week-N.html`
  - for each league ID: `/league/{id}` and `/league/{id}?week=N` for `N` in 1–18 →
    `league/{id}/index.html`, `league/{id}/week-N.html`
- Weeks are always 1–18 regardless of season (`weekNav`, unchanged) — the crawl is fully
  enumerable up front; no link discovery needed.
- A non-2xx response for any page is an error (`RenderSite` returns it; the caller fails
  the build rather than publishing a partial/broken site).

### 3.2 `cmd/viewer-static/main.go`

Thin wrapper, same shape as `cmd/viewer/main.go`:

```go
// Command viewer-static renders the league viewer to static HTML for GitHub Pages,
// calling the deployed API with API_KEY server-side; the output has no API key in it.
```

Reads `API_URL`, `API_KEY`, `OUT_DIR`, `SITE_BASE_URL` from the environment (same
"required, fail fast" `loadConfig` pattern as `cmd/viewer`), builds a `viewer.Client`, calls
`viewer.RenderSite`, exits non-zero on error.

## 4. GitHub Actions Workflow

New `.github/workflows/pages.yml`, separate from `deploy.yml` (different concern, different
triggers, no Terraform):

- **Triggers:** `schedule` (cron, every 30 minutes — see note below), `workflow_dispatch`,
  and `push` to `main` (so a template/markup change ships without waiting for the next
  tick).
- **Permissions:** `contents: read`, `id-token: write`, `pages: write`.
- **Concurrency:** `group: pages`, `cancel-in-progress: true` (a newer run supersedes an
  older one; no reason to finish a stale build).
- **Steps:** checkout; `setup-go`; configure AWS credentials via OIDC (same role as the
  existing smoke-test step uses for SSM read access); fetch `API_KEY` from SSM
  (`${SSM_PREFIX}api-key`, masked); `go run ./cmd/viewer-static` with `API_URL` and
  `SITE_BASE_URL` from repository variables, `OUT_DIR=dist/pages`; `actions/configure-pages`;
  `actions/upload-pages-artifact` with `dist/pages`; `actions/deploy-pages`.

**Cron caveat:** GitHub may delay or skip scheduled workflow runs on low-activity repos by
tens of minutes — it is best-effort, not a precise timer. Acceptable given the "periodic
refresh is fine" decision in §1; not suitable if live-during-games freshness is ever
wanted later.

## 5. One-Time Setup (manual, documented in README)

- Repo Settings → Pages → Source: GitHub Actions.
- New repository variable `SITE_BASE_URL` (e.g. `https://vsnandy.github.io/sports-api-go/`).
- AWS: reuse the existing plan/deploy OIDC role's SSM read permission, or scope a narrower
  role if the owner prefers not to reuse the deploy role for this workflow.

## 6. Testing

- `staticLinker` unit tests: each method's output for week 0 (current/default) and week
  N, matching the documented file layout.
- `RenderSite` test against a fake API client (same fake used in `viewer_test.go`):
  correct set of output files for N leagues, non-2xx response surfaces as an error, content
  of a sampled page matches what `NewStaticHandler` would serve for that path.
- `liveLinker` behavior is covered by the existing `viewer_test.go`/`view_test.go` tests
  unchanged — those tests assert hrefs equal to what today's handlers already produce, so
  they also serve as a regression check that `NewHandler`'s behavior didn't move.
- CI script smoke-check (alongside `scripts/test-ci-scripts.sh`'s pattern): run
  `viewer-static` against a stub API and assert a known set of files exists under the
  output directory.
- Manual, after first deploy: confirm the Pages URL loads, week nav and rooting-guide links
  resolve to real files (not 404s), and no `API_KEY` or `X-API-Key` string appears anywhere
  in the published `dist/pages` output.
