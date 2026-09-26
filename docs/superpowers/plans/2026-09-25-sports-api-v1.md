# sports-api-go v1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a single-user Go API on AWS Lambda that unifies ESPN and Sleeper fantasy football leagues and joins them with Sleeper player stats, game logs, and computed fantasy points.

**Architecture:** A unified domain model (`internal/domain`) sits between provider adapters (`internal/providers/sleeper`, `internal/providers/espn`) and a service layer (`internal/service`) that does ID resolution, stats joins, scoring, and in-memory TTL caching. A thin `net/http` layer (`internal/httpapi`) exposes `/v1/nfl/...` routes and runs unchanged locally or behind API Gateway via the Lambda HTTP adapter. The only persistent state is a slimmed Sleeper players dump cached in S3.

**Tech Stack:** Go ≥ 1.23 (stdlib `net/http` ServeMux patterns, `log/slog`), `github.com/aws/aws-lambda-go`, `github.com/awslabs/aws-lambda-go-api-proxy`, `github.com/aws/aws-sdk-go-v2` (config, ssm, s3), `golang.org/x/sync/errgroup`, Terraform ≥ 1.6 with AWS provider 5.x.

**Spec:** `docs/superpowers/specs/2026-09-25-sports-api-v1-design.md`

## Global Constraints

- Module path `github.com/vsnandy/sports-api-go`; `go.mod` directive `go 1.23`.
- Third-party Go deps are limited to: `aws-lambda-go`, `aws-lambda-go-api-proxy`, `aws-sdk-go-v2` (`config`, `service/ssm`, `service/s3`, `aws`), `golang.org/x/sync`. No router frameworks, no assertion libraries (use stdlib `testing`).
- `go test ./...` makes **no live network calls**. Upstreams are faked with `httptest.Server` or in-memory fakes.
- Canonical player ID = Sleeper player ID. Canonical stat vocabulary = Sleeper stat keys (`pass_yd`, `rec`, `rush_td`, …).
- League IDs are `espn:<digits>` or `sleeper:<digits>`.
- JSON field names are camelCase. Success envelope `{"data": …, "meta": {"season", "week", "warnings": []}}`; error envelope `{"error": {"code", "message"}}`. `warnings` is always an array, never `null`.
- Status/code table: 400 `invalid_param`, 401 `unauthorized`, 404 `not_found`, 500 `internal`, 502 `upstream_error` / `espn_auth_failed`, 504 `upstream_timeout`.
- Upstream HTTP: one `http.Client` per provider, 5 s timeout, request context propagated, **no retries**.
- Cache TTLs: state 5 min; league settings/list 1 h; rosters 5 min; matchups 1 min; stats for completed weeks/seasons 12 h; current week/season 5 min. Players dump: 24 h (memory → S3 `players/nfl.json` → Sleeper).
- Secrets (API key, `espn_s2`, `SWID`) never appear in logs, errors, or Terraform state.
- Lambda: `provided.al2023`, arm64, 512 MB, 15 s timeout, handler `bootstrap`. API Gateway HTTP API, `$default` route, payload v2.0. Log retention 14 days.
- All `/v1/...` routes require `X-API-Key`; `/healthz` does not. An empty configured key rejects every request.

## Review Focus

1. **Expired ESPN cookies:** ESPN replies 401/403 or 200 with an HTML login page → `espn_auth_failed` (502); `/v1/nfl/leagues` still returns Sleeper leagues with a `espn_leagues_unavailable` warning instead of failing entirely. (Tests: Task 6 auth tests, Task 8 `TestListLeaguesDegradesWhenOnePlatformFails`.)
2. **Players dump refresh fails after a good load** (Sleeper down at the 24 h mark) → keep serving the stale index; only fail when nothing was ever loaded, and retry on the next request. (Tests: Task 7 `TestRefreshFailureServesStale`, `TestInitialFailureThenRetry`.)
3. **Hostile or malformed path params** (`espn:12/../3`, `sleeper:abc`, player IDs with `/` or `?`) → 400 before any upstream call; they are never interpolated into upstream URLs. (Tests: Task 1 `TestParseLeagueID`/`TestValidPlayerID`, Task 8 `TestLeagueInvalidIDDoesNotCallProviders`.)
4. **S3 read of a missing key without `s3:ListBucket` returns AccessDenied, not NoSuchKey** → IAM grants `ListBucket`, and the index treats *any* store read error as a miss and falls back to Sleeper. (Tests: Task 7 `TestStoreReadErrorFallsBack`; Terraform in Task 12.)
5. **Offseason and past seasons:** Sleeper state `week: 0` → default week 1; a past-season matchups request without `week` → 400 rather than silently using the current week. (Tests: Task 8 `TestMatchupsOffseasonDefaultsToWeek1`, `TestMatchupsPastSeasonRequiresWeek`.)

---

## File Map

```
go.mod, go.sum, Makefile, .gitignore, README.md
cmd/api/main.go                              # wiring; Lambda vs local
internal/domain/domain.go                    # types, ID helpers
internal/domain/errors.go                    # typed errors
internal/cache/cache.go                      # generic TTL cache
internal/scoring/scoring.go                  # Points, presets
internal/providers/httpx/httpx.go            # shared JSON GET + error mapping
internal/providers/sleeper/{client,leagues,stats}.go + testdata/
internal/providers/espn/{client,league,tables}.go + testdata/
internal/players/{index,s3store}.go          # PlayerIndex with memory→S3→Sleeper tiers
internal/service/{service,leagues,players}.go
internal/httpapi/{server,handlers,respond,middleware}.go
internal/config/config.go
deploy/terraform/{versions,variables,main,outputs}.tf, terraform.tfvars.example
scripts/smoke.sh
```

---

### Task 1: Module scaffold and domain package

**Files:**
- Create: `go.mod`, `Makefile`, `internal/domain/domain.go`, `internal/domain/errors.go`
- Modify: `.gitignore`
- Test: `internal/domain/domain_test.go`

**Interfaces:**
- Consumes: nothing
- Produces (package `domain`):
  - Types: `Sport`, `Platform`, consts `SportNFL`, `PlatformESPN`, `PlatformSleeper`; `SeasonState{Season, Week int}`; `LeagueSummary`, `League`, `Team`, `ScoringRules map[string]float64`, `Player` (`ID *string`), `RosterEntry{Slot, Player, Stats map[string]float64, Points *float64}`, `Roster{TeamID, Starters, Bench, Reserve []RosterEntry}`, `MatchupSide`, `Matchup`, `StatLine`, `GamelogEntry`, `Meta{Season, Week int; Warnings []string}`
  - Provider refs: `PlayerRef{Platform, ID, Name, Position, NFLTeam}`, `RosterEntryRef{Slot, Ref}`, `RosterRef{TeamID, Starters, Bench, Reserve []RosterEntryRef}`, `MatchupSideRef{TeamID, Points, Roster RosterRef}`, `MatchupRef{Week, Home, Away MatchupSideRef}`
  - `func LeagueID(p Platform, nativeID string) string`
  - `func ParseLeagueID(id string) (Platform, string, error)` (error is `*InvalidParamError`)
  - `func ValidPlayerID(id string) bool`
  - Errors: `ErrNotFound`, `ErrUpstreamTimeout`, `ErrESPNAuth`, `*UpstreamError{Provider string; Status int; BadBody bool; Err error}`, `*InvalidParamError{Param, Reason string}`

- [ ] **Step 1: Install Go if missing and initialize the module**

Run: `go version || brew install go`
Expected: `go version go1.23` or newer.

Run: `go mod init github.com/vsnandy/sports-api-go && go mod edit -go=1.23`
Expected: `go.mod` created.

- [ ] **Step 2: Extend `.gitignore` and add the Makefile**

Append to `.gitignore`:

```gitignore

# Build output
dist/

# Terraform
deploy/terraform/.terraform/
*.tfstate
*.tfstate.*
*.tfvars
!terraform.tfvars.example
```

Create `Makefile` (recipe lines start with a TAB):

```make
.PHONY: test

test:
	go test ./...
```

- [ ] **Step 3: Write the failing test**

`internal/domain/domain_test.go`:

```go
package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestParseLeagueID(t *testing.T) {
	tests := []struct {
		in       string
		platform Platform
		native   string
		ok       bool
	}{
		{"espn:123456", PlatformESPN, "123456", true},
		{"sleeper:987654321012345678", PlatformSleeper, "987654321012345678", true},
		{"yahoo:1", "", "", false},
		{"espn:", "", "", false},
		{"espn", "", "", false},
		{"espn:12/../34", "", "", false},
		{"sleeper:12a", "", "", false},
		{"espn:1:2", "", "", false},
		{"espn:123456789012345678901", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			p, native, err := ParseLeagueID(tt.in)
			if !tt.ok {
				var ip *InvalidParamError
				if !errors.As(err, &ip) {
					t.Fatalf("ParseLeagueID(%q) err = %v, want *InvalidParamError", tt.in, err)
				}
				return
			}
			if err != nil || p != tt.platform || native != tt.native {
				t.Fatalf("ParseLeagueID(%q) = %q, %q, %v", tt.in, p, native, err)
			}
			if LeagueID(p, native) != tt.in {
				t.Fatalf("LeagueID round trip = %q, want %q", LeagueID(p, native), tt.in)
			}
		})
	}
}

func TestValidPlayerID(t *testing.T) {
	tests := map[string]bool{
		"4046":          true,
		"KC":            true,
		"":              false,
		"../x":          false,
		"4046?x=1":      false,
		"1234567890123": false,
	}
	for id, want := range tests {
		if got := ValidPlayerID(id); got != want {
			t.Errorf("ValidPlayerID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestUpstreamErrorMessage(t *testing.T) {
	msg := (&UpstreamError{Provider: "espn", Status: 500}).Error()
	if !strings.Contains(msg, "espn") || !strings.Contains(msg, "500") {
		t.Fatalf("message %q should name provider and status", msg)
	}
	wrapped := &UpstreamError{Provider: "sleeper", Err: ErrNotFound}
	if !errors.Is(wrapped, ErrNotFound) {
		t.Fatal("UpstreamError should unwrap to its Err")
	}
}
```

- [ ] **Step 4: Run test to verify it fails**

Run: `go test ./internal/domain/`
Expected: FAIL — `undefined: ParseLeagueID` (and others).

- [ ] **Step 5: Write the implementation**

`internal/domain/domain.go`:

```go
// Package domain holds the platform-neutral types shared by every layer.
package domain

import "strings"

type Sport string

type Platform string

const (
	SportNFL        Sport    = "nfl"
	PlatformESPN    Platform = "espn"
	PlatformSleeper Platform = "sleeper"
)

// SeasonState is the current NFL season and week according to Sleeper.
// Week is 0 in the offseason.
type SeasonState struct {
	Season int
	Week   int
}

type LeagueSummary struct {
	ID       string   `json:"id"`
	Platform Platform `json:"platform"`
	Season   int      `json:"season"`
	Name     string   `json:"name"`
}

type League struct {
	ID               string       `json:"id"`
	Platform         Platform     `json:"platform"`
	Sport            Sport        `json:"sport"`
	Season           int          `json:"season"`
	Name             string       `json:"name"`
	Teams            []Team       `json:"teams"`
	Scoring          ScoringRules `json:"scoring"`
	UnsupportedRules []string     `json:"unsupportedRules"`
	RosterSlots      []string     `json:"rosterSlots"`
}

type Team struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Owner string `json:"owner"`
}

// ScoringRules maps a Sleeper stat key to points per unit of that stat.
type ScoringRules map[string]float64

type Player struct {
	ID          *string           `json:"id"` // Sleeper ID; nil when an ESPN player could not be mapped
	Name        string            `json:"name"`
	Position    string            `json:"position"`
	NFLTeam     string            `json:"nflTeam"`
	PlatformIDs map[string]string `json:"platformIds"`
}

// RosterEntry is a rostered player. Stats and Points are nil unless stats were
// requested and available for that player.
type RosterEntry struct {
	Slot   string             `json:"slot"`
	Player Player             `json:"player"`
	Stats  map[string]float64 `json:"stats"`
	Points *float64           `json:"points"`
}

type Roster struct {
	TeamID   string        `json:"teamId"`
	Starters []RosterEntry `json:"starters"`
	Bench    []RosterEntry `json:"bench"`
	Reserve  []RosterEntry `json:"reserve"`
}

type MatchupSide struct {
	TeamID string  `json:"teamId"`
	Points float64 `json:"points"` // platform-reported; source of truth for team totals
	Roster Roster  `json:"roster"`
}

type Matchup struct {
	Week int         `json:"week"`
	Home MatchupSide `json:"home"`
	Away MatchupSide `json:"away"`
}

type StatLine struct {
	PlayerID string             `json:"playerId"`
	Season   int                `json:"season"`
	Week     int                `json:"week"`
	Opponent string             `json:"opponent"`
	Stats    map[string]float64 `json:"stats"`
}

type GamelogEntry struct {
	Season   int                `json:"season"`
	Week     int                `json:"week"`
	Opponent string             `json:"opponent"`
	Stats    map[string]float64 `json:"stats"`
	Points   *float64           `json:"points"`
}

type Meta struct {
	Season   int      `json:"season,omitempty"`
	Week     int      `json:"week,omitempty"`
	Warnings []string `json:"warnings"`
}

// PlayerRef is a provider's reference to a player, resolved to a Player by the service.
// Name, Position, and NFLTeam are fallbacks for players that cannot be mapped.
type PlayerRef struct {
	Platform Platform
	ID       string
	Name     string
	Position string
	NFLTeam  string
}

type RosterEntryRef struct {
	Slot string
	Ref  PlayerRef
}

type RosterRef struct {
	TeamID   string
	Starters []RosterEntryRef
	Bench    []RosterEntryRef
	Reserve  []RosterEntryRef
}

type MatchupSideRef struct {
	TeamID string
	Points float64
	Roster RosterRef
}

type MatchupRef struct {
	Week int
	Home MatchupSideRef
	Away MatchupSideRef
}

func LeagueID(p Platform, nativeID string) string { return string(p) + ":" + nativeID }

// ParseLeagueID splits "espn:123" into its platform and native ID. Native IDs must be
// digits so they are safe to interpolate into upstream URLs.
func ParseLeagueID(id string) (Platform, string, error) {
	p, native, ok := strings.Cut(id, ":")
	platform := Platform(p)
	if !ok || (platform != PlatformESPN && platform != PlatformSleeper) || !isDigits(native) {
		return "", "", &InvalidParamError{Param: "leagueId", Reason: "must be espn:<digits> or sleeper:<digits>"}
	}
	return platform, native, nil
}

// ValidPlayerID reports whether id looks like a Sleeper player ID: digits, or a team
// abbreviation for defenses.
func ValidPlayerID(id string) bool {
	if len(id) == 0 || len(id) > 12 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z') {
			return false
		}
	}
	return true
}

func isDigits(s string) bool {
	if len(s) == 0 || len(s) > 20 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
```

`internal/domain/errors.go`:

```go
package domain

import (
	"errors"
	"fmt"
)

var (
	ErrNotFound        = errors.New("not found")
	ErrUpstreamTimeout = errors.New("upstream timeout")
	ErrESPNAuth        = errors.New("espn authentication failed")
)

// UpstreamError is a failed call to a provider: a non-2xx status, an unparseable
// body (BadBody), or a transport error (Status 0).
type UpstreamError struct {
	Provider string
	Status   int
	BadBody  bool
	Err      error
}

func (e *UpstreamError) Error() string {
	msg := "upstream " + e.Provider
	if e.Status != 0 {
		msg += fmt.Sprintf(" status %d", e.Status)
	}
	if e.BadBody {
		msg += " returned an unparseable body"
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *UpstreamError) Unwrap() error { return e.Err }

type InvalidParamError struct {
	Param  string
	Reason string
}

func (e *InvalidParamError) Error() string { return e.Param + ": " + e.Reason }
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/domain/ && go vet ./...`
Expected: `ok  github.com/vsnandy/sports-api-go/internal/domain`

- [ ] **Step 7: Commit**

```bash
git add go.mod Makefile .gitignore internal/domain
git commit -m "feat: add module scaffold and domain types"
```

---

### Task 2: Generic TTL cache

**Files:**
- Create: `internal/cache/cache.go`
- Test: `internal/cache/cache_test.go`

**Interfaces:**
- Consumes: nothing
- Produces (package `cache`):
  - `func New[V any](now func() time.Time) *Cache[V]` (nil `now` → `time.Now`)
  - `func (c *Cache[V]) Get(key string) (V, bool)`
  - `func (c *Cache[V]) Set(key string, v V, ttl time.Duration)`
  - `func (c *Cache[V]) GetOrLoad(ctx context.Context, key string, ttl time.Duration, load func(context.Context) (V, error)) (V, error)` — errors are never cached

- [ ] **Step 1: Write the failing test**

`internal/cache/cache_test.go`:

```go
package cache

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestSetGetExpiry(t *testing.T) {
	now := time.Unix(0, 0)
	c := New[string](func() time.Time { return now })
	if _, ok := c.Get("k"); ok {
		t.Fatal("empty cache should miss")
	}
	c.Set("k", "v", time.Minute)
	if v, ok := c.Get("k"); !ok || v != "v" {
		t.Fatalf("Get = %q, %v; want v, true", v, ok)
	}
	now = now.Add(59 * time.Second)
	if _, ok := c.Get("k"); !ok {
		t.Fatal("should still hit before TTL")
	}
	now = now.Add(time.Second)
	if _, ok := c.Get("k"); ok {
		t.Fatal("should miss at TTL")
	}
}

func TestGetOrLoadCachesValuesNotErrors(t *testing.T) {
	c := New[int](nil)
	calls := 0
	boom := errors.New("boom")
	failing := func(context.Context) (int, error) { calls++; return 0, boom }
	if _, err := c.GetOrLoad(context.Background(), "k", time.Minute, failing); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	ok := func(context.Context) (int, error) { calls++; return 7, nil }
	for range 2 {
		v, err := c.GetOrLoad(context.Background(), "k", time.Minute, ok)
		if err != nil || v != 7 {
			t.Fatalf("GetOrLoad = %d, %v", v, err)
		}
	}
	if calls != 2 {
		t.Fatalf("loader calls = %d, want 2 (one failure, one success)", calls)
	}
}

func TestSweepDropsExpiredEntries(t *testing.T) {
	now := time.Unix(0, 0)
	c := New[int](func() time.Time { return now })
	for i := range sweepThreshold {
		c.Set(fmt.Sprint(i), i, time.Second)
	}
	now = now.Add(2 * time.Second)
	c.Set("fresh", 1, time.Minute)
	if n := c.Len(); n != 1 {
		t.Fatalf("Len after sweep = %d, want 1", n)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cache/`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Write the implementation**

`internal/cache/cache.go`:

```go
// Package cache is a small in-memory TTL cache. It lives only as long as a warm
// Lambda container.
package cache

import (
	"context"
	"sync"
	"time"
)

// sweepThreshold is the entry count above which Set drops expired entries.
const sweepThreshold = 1000

type entry[V any] struct {
	v   V
	exp time.Time
}

type Cache[V any] struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[string]entry[V]
}

func New[V any](now func() time.Time) *Cache[V] {
	if now == nil {
		now = time.Now
	}
	return &Cache[V]{now: now, m: map[string]entry[V]{}}
}

func (c *Cache[V]) Get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || !c.now().Before(e.exp) {
		var zero V
		return zero, false
	}
	return e.v, true
}

func (c *Cache[V]) Set(key string, v V, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if len(c.m) >= sweepThreshold {
		for k, e := range c.m {
			if !now.Before(e.exp) {
				delete(c.m, k)
			}
		}
	}
	c.m[key] = entry[V]{v: v, exp: now.Add(ttl)}
}

// Len reports the number of stored entries, including expired ones not yet swept.
func (c *Cache[V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}

// GetOrLoad returns the cached value for key, or calls load and caches its result.
// Errors from load are returned and never cached.
func (c *Cache[V]) GetOrLoad(ctx context.Context, key string, ttl time.Duration, load func(context.Context) (V, error)) (V, error) {
	if v, ok := c.Get(key); ok {
		return v, nil
	}
	v, err := load(ctx)
	if err != nil {
		var zero V
		return zero, err
	}
	c.Set(key, v, ttl)
	return v, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cache/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/cache
git commit -m "feat: add generic TTL cache"
```

---

### Task 3: Scoring

**Files:**
- Create: `internal/scoring/scoring.go`
- Test: `internal/scoring/scoring_test.go`

**Interfaces:**
- Consumes: `domain.ScoringRules`
- Produces (package `scoring`):
  - `func Points(stats map[string]float64, rules domain.ScoringRules) float64` — Σ stats[k]×rules[k], rounded to 2 decimals
  - `func Preset(name string) (domain.ScoringRules, bool)` — `"ppr"`, `"half"`, `"std"`

- [ ] **Step 1: Write the failing test**

`internal/scoring/scoring_test.go`:

```go
package scoring

import (
	"testing"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

func TestPoints(t *testing.T) {
	rules := domain.ScoringRules{"pass_yd": 0.04, "pass_td": 4, "pass_int": -2, "rush_yd": 0.1}
	tests := []struct {
		name  string
		stats map[string]float64
		want  float64
	}{
		{"qb line", map[string]float64{"pass_yd": 250, "pass_td": 2, "pass_int": 1, "rush_yd": 15}, 17.5},
		{"negative rushing", map[string]float64{"rush_yd": -5}, -0.5},
		{"unknown keys ignored", map[string]float64{"pass_att": 40, "pass_td": 1}, 4},
		{"rounds to cents", map[string]float64{"pass_yd": 263}, 10.52},
		{"empty", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Points(tt.stats, rules); got != tt.want {
				t.Fatalf("Points = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPresets(t *testing.T) {
	wr := map[string]float64{"rec": 6, "rec_yd": 85, "rec_td": 1}
	for name, want := range map[string]float64{"ppr": 20.5, "half": 17.5, "std": 14.5} {
		rules, ok := Preset(name)
		if !ok {
			t.Fatalf("Preset(%q) missing", name)
		}
		if got := Points(wr, rules); got != want {
			t.Errorf("%s points = %v, want %v", name, got, want)
		}
	}
	if _, ok := Preset("bogus"); ok {
		t.Fatal("unknown preset should not resolve")
	}
	a, _ := Preset("ppr")
	a["rec"] = 99
	b, _ := Preset("ppr")
	if b["rec"] != 1 {
		t.Fatal("Preset must return a fresh copy")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/scoring/`
Expected: FAIL — `undefined: Points`.

- [ ] **Step 3: Write the implementation**

`internal/scoring/scoring.go`:

```go
// Package scoring computes fantasy points from Sleeper-keyed stat lines.
package scoring

import (
	"maps"
	"math"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

// Points returns Σ stats[k] × rules[k] over keys present in both, rounded to 2 decimals.
func Points(stats map[string]float64, rules domain.ScoringRules) float64 {
	var total float64
	for k, v := range stats {
		if w, ok := rules[k]; ok {
			total += v * w
		}
	}
	return math.Round(total*100) / 100
}

// base is standard (non-PPR) scoring in Sleeper stat keys.
var base = domain.ScoringRules{
	"pass_yd": 0.04, "pass_td": 4, "pass_int": -2, "pass_2pt": 2,
	"rush_yd": 0.1, "rush_td": 6, "rush_2pt": 2,
	"rec_yd": 0.1, "rec_td": 6, "rec_2pt": 2,
	"fum_lost": -2,
	"fgm_0_19": 3, "fgm_20_29": 3, "fgm_30_39": 3, "fgm_40_49": 4, "fgm_50p": 5,
	"fgmiss": -1, "xpm": 1, "xpmiss": -1,
	"def_td": 6, "sack": 1, "int": 2, "fum_rec": 2, "safe": 2, "blk_kick": 2,
	"pts_allow_0": 10, "pts_allow_1_6": 7, "pts_allow_7_13": 4, "pts_allow_14_20": 1,
	"pts_allow_21_27": 0, "pts_allow_28_34": -1, "pts_allow_35p": -4,
}

// Preset returns a fresh copy of the named preset: "ppr", "half", or "std".
func Preset(name string) (domain.ScoringRules, bool) {
	var rec float64
	switch name {
	case "ppr":
		rec = 1
	case "half":
		rec = 0.5
	case "std":
		rec = 0
	default:
		return nil, false
	}
	r := maps.Clone(base)
	r["rec"] = rec
	return r, true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/scoring/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/scoring
git commit -m "feat: add fantasy point scoring and presets"
```

---

### Task 4: Shared upstream HTTP helper

**Files:**
- Create: `internal/providers/httpx/httpx.go`
- Test: `internal/providers/httpx/httpx_test.go`

**Interfaces:**
- Consumes: `domain.ErrNotFound`, `domain.ErrUpstreamTimeout`, `*domain.UpstreamError`
- Produces (package `httpx`):
  - `func New(provider string, timeout time.Duration) *Client`
  - `func (c *Client) GetJSON(ctx context.Context, url string, header http.Header, out any) error`
    - 404 → wraps `domain.ErrNotFound`; other non-2xx → `*domain.UpstreamError{Status}`; undecodable body → `*domain.UpstreamError{BadBody: true}`; timeout/deadline → wraps `domain.ErrUpstreamTimeout`; transport failure → `*domain.UpstreamError{Status: 0}`

- [ ] **Step 1: Write the failing test**

`internal/providers/httpx/httpx_test.go`:

```go
package httpx

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

func TestGetJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			if r.Header.Get("X-Test") != "yes" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, `{"name":"x"}`)
		case "/missing":
			http.NotFound(w, r)
		case "/boom":
			w.WriteHeader(http.StatusInternalServerError)
		case "/html":
			fmt.Fprint(w, "<html>login</html>")
		case "/slow":
			time.Sleep(200 * time.Millisecond)
			fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()
	c := New("test", 100*time.Millisecond)
	ctx := context.Background()

	t.Run("decodes and sends headers", func(t *testing.T) {
		var out struct{ Name string }
		err := c.GetJSON(ctx, srv.URL+"/ok", http.Header{"X-Test": {"yes"}}, &out)
		if err != nil || out.Name != "x" {
			t.Fatalf("out = %+v, err = %v", out, err)
		}
	})
	t.Run("404 is not found", func(t *testing.T) {
		if err := c.GetJSON(ctx, srv.URL+"/missing", nil, &struct{}{}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
	t.Run("500 is upstream error", func(t *testing.T) {
		var ue *domain.UpstreamError
		err := c.GetJSON(ctx, srv.URL+"/boom", nil, &struct{}{})
		if !errors.As(err, &ue) || ue.Status != 500 || ue.Provider != "test" {
			t.Fatalf("err = %v, want UpstreamError status 500", err)
		}
	})
	t.Run("non-JSON body is bad body", func(t *testing.T) {
		var ue *domain.UpstreamError
		err := c.GetJSON(ctx, srv.URL+"/html", nil, &struct{}{})
		if !errors.As(err, &ue) || !ue.BadBody || ue.Status != 200 {
			t.Fatalf("err = %v, want BadBody with status 200", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		if err := c.GetJSON(ctx, srv.URL+"/slow", nil, &struct{}{}); !errors.Is(err, domain.ErrUpstreamTimeout) {
			t.Fatalf("err = %v, want ErrUpstreamTimeout", err)
		}
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/providers/httpx/`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Write the implementation**

`internal/providers/httpx/httpx.go`:

```go
// Package httpx is the shared JSON GET used by provider adapters. It maps HTTP
// failures onto domain errors and logs each upstream call.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

type Client struct {
	http     *http.Client
	provider string
}

func New(provider string, timeout time.Duration) *Client {
	return &Client{http: &http.Client{Timeout: timeout}, provider: provider}
}

// GetJSON GETs url and decodes the JSON body into out. header may be nil.
func (c *Client) GetJSON(ctx context.Context, url string, header http.Header, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("Accept", "application/json")

	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		if isTimeout(err) {
			return fmt.Errorf("%s %s: %w", c.provider, req.URL.Path, domain.ErrUpstreamTimeout)
		}
		return &domain.UpstreamError{Provider: c.provider, Err: err}
	}
	defer resp.Body.Close()
	slog.InfoContext(ctx, "upstream call",
		"provider", c.provider, "path", req.URL.Path,
		"status", resp.StatusCode, "ms", time.Since(start).Milliseconds())

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%s %s: %w", c.provider, req.URL.Path, domain.ErrNotFound)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return &domain.UpstreamError{Provider: c.provider, Status: resp.StatusCode}
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		if isTimeout(err) {
			return fmt.Errorf("%s %s: %w", c.provider, req.URL.Path, domain.ErrUpstreamTimeout)
		}
		return &domain.UpstreamError{Provider: c.provider, Status: resp.StatusCode, BadBody: true, Err: err}
	}
	return nil
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/providers/httpx/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/providers/httpx
git commit -m "feat: add upstream JSON client with domain error mapping"
```

---

### Task 5: Sleeper provider

**Files:**
- Create: `internal/providers/sleeper/client.go`, `internal/providers/sleeper/leagues.go`, `internal/providers/sleeper/stats.go`
- Create fixtures: `internal/providers/sleeper/testdata/{state,user,user_leagues,league,users,rosters,matchups,players,week_stats,gamelog}.json`
- Modify: `docs/superpowers/specs/2026-09-25-sports-api-v1-design.md` (§6 weekly stats URL)
- Test: `internal/providers/sleeper/sleeper_test.go`

**Interfaces:**
- Consumes: `httpx.Client.GetJSON`, domain types
- Produces (package `sleeper`):
  - consts `DefaultAPIBase = "https://api.sleeper.app/v1"`, `DefaultStatsBase = "https://api.sleeper.com"`
  - `func New(hc *httpx.Client, apiBase, statsBase, username string) *Client`
  - `(*Client).Platform() domain.Platform`
  - `(*Client).State(ctx) (domain.SeasonState, error)`
  - `(*Client).ListLeagues(ctx, season int) ([]domain.LeagueSummary, error)`
  - `(*Client).League(ctx, nativeID string, season int) (domain.League, error)` — season ignored (Sleeper league IDs are per season)
  - `(*Client).Rosters(ctx, nativeID string, season int, slots []string) ([]domain.RosterRef, error)`
  - `(*Client).Matchups(ctx, nativeID string, season, week int, slots []string) ([]domain.MatchupRef, error)`
  - `(*Client).Players(ctx) ([]domain.Player, error)`
  - `(*Client).WeekStats(ctx, season, week int) (map[string]domain.StatLine, error)`
  - `(*Client).PlayerGamelog(ctx, playerID string, season int) ([]domain.StatLine, error)`

- [ ] **Step 1: Verify live response shapes (one-time, manual)**

The stats endpoints are unofficial. Confirm the fields this task relies on before writing fixtures:

Run: `curl -s 'https://api.sleeper.com/stats/nfl/2025/1?season_type=regular' | jq '.[0] | {player_id, week, opponent, stats: (.stats | keys | .[0:5])}'`
Expected: an object with `player_id`, `week`, `opponent`, and a `stats` object keyed by Sleeper stat keys.

Run: `curl -s 'https://api.sleeper.com/stats/nfl/player/6794?season_type=regular&season=2025&grouping=week' | jq 'to_entries | .[0:2]'`
Expected: an object keyed by week number strings, values are objects with `stats`/`opponent` or `null` (bye).

Run: `curl -s https://api.sleeper.app/v1/players/nfl | jq '.["4046"] | {player_id, full_name, position, team, espn_id}'`
Expected: Patrick Mahomes with a non-null `espn_id`.

If any shape differs, STOP and report the actual shape before continuing.

Then update spec §6: replace the line `` - `GET https://api.sleeper.com/stats/nfl/regular/{season}/{week}` — weekly stats, all players (unofficial) `` with `` - `GET https://api.sleeper.com/stats/nfl/{season}/{week}?season_type=regular` — weekly stats, all players, array of `{player_id, week, opponent, stats}` (unofficial) ``.

- [ ] **Step 2: Write fixtures**

`testdata/state.json`:
```json
{"season": "2026", "week": 3, "season_type": "regular", "display_week": 3}
```

`testdata/user.json`:
```json
{"user_id": "u1", "username": "varun", "display_name": "Varun"}
```

`testdata/user_leagues.json`:
```json
[
  {"league_id": "111", "name": "Dynasty Bros", "season": "2026"},
  {"league_id": "222", "name": "Work League", "season": "2026"}
]
```

`testdata/league.json`:
```json
{
  "league_id": "111", "name": "Dynasty Bros", "season": "2026", "sport": "nfl",
  "scoring_settings": {"pass_yd": 0.04, "pass_td": 4, "rec": 1, "rec_yd": 0.1, "rec_td": 6, "rush_yd": 0.1, "rush_td": 6},
  "roster_positions": ["QB", "RB", "WR", "FLEX", "DEF", "BN", "BN"]
}
```

`testdata/users.json`:
```json
[
  {"user_id": "u1", "display_name": "Varun", "metadata": {"team_name": "Gridiron Gurus"}},
  {"user_id": "u2", "display_name": "Sam", "metadata": {}}
]
```

`testdata/rosters.json`:
```json
[
  {"roster_id": 2, "owner_id": "u2", "starters": ["4046", "4035", "2133", "0", "KC"], "players": ["4046", "4035", "2133", "KC", "6794"], "reserve": null, "taxi": null},
  {"roster_id": 1, "owner_id": "u1", "starters": ["4984", "4866", "7564", "9509", "SF"], "players": ["4984", "4866", "7564", "9509", "SF", "1234", "5678", "8888"], "reserve": ["5678"], "taxi": ["8888"]}
]
```

`testdata/matchups.json`:
```json
[
  {"roster_id": 2, "matchup_id": 1, "points": 98.5, "starters": ["4046", "4035", "2133", "0", "KC"], "players": ["4046", "4035", "2133", "KC", "6794"]},
  {"roster_id": 1, "matchup_id": 1, "points": 112.34, "starters": ["4984", "4866", "7564", "9509", "SF"], "players": ["4984", "4866", "7564", "9509", "SF", "1234"]},
  {"roster_id": 3, "matchup_id": null, "points": 0, "starters": [], "players": []}
]
```

`testdata/players.json`:
```json
{
  "4046": {"player_id": "4046", "full_name": "Patrick Mahomes", "position": "QB", "team": "KC", "espn_id": 3139477},
  "KC": {"player_id": "KC", "first_name": "Kansas City", "last_name": "Chiefs", "position": "DEF", "team": "KC", "espn_id": null},
  "6794": {"player_id": "6794", "full_name": "Justin Jefferson", "position": "WR", "team": "MIN", "espn_id": "4262921"}
}
```

`testdata/week_stats.json`:
```json
[
  {"player_id": "4046", "week": 3, "season": "2026", "opponent": "ATL", "stats": {"pass_yd": 250, "pass_td": 2}},
  {"player_id": "6794", "week": 3, "season": "2026", "opponent": "GB", "stats": {"rec": 6, "rec_yd": 85, "rec_td": 1}}
]
```

`testdata/gamelog.json`:
```json
{
  "1": {"player_id": "6794", "week": 1, "opponent": "NYG", "stats": {"rec": 5, "rec_yd": 59}},
  "2": null,
  "3": {"player_id": "6794", "week": 3, "opponent": "GB", "stats": {"rec": 6, "rec_yd": 85, "rec_td": 1}}
}
```

- [ ] **Step 3: Write the failing test**

`internal/providers/sleeper/sleeper_test.go`:

```go
package sleeper

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vsnandy/sports-api-go/internal/domain"
	"github.com/vsnandy/sports-api-go/internal/providers/httpx"
)

var slots = []string{"QB", "RB", "WR", "FLEX", "DEF", "BN", "BN"}

func newTestClient(t *testing.T) *Client {
	t.Helper()
	routes := map[string]string{
		"/v1/state/nfl":                "state.json",
		"/v1/user/varun":               "user.json",
		"/v1/user/u1/leagues/nfl/2026": "user_leagues.json",
		"/v1/league/111":               "league.json",
		"/v1/league/111/users":         "users.json",
		"/v1/league/111/rosters":       "rosters.json",
		"/v1/league/111/matchups/3":    "matchups.json",
		"/v1/players/nfl":              "players.json",
		"/stats/nfl/2026/3":            "week_stats.json",
		"/stats/nfl/player/6794":       "gamelog.json",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/stats/") && r.URL.Query().Get("season_type") != "regular" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join("testdata", f))
	}))
	t.Cleanup(srv.Close)
	return New(httpx.New("sleeper", 2*time.Second), srv.URL+"/v1", srv.URL, "varun")
}

func ids(entries []domain.RosterEntryRef) []string {
	out := []string{}
	for _, e := range entries {
		out = append(out, e.Slot+"="+e.Ref.ID)
	}
	return out
}

func TestState(t *testing.T) {
	st, err := newTestClient(t).State(context.Background())
	if err != nil || st != (domain.SeasonState{Season: 2026, Week: 3}) {
		t.Fatalf("State = %+v, %v", st, err)
	}
}

func TestListLeagues(t *testing.T) {
	ls, err := newTestClient(t).ListLeagues(context.Background(), 2026)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.LeagueSummary{
		{ID: "sleeper:111", Platform: domain.PlatformSleeper, Season: 2026, Name: "Dynasty Bros"},
		{ID: "sleeper:222", Platform: domain.PlatformSleeper, Season: 2026, Name: "Work League"},
	}
	if !reflect.DeepEqual(ls, want) {
		t.Fatalf("ListLeagues = %+v", ls)
	}
}

func TestLeague(t *testing.T) {
	l, err := newTestClient(t).League(context.Background(), "111", 0)
	if err != nil {
		t.Fatal(err)
	}
	if l.ID != "sleeper:111" || l.Name != "Dynasty Bros" || l.Season != 2026 || l.Sport != domain.SportNFL {
		t.Fatalf("League header = %+v", l)
	}
	wantTeams := []domain.Team{{ID: "1", Name: "Gridiron Gurus", Owner: "Varun"}, {ID: "2", Name: "Sam", Owner: "Sam"}}
	if !reflect.DeepEqual(l.Teams, wantTeams) {
		t.Fatalf("Teams = %+v", l.Teams)
	}
	if l.Scoring["rec"] != 1 || !reflect.DeepEqual(l.RosterSlots, slots) {
		t.Fatalf("Scoring/RosterSlots = %v / %v", l.Scoring, l.RosterSlots)
	}
	if l.UnsupportedRules == nil || len(l.UnsupportedRules) != 0 {
		t.Fatalf("UnsupportedRules = %#v, want empty non-nil", l.UnsupportedRules)
	}
}

func TestLeagueNotFound(t *testing.T) {
	if _, err := newTestClient(t).League(context.Background(), "999", 0); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestRosters(t *testing.T) {
	rs, err := newTestClient(t).Rosters(context.Background(), "111", 2026, slots)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 || rs[0].TeamID != "1" || rs[1].TeamID != "2" {
		t.Fatalf("roster order = %+v", rs)
	}
	if got, want := ids(rs[0].Starters), []string{"QB=4984", "RB=4866", "WR=7564", "FLEX=9509", "DEF=SF"}; !reflect.DeepEqual(got, want) {
		t.Errorf("team 1 starters = %v, want %v", got, want)
	}
	if got, want := ids(rs[0].Bench), []string{"BN=1234"}; !reflect.DeepEqual(got, want) {
		t.Errorf("team 1 bench = %v, want %v", got, want)
	}
	if got, want := ids(rs[0].Reserve), []string{"IR=5678", "TAXI=8888"}; !reflect.DeepEqual(got, want) {
		t.Errorf("team 1 reserve = %v, want %v", got, want)
	}
	if got, want := ids(rs[1].Starters), []string{"QB=4046", "RB=4035", "WR=2133", "DEF=KC"}; !reflect.DeepEqual(got, want) {
		t.Errorf("team 2 starters (empty FLEX skipped) = %v, want %v", got, want)
	}
	if rs[1].Reserve == nil {
		t.Error("Reserve should be non-nil even when empty")
	}
	if rs[0].Starters[0].Ref.Platform != domain.PlatformSleeper {
		t.Error("refs should carry the sleeper platform")
	}
}

func TestMatchups(t *testing.T) {
	ms, err := newTestClient(t).Matchups(context.Background(), "111", 2026, 3, slots)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 {
		t.Fatalf("got %d matchups, want 1 (null matchup_id skipped)", len(ms))
	}
	m := ms[0]
	if m.Week != 3 || m.Home.TeamID != "1" || m.Home.Points != 112.34 || m.Away.TeamID != "2" || m.Away.Points != 98.5 {
		t.Fatalf("matchup = %+v", m)
	}
	if len(m.Home.Roster.Starters) != 5 || len(m.Home.Roster.Bench) != 1 {
		t.Fatalf("home roster = %+v", m.Home.Roster)
	}
}

func TestPlayers(t *testing.T) {
	ps, err := newTestClient(t).Players(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]domain.Player{}
	for _, p := range ps {
		byID[*p.ID] = p
	}
	if p := byID["4046"]; p.Name != "Patrick Mahomes" || p.PlatformIDs["espn"] != "3139477" || p.PlatformIDs["sleeper"] != "4046" {
		t.Errorf("numeric espn_id: %+v", p)
	}
	if p := byID["6794"]; p.PlatformIDs["espn"] != "4262921" {
		t.Errorf("string espn_id: %+v", p)
	}
	if p := byID["KC"]; p.Name != "Kansas City Chiefs" || p.Position != "DEF" {
		t.Errorf("defense: %+v", p)
	} else if _, ok := p.PlatformIDs["espn"]; ok {
		t.Errorf("null espn_id should be omitted: %+v", p.PlatformIDs)
	}
}

func TestWeekStats(t *testing.T) {
	lines, err := newTestClient(t).WeekStats(context.Background(), 2026, 3)
	if err != nil {
		t.Fatal(err)
	}
	l := lines["6794"]
	if len(lines) != 2 || l.Stats["rec"] != 6 || l.Opponent != "GB" || l.Week != 3 || l.Season != 2026 {
		t.Fatalf("WeekStats = %+v", lines)
	}
}

func TestPlayerGamelog(t *testing.T) {
	lines, err := newTestClient(t).PlayerGamelog(context.Background(), "6794", 2026)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].Week != 1 || lines[1].Week != 3 || lines[1].Stats["rec_td"] != 1 {
		t.Fatalf("gamelog = %+v (bye week should be skipped, sorted by week)", lines)
	}
}
```

- [ ] **Step 4: Run test to verify it fails**

Run: `go test ./internal/providers/sleeper/`
Expected: FAIL — `undefined: New`.

- [ ] **Step 5: Write the implementation**

`internal/providers/sleeper/client.go`:

```go
// Package sleeper adapts the Sleeper API (league data, players dump, stats) to domain types.
package sleeper

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/vsnandy/sports-api-go/internal/domain"
	"github.com/vsnandy/sports-api-go/internal/providers/httpx"
)

const (
	DefaultAPIBase   = "https://api.sleeper.app/v1"
	DefaultStatsBase = "https://api.sleeper.com"
)

type Client struct {
	http      *httpx.Client
	apiBase   string
	statsBase string
	username  string
}

func New(hc *httpx.Client, apiBase, statsBase, username string) *Client {
	return &Client{http: hc, apiBase: apiBase, statsBase: statsBase, username: username}
}

func (c *Client) Platform() domain.Platform { return domain.PlatformSleeper }

func (c *Client) get(ctx context.Context, url string, out any) error {
	return c.http.GetJSON(ctx, url, nil, out)
}

func badBody(err error) error {
	return &domain.UpstreamError{Provider: "sleeper", BadBody: true, Err: err}
}

func (c *Client) State(ctx context.Context) (domain.SeasonState, error) {
	var s struct {
		Season string `json:"season"`
		Week   int    `json:"week"`
	}
	if err := c.get(ctx, c.apiBase+"/state/nfl", &s); err != nil {
		return domain.SeasonState{}, err
	}
	season, err := strconv.Atoi(s.Season)
	if err != nil {
		return domain.SeasonState{}, badBody(err)
	}
	return domain.SeasonState{Season: season, Week: s.Week}, nil
}

type playerJSON struct {
	FullName  string     `json:"full_name"`
	FirstName string     `json:"first_name"`
	LastName  string     `json:"last_name"`
	Position  string     `json:"position"`
	Team      string     `json:"team"`
	ESPNID    flexString `json:"espn_id"`
}

// flexString accepts a JSON string, number, or null; the players dump mixes them.
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*f = ""
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = flexString(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = flexString(n.String())
	return nil
}

// Players fetches the full NFL players dump. Sleeper asks callers to do this at most daily.
func (c *Client) Players(ctx context.Context) ([]domain.Player, error) {
	var m map[string]playerJSON
	if err := c.get(ctx, c.apiBase+"/players/nfl", &m); err != nil {
		return nil, err
	}
	out := make([]domain.Player, 0, len(m))
	for id, p := range m {
		name := p.FullName
		if name == "" {
			name = strings.TrimSpace(p.FirstName + " " + p.LastName)
		}
		ids := map[string]string{"sleeper": id}
		if p.ESPNID != "" {
			ids["espn"] = string(p.ESPNID)
		}
		out = append(out, domain.Player{ID: &id, Name: name, Position: p.Position, NFLTeam: p.Team, PlatformIDs: ids})
	}
	slices.SortFunc(out, func(a, b domain.Player) int { return cmp.Compare(*a.ID, *b.ID) })
	return out, nil
}
```

`internal/providers/sleeper/leagues.go`:

```go
package sleeper

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

type leagueJSON struct {
	LeagueID        string             `json:"league_id"`
	Name            string             `json:"name"`
	Season          string             `json:"season"`
	ScoringSettings map[string]float64 `json:"scoring_settings"`
	RosterPositions []string           `json:"roster_positions"`
}

type userJSON struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	Metadata    struct {
		TeamName string `json:"team_name"`
	} `json:"metadata"`
}

type rosterJSON struct {
	RosterID int      `json:"roster_id"`
	OwnerID  string   `json:"owner_id"`
	Starters []string `json:"starters"`
	Players  []string `json:"players"`
	Reserve  []string `json:"reserve"`
	Taxi     []string `json:"taxi"`
}

type matchupJSON struct {
	RosterID  int      `json:"roster_id"`
	MatchupID *int     `json:"matchup_id"`
	Points    float64  `json:"points"`
	Starters  []string `json:"starters"`
	Players   []string `json:"players"`
}

func (c *Client) ListLeagues(ctx context.Context, season int) ([]domain.LeagueSummary, error) {
	var u *struct {
		UserID string `json:"user_id"`
	}
	if err := c.get(ctx, c.apiBase+"/user/"+url.PathEscape(c.username), &u); err != nil {
		return nil, err
	}
	if u == nil {
		// Misconfiguration, not a client error: surface as 502.
		return nil, &domain.UpstreamError{Provider: "sleeper", Err: fmt.Errorf("user %q not found", c.username)}
	}
	var ls []leagueJSON
	if err := c.get(ctx, fmt.Sprintf("%s/user/%s/leagues/nfl/%d", c.apiBase, u.UserID, season), &ls); err != nil {
		return nil, err
	}
	out := make([]domain.LeagueSummary, 0, len(ls))
	for _, l := range ls {
		out = append(out, domain.LeagueSummary{
			ID: domain.LeagueID(domain.PlatformSleeper, l.LeagueID), Platform: domain.PlatformSleeper,
			Season: season, Name: l.Name,
		})
	}
	return out, nil
}

// League ignores season: a Sleeper league ID already identifies a single season.
func (c *Client) League(ctx context.Context, nativeID string, _ int) (domain.League, error) {
	var l leagueJSON
	if err := c.get(ctx, c.apiBase+"/league/"+nativeID, &l); err != nil {
		return domain.League{}, err
	}
	if l.LeagueID == "" { // Sleeper answers unknown IDs with 200 null
		return domain.League{}, fmt.Errorf("sleeper league %s: %w", nativeID, domain.ErrNotFound)
	}
	season, err := strconv.Atoi(l.Season)
	if err != nil {
		return domain.League{}, badBody(err)
	}
	var users []userJSON
	if err := c.get(ctx, c.apiBase+"/league/"+nativeID+"/users", &users); err != nil {
		return domain.League{}, err
	}
	rosters, err := c.rosters(ctx, nativeID)
	if err != nil {
		return domain.League{}, err
	}
	byUser := map[string]userJSON{}
	for _, u := range users {
		byUser[u.UserID] = u
	}
	teams := make([]domain.Team, 0, len(rosters))
	for _, r := range rosters {
		u := byUser[r.OwnerID]
		name := u.Metadata.TeamName
		if name == "" {
			name = u.DisplayName
		}
		if name == "" {
			name = fmt.Sprintf("Team %d", r.RosterID)
		}
		teams = append(teams, domain.Team{ID: strconv.Itoa(r.RosterID), Name: name, Owner: u.DisplayName})
	}
	scoring := domain.ScoringRules(l.ScoringSettings)
	if scoring == nil {
		scoring = domain.ScoringRules{}
	}
	return domain.League{
		ID: domain.LeagueID(domain.PlatformSleeper, nativeID), Platform: domain.PlatformSleeper,
		Sport: domain.SportNFL, Season: season, Name: l.Name, Teams: teams,
		Scoring: scoring, UnsupportedRules: []string{}, RosterSlots: l.RosterPositions,
	}, nil
}

func (c *Client) rosters(ctx context.Context, nativeID string) ([]rosterJSON, error) {
	var rs []rosterJSON
	if err := c.get(ctx, c.apiBase+"/league/"+nativeID+"/rosters", &rs); err != nil {
		return nil, err
	}
	slices.SortFunc(rs, func(a, b rosterJSON) int { return cmp.Compare(a.RosterID, b.RosterID) })
	return rs, nil
}

func (c *Client) Rosters(ctx context.Context, nativeID string, _ int, slots []string) ([]domain.RosterRef, error) {
	rs, err := c.rosters(ctx, nativeID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.RosterRef, 0, len(rs))
	for _, r := range rs {
		out = append(out, buildRoster(strconv.Itoa(r.RosterID), slots, r.Starters, r.Players, r.Reserve, r.Taxi))
	}
	return out, nil
}

func (c *Client) Matchups(ctx context.Context, nativeID string, _ int, week int, slots []string) ([]domain.MatchupRef, error) {
	var ms []matchupJSON
	if err := c.get(ctx, fmt.Sprintf("%s/league/%s/matchups/%d", c.apiBase, nativeID, week), &ms); err != nil {
		return nil, err
	}
	groups := map[int][]matchupJSON{}
	for _, m := range ms {
		if m.MatchupID != nil { // nil = bye / median-only entry
			groups[*m.MatchupID] = append(groups[*m.MatchupID], m)
		}
	}
	keys := slices.Sorted(maps.Keys(groups))
	out := make([]domain.MatchupRef, 0, len(keys))
	for _, k := range keys {
		pair := groups[k]
		if len(pair) != 2 {
			continue
		}
		slices.SortFunc(pair, func(a, b matchupJSON) int { return cmp.Compare(a.RosterID, b.RosterID) })
		out = append(out, domain.MatchupRef{Week: week, Home: side(pair[0], slots), Away: side(pair[1], slots)})
	}
	return out, nil
}

func side(m matchupJSON, slots []string) domain.MatchupSideRef {
	id := strconv.Itoa(m.RosterID)
	return domain.MatchupSideRef{TeamID: id, Points: m.Points, Roster: buildRoster(id, slots, m.Starters, m.Players, nil, nil)}
}

// buildRoster splits a Sleeper roster into starters (aligned with the league's
// non-bench roster_positions), reserve (IR and taxi), and bench (everyone else).
// Empty starter slots ("0" or "") are skipped.
func buildRoster(teamID string, slots, starters, players, reserve, taxi []string) domain.RosterRef {
	starterSlots := make([]string, 0, len(slots))
	for _, s := range slots {
		if s != "BN" && s != "IR" && s != "TAXI" {
			starterSlots = append(starterSlots, s)
		}
	}
	ref := func(slot, id string) domain.RosterEntryRef {
		return domain.RosterEntryRef{Slot: slot, Ref: domain.PlayerRef{Platform: domain.PlatformSleeper, ID: id}}
	}
	r := domain.RosterRef{TeamID: teamID, Starters: []domain.RosterEntryRef{}, Bench: []domain.RosterEntryRef{}, Reserve: []domain.RosterEntryRef{}}
	used := map[string]bool{}
	for i, id := range starters {
		if id == "" || id == "0" {
			continue
		}
		slot := "UNKNOWN"
		if i < len(starterSlots) {
			slot = starterSlots[i]
		}
		r.Starters = append(r.Starters, ref(slot, id))
		used[id] = true
	}
	for _, group := range []struct {
		slot string
		ids  []string
	}{{"IR", reserve}, {"TAXI", taxi}} {
		for _, id := range group.ids {
			if id != "" && !used[id] {
				r.Reserve = append(r.Reserve, ref(group.slot, id))
				used[id] = true
			}
		}
	}
	for _, id := range players {
		if id != "" && !used[id] {
			r.Bench = append(r.Bench, ref("BN", id))
			used[id] = true
		}
	}
	return r
}
```

`internal/providers/sleeper/stats.go`:

```go
package sleeper

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

type statJSON struct {
	PlayerID string             `json:"player_id"`
	Opponent string             `json:"opponent"`
	Stats    map[string]float64 `json:"stats"`
}

// WeekStats returns every player's regular-season stat line for one week, keyed by Sleeper ID.
func (c *Client) WeekStats(ctx context.Context, season, week int) (map[string]domain.StatLine, error) {
	var rows []statJSON
	if err := c.get(ctx, fmt.Sprintf("%s/stats/nfl/%d/%d?season_type=regular", c.statsBase, season, week), &rows); err != nil {
		return nil, err
	}
	out := make(map[string]domain.StatLine, len(rows))
	for _, r := range rows {
		if r.PlayerID == "" {
			continue
		}
		out[r.PlayerID] = domain.StatLine{PlayerID: r.PlayerID, Season: season, Week: week, Opponent: r.Opponent, Stats: r.Stats}
	}
	return out, nil
}

// PlayerGamelog returns one player's weekly regular-season stat lines, sorted by week.
// Bye weeks (null entries) are omitted.
func (c *Client) PlayerGamelog(ctx context.Context, playerID string, season int) ([]domain.StatLine, error) {
	var m map[string]*statJSON
	u := fmt.Sprintf("%s/stats/nfl/player/%s?season_type=regular&season=%d&grouping=week", c.statsBase, url.PathEscape(playerID), season)
	if err := c.get(ctx, u, &m); err != nil {
		return nil, err
	}
	out := make([]domain.StatLine, 0, len(m))
	for wk, r := range m {
		week, err := strconv.Atoi(wk)
		if r == nil || err != nil {
			continue
		}
		out = append(out, domain.StatLine{PlayerID: playerID, Season: season, Week: week, Opponent: r.Opponent, Stats: r.Stats})
	}
	slices.SortFunc(out, func(a, b domain.StatLine) int { return cmp.Compare(a.Week, b.Week) })
	return out, nil
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/providers/sleeper/ && go vet ./...`
Expected: `ok`

- [ ] **Step 7: Commit**

```bash
git add internal/providers/sleeper docs/superpowers/specs/2026-09-25-sports-api-v1-design.md
git commit -m "feat: add Sleeper provider for leagues, players, and stats"
```

---

### Task 6: ESPN provider

**Files:**
- Create: `internal/providers/espn/client.go`, `internal/providers/espn/league.go`, `internal/providers/espn/tables.go`
- Create fixture: `internal/providers/espn/testdata/league.json`
- Test: `internal/providers/espn/espn_test.go`

**Interfaces:**
- Consumes: `httpx.Client.GetJSON`, `golang.org/x/sync/errgroup`, domain types
- Produces (package `espn`):
  - const `DefaultBase = "https://lm-api-reads.fantasy.espn.com/apis/v3/games/ffl"`
  - `func New(hc *httpx.Client, base, espnS2, swid string, leagueIDs []string) *Client`
  - `(*Client).Platform() domain.Platform`
  - `(*Client).ListLeagues(ctx, season int) ([]domain.LeagueSummary, error)` — configured IDs; leagues that 404 for the season are skipped
  - `(*Client).League(ctx, nativeID string, season int) (domain.League, error)`
  - `(*Client).Rosters(ctx, nativeID string, season int, slots []string) ([]domain.RosterRef, error)` — slots ignored
  - `(*Client).Matchups(ctx, nativeID string, season, week int, slots []string) ([]domain.MatchupRef, error)` — slots ignored
  - Auth failures (401, 403, or non-JSON 2xx) wrap `domain.ErrESPNAuth`.

- [ ] **Step 1: Write the fixture**

`internal/providers/espn/testdata/league.json`:

```json
{
  "id": 123456,
  "seasonId": 2026,
  "settings": {
    "name": "Office League",
    "rosterSettings": {"lineupSlotCounts": {"0": 1, "2": 2, "4": 2, "6": 1, "23": 1, "16": 1, "17": 1, "20": 6, "21": 1, "7": 0}},
    "scoringSettings": {"scoringItems": [
      {"statId": 3, "points": 0.04},
      {"statId": 4, "points": 4},
      {"statId": 53, "points": 1, "pointsOverrides": {"6": 1.5}},
      {"statId": 80, "points": 3},
      {"statId": 92, "points": 1}
    ]}
  },
  "members": [{"id": "{AAA}", "displayName": "varun"}, {"id": "{BBB}", "displayName": "alex"}],
  "teams": [
    {"id": 1, "name": "Team Varun", "owners": ["{AAA}"], "roster": {"entries": [
      {"playerId": 4262921, "lineupSlotId": 20, "playerPoolEntry": {"player": {"fullName": "Justin Jefferson", "defaultPositionId": 3, "proTeamId": 16}}},
      {"playerId": -16012, "lineupSlotId": 16, "playerPoolEntry": {"player": {"fullName": "Chiefs D/ST", "defaultPositionId": 16, "proTeamId": 12}}},
      {"playerId": 3139477, "lineupSlotId": 0, "playerPoolEntry": {"player": {"fullName": "Patrick Mahomes", "defaultPositionId": 1, "proTeamId": 12}}},
      {"playerId": 9999999, "lineupSlotId": 21, "playerPoolEntry": {"player": {"fullName": "Rookie Guy", "defaultPositionId": 2, "proTeamId": 0}}}
    ]}},
    {"id": 2, "location": "Alex's", "nickname": "Aces", "owners": ["{BBB}"], "roster": {"entries": []}}
  ],
  "schedule": [
    {"matchupPeriodId": 2, "home": {"teamId": 1, "totalPoints": 90}, "away": {"teamId": 2, "totalPoints": 80}},
    {"matchupPeriodId": 3,
      "home": {"teamId": 2, "totalPoints": 101.2, "rosterForCurrentScoringPeriod": {"entries": []}},
      "away": {"teamId": 1, "totalPoints": 120.5, "rosterForCurrentScoringPeriod": {"entries": [
        {"playerId": 3139477, "lineupSlotId": 0, "playerPoolEntry": {"player": {"fullName": "Patrick Mahomes", "defaultPositionId": 1, "proTeamId": 12}}}
      ]}}},
    {"matchupPeriodId": 3, "home": {"teamId": 3, "totalPoints": 0}}
  ]
}
```

- [ ] **Step 2: Write the failing test**

`internal/providers/espn/espn_test.go`:

```go
package espn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vsnandy/sports-api-go/internal/domain"
	"github.com/vsnandy/sports-api-go/internal/providers/httpx"
)

type recorder struct {
	mu      sync.Mutex
	queries []url.Values
}

func (r *recorder) last() url.Values {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.queries[len(r.queries)-1]
}

func newTestClient(t *testing.T, s2 string, leagueIDs ...string) (*Client, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.queries = append(rec.queries, r.URL.Query())
		rec.mu.Unlock()
		if r.Header.Get("Cookie") != "espn_s2=S2; SWID={SWID}" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/seasons/2026/segments/0/leagues/123456":
			http.ServeFile(w, r, "testdata/league.json")
		case "/seasons/2026/segments/0/leagues/401":
			w.WriteHeader(http.StatusUnauthorized)
		case "/seasons/2026/segments/0/leagues/777":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<html>please log in</html>")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return New(httpx.New("espn", 2*time.Second), srv.URL, s2, "{SWID}", leagueIDs), rec
}

func entryIDs(es []domain.RosterEntryRef) []string {
	out := []string{}
	for _, e := range es {
		out = append(out, e.Slot+"="+e.Ref.ID)
	}
	return out
}

func TestLeague(t *testing.T) {
	c, rec := newTestClient(t, "S2", "123456")
	l, err := c.League(context.Background(), "123456", 2026)
	if err != nil {
		t.Fatal(err)
	}
	if l.ID != "espn:123456" || l.Name != "Office League" || l.Season != 2026 || l.Platform != domain.PlatformESPN {
		t.Fatalf("header = %+v", l)
	}
	wantTeams := []domain.Team{{ID: "1", Name: "Team Varun", Owner: "varun"}, {ID: "2", Name: "Alex's Aces", Owner: "alex"}}
	if !reflect.DeepEqual(l.Teams, wantTeams) {
		t.Errorf("Teams = %+v", l.Teams)
	}
	wantSlots := []string{"QB", "RB", "RB", "WR", "WR", "TE", "FLEX", "DEF", "K", "BN", "BN", "BN", "BN", "BN", "BN", "IR"}
	if !reflect.DeepEqual(l.RosterSlots, wantSlots) {
		t.Errorf("RosterSlots = %v", l.RosterSlots)
	}
	wantRules := domain.ScoringRules{"pass_yd": 0.04, "pass_td": 4, "rec": 1, "fgm_0_19": 3, "fgm_20_29": 3, "fgm_30_39": 3}
	if !reflect.DeepEqual(l.Scoring, wantRules) {
		t.Errorf("Scoring = %v", l.Scoring)
	}
	wantUnsupported := []string{"espn stat 53 position overrides", "espn stat 92 (1 pts)"}
	if !reflect.DeepEqual(l.UnsupportedRules, wantUnsupported) {
		t.Errorf("UnsupportedRules = %v", l.UnsupportedRules)
	}
	if v := rec.last()["view"]; !slices.Contains(v, "mSettings") || !slices.Contains(v, "mTeam") {
		t.Errorf("views = %v", v)
	}
}

func TestRosters(t *testing.T) {
	c, _ := newTestClient(t, "S2")
	rs, err := c.Rosters(context.Background(), "123456", 2026, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 {
		t.Fatalf("got %d rosters", len(rs))
	}
	r := rs[0]
	if got, want := entryIDs(r.Starters), []string{"QB=3139477", "DEF=-16012"}; !reflect.DeepEqual(got, want) {
		t.Errorf("starters = %v, want %v (sorted by slot)", got, want)
	}
	if got, want := entryIDs(r.Bench), []string{"BN=4262921"}; !reflect.DeepEqual(got, want) {
		t.Errorf("bench = %v", got)
	}
	if got, want := entryIDs(r.Reserve), []string{"IR=9999999"}; !reflect.DeepEqual(got, want) {
		t.Errorf("reserve = %v", got)
	}
	dst := r.Starters[1].Ref
	if dst.Platform != domain.PlatformESPN || dst.Position != "DEF" || dst.NFLTeam != "KC" || dst.Name != "Chiefs D/ST" {
		t.Errorf("D/ST ref = %+v", dst)
	}
	if fa := r.Reserve[0].Ref; fa.NFLTeam != "" || fa.Position != "RB" {
		t.Errorf("free agent ref = %+v", fa)
	}
	if rs[1].Starters == nil || rs[1].Bench == nil || rs[1].Reserve == nil {
		t.Error("empty roster slices should be non-nil")
	}
}

func TestMatchups(t *testing.T) {
	c, rec := newTestClient(t, "S2")
	ms, err := c.Matchups(context.Background(), "123456", 2026, 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 {
		t.Fatalf("got %d matchups, want 1 (other weeks and byes skipped)", len(ms))
	}
	m := ms[0]
	if m.Week != 3 || m.Home.TeamID != "2" || m.Home.Points != 101.2 || m.Away.TeamID != "1" || m.Away.Points != 120.5 {
		t.Fatalf("matchup = %+v", m)
	}
	if got := entryIDs(m.Away.Roster.Starters); !reflect.DeepEqual(got, []string{"QB=3139477"}) {
		t.Errorf("away starters = %v", got)
	}
	q := rec.last()
	if q.Get("scoringPeriodId") != "3" || !slices.Contains(q["view"], "mMatchupScore") {
		t.Errorf("query = %v", q)
	}
}

func TestListLeaguesSkipsMissingSeasons(t *testing.T) {
	c, _ := newTestClient(t, "S2", "123456", "555")
	ls, err := c.ListLeagues(context.Background(), 2026)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.LeagueSummary{{ID: "espn:123456", Platform: domain.PlatformESPN, Season: 2026, Name: "Office League"}}
	if !reflect.DeepEqual(ls, want) {
		t.Fatalf("ListLeagues = %+v", ls)
	}
}

func TestAuthFailures(t *testing.T) {
	ctx := context.Background()
	good, _ := newTestClient(t, "S2")
	for _, id := range []string{"401", "777"} {
		if _, err := good.League(ctx, id, 2026); !errors.Is(err, domain.ErrESPNAuth) {
			t.Errorf("league %s: err = %v, want ErrESPNAuth", id, err)
		}
	}
	bad, _ := newTestClient(t, "WRONG", "123456")
	if _, err := bad.ListLeagues(ctx, 2026); !errors.Is(err, domain.ErrESPNAuth) {
		t.Errorf("wrong cookie: err = %v, want ErrESPNAuth", err)
	}
}

func TestSecretsNotInErrors(t *testing.T) {
	bad, _ := newTestClient(t, "SUPERSECRET", "123456")
	_, err := bad.ListLeagues(context.Background(), 2026)
	if err == nil || strings.Contains(err.Error(), "SUPERSECRET") {
		t.Fatalf("err = %v; must exist and must not include the cookie", err)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go get golang.org/x/sync/errgroup && go test ./internal/providers/espn/`
Expected: FAIL — `undefined: New`.

- [ ] **Step 4: Write the implementation**

`internal/providers/espn/tables.go`:

```go
package espn

import (
	"fmt"
	"slices"
)

// Lineup slot IDs → Sleeper-style slot names.
var slotNames = map[int]string{
	0: "QB", 2: "RB", 3: "WRRB_FLEX", 4: "WR", 5: "REC_FLEX", 6: "TE", 7: "SUPER_FLEX",
	16: "DEF", 17: "K", 20: "BN", 21: "IR", 23: "FLEX",
}

// slotOrder is the display order for slots; unknown slots sort after these.
var slotOrder = []int{0, 2, 3, 4, 5, 6, 23, 7, 16, 17, 20, 21}

func slotName(id int) string {
	if n, ok := slotNames[id]; ok {
		return n
	}
	return fmt.Sprintf("SLOT_%d", id)
}

func slotRank(id int) int {
	if i := slices.Index(slotOrder, id); i >= 0 {
		return i
	}
	return 100 + id
}

var positions = map[int]string{1: "QB", 2: "RB", 3: "WR", 4: "TE", 5: "K", 16: "DEF"}

// proTeams maps ESPN pro team IDs to Sleeper team abbreviations. 0 (free agent) is absent.
var proTeams = map[int]string{
	1: "ATL", 2: "BUF", 3: "CHI", 4: "CIN", 5: "CLE", 6: "DAL", 7: "DEN", 8: "DET",
	9: "GB", 10: "TEN", 11: "IND", 12: "KC", 13: "LV", 14: "LAR", 15: "MIA", 16: "MIN",
	17: "NE", 18: "NO", 19: "NYG", 20: "NYJ", 21: "PHI", 22: "ARI", 23: "PIT", 24: "LAC",
	25: "SF", 26: "SEA", 27: "TB", 28: "WAS", 29: "CAR", 30: "JAX", 33: "BAL", 34: "HOU",
}

// statKeys maps ESPN scoring stat IDs to Sleeper stat keys, following the community
// mapping in cwendt94/espn-api (PLAYER_STATS_MAP). IDs not listed here surface as
// unsupported rules rather than guesses. ESPN's "FG under 40" (80) covers three Sleeper keys.
var statKeys = map[int][]string{
	3: {"pass_yd"}, 4: {"pass_td"}, 19: {"pass_2pt"}, 20: {"pass_int"},
	24: {"rush_yd"}, 25: {"rush_td"}, 26: {"rush_2pt"},
	42: {"rec_yd"}, 43: {"rec_td"}, 44: {"rec_2pt"}, 53: {"rec"},
	72: {"fum_lost"},
	74: {"fgm_50p"}, 77: {"fgm_40_49"}, 80: {"fgm_0_19", "fgm_20_29", "fgm_30_39"},
	85: {"fgmiss"}, 86: {"xpm"}, 88: {"xpmiss"},
	89: {"pts_allow_0"}, 90: {"pts_allow_1_6"}, 91: {"pts_allow_7_13"},
	95: {"int"}, 96: {"fum_rec"}, 97: {"blk_kick"}, 98: {"safe"}, 99: {"sack"},
}
```

`internal/providers/espn/client.go`:

```go
// Package espn adapts the ESPN fantasy football v3 API to domain types. Private
// leagues require the espn_s2 and SWID cookies.
package espn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"golang.org/x/sync/errgroup"

	"github.com/vsnandy/sports-api-go/internal/domain"
	"github.com/vsnandy/sports-api-go/internal/providers/httpx"
)

const DefaultBase = "https://lm-api-reads.fantasy.espn.com/apis/v3/games/ffl"

type Client struct {
	http      *httpx.Client
	base      string
	cookie    string
	leagueIDs []string
}

func New(hc *httpx.Client, base, espnS2, swid string, leagueIDs []string) *Client {
	return &Client{http: hc, base: base, cookie: fmt.Sprintf("espn_s2=%s; SWID=%s", espnS2, swid), leagueIDs: leagueIDs}
}

func (c *Client) Platform() domain.Platform { return domain.PlatformESPN }

func (c *Client) fetch(ctx context.Context, nativeID string, season, week int, views ...string) (*leagueJSON, error) {
	q := url.Values{"view": views}
	if week > 0 {
		q.Set("scoringPeriodId", strconv.Itoa(week))
	}
	u := fmt.Sprintf("%s/seasons/%d/segments/0/leagues/%s?%s", c.base, season, nativeID, q.Encode())
	var l leagueJSON
	if err := c.http.GetJSON(ctx, u, http.Header{"Cookie": {c.cookie}}, &l); err != nil {
		return nil, classifyAuth(err)
	}
	return &l, nil
}

// classifyAuth turns ESPN's ways of rejecting cookies (401/403, or a 2xx HTML login
// page) into ErrESPNAuth.
func classifyAuth(err error) error {
	var ue *domain.UpstreamError
	if errors.As(err, &ue) && (ue.Status == http.StatusUnauthorized || ue.Status == http.StatusForbidden || ue.BadBody) {
		return fmt.Errorf("%w (%v)", domain.ErrESPNAuth, err)
	}
	return err
}

// ListLeagues returns the configured leagues that exist for season.
func (c *Client) ListLeagues(ctx context.Context, season int) ([]domain.LeagueSummary, error) {
	found := make([]*domain.LeagueSummary, len(c.leagueIDs))
	g, gctx := errgroup.WithContext(ctx)
	for i, id := range c.leagueIDs {
		g.Go(func() error {
			l, err := c.fetch(gctx, id, season, 0, "mSettings")
			if errors.Is(err, domain.ErrNotFound) {
				return nil // league did not exist that season
			}
			if err != nil {
				return err
			}
			found[i] = &domain.LeagueSummary{
				ID: domain.LeagueID(domain.PlatformESPN, id), Platform: domain.PlatformESPN,
				Season: season, Name: l.Settings.Name,
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	out := make([]domain.LeagueSummary, 0, len(found))
	for _, s := range found {
		if s != nil {
			out = append(out, *s)
		}
	}
	return out, nil
}
```

`internal/providers/espn/league.go`:

```go
package espn

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

type leagueJSON struct {
	Settings struct {
		Name           string `json:"name"`
		RosterSettings struct {
			LineupSlotCounts map[string]int `json:"lineupSlotCounts"`
		} `json:"rosterSettings"`
		ScoringSettings struct {
			ScoringItems []scoringItemJSON `json:"scoringItems"`
		} `json:"scoringSettings"`
	} `json:"settings"`
	Members []struct {
		ID          string `json:"id"`
		DisplayName string `json:"displayName"`
	} `json:"members"`
	Teams    []teamJSON     `json:"teams"`
	Schedule []scheduleJSON `json:"schedule"`
}

type scoringItemJSON struct {
	StatID          int                `json:"statId"`
	Points          float64            `json:"points"`
	PointsOverrides map[string]float64 `json:"pointsOverrides"`
}

type teamJSON struct {
	ID       int         `json:"id"`
	Name     string      `json:"name"`
	Location string      `json:"location"`
	Nickname string      `json:"nickname"`
	Owners   []string    `json:"owners"`
	Roster   *rosterJSON `json:"roster"`
}

// displayName prefers the modern "name" field, falling back to location + nickname.
func (t teamJSON) displayName() string {
	if t.Name != "" {
		return t.Name
	}
	return strings.TrimSpace(t.Location + " " + t.Nickname)
}

type rosterJSON struct {
	Entries []entryJSON `json:"entries"`
}

type entryJSON struct {
	PlayerID        int `json:"playerId"`
	LineupSlotID    int `json:"lineupSlotId"`
	PlayerPoolEntry struct {
		Player struct {
			FullName          string `json:"fullName"`
			DefaultPositionID int    `json:"defaultPositionId"`
			ProTeamID         int    `json:"proTeamId"`
		} `json:"player"`
	} `json:"playerPoolEntry"`
}

type scheduleJSON struct {
	MatchupPeriodID int       `json:"matchupPeriodId"`
	Home            *sideJSON `json:"home"`
	Away            *sideJSON `json:"away"`
}

type sideJSON struct {
	TeamID                        int         `json:"teamId"`
	TotalPoints                   float64     `json:"totalPoints"`
	RosterForCurrentScoringPeriod *rosterJSON `json:"rosterForCurrentScoringPeriod"`
}

func (c *Client) League(ctx context.Context, nativeID string, season int) (domain.League, error) {
	l, err := c.fetch(ctx, nativeID, season, 0, "mSettings", "mTeam")
	if err != nil {
		return domain.League{}, err
	}
	names := map[string]string{}
	for _, m := range l.Members {
		names[m.ID] = m.DisplayName
	}
	teams := make([]domain.Team, 0, len(l.Teams))
	for _, t := range l.Teams {
		owner := ""
		if len(t.Owners) > 0 {
			owner = names[t.Owners[0]]
		}
		teams = append(teams, domain.Team{ID: strconv.Itoa(t.ID), Name: t.displayName(), Owner: owner})
	}
	rules, unsupported := convertScoring(l.Settings.ScoringSettings.ScoringItems)
	return domain.League{
		ID: domain.LeagueID(domain.PlatformESPN, nativeID), Platform: domain.PlatformESPN,
		Sport: domain.SportNFL, Season: season, Name: l.Settings.Name, Teams: teams,
		Scoring: rules, UnsupportedRules: unsupported,
		RosterSlots: rosterSlots(l.Settings.RosterSettings.LineupSlotCounts),
	}, nil
}

func (c *Client) Rosters(ctx context.Context, nativeID string, season int, _ []string) ([]domain.RosterRef, error) {
	l, err := c.fetch(ctx, nativeID, season, 0, "mRoster")
	if err != nil {
		return nil, err
	}
	out := make([]domain.RosterRef, 0, len(l.Teams))
	for _, t := range l.Teams {
		var entries []entryJSON
		if t.Roster != nil {
			entries = t.Roster.Entries
		}
		out = append(out, toRoster(strconv.Itoa(t.ID), entries))
	}
	return out, nil
}

// Matchups returns the matchups whose matchup period equals week. This assumes one
// scoring period per matchup period (true for regular-season weeks).
func (c *Client) Matchups(ctx context.Context, nativeID string, season, week int, _ []string) ([]domain.MatchupRef, error) {
	l, err := c.fetch(ctx, nativeID, season, week, "mMatchupScore", "mBoxscore")
	if err != nil {
		return nil, err
	}
	out := []domain.MatchupRef{}
	for _, m := range l.Schedule {
		if m.MatchupPeriodID != week || m.Home == nil || m.Away == nil {
			continue
		}
		out = append(out, domain.MatchupRef{Week: week, Home: side(*m.Home), Away: side(*m.Away)})
	}
	return out, nil
}

func side(s sideJSON) domain.MatchupSideRef {
	var entries []entryJSON
	if s.RosterForCurrentScoringPeriod != nil {
		entries = s.RosterForCurrentScoringPeriod.Entries
	}
	id := strconv.Itoa(s.TeamID)
	return domain.MatchupSideRef{TeamID: id, Points: s.TotalPoints, Roster: toRoster(id, entries)}
}

func toRoster(teamID string, entries []entryJSON) domain.RosterRef {
	sorted := slices.Clone(entries)
	slices.SortStableFunc(sorted, func(a, b entryJSON) int { return cmp.Compare(slotRank(a.LineupSlotID), slotRank(b.LineupSlotID)) })
	r := domain.RosterRef{TeamID: teamID, Starters: []domain.RosterEntryRef{}, Bench: []domain.RosterEntryRef{}, Reserve: []domain.RosterEntryRef{}}
	for _, e := range sorted {
		p := e.PlayerPoolEntry.Player
		ref := domain.RosterEntryRef{Slot: slotName(e.LineupSlotID), Ref: domain.PlayerRef{
			Platform: domain.PlatformESPN, ID: strconv.Itoa(e.PlayerID), Name: p.FullName,
			Position: positions[p.DefaultPositionID], NFLTeam: proTeams[p.ProTeamID],
		}}
		switch ref.Slot {
		case "BN":
			r.Bench = append(r.Bench, ref)
		case "IR":
			r.Reserve = append(r.Reserve, ref)
		default:
			r.Starters = append(r.Starters, ref)
		}
	}
	return r
}

func rosterSlots(counts map[string]int) []string {
	byID := map[int]int{}
	ids := []int{}
	for k, n := range counts {
		id, err := strconv.Atoi(k)
		if err != nil || n <= 0 {
			continue
		}
		byID[id] = n
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b int) int { return cmp.Compare(slotRank(a), slotRank(b)) })
	out := []string{}
	for _, id := range ids {
		for range byID[id] {
			out = append(out, slotName(id))
		}
	}
	return out
}

// convertScoring translates ESPN scoring items into Sleeper-keyed rules. Items with
// no mapping, and per-position overrides (which flat rules cannot express), are
// reported as unsupported.
func convertScoring(items []scoringItemJSON) (domain.ScoringRules, []string) {
	rules := domain.ScoringRules{}
	unsupported := []string{}
	for _, it := range items {
		keys, ok := statKeys[it.StatID]
		if !ok {
			unsupported = append(unsupported, fmt.Sprintf("espn stat %d (%g pts)", it.StatID, it.Points))
			continue
		}
		for _, k := range keys {
			rules[k] = it.Points
		}
		if len(it.PointsOverrides) > 0 {
			unsupported = append(unsupported, fmt.Sprintf("espn stat %d position overrides", it.StatID))
		}
	}
	sort.Strings(unsupported)
	return rules, unsupported
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/providers/espn/ && go vet ./...`
Expected: `ok`

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/providers/espn
git commit -m "feat: add ESPN provider with scoring conversion and auth detection"
```

---

### Task 7: Players index with S3 cache

**Files:**
- Create: `internal/players/index.go`, `internal/players/s3store.go`
- Test: `internal/players/index_test.go`, `internal/players/s3store_test.go`

**Interfaces:**
- Consumes: `domain.Player`, `domain.PlayerRef`, `domain.ErrNotFound`; `(*sleeper.Client).Players` satisfies `Source`
- Produces (package `players`):
  - `type Source interface { Players(ctx context.Context) ([]domain.Player, error) }`
  - `type Store interface { Get(ctx context.Context, key string) ([]byte, time.Time, error); Put(ctx context.Context, key string, data []byte) error }` — `Get` returns `domain.ErrNotFound` when absent
  - `type NoStore struct{}` (always misses; for local dev without a bucket)
  - `func New(src Source, store Store, now func() time.Time) *Index`
  - `(*Index).Get(ctx, id string) (domain.Player, bool, error)`
  - `(*Index).Resolve(ctx, ref domain.PlayerRef) (domain.Player, bool, error)`
  - `type S3API interface { GetObject(...); PutObject(...) }` (aws-sdk-go-v2 signatures), `type S3Store struct { Client S3API; Bucket string }`

- [ ] **Step 1: Write the failing tests**

`internal/players/index_test.go`:

```go
package players

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

func p(id, name, pos, team, espn string) domain.Player {
	ids := map[string]string{"sleeper": id}
	if espn != "" {
		ids["espn"] = espn
	}
	return domain.Player{ID: &id, Name: name, Position: pos, NFLTeam: team, PlatformIDs: ids}
}

var sample = []domain.Player{
	p("4046", "Patrick Mahomes", "QB", "KC", "3139477"),
	p("6794", "Justin Jefferson", "WR", "MIN", "4262921"),
	p("KC", "Kansas City Chiefs", "DEF", "KC", ""),
}

type fakeSource struct {
	calls atomic.Int32
	mu    sync.Mutex
	err   error
}

func (f *fakeSource) Players(context.Context) ([]domain.Player, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return sample, nil
}

type fakeStore struct {
	now    func() time.Time
	data   []byte
	mod    time.Time
	getErr error
	puts   int
	key    string
}

func (s *fakeStore) Get(_ context.Context, key string) ([]byte, time.Time, error) {
	s.key = key
	if s.getErr != nil {
		return nil, time.Time{}, s.getErr
	}
	if s.data == nil {
		return nil, time.Time{}, domain.ErrNotFound
	}
	return s.data, s.mod, nil
}

func (s *fakeStore) Put(_ context.Context, key string, data []byte) error {
	s.key, s.data, s.mod = key, data, s.now()
	s.puts++
	return nil
}

type harness struct {
	now   time.Time
	src   *fakeSource
	store *fakeStore
	ix    *Index
}

func newHarness() *harness {
	h := &harness{now: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC), src: &fakeSource{}}
	clock := func() time.Time { return h.now }
	h.store = &fakeStore{now: clock}
	h.ix = New(h.src, h.store, clock)
	return h
}

func (h *harness) seedStore(t *testing.T, age time.Duration) {
	t.Helper()
	data, err := json.Marshal(toSlim(sample))
	if err != nil {
		t.Fatal(err)
	}
	h.store.data, h.store.mod = data, h.now.Add(-age)
}

func mustGet(t *testing.T, ix *Index, id string) domain.Player {
	t.Helper()
	pl, ok, err := ix.Get(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("Get(%s) = %v, %v", id, ok, err)
	}
	return pl
}

func TestLoadsFromSourceWhenStoreEmpty(t *testing.T) {
	h := newHarness()
	if got := mustGet(t, h.ix, "4046"); got.Name != "Patrick Mahomes" {
		t.Fatalf("got %+v", got)
	}
	if h.src.calls.Load() != 1 || h.store.puts != 1 || h.store.key != "players/nfl.json" {
		t.Fatalf("calls=%d puts=%d key=%q", h.src.calls.Load(), h.store.puts, h.store.key)
	}
	if !strings.Contains(string(h.store.data), "3139477") {
		t.Fatal("stored slim dump should include espn ids")
	}
}

func TestUsesFreshStore(t *testing.T) {
	h := newHarness()
	h.seedStore(t, time.Hour)
	mustGet(t, h.ix, "6794")
	if h.src.calls.Load() != 0 {
		t.Fatal("fresh S3 copy should avoid a Sleeper fetch")
	}
}

func TestStaleStoreRefetches(t *testing.T) {
	h := newHarness()
	h.seedStore(t, 25*time.Hour)
	mustGet(t, h.ix, "6794")
	if h.src.calls.Load() != 1 {
		t.Fatal("stale S3 copy should trigger a Sleeper fetch")
	}
}

func TestStoreReadErrorFallsBack(t *testing.T) {
	h := newHarness()
	h.store.getErr = errors.New("AccessDenied")
	mustGet(t, h.ix, "4046")
	if h.src.calls.Load() != 1 {
		t.Fatal("store read errors should fall back to Sleeper")
	}
}

func TestCorruptStoreFallsBack(t *testing.T) {
	h := newHarness()
	h.store.data, h.store.mod = []byte("not json"), h.now
	mustGet(t, h.ix, "4046")
	if h.src.calls.Load() != 1 {
		t.Fatal("corrupt store data should fall back to Sleeper")
	}
}

func TestMemoryReuseAndDailyRefresh(t *testing.T) {
	h := newHarness()
	mustGet(t, h.ix, "4046")
	mustGet(t, h.ix, "6794")
	if h.src.calls.Load() != 1 {
		t.Fatal("second Get within 24h should use memory")
	}
	h.now = h.now.Add(25 * time.Hour)
	mustGet(t, h.ix, "4046")
	if h.src.calls.Load() != 2 {
		t.Fatalf("calls = %d, want refresh after 24h", h.src.calls.Load())
	}
}

func TestRefreshFailureServesStale(t *testing.T) {
	h := newHarness()
	mustGet(t, h.ix, "4046")
	h.now = h.now.Add(25 * time.Hour)
	h.src.err = errors.New("sleeper down")
	mustGet(t, h.ix, "4046")
}

func TestInitialFailureThenRetry(t *testing.T) {
	h := newHarness()
	h.src.err = errors.New("sleeper down")
	if _, _, err := h.ix.Get(context.Background(), "4046"); err == nil {
		t.Fatal("first load failure with nothing cached should error")
	}
	h.src.mu.Lock()
	h.src.err = nil
	h.src.mu.Unlock()
	mustGet(t, h.ix, "4046")
}

func TestConcurrentGetsLoadOnce(t *testing.T) {
	h := newHarness()
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); h.ix.Get(context.Background(), "4046") }()
	}
	wg.Wait()
	if h.src.calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", h.src.calls.Load())
	}
}

func TestResolve(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	tests := []struct {
		name   string
		ref    domain.PlayerRef
		wantID string
		ok     bool
	}{
		{"sleeper id", domain.PlayerRef{Platform: domain.PlatformSleeper, ID: "4046"}, "4046", true},
		{"espn id", domain.PlayerRef{Platform: domain.PlatformESPN, ID: "4262921"}, "6794", true},
		{"espn D/ST by team", domain.PlayerRef{Platform: domain.PlatformESPN, ID: "-16012", Position: "DEF", NFLTeam: "KC"}, "KC", true},
		{"unmapped espn", domain.PlayerRef{Platform: domain.PlatformESPN, ID: "9999999"}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := h.ix.Resolve(ctx, tt.ref)
			if err != nil || ok != tt.ok || (ok && *got.ID != tt.wantID) {
				t.Fatalf("Resolve = %+v, %v, %v", got, ok, err)
			}
		})
	}
	dst, _, _ := h.ix.Resolve(ctx, domain.PlayerRef{Platform: domain.PlatformESPN, ID: "-16012", Position: "DEF", NFLTeam: "KC"})
	if dst.PlatformIDs["espn"] != "-16012" {
		t.Errorf("resolved D/ST should carry its espn id: %v", dst.PlatformIDs)
	}
	if kc := mustGet(t, h.ix, "KC"); kc.PlatformIDs["espn"] != "" {
		t.Errorf("Resolve must not mutate the index entry: %v", kc.PlatformIDs)
	}
}
```

`internal/players/s3store_test.go`:

```go
package players

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

type fakeS3 struct {
	getOut *s3.GetObjectOutput
	getErr error
	putIn  *s3.PutObjectInput
}

func (f *fakeS3) GetObject(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return f.getOut, f.getErr
}

func (f *fakeS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.putIn = in
	return &s3.PutObjectOutput{}, nil
}

func TestS3StoreMissingKey(t *testing.T) {
	s := S3Store{Client: &fakeS3{getErr: &types.NoSuchKey{}}, Bucket: "b"}
	if _, _, err := s.Get(context.Background(), "players/nfl.json"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestS3StoreRoundTrip(t *testing.T) {
	mod := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	f := &fakeS3{getOut: &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader([]byte("[]"))), LastModified: aws.Time(mod)}}
	s := S3Store{Client: f, Bucket: "b"}
	data, got, err := s.Get(context.Background(), "k")
	if err != nil || string(data) != "[]" || !got.Equal(mod) {
		t.Fatalf("Get = %q, %v, %v", data, got, err)
	}
	if err := s.Put(context.Background(), "k", []byte("[1]")); err != nil {
		t.Fatal(err)
	}
	if aws.ToString(f.putIn.Bucket) != "b" || aws.ToString(f.putIn.Key) != "k" || aws.ToString(f.putIn.ContentType) != "application/json" {
		t.Fatalf("PutObject input = %+v", f.putIn)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go get github.com/aws/aws-sdk-go-v2/aws github.com/aws/aws-sdk-go-v2/service/s3 && go test ./internal/players/`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Write the implementation**

`internal/players/index.go`:

```go
// Package players maintains the Sleeper player index used to normalize player IDs.
// The dump is loaded lazily and refreshed daily: memory, then S3, then Sleeper.
package players

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"sync"
	"time"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

const (
	storeKey = "players/nfl.json"
	maxAge   = 24 * time.Hour
)

type Source interface {
	Players(ctx context.Context) ([]domain.Player, error)
}

// Store persists the slimmed dump. Get returns domain.ErrNotFound when the key is absent.
type Store interface {
	Get(ctx context.Context, key string) ([]byte, time.Time, error)
	Put(ctx context.Context, key string, data []byte) error
}

// NoStore is a Store that never holds anything, for local runs without a bucket.
type NoStore struct{}

func (NoStore) Get(context.Context, string) ([]byte, time.Time, error) {
	return nil, time.Time{}, domain.ErrNotFound
}
func (NoStore) Put(context.Context, string, []byte) error { return nil }

type slimPlayer struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Pos    string `json:"pos"`
	Team   string `json:"team"`
	ESPNID string `json:"espn_id,omitempty"`
}

type snapshot struct {
	loadedAt time.Time
	byID     map[string]domain.Player
	byESPN   map[string]string // espn id -> sleeper id
}

type Index struct {
	src   Source
	store Store
	now   func() time.Time

	mu   sync.Mutex
	snap *snapshot
}

func New(src Source, store Store, now func() time.Time) *Index {
	return &Index{src: src, store: store, now: now}
}

func (ix *Index) Get(ctx context.Context, id string) (domain.Player, bool, error) {
	s, err := ix.snapshot(ctx)
	if err != nil {
		return domain.Player{}, false, err
	}
	p, ok := s.byID[id]
	return p, ok, nil
}

// Resolve maps a provider's player reference to a Sleeper player. ESPN team
// defenses are matched by NFL team, since Sleeper keys defenses by team abbreviation.
func (ix *Index) Resolve(ctx context.Context, ref domain.PlayerRef) (domain.Player, bool, error) {
	s, err := ix.snapshot(ctx)
	if err != nil {
		return domain.Player{}, false, err
	}
	switch ref.Platform {
	case domain.PlatformSleeper:
		p, ok := s.byID[ref.ID]
		return p, ok, nil
	case domain.PlatformESPN:
		if ref.Position == "DEF" {
			p, ok := s.byID[ref.NFLTeam]
			if !ok {
				return domain.Player{}, false, nil
			}
			p.PlatformIDs = maps.Clone(p.PlatformIDs)
			p.PlatformIDs["espn"] = ref.ID
			return p, true, nil
		}
		id, ok := s.byESPN[ref.ID]
		if !ok {
			return domain.Player{}, false, nil
		}
		return s.byID[id], true, nil
	}
	return domain.Player{}, false, nil
}

// snapshot returns the current index, loading or refreshing it when older than
// maxAge. A failed refresh keeps serving the previous snapshot.
func (ix *Index) snapshot(ctx context.Context) (*snapshot, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.snap != nil && ix.now().Sub(ix.snap.loadedAt) < maxAge {
		return ix.snap, nil
	}
	s, err := ix.load(ctx)
	if err != nil {
		if ix.snap != nil {
			slog.WarnContext(ctx, "players refresh failed; serving stale index", "err", err)
			return ix.snap, nil
		}
		return nil, err
	}
	ix.snap = s
	return s, nil
}

func (ix *Index) load(ctx context.Context) (*snapshot, error) {
	data, mod, err := ix.store.Get(ctx, storeKey)
	switch {
	case err == nil && ix.now().Sub(mod) < maxAge:
		var slim []slimPlayer
		if err := json.Unmarshal(data, &slim); err == nil {
			return build(fromSlim(slim), mod), nil
		}
		slog.WarnContext(ctx, "players cache is corrupt; refetching")
	case err != nil && !errors.Is(err, domain.ErrNotFound):
		slog.WarnContext(ctx, "players cache read failed; refetching", "err", err)
	}

	ps, err := ix.src.Players(ctx)
	if err != nil {
		return nil, err
	}
	if data, err := json.Marshal(toSlim(ps)); err == nil {
		if err := ix.store.Put(ctx, storeKey, data); err != nil {
			slog.WarnContext(ctx, "players cache write failed", "err", err)
		}
	}
	return build(ps, ix.now()), nil
}

func build(ps []domain.Player, loadedAt time.Time) *snapshot {
	s := &snapshot{loadedAt: loadedAt, byID: make(map[string]domain.Player, len(ps)), byESPN: map[string]string{}}
	for _, p := range ps {
		if p.ID == nil {
			continue
		}
		s.byID[*p.ID] = p
		if e := p.PlatformIDs["espn"]; e != "" {
			s.byESPN[e] = *p.ID
		}
	}
	return s
}

func toSlim(ps []domain.Player) []slimPlayer {
	out := make([]slimPlayer, 0, len(ps))
	for _, p := range ps {
		if p.ID != nil {
			out = append(out, slimPlayer{ID: *p.ID, Name: p.Name, Pos: p.Position, Team: p.NFLTeam, ESPNID: p.PlatformIDs["espn"]})
		}
	}
	return out
}

func fromSlim(slim []slimPlayer) []domain.Player {
	out := make([]domain.Player, 0, len(slim))
	for _, s := range slim {
		ids := map[string]string{"sleeper": s.ID}
		if s.ESPNID != "" {
			ids["espn"] = s.ESPNID
		}
		out = append(out, domain.Player{ID: &s.ID, Name: s.Name, Position: s.Pos, NFLTeam: s.Team, PlatformIDs: ids})
	}
	return out
}
```

`internal/players/s3store.go`:

```go
package players

import (
	"bytes"
	"context"
	"errors"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

// S3API is the subset of *s3.Client that S3Store uses.
type S3API interface {
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

type S3Store struct {
	Client S3API
	Bucket string
}

func (s S3Store) Get(ctx context.Context, key string) ([]byte, time.Time, error) {
	out, err := s.Client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(key)})
	if err != nil {
		var nsk *types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil, time.Time{}, domain.ErrNotFound
		}
		return nil, time.Time{}, err
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	return data, aws.ToTime(out.LastModified), err
}

func (s S3Store) Put(ctx context.Context, key string, data []byte) error {
	_, err := s.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.Bucket), Key: aws.String(key),
		Body: bytes.NewReader(data), ContentType: aws.String("application/json"),
	})
	return err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/players/ && go vet ./...`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/players
git commit -m "feat: add players index with memory, S3, and Sleeper tiers"
```

---

### Task 8: Service layer

**Files:**
- Create: `internal/service/service.go`, `internal/service/leagues.go`, `internal/service/players.go`
- Test: `internal/service/fakes_test.go`, `internal/service/service_test.go`

**Interfaces:**
- Consumes: `cache.New/GetOrLoad`, `scoring.Points/Preset`, domain types; `*sleeper.Client` satisfies `LeagueProvider`, `StatsProvider`, `StateProvider`; `*espn.Client` satisfies `LeagueProvider`; `*players.Index` satisfies `PlayerIndex`
- Produces (package `service`):
  - Interfaces `LeagueProvider`, `StatsProvider`, `StateProvider`, `PlayerIndex` (signatures below)
  - `func New(providers []LeagueProvider, stats StatsProvider, state StateProvider, players PlayerIndex, now func() time.Time) *Service`
  - `(*Service).ListLeagues(ctx, season int) ([]domain.LeagueSummary, domain.Meta, error)`
  - `(*Service).League(ctx, leagueID string, season int) (domain.League, domain.Meta, error)`
  - `(*Service).Rosters(ctx, leagueID string, season int) ([]domain.Roster, domain.Meta, error)`
  - `(*Service).Matchups(ctx, leagueID string, season, week int, withStats bool) ([]domain.Matchup, domain.Meta, error)`
  - `(*Service).Player(ctx, playerID string) (domain.Player, domain.Meta, error)`
  - `(*Service).Gamelog(ctx, playerID string, season int, scoring string) ([]domain.GamelogEntry, domain.Meta, error)`
  - `season`/`week` of 0 mean "default". Warning prefixes: `<platform>_leagues_unavailable:`, `unmapped_player:`, `stats_unavailable:`, `unsupported_rule:`.

- [ ] **Step 1: Write the fakes**

`internal/service/fakes_test.go`:

```go
package service

import (
	"context"
	"sync"
	"time"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

type fakeProvider struct {
	mu          sync.Mutex
	platform    domain.Platform
	leagues     []domain.LeagueSummary
	listErr     error
	league      domain.League
	rosters     []domain.RosterRef
	matchups    []domain.MatchupRef
	matchupsErr error
	calls       map[string]int
}

func (f *fakeProvider) count(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[name]++
}

func (f *fakeProvider) Calls(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[name]
}

func (f *fakeProvider) Platform() domain.Platform { return f.platform }

func (f *fakeProvider) ListLeagues(context.Context, int) ([]domain.LeagueSummary, error) {
	f.count("list")
	return f.leagues, f.listErr
}

func (f *fakeProvider) League(context.Context, string, int) (domain.League, error) {
	f.count("league")
	return f.league, nil
}

func (f *fakeProvider) Rosters(context.Context, string, int, []string) ([]domain.RosterRef, error) {
	f.count("rosters")
	return f.rosters, nil
}

func (f *fakeProvider) Matchups(context.Context, string, int, int, []string) ([]domain.MatchupRef, error) {
	f.count("matchups")
	return f.matchups, f.matchupsErr
}

type fakeStats struct {
	mu        sync.Mutex
	week      map[string]domain.StatLine
	weekErr   error
	gamelog   []domain.StatLine
	weekCalls int
}

func (f *fakeStats) WeekStats(context.Context, int, int) (map[string]domain.StatLine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.weekCalls++
	return f.week, f.weekErr
}

func (f *fakeStats) PlayerGamelog(context.Context, string, int) ([]domain.StatLine, error) {
	return f.gamelog, nil
}

type fakeState struct{ st domain.SeasonState }

func (f *fakeState) State(context.Context) (domain.SeasonState, error) { return f.st, nil }

type fakeIndex struct {
	byID   map[string]domain.Player
	byESPN map[string]string
}

func (f fakeIndex) Get(_ context.Context, id string) (domain.Player, bool, error) {
	p, ok := f.byID[id]
	return p, ok, nil
}

func (f fakeIndex) Resolve(ctx context.Context, ref domain.PlayerRef) (domain.Player, bool, error) {
	switch {
	case ref.Platform == domain.PlatformSleeper:
		return f.Get(ctx, ref.ID)
	case ref.Position == "DEF":
		return f.Get(ctx, ref.NFLTeam)
	}
	id, ok := f.byESPN[ref.ID]
	if !ok {
		return domain.Player{}, false, nil
	}
	return f.Get(ctx, id)
}

func player(id, name, pos, team string, ids map[string]string) domain.Player {
	return domain.Player{ID: &id, Name: name, Position: pos, NFLTeam: team, PlatformIDs: ids}
}

func sleeperRef(slot, id string) domain.RosterEntryRef {
	return domain.RosterEntryRef{Slot: slot, Ref: domain.PlayerRef{Platform: domain.PlatformSleeper, ID: id}}
}

func espnRef(slot, id, name, pos, team string) domain.RosterEntryRef {
	return domain.RosterEntryRef{Slot: slot, Ref: domain.PlayerRef{Platform: domain.PlatformESPN, ID: id, Name: name, Position: pos, NFLTeam: team}}
}

type fixture struct {
	svc     *Service
	sleeper *fakeProvider
	espn    *fakeProvider
	stats   *fakeStats
	state   *fakeState
	now     time.Time
}

func newFixture() *fixture {
	f := &fixture{now: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}
	f.sleeper = &fakeProvider{
		platform: domain.PlatformSleeper,
		leagues:  []domain.LeagueSummary{{ID: "sleeper:111", Platform: domain.PlatformSleeper, Season: 2026, Name: "Dynasty"}},
		league: domain.League{
			ID: "sleeper:111", Platform: domain.PlatformSleeper, Sport: domain.SportNFL, Season: 2026, Name: "Dynasty",
			Scoring:          domain.ScoringRules{"pass_yd": 0.04, "pass_td": 4, "rec": 1, "rec_yd": 0.1, "rec_td": 6},
			UnsupportedRules: []string{}, RosterSlots: []string{"QB", "WR", "BN"},
		},
		matchups: []domain.MatchupRef{{
			Week: 3,
			Home: domain.MatchupSideRef{TeamID: "1", Points: 112.34, Roster: domain.RosterRef{
				TeamID:   "1",
				Starters: []domain.RosterEntryRef{sleeperRef("QB", "4046")},
				Bench:    []domain.RosterEntryRef{sleeperRef("BN", "6794")},
			}},
			Away: domain.MatchupSideRef{TeamID: "2", Points: 98.5, Roster: domain.RosterRef{TeamID: "2"}},
		}},
	}
	f.espn = &fakeProvider{
		platform: domain.PlatformESPN,
		leagues:  []domain.LeagueSummary{{ID: "espn:123456", Platform: domain.PlatformESPN, Season: 2026, Name: "Office"}},
		league: domain.League{
			ID: "espn:123456", Platform: domain.PlatformESPN, Sport: domain.SportNFL, Season: 2026, Name: "Office",
			Scoring:          domain.ScoringRules{"rec": 0.5, "rec_yd": 0.1},
			UnsupportedRules: []string{"espn stat 92 (1 pts)"},
		},
		rosters: []domain.RosterRef{{
			TeamID: "1",
			Starters: []domain.RosterEntryRef{
				espnRef("QB", "3139477", "Patrick Mahomes", "QB", "KC"),
				espnRef("DEF", "-16012", "Chiefs D/ST", "DEF", "KC"),
			},
			Bench: []domain.RosterEntryRef{espnRef("BN", "9999999", "Rookie Guy", "RB", "")},
		}},
	}
	f.stats = &fakeStats{
		week: map[string]domain.StatLine{
			"4046": {PlayerID: "4046", Season: 2026, Week: 3, Opponent: "ATL", Stats: map[string]float64{"pass_yd": 250, "pass_td": 2}},
			"6794": {PlayerID: "6794", Season: 2026, Week: 3, Opponent: "GB", Stats: map[string]float64{"rec": 6, "rec_yd": 85, "rec_td": 1}},
		},
		gamelog: []domain.StatLine{
			{PlayerID: "6794", Season: 2026, Week: 1, Opponent: "NYG", Stats: map[string]float64{"rec": 5, "rec_yd": 59}},
			{PlayerID: "6794", Season: 2026, Week: 3, Opponent: "GB", Stats: map[string]float64{"rec": 6, "rec_yd": 85, "rec_td": 1}},
		},
	}
	f.state = &fakeState{st: domain.SeasonState{Season: 2026, Week: 3}}
	idx := fakeIndex{
		byID: map[string]domain.Player{
			"4046": player("4046", "Patrick Mahomes", "QB", "KC", map[string]string{"sleeper": "4046", "espn": "3139477"}),
			"6794": player("6794", "Justin Jefferson", "WR", "MIN", map[string]string{"sleeper": "6794", "espn": "4262921"}),
			"KC":   player("KC", "Kansas City Chiefs", "DEF", "KC", map[string]string{"sleeper": "KC"}),
		},
		byESPN: map[string]string{"3139477": "4046", "4262921": "6794"},
	}
	f.svc = New([]LeagueProvider{f.espn, f.sleeper}, f.stats, f.state, idx, func() time.Time { return f.now })
	return f
}
```

- [ ] **Step 2: Write the failing tests**

`internal/service/service_test.go`:

```go
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

var ctx = context.Background()

func wantInvalid(t *testing.T, err error, param string) {
	t.Helper()
	var ip *domain.InvalidParamError
	if !errors.As(err, &ip) || ip.Param != param {
		t.Fatalf("err = %v, want InvalidParamError for %q", err, param)
	}
}

func TestListLeaguesMergesPlatforms(t *testing.T) {
	f := newFixture()
	ls, meta, err := f.svc.ListLeagues(ctx, 0)
	if err != nil || len(ls) != 2 || ls[0].ID != "espn:123456" || ls[1].ID != "sleeper:111" {
		t.Fatalf("ListLeagues = %+v, %v", ls, err)
	}
	if meta.Season != 2026 || len(meta.Warnings) != 0 {
		t.Fatalf("meta = %+v", meta)
	}
}

func TestListLeaguesDegradesWhenOnePlatformFails(t *testing.T) {
	f := newFixture()
	f.espn.listErr = fmt.Errorf("fetch: %w", domain.ErrESPNAuth)
	ls, meta, err := f.svc.ListLeagues(ctx, 0)
	if err != nil || len(ls) != 1 || ls[0].ID != "sleeper:111" {
		t.Fatalf("ListLeagues = %+v, %v", ls, err)
	}
	if len(meta.Warnings) != 1 || !strings.HasPrefix(meta.Warnings[0], "espn_leagues_unavailable:") {
		t.Fatalf("warnings = %v", meta.Warnings)
	}
}

func TestListLeaguesFailsWhenAllFail(t *testing.T) {
	f := newFixture()
	f.espn.listErr = domain.ErrESPNAuth
	f.sleeper.listErr = domain.ErrUpstreamTimeout
	if _, _, err := f.svc.ListLeagues(ctx, 0); !errors.Is(err, domain.ErrESPNAuth) {
		t.Fatalf("err = %v, want first failure", err)
	}
}

func TestLeagueInvalidIDDoesNotCallProviders(t *testing.T) {
	f := newFixture()
	_, _, err := f.svc.League(ctx, "espn:12/../3", 0)
	wantInvalid(t, err, "leagueId")
	if f.espn.Calls("list")+f.espn.Calls("league") != 0 {
		t.Fatal("invalid IDs must not reach providers")
	}
}

func TestLeagueNotOwned(t *testing.T) {
	f := newFixture()
	if _, _, err := f.svc.League(ctx, "espn:999", 0); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if f.espn.Calls("league") != 0 {
		t.Fatal("unowned league must not be fetched")
	}
}

func TestLeagueCached(t *testing.T) {
	f := newFixture()
	for range 2 {
		l, meta, err := f.svc.League(ctx, "espn:123456", 0)
		if err != nil || l.Name != "Office" || meta.Season != 2026 {
			t.Fatalf("League = %+v, %+v, %v", l, meta, err)
		}
	}
	if f.espn.Calls("league") != 1 || f.espn.Calls("list") != 1 {
		t.Fatalf("calls = %v, want one of each", f.espn.calls)
	}
}

func TestRostersResolvePlayers(t *testing.T) {
	f := newFixture()
	rs, meta, err := f.svc.Rosters(ctx, "espn:123456", 0)
	if err != nil || len(rs) != 1 {
		t.Fatalf("Rosters = %+v, %v", rs, err)
	}
	r := rs[0]
	if *r.Starters[0].Player.ID != "4046" || *r.Starters[1].Player.ID != "KC" || r.Starters[1].Slot != "DEF" {
		t.Fatalf("starters = %+v", r.Starters)
	}
	rookie := r.Bench[0].Player
	if rookie.ID != nil || rookie.Name != "Rookie Guy" || rookie.PlatformIDs["espn"] != "9999999" {
		t.Fatalf("unmapped player = %+v", rookie)
	}
	if r.Reserve == nil {
		t.Fatal("Reserve should be non-nil")
	}
	if len(meta.Warnings) != 1 || !strings.Contains(meta.Warnings[0], "unmapped_player: espn:9999999") {
		t.Fatalf("warnings = %v", meta.Warnings)
	}
}

func TestMatchupsWithStats(t *testing.T) {
	f := newFixture()
	ms, meta, err := f.svc.Matchups(ctx, "sleeper:111", 0, 0, true)
	if err != nil || len(ms) != 1 {
		t.Fatalf("Matchups = %+v, %v", ms, err)
	}
	if meta.Week != 3 || meta.Season != 2026 || len(meta.Warnings) != 0 {
		t.Fatalf("meta = %+v", meta)
	}
	home := ms[0].Home
	if home.Points != 112.34 || *home.Roster.Starters[0].Points != 18 || *home.Roster.Bench[0].Points != 20.5 {
		t.Fatalf("home = %+v", home)
	}
	if home.Roster.Starters[0].Stats["pass_td"] != 2 {
		t.Fatalf("stats not attached: %+v", home.Roster.Starters[0])
	}
}

func TestMatchupsWithoutStats(t *testing.T) {
	f := newFixture()
	ms, _, err := f.svc.Matchups(ctx, "sleeper:111", 0, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	e := ms[0].Home.Roster.Starters[0]
	if e.Stats != nil || e.Points != nil || f.stats.weekCalls != 0 {
		t.Fatalf("entry = %+v, weekCalls = %d", e, f.stats.weekCalls)
	}
}

func TestMatchupsStatsFailureDegrades(t *testing.T) {
	f := newFixture()
	f.stats.weekErr = &domain.UpstreamError{Provider: "sleeper", Status: 500}
	ms, meta, err := f.svc.Matchups(ctx, "sleeper:111", 0, 3, true)
	if err != nil {
		t.Fatalf("stats failure should not fail the request: %v", err)
	}
	if ms[0].Home.Roster.Starters[0].Points != nil {
		t.Fatal("points should be nil when stats are unavailable")
	}
	if len(meta.Warnings) != 1 || !strings.HasPrefix(meta.Warnings[0], "stats_unavailable:") {
		t.Fatalf("warnings = %v", meta.Warnings)
	}
}

func TestMatchupsPrimaryFailure(t *testing.T) {
	f := newFixture()
	f.sleeper.matchupsErr = domain.ErrUpstreamTimeout
	if _, _, err := f.svc.Matchups(ctx, "sleeper:111", 0, 3, true); !errors.Is(err, domain.ErrUpstreamTimeout) {
		t.Fatalf("err = %v, want timeout", err)
	}
}

func TestMatchupsWeekValidation(t *testing.T) {
	f := newFixture()
	_, _, err := f.svc.Matchups(ctx, "sleeper:111", 0, 19, false)
	wantInvalid(t, err, "week")
}

func TestMatchupsOffseasonDefaultsToWeek1(t *testing.T) {
	f := newFixture()
	f.state.st.Week = 0
	_, meta, err := f.svc.Matchups(ctx, "sleeper:111", 0, 0, false)
	if err != nil || meta.Week != 1 {
		t.Fatalf("meta = %+v, err = %v", meta, err)
	}
}

func TestMatchupsPastSeasonRequiresWeek(t *testing.T) {
	f := newFixture()
	f.sleeper.league.Season = 2025
	_, _, err := f.svc.Matchups(ctx, "sleeper:111", 2025, 0, false)
	wantInvalid(t, err, "week")
}

func TestMatchupsCachedWithinTTL(t *testing.T) {
	f := newFixture()
	for range 2 {
		if _, _, err := f.svc.Matchups(ctx, "sleeper:111", 0, 3, false); err != nil {
			t.Fatal(err)
		}
	}
	if f.sleeper.Calls("matchups") != 1 {
		t.Fatalf("matchups calls = %d, want 1", f.sleeper.Calls("matchups"))
	}
	f.now = f.now.Add(2 * time.Minute)
	f.svc.Matchups(ctx, "sleeper:111", 0, 3, false)
	if f.sleeper.Calls("matchups") != 2 {
		t.Fatal("matchups should refetch after the 1 minute TTL")
	}
}

func TestPlayer(t *testing.T) {
	f := newFixture()
	if p, _, err := f.svc.Player(ctx, "6794"); err != nil || p.Name != "Justin Jefferson" {
		t.Fatalf("Player = %+v, %v", p, err)
	}
	if _, _, err := f.svc.Player(ctx, "0000"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown player err = %v", err)
	}
	_, _, err := f.svc.Player(ctx, "../x")
	wantInvalid(t, err, "playerId")
}

func TestGamelogPreset(t *testing.T) {
	f := newFixture()
	es, meta, err := f.svc.Gamelog(ctx, "6794", 0, "ppr")
	if err != nil || len(es) != 2 || meta.Season != 2026 {
		t.Fatalf("Gamelog = %+v, %+v, %v", es, meta, err)
	}
	if *es[0].Points != 10.9 || *es[1].Points != 20.5 {
		t.Fatalf("points = %v, %v", *es[0].Points, *es[1].Points)
	}
}

func TestGamelogLeagueScoringAddsWarnings(t *testing.T) {
	f := newFixture()
	es, meta, err := f.svc.Gamelog(ctx, "6794", 0, "espn:123456")
	if err != nil {
		t.Fatal(err)
	}
	if *es[0].Points != 8.4 {
		t.Fatalf("week 1 points = %v, want 8.4", *es[0].Points)
	}
	if len(meta.Warnings) != 1 || meta.Warnings[0] != "unsupported_rule: espn stat 92 (1 pts)" {
		t.Fatalf("warnings = %v", meta.Warnings)
	}
}

func TestGamelogRawWhenNoScoring(t *testing.T) {
	f := newFixture()
	es, _, err := f.svc.Gamelog(ctx, "6794", 0, "")
	if err != nil || es[0].Points != nil || es[0].Stats["rec"] != 5 {
		t.Fatalf("Gamelog = %+v, %v", es, err)
	}
}

func TestGamelogBadScoring(t *testing.T) {
	f := newFixture()
	_, _, err := f.svc.Gamelog(ctx, "6794", 0, "bogus")
	wantInvalid(t, err, "scoring")
}

func TestGamelogUnknownPlayer(t *testing.T) {
	f := newFixture()
	if _, _, err := f.svc.Gamelog(ctx, "0000", 0, "ppr"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/service/`
Expected: FAIL — `undefined: Service` / `undefined: New`.

- [ ] **Step 4: Write the implementation**

`internal/service/service.go`:

```go
// Package service joins league data from providers with player identities, stats,
// and scoring. It owns caching and ownership checks.
package service

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/vsnandy/sports-api-go/internal/cache"
	"github.com/vsnandy/sports-api-go/internal/domain"
)

type LeagueProvider interface {
	Platform() domain.Platform
	ListLeagues(ctx context.Context, season int) ([]domain.LeagueSummary, error)
	League(ctx context.Context, nativeID string, season int) (domain.League, error)
	Rosters(ctx context.Context, nativeID string, season int, slots []string) ([]domain.RosterRef, error)
	Matchups(ctx context.Context, nativeID string, season, week int, slots []string) ([]domain.MatchupRef, error)
}

type StatsProvider interface {
	WeekStats(ctx context.Context, season, week int) (map[string]domain.StatLine, error)
	PlayerGamelog(ctx context.Context, playerID string, season int) ([]domain.StatLine, error)
}

type StateProvider interface {
	State(ctx context.Context) (domain.SeasonState, error)
}

type PlayerIndex interface {
	Get(ctx context.Context, id string) (domain.Player, bool, error)
	Resolve(ctx context.Context, ref domain.PlayerRef) (domain.Player, bool, error)
}

const (
	ttlState      = 5 * time.Minute
	ttlLeague     = time.Hour
	ttlRosters    = 5 * time.Minute
	ttlMatchups   = time.Minute
	ttlStatsFinal = 12 * time.Hour
	ttlStatsLive  = 5 * time.Minute
)

type Service struct {
	providers map[domain.Platform]LeagueProvider
	order     []domain.Platform
	stats     StatsProvider
	stateProv StateProvider
	players   PlayerIndex

	stateCache    *cache.Cache[domain.SeasonState]
	listCache     *cache.Cache[[]domain.LeagueSummary]
	leagueCache   *cache.Cache[domain.League]
	rosterCache   *cache.Cache[[]domain.RosterRef]
	matchupCache  *cache.Cache[[]domain.MatchupRef]
	weekStatCache *cache.Cache[map[string]domain.StatLine]
	gamelogCache  *cache.Cache[[]domain.StatLine]
}

func New(providers []LeagueProvider, stats StatsProvider, state StateProvider, players PlayerIndex, now func() time.Time) *Service {
	s := &Service{
		providers: map[domain.Platform]LeagueProvider{},
		stats:     stats, stateProv: state, players: players,
		stateCache:    cache.New[domain.SeasonState](now),
		listCache:     cache.New[[]domain.LeagueSummary](now),
		leagueCache:   cache.New[domain.League](now),
		rosterCache:   cache.New[[]domain.RosterRef](now),
		matchupCache:  cache.New[[]domain.MatchupRef](now),
		weekStatCache: cache.New[map[string]domain.StatLine](now),
		gamelogCache:  cache.New[[]domain.StatLine](now),
	}
	for _, p := range providers {
		s.providers[p.Platform()] = p
		s.order = append(s.order, p.Platform())
	}
	return s
}

func (s *Service) state(ctx context.Context) (domain.SeasonState, error) {
	return s.stateCache.GetOrLoad(ctx, "nfl", ttlState, s.stateProv.State)
}

func (s *Service) seasonOrCurrent(ctx context.Context, season int) (int, domain.SeasonState, error) {
	st, err := s.state(ctx)
	if err != nil {
		return 0, domain.SeasonState{}, err
	}
	if season == 0 {
		season = st.Season
	}
	return season, st, nil
}

func (s *Service) listFor(ctx context.Context, prov LeagueProvider, season int) ([]domain.LeagueSummary, error) {
	return s.listCache.GetOrLoad(ctx, fmt.Sprintf("%s/%d", prov.Platform(), season), ttlLeague,
		func(ctx context.Context) ([]domain.LeagueSummary, error) { return prov.ListLeagues(ctx, season) })
}

// resolveLeague validates leagueID, checks it belongs to the owner for season, and
// returns its provider, native ID, and settings.
func (s *Service) resolveLeague(ctx context.Context, leagueID string, season int) (LeagueProvider, string, domain.League, error) {
	platform, native, err := domain.ParseLeagueID(leagueID)
	if err != nil {
		return nil, "", domain.League{}, err
	}
	prov, ok := s.providers[platform]
	if !ok {
		return nil, "", domain.League{}, fmt.Errorf("league %s: %w", leagueID, domain.ErrNotFound)
	}
	season, _, err = s.seasonOrCurrent(ctx, season)
	if err != nil {
		return nil, "", domain.League{}, err
	}
	owned, err := s.listFor(ctx, prov, season)
	if err != nil {
		return nil, "", domain.League{}, err
	}
	if !slices.ContainsFunc(owned, func(l domain.LeagueSummary) bool { return l.ID == leagueID }) {
		return nil, "", domain.League{}, fmt.Errorf("league %s: %w", leagueID, domain.ErrNotFound)
	}
	league, err := s.leagueCache.GetOrLoad(ctx, fmt.Sprintf("%s/%d", leagueID, season), ttlLeague,
		func(ctx context.Context) (domain.League, error) { return prov.League(ctx, native, season) })
	return prov, native, league, err
}

// statsTTL keeps completed weeks for longer; the current and future weeks can still change.
func statsTTL(season, week int, st domain.SeasonState) time.Duration {
	if season < st.Season || (season == st.Season && week < st.Week) {
		return ttlStatsFinal
	}
	return ttlStatsLive
}
```

`internal/service/leagues.go`:

```go
package service

import (
	"context"
	"fmt"
	"log/slog"

	"golang.org/x/sync/errgroup"

	"github.com/vsnandy/sports-api-go/internal/domain"
	"github.com/vsnandy/sports-api-go/internal/scoring"
)

// ListLeagues returns the owner's leagues across platforms. A platform that fails
// becomes a warning unless every platform fails.
func (s *Service) ListLeagues(ctx context.Context, season int) ([]domain.LeagueSummary, domain.Meta, error) {
	season, _, err := s.seasonOrCurrent(ctx, season)
	if err != nil {
		return nil, domain.Meta{}, err
	}
	out := []domain.LeagueSummary{}
	var warnings []string
	var firstErr error
	for _, p := range s.order {
		ls, err := s.listFor(ctx, s.providers[p], season)
		if err != nil {
			slog.WarnContext(ctx, "list leagues failed", "platform", p, "err", err)
			warnings = append(warnings, fmt.Sprintf("%s_leagues_unavailable: %v", p, err))
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		out = append(out, ls...)
	}
	if len(warnings) == len(s.order) {
		return nil, domain.Meta{}, firstErr
	}
	return out, domain.Meta{Season: season, Warnings: warnings}, nil
}

func (s *Service) League(ctx context.Context, leagueID string, season int) (domain.League, domain.Meta, error) {
	_, _, league, err := s.resolveLeague(ctx, leagueID, season)
	if err != nil {
		return domain.League{}, domain.Meta{}, err
	}
	return league, domain.Meta{Season: league.Season}, nil
}

func (s *Service) Rosters(ctx context.Context, leagueID string, season int) ([]domain.Roster, domain.Meta, error) {
	prov, native, league, err := s.resolveLeague(ctx, leagueID, season)
	if err != nil {
		return nil, domain.Meta{}, err
	}
	refs, err := s.rosterCache.GetOrLoad(ctx, fmt.Sprintf("%s/%d", leagueID, league.Season), ttlRosters,
		func(ctx context.Context) ([]domain.RosterRef, error) {
			return prov.Rosters(ctx, native, league.Season, league.RosterSlots)
		})
	if err != nil {
		return nil, domain.Meta{}, err
	}
	var warnings []string
	out := make([]domain.Roster, 0, len(refs))
	for _, ref := range refs {
		r, err := s.resolveRoster(ctx, ref, &warnings)
		if err != nil {
			return nil, domain.Meta{}, err
		}
		out = append(out, r)
	}
	return out, domain.Meta{Season: league.Season, Warnings: warnings}, nil
}

// Matchups returns a week's matchups. With withStats, each rostered player gets that
// week's stat line and points under the league's scoring; a stats failure degrades
// to a warning.
func (s *Service) Matchups(ctx context.Context, leagueID string, season, week int, withStats bool) ([]domain.Matchup, domain.Meta, error) {
	prov, native, league, err := s.resolveLeague(ctx, leagueID, season)
	if err != nil {
		return nil, domain.Meta{}, err
	}
	st, err := s.state(ctx)
	if err != nil {
		return nil, domain.Meta{}, err
	}
	if week == 0 {
		if league.Season != st.Season {
			return nil, domain.Meta{}, &domain.InvalidParamError{Param: "week", Reason: "required for past seasons"}
		}
		week = max(st.Week, 1)
	}
	if week < 1 || week > 18 {
		return nil, domain.Meta{}, &domain.InvalidParamError{Param: "week", Reason: "must be between 1 and 18"}
	}

	var refs []domain.MatchupRef
	var lines map[string]domain.StatLine
	var statsErr error
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		var err error
		refs, err = s.matchupCache.GetOrLoad(gctx, fmt.Sprintf("%s/%d/%d", leagueID, league.Season, week), ttlMatchups,
			func(ctx context.Context) ([]domain.MatchupRef, error) {
				return prov.Matchups(ctx, native, league.Season, week, league.RosterSlots)
			})
		return err
	})
	if withStats {
		g.Go(func() error {
			lines, statsErr = s.weekStats(gctx, league.Season, week, st)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, domain.Meta{}, err
	}

	var warnings []string
	if withStats && statsErr != nil {
		slog.WarnContext(ctx, "week stats unavailable", "err", statsErr)
		warnings = append(warnings, fmt.Sprintf("stats_unavailable: %v", statsErr))
		lines = nil
	}
	out := make([]domain.Matchup, 0, len(refs))
	for _, m := range refs {
		home, err := s.resolveRoster(ctx, m.Home.Roster, &warnings)
		if err != nil {
			return nil, domain.Meta{}, err
		}
		away, err := s.resolveRoster(ctx, m.Away.Roster, &warnings)
		if err != nil {
			return nil, domain.Meta{}, err
		}
		if lines != nil {
			attachStats(home, lines, league.Scoring)
			attachStats(away, lines, league.Scoring)
		}
		out = append(out, domain.Matchup{
			Week: m.Week,
			Home: domain.MatchupSide{TeamID: m.Home.TeamID, Points: m.Home.Points, Roster: home},
			Away: domain.MatchupSide{TeamID: m.Away.TeamID, Points: m.Away.Points, Roster: away},
		})
	}
	return out, domain.Meta{Season: league.Season, Week: week, Warnings: warnings}, nil
}

func (s *Service) weekStats(ctx context.Context, season, week int, st domain.SeasonState) (map[string]domain.StatLine, error) {
	return s.weekStatCache.GetOrLoad(ctx, fmt.Sprintf("%d/%d", season, week), statsTTL(season, week, st),
		func(ctx context.Context) (map[string]domain.StatLine, error) { return s.stats.WeekStats(ctx, season, week) })
}

func (s *Service) resolveRoster(ctx context.Context, ref domain.RosterRef, warnings *[]string) (domain.Roster, error) {
	starters, err := s.resolveEntries(ctx, ref.Starters, warnings)
	if err != nil {
		return domain.Roster{}, err
	}
	bench, err := s.resolveEntries(ctx, ref.Bench, warnings)
	if err != nil {
		return domain.Roster{}, err
	}
	reserve, err := s.resolveEntries(ctx, ref.Reserve, warnings)
	if err != nil {
		return domain.Roster{}, err
	}
	return domain.Roster{TeamID: ref.TeamID, Starters: starters, Bench: bench, Reserve: reserve}, nil
}

// resolveEntries maps provider refs to players. Unmapped players are kept with a nil
// ID and their platform ID, and reported as a warning.
func (s *Service) resolveEntries(ctx context.Context, refs []domain.RosterEntryRef, warnings *[]string) ([]domain.RosterEntry, error) {
	out := make([]domain.RosterEntry, 0, len(refs))
	for _, r := range refs {
		p, ok, err := s.players.Resolve(ctx, r.Ref)
		if err != nil {
			return nil, err
		}
		if !ok {
			p = domain.Player{
				Name: r.Ref.Name, Position: r.Ref.Position, NFLTeam: r.Ref.NFLTeam,
				PlatformIDs: map[string]string{string(r.Ref.Platform): r.Ref.ID},
			}
			*warnings = append(*warnings, fmt.Sprintf("unmapped_player: %s:%s (%s)", r.Ref.Platform, r.Ref.ID, r.Ref.Name))
		}
		out = append(out, domain.RosterEntry{Slot: r.Slot, Player: p})
	}
	return out, nil
}

// attachStats fills Stats and Points in place for players with a stat line.
func attachStats(r domain.Roster, lines map[string]domain.StatLine, rules domain.ScoringRules) {
	for _, entries := range [][]domain.RosterEntry{r.Starters, r.Bench, r.Reserve} {
		for i := range entries {
			e := &entries[i]
			if e.Player.ID == nil {
				continue
			}
			line, ok := lines[*e.Player.ID]
			if !ok {
				continue
			}
			pts := scoring.Points(line.Stats, rules)
			e.Stats, e.Points = line.Stats, &pts
		}
	}
}
```

`internal/service/players.go`:

```go
package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/vsnandy/sports-api-go/internal/domain"
	"github.com/vsnandy/sports-api-go/internal/scoring"
)

func (s *Service) Player(ctx context.Context, playerID string) (domain.Player, domain.Meta, error) {
	p, err := s.player(ctx, playerID)
	return p, domain.Meta{}, err
}

func (s *Service) player(ctx context.Context, id string) (domain.Player, error) {
	if !domain.ValidPlayerID(id) {
		return domain.Player{}, &domain.InvalidParamError{Param: "playerId", Reason: "must be 1-12 letters or digits"}
	}
	p, ok, err := s.players.Get(ctx, id)
	if err != nil {
		return domain.Player{}, err
	}
	if !ok {
		return domain.Player{}, fmt.Errorf("player %s: %w", id, domain.ErrNotFound)
	}
	return p, nil
}

// Gamelog returns a player's weekly stat lines for season. scoringParam is "" (raw
// stats), a preset name, or a league ID whose scoring rules apply.
func (s *Service) Gamelog(ctx context.Context, playerID string, season int, scoringParam string) ([]domain.GamelogEntry, domain.Meta, error) {
	if _, err := s.player(ctx, playerID); err != nil {
		return nil, domain.Meta{}, err
	}
	season, st, err := s.seasonOrCurrent(ctx, season)
	if err != nil {
		return nil, domain.Meta{}, err
	}
	rules, warnings, err := s.rulesFor(ctx, scoringParam, season)
	if err != nil {
		return nil, domain.Meta{}, err
	}
	ttl := ttlStatsLive
	if season < st.Season {
		ttl = ttlStatsFinal
	}
	lines, err := s.gamelogCache.GetOrLoad(ctx, fmt.Sprintf("%s/%d", playerID, season), ttl,
		func(ctx context.Context) ([]domain.StatLine, error) { return s.stats.PlayerGamelog(ctx, playerID, season) })
	if err != nil {
		return nil, domain.Meta{}, err
	}
	out := make([]domain.GamelogEntry, 0, len(lines))
	for _, l := range lines {
		e := domain.GamelogEntry{Season: l.Season, Week: l.Week, Opponent: l.Opponent, Stats: l.Stats}
		if rules != nil {
			pts := scoring.Points(l.Stats, rules)
			e.Points = &pts
		}
		out = append(out, e)
	}
	return out, domain.Meta{Season: season, Warnings: warnings}, nil
}

func (s *Service) rulesFor(ctx context.Context, param string, season int) (domain.ScoringRules, []string, error) {
	if param == "" {
		return nil, nil, nil
	}
	if r, ok := scoring.Preset(param); ok {
		return r, nil, nil
	}
	if !strings.Contains(param, ":") {
		return nil, nil, &domain.InvalidParamError{Param: "scoring", Reason: "must be ppr, half, std, or a league id like espn:123456"}
	}
	_, _, league, err := s.resolveLeague(ctx, param, season)
	if err != nil {
		return nil, nil, err
	}
	var warnings []string
	for _, u := range league.UnsupportedRules {
		warnings = append(warnings, "unsupported_rule: "+u)
	}
	return league.Scoring, warnings, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test -race ./internal/service/ && go vet ./...`
Expected: `ok`

- [ ] **Step 6: Commit**

```bash
git add internal/service
git commit -m "feat: add service layer joining leagues, players, stats, and scoring"
```

---

### Task 9: HTTP API

**Files:**
- Create: `internal/httpapi/server.go`, `internal/httpapi/handlers.go`, `internal/httpapi/respond.go`, `internal/httpapi/middleware.go`
- Test: `internal/httpapi/httpapi_test.go`

**Interfaces:**
- Consumes: domain types and errors. `*service.Service` satisfies `httpapi.Service`.
- Produces (package `httpapi`):
  - `type Service interface` — the six methods listed in Task 8's Produces, with identical signatures
  - `func NewHandler(svc Service, apiKey string) http.Handler`
  - `func NewLogHandler(h slog.Handler) slog.Handler` — adds `request_id` from context to every record
  - `func RequestID(ctx context.Context) string`

- [ ] **Step 1: Write the failing test**

`internal/httpapi/httpapi_test.go`:

```go
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		{errors.New("surprise"), 500, "internal"},
	}
	for _, tt := range tests {
		rec := do(NewHandler(&fakeService{err: tt.err}, key), "/v1/nfl/leagues/espn:1", key)
		if rec.Code != tt.status || errCode(t, rec) != tt.code {
			t.Errorf("%v: got %d %s", tt.err, rec.Code, rec.Body)
		}
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/httpapi/`
Expected: FAIL — `undefined: NewHandler`.

- [ ] **Step 3: Write the implementation**

`internal/httpapi/server.go`:

```go
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
```

`internal/httpapi/handlers.go`:

```go
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
```

`internal/httpapi/respond.go`:

```go
package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

type envelope struct {
	Data any         `json:"data"`
	Meta domain.Meta `json:"meta"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorEnvelope struct {
	Error apiError `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func respond(w http.ResponseWriter, r *http.Request, data any, meta domain.Meta, err error) {
	if err != nil {
		writeError(w, r, err)
		return
	}
	if meta.Warnings == nil {
		meta.Warnings = []string{}
	}
	writeJSON(w, http.StatusOK, envelope{Data: data, Meta: meta})
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, msg := classify(err)
	if status >= 500 {
		slog.ErrorContext(r.Context(), "request failed", "status", status, "code", code, "err", err)
	}
	writeJSON(w, status, errorEnvelope{Error: apiError{Code: code, Message: msg}})
}

// classify maps domain errors to HTTP status, error code, and a client-safe message.
func classify(err error) (int, string, string) {
	var ip *domain.InvalidParamError
	var ue *domain.UpstreamError
	switch {
	case errors.As(err, &ip):
		return http.StatusBadRequest, "invalid_param", ip.Error()
	case errors.Is(err, domain.ErrNotFound):
		return http.StatusNotFound, "not_found", err.Error()
	case errors.Is(err, domain.ErrESPNAuth):
		return http.StatusBadGateway, "espn_auth_failed", "ESPN rejected the configured espn_s2/SWID cookies; rotate them in SSM"
	case errors.Is(err, domain.ErrUpstreamTimeout):
		return http.StatusGatewayTimeout, "upstream_timeout", "upstream provider timed out"
	case errors.As(err, &ue):
		return http.StatusBadGateway, "upstream_error", ue.Provider + " returned an error"
	default:
		return http.StatusInternalServerError, "internal", "internal error"
	}
}
```

`internal/httpapi/middleware.go`:

```go
package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

type ctxKey struct{}

func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" || len(id) > 64 {
			id = newID()
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		slog.InfoContext(r.Context(), "request",
			"method", r.Method, "path", r.URL.Path, "status", rec.status, "ms", time.Since(start).Milliseconds())
	})
}

func withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				slog.ErrorContext(r.Context(), "panic", "value", v, "stack", string(debug.Stack()))
				writeJSON(w, http.StatusInternalServerError, errorEnvelope{Error: apiError{Code: "internal", Message: "internal error"}})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// requireAPIKey rejects requests whose X-API-Key does not match. An empty
// configured key rejects everything.
func requireAPIKey(apiKey string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-API-Key")
		if apiKey == "" || subtle.ConstantTimeCompare([]byte(got), []byte(apiKey)) != 1 {
			writeJSON(w, http.StatusUnauthorized, errorEnvelope{Error: apiError{Code: "unauthorized", Message: "missing or invalid X-API-Key"}})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// logHandler adds the request ID from the context to every log record.
type logHandler struct{ slog.Handler }

func NewLogHandler(h slog.Handler) slog.Handler { return logHandler{h} }

func (h logHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestID(ctx); id != "" {
		r.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h logHandler) WithAttrs(as []slog.Attr) slog.Handler { return logHandler{h.Handler.WithAttrs(as)} }
func (h logHandler) WithGroup(name string) slog.Handler     { return logHandler{h.Handler.WithGroup(name)} }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/httpapi/ && go vet ./...`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi
git commit -m "feat: add HTTP API with auth, envelopes, and error mapping"
```

---

### Task 10: Config loading

**Files:**
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: `domain.ParseLeagueID`; `github.com/aws/aws-sdk-go-v2/service/ssm`
- Produces (package `config`):
  - `type Config struct { SleeperUsername string; ESPNLeagueIDs []string; APIKey, ESPNS2, ESPNSWID, PlayersBucket, Port string }`
  - `type SSMAPI interface { GetParameters(ctx, *ssm.GetParametersInput, ...func(*ssm.Options)) (*ssm.GetParametersOutput, error) }`
  - `func Load(ctx context.Context, getenv func(string) string, client SSMAPI) (Config, error)`
  - `func NeedsAWS(getenv func(string) string) bool` — true when any `SSM_*_PARAM` or `PLAYERS_BUCKET` is set

Env vars: `SLEEPER_USERNAME` (required), `ESPN_LEAGUE_IDS` (comma-separated, optional), `SSM_API_KEY_PARAM`, `SSM_ESPN_S2_PARAM`, `SSM_ESPN_SWID_PARAM`, `PLAYERS_BUCKET`, `PORT` (default `8080`), local overrides `API_KEY`, `ESPN_S2`, `ESPN_SWID`. ESPN secrets are only required (and only fetched) when `ESPN_LEAGUE_IDS` is non-empty.

- [ ] **Step 1: Write the failing test**

`internal/config/config_test.go`:

```go
package config

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

type fakeSSM struct {
	values  map[string]string
	gotReq  *ssm.GetParametersInput
	invalid []string
}

func (f *fakeSSM) GetParameters(_ context.Context, in *ssm.GetParametersInput, _ ...func(*ssm.Options)) (*ssm.GetParametersOutput, error) {
	f.gotReq = in
	out := &ssm.GetParametersOutput{InvalidParameters: f.invalid}
	for _, n := range in.Names {
		if v, ok := f.values[n]; ok {
			out.Parameters = append(out.Parameters, types.Parameter{Name: aws.String(n), Value: aws.String(v)})
		}
	}
	return out, nil
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadFromSSM(t *testing.T) {
	f := &fakeSSM{values: map[string]string{"/k": "apikey", "/s2": "S2", "/swid": "{SWID}"}}
	cfg, err := Load(context.Background(), env(map[string]string{
		"SLEEPER_USERNAME": "varun", "ESPN_LEAGUE_IDS": " 123 , 456,",
		"SSM_API_KEY_PARAM": "/k", "SSM_ESPN_S2_PARAM": "/s2", "SSM_ESPN_SWID_PARAM": "/swid",
		"PLAYERS_BUCKET": "bucket",
	}), f)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{SleeperUsername: "varun", ESPNLeagueIDs: []string{"123", "456"}, APIKey: "apikey", ESPNS2: "S2", ESPNSWID: "{SWID}", PlayersBucket: "bucket", Port: "8080"}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("cfg = %+v", cfg)
	}
	if !aws.ToBool(f.gotReq.WithDecryption) {
		t.Fatal("secrets must be fetched WithDecryption")
	}
}

func TestOverridesSkipSSM(t *testing.T) {
	cfg, err := Load(context.Background(), env(map[string]string{"SLEEPER_USERNAME": "varun", "API_KEY": "dev", "PORT": "9000"}), nil)
	if err != nil || cfg.APIKey != "dev" || cfg.Port != "9000" || len(cfg.ESPNLeagueIDs) != 0 {
		t.Fatalf("cfg = %+v, err = %v", cfg, err)
	}
}

func TestESPNSecretsOnlyWhenLeaguesConfigured(t *testing.T) {
	f := &fakeSSM{values: map[string]string{"/k": "apikey"}}
	_, err := Load(context.Background(), env(map[string]string{
		"SLEEPER_USERNAME": "varun", "SSM_API_KEY_PARAM": "/k",
		"SSM_ESPN_S2_PARAM": "/s2", "SSM_ESPN_SWID_PARAM": "/swid",
	}), f)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.gotReq.Names, []string{"/k"}) {
		t.Fatalf("fetched %v, want only the API key", f.gotReq.Names)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := map[string]struct {
		env     map[string]string
		ssm     SSMAPI
		wantSub string
	}{
		"missing username":     {map[string]string{"API_KEY": "k"}, nil, "SLEEPER_USERNAME"},
		"missing api key":      {map[string]string{"SLEEPER_USERNAME": "v"}, nil, "API_KEY"},
		"bad league id":        {map[string]string{"SLEEPER_USERNAME": "v", "API_KEY": "k", "ESPN_LEAGUE_IDS": "12/3"}, nil, "ESPN_LEAGUE_IDS"},
		"espn creds missing":   {map[string]string{"SLEEPER_USERNAME": "v", "API_KEY": "k", "ESPN_LEAGUE_IDS": "123"}, nil, "ESPN_S2"},
		"ssm client missing":   {map[string]string{"SLEEPER_USERNAME": "v", "SSM_API_KEY_PARAM": "/k"}, nil, "SSM client"},
		"ssm param not found":  {map[string]string{"SLEEPER_USERNAME": "v", "SSM_API_KEY_PARAM": "/k"}, &fakeSSM{invalid: []string{"/k"}}, "/k"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Load(context.Background(), env(tt.env), tt.ssm)
			if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("err = %v, want mention of %q", err, tt.wantSub)
			}
		})
	}
}

func TestNeedsAWS(t *testing.T) {
	if NeedsAWS(env(map[string]string{"API_KEY": "k"})) {
		t.Fatal("pure local config should not need AWS")
	}
	if !NeedsAWS(env(map[string]string{"PLAYERS_BUCKET": "b"})) {
		t.Fatal("bucket needs AWS")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go get github.com/aws/aws-sdk-go-v2/service/ssm && go test ./internal/config/`
Expected: FAIL — `undefined: Load`.

- [ ] **Step 3: Write the implementation**

`internal/config/config.go`:

```go
// Package config loads settings from the environment and secrets from SSM
// Parameter Store (or local env overrides).
package config

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

type Config struct {
	SleeperUsername string
	ESPNLeagueIDs   []string
	APIKey          string
	ESPNS2          string
	ESPNSWID        string
	PlayersBucket   string
	Port            string
}

type SSMAPI interface {
	GetParameters(ctx context.Context, in *ssm.GetParametersInput, opts ...func(*ssm.Options)) (*ssm.GetParametersOutput, error)
}

var awsEnv = []string{"SSM_API_KEY_PARAM", "SSM_ESPN_S2_PARAM", "SSM_ESPN_SWID_PARAM", "PLAYERS_BUCKET"}

// NeedsAWS reports whether the environment references SSM or S3.
func NeedsAWS(getenv func(string) string) bool {
	return slices.ContainsFunc(awsEnv, func(k string) bool { return getenv(k) != "" })
}

type secret struct {
	dst         *string
	overrideEnv string
	paramEnv    string
	required    bool
}

func Load(ctx context.Context, getenv func(string) string, client SSMAPI) (Config, error) {
	cfg := Config{
		SleeperUsername: strings.TrimSpace(getenv("SLEEPER_USERNAME")),
		PlayersBucket:   getenv("PLAYERS_BUCKET"),
		Port:            getenv("PORT"),
	}
	if cfg.Port == "" {
		cfg.Port = "8080"
	}
	if cfg.SleeperUsername == "" {
		return Config{}, errors.New("SLEEPER_USERNAME is required")
	}
	for _, id := range strings.Split(getenv("ESPN_LEAGUE_IDS"), ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, _, err := domain.ParseLeagueID("espn:" + id); err != nil {
			return Config{}, fmt.Errorf("ESPN_LEAGUE_IDS: invalid league id %q", id)
		}
		cfg.ESPNLeagueIDs = append(cfg.ESPNLeagueIDs, id)
	}

	useESPN := len(cfg.ESPNLeagueIDs) > 0
	secrets := []secret{
		{&cfg.APIKey, "API_KEY", "SSM_API_KEY_PARAM", true},
		{&cfg.ESPNS2, "ESPN_S2", "SSM_ESPN_S2_PARAM", useESPN},
		{&cfg.ESPNSWID, "ESPN_SWID", "SSM_ESPN_SWID_PARAM", useESPN},
	}
	byParam := map[string]*string{}
	for _, s := range secrets {
		if !s.required {
			continue
		}
		if v := getenv(s.overrideEnv); v != "" {
			*s.dst = v
			continue
		}
		name := getenv(s.paramEnv)
		if name == "" {
			return Config{}, fmt.Errorf("%s or %s is required", s.overrideEnv, s.paramEnv)
		}
		byParam[name] = s.dst
	}

	if len(byParam) > 0 {
		if client == nil {
			return Config{}, errors.New("SSM client required to load secrets")
		}
		out, err := client.GetParameters(ctx, &ssm.GetParametersInput{
			Names: slices.Sorted(maps.Keys(byParam)), WithDecryption: aws.Bool(true),
		})
		if err != nil {
			return Config{}, fmt.Errorf("ssm GetParameters: %w", err)
		}
		if len(out.InvalidParameters) > 0 {
			return Config{}, fmt.Errorf("ssm parameters not found: %s", strings.Join(out.InvalidParameters, ", "))
		}
		for _, p := range out.Parameters {
			if dst, ok := byParam[aws.ToString(p.Name)]; ok {
				*dst = aws.ToString(p.Value)
			}
		}
	}

	for _, s := range secrets {
		if s.required && *s.dst == "" {
			return Config{}, fmt.Errorf("%s resolved to an empty value", s.overrideEnv)
		}
	}
	return cfg, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/ && go vet ./...`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/config
git commit -m "feat: add config loading from env and SSM"
```

---

### Task 11: Entrypoint and local run

**Files:**
- Create: `cmd/api/main.go`
- Modify: `Makefile`

**Interfaces:**
- Consumes: `config.Load`, `config.NeedsAWS`, `httpx.New`, `sleeper.New` + defaults, `espn.New` + `DefaultBase`, `players.New/NoStore/S3Store`, `service.New`, `httpapi.NewHandler/NewLogHandler`
- Produces: the `bootstrap` binary; `make run`, `make build`

- [ ] **Step 1: Write the entrypoint**

`cmd/api/main.go`:

```go
// Command api serves the sports API locally (net/http) or on AWS Lambda behind an
// API Gateway HTTP API.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"

	"github.com/vsnandy/sports-api-go/internal/config"
	"github.com/vsnandy/sports-api-go/internal/httpapi"
	"github.com/vsnandy/sports-api-go/internal/players"
	"github.com/vsnandy/sports-api-go/internal/providers/espn"
	"github.com/vsnandy/sports-api-go/internal/providers/httpx"
	"github.com/vsnandy/sports-api-go/internal/providers/sleeper"
	"github.com/vsnandy/sports-api-go/internal/service"
)

const upstreamTimeout = 5 * time.Second

func main() {
	slog.SetDefault(slog.New(httpapi.NewLogHandler(slog.NewJSONHandler(os.Stdout, nil))))
	ctx := context.Background()

	handler, cfg, err := build(ctx)
	if err != nil {
		slog.Error("startup failed", "err", err)
		os.Exit(1)
	}

	if os.Getenv("AWS_LAMBDA_FUNCTION_NAME") != "" {
		lambda.Start(httpadapter.NewV2(handler).ProxyWithContext)
		return
	}
	addr := ":" + cfg.Port
	slog.Info("listening", "addr", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func build(ctx context.Context) (http.Handler, config.Config, error) {
	var ssmClient config.SSMAPI
	var s3Client *s3.Client
	if config.NeedsAWS(os.Getenv) {
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
		if err != nil {
			return nil, config.Config{}, err
		}
		ssmClient = ssm.NewFromConfig(awsCfg)
		s3Client = s3.NewFromConfig(awsCfg)
	}

	cfg, err := config.Load(ctx, os.Getenv, ssmClient)
	if err != nil {
		return nil, config.Config{}, err
	}

	sl := sleeper.New(httpx.New("sleeper", upstreamTimeout), sleeper.DefaultAPIBase, sleeper.DefaultStatsBase, cfg.SleeperUsername)
	providers := []service.LeagueProvider{sl}
	if len(cfg.ESPNLeagueIDs) > 0 {
		providers = append(providers, espn.New(httpx.New("espn", upstreamTimeout), espn.DefaultBase, cfg.ESPNS2, cfg.ESPNSWID, cfg.ESPNLeagueIDs))
	}

	var store players.Store = players.NoStore{}
	if cfg.PlayersBucket != "" {
		store = players.S3Store{Client: s3Client, Bucket: cfg.PlayersBucket}
	}
	idx := players.New(sl, store, time.Now)

	svc := service.New(providers, sl, sl, idx, time.Now)
	return httpapi.NewHandler(svc, cfg.APIKey), cfg, nil
}
```

- [ ] **Step 2: Add dependencies and build**

Run: `go get github.com/aws/aws-lambda-go github.com/awslabs/aws-lambda-go-api-proxy github.com/aws/aws-sdk-go-v2/config && go mod tidy && go build ./... && go vet ./...`
Expected: no output (success).

- [ ] **Step 3: Extend the Makefile**

Replace `Makefile` with (recipe lines start with a TAB):

```make
.PHONY: test run build deploy smoke

test:
	go test ./...

run:
	go run ./cmd/api

build:
	mkdir -p dist
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags lambda.norpc -trimpath -ldflags="-s -w" -o dist/bootstrap ./cmd/api
	cd dist && rm -f bootstrap.zip && zip -q bootstrap.zip bootstrap

deploy: build
	terraform -chdir=deploy/terraform apply

smoke:
	./scripts/smoke.sh
```

- [ ] **Step 4: Verify the build artifact**

Run: `make test && make build && unzip -l dist/bootstrap.zip`
Expected: all tests pass; the zip lists a single `bootstrap` entry.

- [ ] **Step 5: Verify a local run (live Sleeper calls, manual)**

Run in one terminal: `API_KEY=dev SLEEPER_USERNAME=<your sleeper username> make run`
Run in another:
- `curl -s localhost:8080/healthz` → `{"status":"ok"}`
- `curl -s localhost:8080/v1/nfl/leagues` → 401 `unauthorized`
- `curl -s -H 'X-API-Key: dev' localhost:8080/v1/nfl/leagues | jq` → your Sleeper leagues in the envelope
- `curl -s -H 'X-API-Key: dev' 'localhost:8080/v1/nfl/players/4046/gamelog?scoring=ppr' | jq '.data[0]'` → a week with `points`

Expected: as listed. Stop the server with Ctrl-C.

- [ ] **Step 6: Commit**

```bash
git add cmd go.mod go.sum Makefile
git commit -m "feat: add entrypoint for local and Lambda execution"
```

---

### Task 12: Terraform, smoke test, and README

**Files:**
- Create: `deploy/terraform/versions.tf`, `deploy/terraform/variables.tf`, `deploy/terraform/main.tf`, `deploy/terraform/outputs.tf`, `deploy/terraform/terraform.tfvars.example`, `scripts/smoke.sh`
- Modify: `README.md`

**Interfaces:**
- Consumes: `dist/bootstrap.zip` from `make build`; env var names from Task 10
- Produces: `terraform output -raw api_url`; `make smoke`

- [ ] **Step 1: Write the Terraform**

`deploy/terraform/versions.tf`:

```hcl
terraform {
  required_version = ">= 1.6"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = var.region
}
```

`deploy/terraform/variables.tf`:

```hcl
variable "region" {
  type    = string
  default = "us-east-1"
}

variable "sleeper_username" {
  type = string
}

variable "espn_league_ids" {
  type    = list(string)
  default = []
}

variable "ssm_api_key_param" {
  type    = string
  default = "/sports-api/api-key"
  validation {
    condition     = startswith(var.ssm_api_key_param, "/")
    error_message = "SSM parameter names must start with /."
  }
}

variable "ssm_espn_s2_param" {
  type    = string
  default = "/sports-api/espn-s2"
  validation {
    condition     = startswith(var.ssm_espn_s2_param, "/")
    error_message = "SSM parameter names must start with /."
  }
}

variable "ssm_espn_swid_param" {
  type    = string
  default = "/sports-api/espn-swid"
  validation {
    condition     = startswith(var.ssm_espn_swid_param, "/")
    error_message = "SSM parameter names must start with /."
  }
}

variable "lambda_zip" {
  type    = string
  default = "../../dist/bootstrap.zip"
}
```

`deploy/terraform/main.tf`:

```hcl
data "aws_caller_identity" "current" {}
data "aws_region" "current" {}

locals {
  name    = "sports-api"
  ssm_arn = "arn:aws:ssm:${data.aws_region.current.name}:${data.aws_caller_identity.current.account_id}:parameter"
}

# --- Players dump cache ---

resource "aws_s3_bucket" "players" {
  bucket_prefix = "sports-api-players-"
  force_destroy = true
}

resource "aws_s3_bucket_public_access_block" "players" {
  bucket                  = aws_s3_bucket.players.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "players" {
  bucket = aws_s3_bucket.players.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

# --- Logs ---

resource "aws_cloudwatch_log_group" "api" {
  name              = "/aws/lambda/${local.name}"
  retention_in_days = 14
}

# --- IAM ---

data "aws_iam_policy_document" "assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "api" {
  name               = "${local.name}-lambda"
  assume_role_policy = data.aws_iam_policy_document.assume.json
}

data "aws_iam_policy_document" "api" {
  statement {
    actions = ["ssm:GetParameters"]
    resources = [
      for p in [var.ssm_api_key_param, var.ssm_espn_s2_param, var.ssm_espn_swid_param] : "${local.ssm_arn}${p}"
    ]
  }
  statement {
    actions   = ["kms:Decrypt"]
    resources = ["*"]
    condition {
      test     = "StringEquals"
      variable = "kms:ViaService"
      values   = ["ssm.${data.aws_region.current.name}.amazonaws.com"]
    }
  }
  statement {
    actions   = ["s3:GetObject", "s3:PutObject"]
    resources = ["${aws_s3_bucket.players.arn}/players/*"]
  }
  # Without ListBucket, GetObject on a missing key returns AccessDenied instead of NoSuchKey.
  statement {
    actions   = ["s3:ListBucket"]
    resources = [aws_s3_bucket.players.arn]
    condition {
      test     = "StringLike"
      variable = "s3:prefix"
      values   = ["players/*"]
    }
  }
  statement {
    actions   = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = ["${aws_cloudwatch_log_group.api.arn}:*"]
  }
}

resource "aws_iam_role_policy" "api" {
  role   = aws_iam_role.api.id
  policy = data.aws_iam_policy_document.api.json
}

# --- Lambda ---

resource "aws_lambda_function" "api" {
  function_name    = local.name
  role             = aws_iam_role.api.arn
  runtime          = "provided.al2023"
  architectures    = ["arm64"]
  handler          = "bootstrap"
  filename         = var.lambda_zip
  source_code_hash = filebase64sha256(var.lambda_zip)
  memory_size      = 512
  timeout          = 15

  environment {
    variables = {
      SLEEPER_USERNAME    = var.sleeper_username
      ESPN_LEAGUE_IDS     = join(",", var.espn_league_ids)
      SSM_API_KEY_PARAM   = var.ssm_api_key_param
      SSM_ESPN_S2_PARAM   = var.ssm_espn_s2_param
      SSM_ESPN_SWID_PARAM = var.ssm_espn_swid_param
      PLAYERS_BUCKET      = aws_s3_bucket.players.bucket
    }
  }

  depends_on = [aws_cloudwatch_log_group.api, aws_iam_role_policy.api]
}

# --- API Gateway HTTP API ---

resource "aws_apigatewayv2_api" "api" {
  name          = local.name
  protocol_type = "HTTP"
}

resource "aws_apigatewayv2_integration" "api" {
  api_id                 = aws_apigatewayv2_api.api.id
  integration_type       = "AWS_PROXY"
  integration_uri        = aws_lambda_function.api.invoke_arn
  payload_format_version = "2.0"
}

resource "aws_apigatewayv2_route" "default" {
  api_id    = aws_apigatewayv2_api.api.id
  route_key = "$default"
  target    = "integrations/${aws_apigatewayv2_integration.api.id}"
}

resource "aws_apigatewayv2_stage" "default" {
  api_id      = aws_apigatewayv2_api.api.id
  name        = "$default"
  auto_deploy = true
}

resource "aws_lambda_permission" "apigw" {
  statement_id  = "AllowAPIGatewayInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.api.function_name
  principal     = "apigateway.amazonaws.com"
  source_arn    = "${aws_apigatewayv2_api.api.execution_arn}/*/*"
}
```

`deploy/terraform/outputs.tf`:

```hcl
output "api_url" {
  value = aws_apigatewayv2_api.api.api_endpoint
}

output "players_bucket" {
  value = aws_s3_bucket.players.bucket
}
```

`deploy/terraform/terraform.tfvars.example`:

```hcl
sleeper_username = "your-sleeper-username"
espn_league_ids  = ["123456"]
# region = "us-east-1"
```

- [ ] **Step 2: Validate the Terraform**

Run: `terraform -chdir=deploy/terraform fmt -check && terraform -chdir=deploy/terraform init -backend=false && terraform -chdir=deploy/terraform validate`
Expected: `Success! The configuration is valid.`

- [ ] **Step 3: Write the smoke script**

`scripts/smoke.sh`:

```bash
#!/usr/bin/env bash
# Hits a deployed (or local) API with real config. Not part of `go test`.
# Usage: API_URL=$(terraform -chdir=deploy/terraform output -raw api_url) API_KEY=... make smoke
set -euo pipefail
: "${API_URL:?set API_URL}"
: "${API_KEY:?set API_KEY}"

body=$(mktemp)
trap 'rm -f "$body"' EXIT

get() {
  local code
  code=$(curl -sS -o "$body" -w '%{http_code}' -H "X-API-Key: $API_KEY" "$API_URL$1")
  if [[ $code != "${2:-200}" ]]; then
    echo "FAIL $1 -> $code: $(cat "$body")"
    exit 1
  fi
  echo "ok   $1 ($code)"
  jq -r '.meta.warnings[]? | "     warning: " + .' "$body"
}

get /healthz
code=$(curl -sS -o /dev/null -w '%{http_code}' "$API_URL/v1/nfl/leagues")
[[ $code == 401 ]] || { echo "FAIL unauthenticated request returned $code"; exit 1; }
echo "ok   /v1/nfl/leagues without key (401)"

get /v1/nfl/leagues
leagues=$(jq -r '.data[].id' "$body")
for league in $leagues; do
  get "/v1/nfl/leagues/$league"
  get "/v1/nfl/leagues/$league/rosters"
  get "/v1/nfl/leagues/$league/matchups?include=stats"
done

get /v1/nfl/players/4046
get "/v1/nfl/players/4046/gamelog?scoring=ppr"
echo "smoke test passed"
```

Run: `chmod +x scripts/smoke.sh && bash -n scripts/smoke.sh`
Expected: no output.

- [ ] **Step 4: Write the README**

Replace `README.md` with:

````markdown
# sports-api-go

Personal fantasy football API that unifies ESPN and Sleeper leagues with Sleeper
player stats, game logs, and fantasy points. Runs locally or on AWS Lambda.

Design: `docs/superpowers/specs/2026-09-25-sports-api-v1-design.md`

## Endpoints

All `/v1` routes require `X-API-Key`.

| Route | Returns |
|---|---|
| `GET /healthz` | `{"status":"ok"}` |
| `GET /v1/nfl/leagues?season=` | Your ESPN and Sleeper leagues |
| `GET /v1/nfl/leagues/{id}?season=` | Teams, scoring, roster slots |
| `GET /v1/nfl/leagues/{id}/rosters?season=` | Rosters with normalized players |
| `GET /v1/nfl/leagues/{id}/matchups?week=&season=&include=stats` | Matchups, optionally with player stats and points |
| `GET /v1/nfl/players/{id}` | Player info (Sleeper ID) |
| `GET /v1/nfl/players/{id}/gamelog?season=&scoring=` | Weekly stats; `scoring` = `ppr`, `half`, `std`, or a league ID |

League IDs look like `espn:123456` or `sleeper:987654321`.

## Run locally

```bash
API_KEY=dev SLEEPER_USERNAME=you make run
# with ESPN:
API_KEY=dev SLEEPER_USERNAME=you ESPN_LEAGUE_IDS=123456 ESPN_S2='...' ESPN_SWID='{...}' make run
curl -H 'X-API-Key: dev' localhost:8080/v1/nfl/leagues
```

## Deploy

1. Create secrets (once; they never enter Terraform state). `espn_s2` and `SWID` are
   cookies from a logged-in espn.com session; keep the braces in `SWID`.
   ```bash
   aws ssm put-parameter --name /sports-api/api-key   --type SecureString --value "$(openssl rand -hex 32)"
   aws ssm put-parameter --name /sports-api/espn-s2   --type SecureString --value '<espn_s2>'
   aws ssm put-parameter --name /sports-api/espn-swid --type SecureString --value '{<SWID>}'
   ```
2. `cp deploy/terraform/terraform.tfvars.example deploy/terraform/terraform.tfvars` and fill it in.
3. `terraform -chdir=deploy/terraform init`, then `make deploy`.
4. Smoke test:
   ```bash
   API_URL=$(terraform -chdir=deploy/terraform output -raw api_url) \
   API_KEY=$(aws ssm get-parameter --name /sports-api/api-key --with-decryption --query Parameter.Value --output text) \
   make smoke
   ```

## Rotating ESPN cookies

A `502` with code `espn_auth_failed` (or an `espn_leagues_unavailable` warning) means the
cookies expired. Update them with `aws ssm put-parameter --overwrite ...`, then force a
cold start: `aws lambda update-function-configuration --function-name sports-api --description "rotated $(date +%F)"`.

## Test

```bash
make test
```
````

- [ ] **Step 5: Final verification**

Run: `make test && go vet ./... && make build && terraform -chdir=deploy/terraform validate`
Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add deploy scripts README.md
git commit -m "feat: add Terraform deployment, smoke test, and README"
```

- [ ] **Step 7 (manual, requires AWS credentials): Deploy and smoke test**

Follow README "Deploy" steps 1–4. Expected: `smoke test passed`. If the user has not provided AWS credentials or asked to deploy, stop here and report that the deploy is ready to run.
