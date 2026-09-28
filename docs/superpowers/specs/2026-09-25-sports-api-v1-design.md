# sports-api-go v1 Design

Date: 2026-09-25
Status: Approved design, pending spec review

## 1. Purpose and Scope

A personal Go API, deployed on AWS Lambda, that gives one clean, platform-neutral
interface over fantasy football data. It unifies the owner's fantasy leagues on
**ESPN** and **Sleeper** and joins them with player stats, game logs, and computed
fantasy points.

**Users:** a single user (the owner). Credentials and league configuration live in
config/SSM behind interfaces so multi-user can be added later without restructuring.

**Sports:** NFL in v1. Routes, types, and interfaces are sport-parameterized so NBA
can be added later.

### In scope (v1, NFL)
1. Unified league listing across ESPN and Sleeper
2. Rosters and weekly matchups with players normalized into one ID space
3. Player info and per-season game logs (weekly stat lines)
4. Fantasy points computed under a league's scoring rules or a preset (ppr/half/std)

### Out of scope (v1)
NBA, projections, standings, transactions, persistent storage (beyond the players
cache in S3), multi-user accounts, upstream retries, rate limiting.

## 2. Key Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Architecture | Unified domain model + provider adapters | Client never sees platform differences; new platforms/sports are new adapters |
| Stats source | Sleeper stats endpoints (unofficial) | Free, NFL+NBA, fantasy-native stat keys, provides ESPN ID bridge |
| Canonical player ID | Sleeper player ID | Matches stats source; players dump contains `espn_id` |
| Canonical stat vocabulary | Sleeper stat keys (`pass_yd`, `rec`, …) | Same as stats source; ESPN stat IDs translated in adapter |
| Storage | Stateless (in-memory TTL cache), plus S3 cache for players dump only | Simplicity; S3 avoids re-downloading ~5 MB dump per cold start |
| Runtime | Single Go Lambda (`provided.al2023`, arm64) behind API Gateway HTTP API | Same `net/http` mux runs locally and on Lambda |
| API auth | Shared-secret `X-API-Key` header, key in SSM | Single user; minimal |
| Secrets | SSM Parameter Store (SecureString), created out-of-band | Keeps secrets out of Terraform state |
| IaC | Terraform | Owner preference |

## 3. Domain Model and IDs

### IDs
- **League IDs** are platform-namespaced strings: `espn:123456`, `sleeper:9876543210`.
  The prefix selects the provider adapter. ESPN leagues are per-season; league routes
  accept `?season=` defaulting to the current NFL season.
- **Player IDs** are Sleeper player IDs (strings). ESPN roster players are mapped via
  the `espn_id` field of Sleeper's players dump.
- **Team defenses:** ESPN D/ST IDs are negative (e.g. `-16012`); Sleeper uses NFL team
  abbreviations (e.g. `KC`). Mapped by NFL team via a static ESPN pro-team-ID → abbreviation table.
- **Unmapped ESPN players** are returned with `id: null`, their ESPN ID in
  `platformIds.espn`, no stats, and a warning in `meta.warnings`. Never dropped.
- **Team IDs** (fantasy teams within a league) are the platform-native ID as a string.

### Types (`internal/domain`)
```go
type Sport string      // "nfl"
type Platform string   // "espn", "sleeper"

type League struct {
    ID               string            // "espn:123456"
    Platform         Platform
    Sport            Sport
    Season           int
    Name             string
    Teams            []Team
    Scoring          ScoringRules
    UnsupportedRules []string          // platform rules with no canonical mapping
    RosterSlots      []string          // e.g. ["QB","RB","RB","WR","WR","TE","FLEX","DEF","K","BN",...]
}

type Team struct {
    ID    string
    Name  string
    Owner string
}

type Player struct {
    ID          *string           // Sleeper ID; nil if unmapped
    Name        string
    Position    string
    NFLTeam     string
    PlatformIDs map[string]string // {"sleeper": "...", "espn": "..."}
}

type Slotted struct {
    Slot   string // "QB", "FLEX", ...
    Player Player
}

type Roster struct {
    TeamID   string
    Starters []Slotted
    Bench    []Player
    Reserve  []Player // IR/taxi
}

type MatchupSide struct {
    TeamID string
    Points float64 // platform-reported; source of truth for team totals
    Roster Roster
}

type Matchup struct {
    Week int
    Home MatchupSide
    Away MatchupSide
}

type StatLine struct {
    PlayerID string
    Season   int
    Week     int
    Opponent string
    Stats    map[string]float64 // Sleeper stat keys
}

type ScoringRules map[string]float64 // Sleeper stat key -> points per unit
```

For NBA later, `StatLine` gains a per-game period (e.g. game date) without changing
the shape of existing fields.

### Fantasy points
`points = Σ stats[k] × rules[k]` over keys present in both. Computed per player per week.
Presets `ppr`, `half`, `std` are defined in `internal/scoring`.

Sleeper league scoring settings map nearly 1:1. ESPN scoring items (numeric stat IDs)
are converted via a lookup table in the ESPN adapter; items with no mapping are listed
in `League.UnsupportedRules` and surfaced in responses. Platform-reported team scores
remain authoritative for matchup totals.

## 4. API Surface

All routes are under `/v1/{sport}/` (`nfl` in v1). All routes except `/healthz`
require header `X-API-Key`.

| Method & path | Returns |
|---|---|
| `GET /healthz` | `200 ok`; no auth, no upstream calls |
| `GET /v1/nfl/leagues?season=` | Summaries of the owner's leagues on both platforms |
| `GET /v1/nfl/leagues/{leagueId}?season=` | League detail: teams, scoring, roster slots, unsupported rules |
| `GET /v1/nfl/leagues/{leagueId}/rosters?season=` | All team rosters, normalized players |
| `GET /v1/nfl/leagues/{leagueId}/matchups?week=&season=&include=stats` | Week's matchups; with `include=stats`, each rostered player gets that week's `StatLine` and fantasy points under this league's scoring |
| `GET /v1/nfl/players/{playerId}` | Player info and platform IDs |
| `GET /v1/nfl/players/{playerId}/gamelog?season=&scoring=` | Weekly stat lines for the season; `scoring` = `ppr` \| `half` \| `std` \| a league ID (e.g. `espn:123456`) to add fantasy points; omitted = raw stats |

### Conventions
- **League discovery:** config contains a Sleeper username (leagues discovered via
  Sleeper `/user/{username}/leagues/nfl/{season}`) and a list of ESPN league IDs.
  A league ID not in the owner's set returns 404.
- **Defaults:** `season` and `week` default from Sleeper `/state/nfl`.
- **Success envelope:**
  ```json
  {"data": ..., "meta": {"season": 2026, "week": 3, "warnings": ["..."]}}
  ```
- **Error envelope:**
  ```json
  {"error": {"code": "espn_auth_failed", "message": "..."}}
  ```

| Status | Code(s) | When |
|---|---|---|
| 400 | `invalid_param` | Bad/malformed query or path params |
| 401 | `unauthorized` | Missing/incorrect `X-API-Key` |
| 404 | `not_found` | Unknown league or player |
| 500 | `internal` | Panic or unexpected error |
| 502 | `upstream_error`, `espn_auth_failed` | Upstream non-2xx or unparseable response; ESPN cookie invalid/expired |
| 504 | `upstream_timeout` | Upstream call exceeded timeout |

## 5. Package Layout

```
cmd/api/main.go             # Lambda: lambda.Start(httpadapter.NewV2(mux)); local: http.ListenAndServe
internal/config/            # env + SSM load at cold start; env overrides for local dev
internal/domain/            # types above + typed errors; no dependencies
internal/providers/sleeper/ # client + mapping: state, user leagues, league, rosters, users, matchups, players dump, stats
internal/providers/espn/    # client + mapping: league views, cookie auth, stat/slot/position/pro-team tables
internal/players/           # PlayerIndex: sleeperID→Player, espnID→sleeperID, NFL team→DEF; memory→S3→Sleeper tiers
internal/scoring/           # Points(stats, rules), presets
internal/service/           # LeagueService, PlayerService: join logic over interfaces
internal/httpapi/           # Go 1.22 ServeMux routes, middleware (auth, logging, recover), envelope, error mapping
internal/cache/             # generic in-memory TTL cache
deploy/terraform/           # infrastructure
```

`service` depends only on interfaces (`LeagueProvider`, `StatsProvider`,
`PlayerIndex`), enabling fake-based tests.

Lambda detection: `cmd/api` checks `AWS_LAMBDA_FUNCTION_NAME`; if set, it runs the
`aws-lambda-go-api-proxy` HTTP API v2 adapter, otherwise it listens on `:8080`.

## 6. Upstream Integrations

### Sleeper (no auth)
- `GET https://api.sleeper.app/v1/state/nfl` — current season/week
- `GET https://api.sleeper.app/v1/user/{username}` → user ID
- `GET https://api.sleeper.app/v1/user/{user_id}/leagues/nfl/{season}`
- `GET https://api.sleeper.app/v1/league/{league_id}` — settings, scoring, roster positions
- `GET https://api.sleeper.app/v1/league/{league_id}/users` and `/rosters`
- `GET https://api.sleeper.app/v1/league/{league_id}/matchups/{week}`
- `GET https://api.sleeper.app/v1/players/nfl` — players dump (≤ once/day)
- `GET https://api.sleeper.com/stats/nfl/{season}/{week}?season_type=regular` — weekly stats, all players, array of `{player_id, week, opponent, stats}` (unofficial)
- `GET https://api.sleeper.com/stats/nfl/player/{player_id}?season_type=regular&season={season}&grouping=week` — player game log (unofficial)

### ESPN (cookie auth: `espn_s2`, `SWID`)
- `GET https://lm-api-reads.fantasy.espn.com/apis/v3/games/ffl/seasons/{season}/segments/0/leagues/{league_id}?view=mSettings&view=mTeam&view=mRoster&view=mMatchupScore&scoringPeriodId={week}`
- Auth failure detection: HTTP 401/403, or a 2xx response whose body is not JSON
  (login HTML). Both → `ErrESPNAuth`.
- Static tables in the adapter: stat ID → Sleeper stat key, lineup slot ID → slot name,
  position ID → position, pro team ID → NFL abbreviation.

Because the Sleeper stats endpoints are unofficial, they sit behind `StatsProvider`
so another source (e.g. nflverse) can be substituted.

## 7. Runtime Behavior

### Configuration
| Env var | Purpose |
|---|---|
| `SLEEPER_USERNAME` | Sleeper league discovery |
| `ESPN_LEAGUE_IDS` | Comma-separated ESPN league IDs |
| `SSM_API_KEY_PARAM`, `SSM_ESPN_S2_PARAM`, `SSM_ESPN_SWID_PARAM` | SSM parameter names |
| `PLAYERS_BUCKET` | S3 bucket for players cache |
| `API_KEY`, `ESPN_S2`, `ESPN_SWID` | Local-dev overrides; when set, SSM is skipped for that value |

Secrets are fetched once per cold start with `ssm:GetParameters` (WithDecryption).

### In-memory TTL cache (per warm container)
| Data | TTL |
|---|---|
| Sleeper `/state/nfl` | 5 min |
| League settings / teams | 1 h |
| Rosters | 5 min |
| Matchups | 1 min |
| Stats, completed weeks | 12 h |
| Stats, current week | 5 min |

### Players index
Loaded lazily on first use (not at cold start), guarded so concurrent requests share
one load and a failed load can be retried:
1. In memory (if loaded and < 24 h old)
2. S3 object `players/nfl.json` (if < 24 h old by `LastModified`)
3. Sleeper `/players/nfl` → slimmed to `{id, name, pos, team, espn_id}` → written to S3

Concurrent refreshes from separate containers are acceptable (idempotent write).

### Upstream calls
One shared `http.Client`; 5 s per-call timeout; request context propagated.
Independent fetches run concurrently via `errgroup` (e.g. matchups + weekly stats for
`include=stats`). No retries in v1.

### Logging
`slog` JSON to stdout (CloudWatch): request ID, method, route, status, duration, and
per-upstream-call provider, URL path, status, latency. Secrets and cookies are never logged.

## 8. Error Handling

- Providers return typed errors defined in `domain`: `ErrNotFound`,
  `ErrUpstream{Provider, Status}`, `ErrUpstreamTimeout`, `ErrESPNAuth`.
  A single function in `httpapi` maps them to the status/code table in §4.
  Handlers never write error responses directly.
- **Partial failure:** for `matchups?include=stats`, if the stats fetch fails, matchups
  are still returned with each player's `stats: null`, `points: null`, and
  `meta.warnings` containing `stats_unavailable`. Only failure of the primary resource
  fails the request.
- **Unmapped players/rules** surface as warnings, never errors.
- **Panics:** recover middleware returns 500 `internal` with the request ID and logs the stack.

## 9. Infrastructure (Terraform, `deploy/terraform`)

- `aws_lambda_function`: `provided.al2023`, arm64, 512 MB, 15 s timeout, handler `bootstrap`
- `aws_apigatewayv2_api` (HTTP) with `$default` route → Lambda integration (payload v2.0)
- `aws_s3_bucket` for players cache (private, SSE-S3, public access blocked)
- `aws_cloudwatch_log_group` with 14-day retention
- IAM role: `ssm:GetParameters` on the three parameter ARNs, `kms:Decrypt` for the
  default SSM key, `s3:GetObject`/`s3:PutObject` on `players/*` in the bucket, and
  CloudWatch Logs write
- SSM parameters are created by the owner via CLI; Terraform takes their names as variables
- Variables: `sleeper_username`, `espn_league_ids`, SSM param names, region
- Outputs: API endpoint URL

Build: `make build` → `GOOS=linux GOARCH=arm64 CGO_ENABLED=0` → `bootstrap` → `bootstrap.zip`.
`make deploy` builds and runs `terraform apply`.

## 10. Testing Strategy

Implementation follows TDD. `go test ./...` makes no live network calls.

- **Adapters:** real upstream JSON captured once into `testdata/` fixtures (Sleeper:
  state, user, leagues, league, users, rosters, matchups, weekly stats, trimmed players
  dump, player gamelog; ESPN: league with mSettings/mTeam/mRoster/mMatchupScore).
  Tested against `httptest.Server`: mapping correctness, ESPN tables, D/ST mapping,
  ESPN auth-failure detection (401 and HTML body).
- **Scoring:** table-driven ppr/half/std against hand-computed lines; ESPN rule
  conversion including `UnsupportedRules`; fractional and negative stats.
- **Service:** fake providers; ID join including unmapped ESPN players; partial-failure
  path; season/week defaulting from state.
- **HTTP:** full mux with fake services via `httptest`: auth, envelope, error mapping,
  param validation, `/healthz`.
- **Cache / players index:** TTL expiry; memory → S3 → Sleeper tiering with an
  in-memory S3 fake behind an interface.
- **Smoke:** opt-in `make smoke` hits the deployed endpoint with real config; not part
  of `go test`.

## 11. Future Extensions (not v1)

- NBA: `nba` sport routes, Sleeper NBA stats, per-game `StatLine` period
- Projections via Sleeper projections endpoints
- Standings and transactions
- SQLite/DynamoDB persistence for historical data
- Multi-user: per-user credentials and league lists behind the existing config interface
