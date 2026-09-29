# League Viewer Design

Date: 2026-09-28
Status: Approved design, pending spec review
Builds on: `docs/superpowers/specs/2026-09-28-espn-scoring-fidelity-design.md`

## 1. Goal

A quick, local-only web view of the owner's fantasy leagues, backed by the deployed API.

**Owner's requirements:**
- League by league (3 Sleeper + 4 ESPN): pick a league and a week, see that week's
  matchups with both team totals.
- Expand a matchup to see every starter and bench player with their points.
- Expand a player to see how the points were earned (points per stat).
- See each league's scoring rules (including D/ST tiers and unsupported rules).
- Refresh button; no live auto-refresh.

**Constraints:** runs on the owner's laptop only; the API key never reaches a browser;
readable over polished; no login, hosting, or database.

**Success:** one command, open a URL, browse all 7 leagues for any week. Every team total
equals the sum of its starters' points, and each player's breakdown sums to their points
(within 0.01).

**Out of scope:** auto-refresh, rosters outside matchups, player search, projections, mobile
layout, deploying the viewer.

## 2. API Addition: `pointsBreakdown`

`GET /v1/nfl/leagues/{id}/matchups?include=stats` gains, per roster entry, a
`pointsBreakdown` object: points contributed per stat key. Set only when `points`
is non-null (same rule as `pointsSource`); omitted when null or when no stat contributed
(e.g. 0 points). No other response change.

```json
{"slot": "DEF", "points": 8, "pointsSource": "platform",
 "pointsBreakdown": {"sack": 3, "int": 2, "fum_rec": 2, "espn_pa_14_17": 1}}
```

- **Platform (ESPN):** from ESPN's `appliedStats` (ESPN stat ID → points) on the same
  actual-stats row as `PlatformPoints`. IDs are translated to stat keys with
  `espnKeyForID(id)`: tier key, then step key, then the first key in `statKeys[id]`;
  unmapped IDs become `espn_<id>`. Zero values are omitted. Values sum to ESPN's total.
- **Computed:** `scoring.Breakdown(line.Stats, league scoring, position)`, each value
  rounded to 2 decimals.

**Code:**
- `domain.RosterEntryRef` gains `PlatformBreakdown map[string]float64`.
- `domain.RosterEntry` gains `PointsBreakdown map[string]float64 \`json:"pointsBreakdown,omitempty"\``.
- ESPN `toRoster` sets `PlatformBreakdown` alongside `PlatformPoints`; new unexported
  `espnKeyForID(id int) string` in `tables.go` (deterministic: the lowest-ID rule of
  `ESPNStatIDForKey` is the reverse direction; each ID has exactly one canonical key).
- Service `attachPoints` sets `PointsBreakdown` (a copy of `PlatformBreakdown`, or the
  rounded computed breakdown) whenever it sets `Points`.

## 3. Viewer (`cmd/viewer`, `internal/viewer`)

**Run:** `make viewer` → `go run ./cmd/viewer` with `API_URL` and `API_KEY` from `.env`
(git-ignored; `.env.example` committed). Listens on `127.0.0.1:8081` only. Missing
`API_URL` or `API_KEY` → exit with a message naming the variable.

**Package `internal/viewer`:**

| File | Responsibility |
|---|---|
| `client.go` | `Client{BaseURL, APIKey, HTTP}` — GET JSON with `X-API-Key`; decode `{data, meta}`; non-2xx → `*APIError{Status, Code, Message}` from the error envelope; transport failure → error |
| `handlers.go` | `NewHandler(c *Client) http.Handler` with the two pages |
| `labels.go` | `Label(key string) string` — human label for common stat keys and all `espn_pa_*`/`espn_ya_*`/`espn_pass_yd_per_25` keys; unknown → key itself |
| `templates/*.html` | `embed.FS`, `html/template`, minimal inline CSS |

**Pages:**
1. `GET /` — calls `/v1/nfl/leagues`; lists leagues grouped by platform (ESPN, Sleeper),
   each linking to `/league/{id}`. Shows API warnings.
2. `GET /league/{id}?week=N` — calls `/v1/nfl/leagues/{id}` (name, teams, scoring) and
   `/v1/nfl/leagues/{id}/matchups?include=stats` (+ `&week=N` when given; the API defaults
   to the current week and reports it in `meta.week`).
   - Header: league name, season, week; ← / → links (hidden at 1 / 18), a 1–18 week
     `<select>` in a GET form, Refresh link (same URL).
   - API warnings listed at the top.
   - One `<details>` per matchup; summary `Home Team  112.34 — 98.50  Away Team`
     (team names from league detail by team ID; fall back to `Team <id>`).
   - Expanded: per side, Starters and Bench tables — slot, player name, NFL team, points
     (`—` when null), and a source tag `ESPN` (platform) or `computed`.
   - Each player row with points has a nested `<details>`: breakdown table
     (`Label(key)`, points), sorted by absolute points descending then key, with a total
     row; raw stat line below in small text.
   - `<details>` "Scoring rules": base rules (label, value), per-position rules, derived
     stats (key, from, range or step), unsupported rules.
- `week` query outside 1–18 or non-numeric → 400 page with a message; no API call.
- League ID is passed through as a path segment (the API validates it); the viewer
  escapes it with `url.PathEscape`.

**Errors:** `*APIError` → page with status, code and message (e.g. `espn_auth_failed`);
transport error → "API unreachable" page with the error text. HTTP status of the viewer
response mirrors 4xx/5xx (502 for transport failures).

## 4. Testing

- API addition: ESPN fixture entry breakdown keys/values and sum = PlatformPoints;
  unmapped ID → `espn_<id>`; computed breakdown equals rounded `scoring.Breakdown`;
  no breakdown when points is null; `espnKeyForID` for tier, step, multi-key, unknown IDs.
- Viewer (fake API via `httptest`): `X-API-Key` sent on every call; `/` lists and links
  all leagues; league page shows team names, totals, player points, source tags,
  breakdown rows with labels, and scoring rules; `week` passed through when given and
  omitted otherwise; invalid week → 400 without API calls; API error envelope rendered;
  unreachable API → 502 page; `Label` known/unknown keys; startup fails without config.
- Manual: `make viewer` against the deployed API; check all 7 leagues for weeks 1–3.
