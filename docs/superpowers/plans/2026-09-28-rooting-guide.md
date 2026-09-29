# Rooting Guide Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A `/rooting` viewer page listing, for one week, the players in two or more of the owner's matchups across all leagues, with for/against counts and net exposure — backed by a new `mine` flag on API teams.

**Architecture:** The API marks the owner's team per league (`domain.Team.Mine`): ESPN by comparing team owners with the configured SWID, Sleeper by comparing roster owner/co-owners with the configured user's ID (resolved once per client). The viewer fetches every league's detail and matchups in parallel, aggregates starters with a pure function (`buildRooting`), and renders a new template in the existing dark theme.

**Tech Stack:** Go 1.26 (stdlib `net/http`, `html/template`, `httptest`), `golang.org/x/sync/errgroup` (already a dependency).

**Spec:** `docs/superpowers/specs/2026-09-28-rooting-guide-design.md`

## Global Constraints

- JSON field name is `mine` (`Mine bool \`json:"mine"\``) on `domain.Team`; additive only.
- The SWID is never written to responses or logs — only the boolean.
- SWID comparison: trim surrounding whitespace and `{`/`}`, then compare case-insensitively.
- Sleeper: a roster is mine when `owner_id` equals my user ID or `co_owners` contains it; user ID resolved from `SLEEPER_USERNAME` once per client.
- Only **starters** count; bench and reserve never count.
- Player key: canonical player ID when present, else `<platform>:<platformIds[platform]>`; entries with neither are skipped.
- Listed players: appearances ≥ 2. Sort: |net| desc, then appearances desc, then name asc (then key asc for determinism).
- Viewer route `GET /rooting?week=N`; week validation and error page identical to `/league/{id}` ("Invalid week", "week must be a number from 1 to 18").
- Warning copy: `<league>: couldn't find your team`, `<league>: <error message>`, `<league>: returned week X`; empty state `No shared players this week.`
- Matchups are fetched with `include=stats` (points only come with stats).
- Viewer stays JavaScript-free; reuse theme tokens and the `avatar` template.
- Commit trailer: `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

1. **API not yet redeployed (no team has `mine`)** → the page renders 200 with one "couldn't find your team" warning per league, never an error page. (Task 4: `TestRootingBeforeRedeploy`.)
2. **First league fails and no week was requested** → the shown week comes from the next league that loaded, not 0. (Task 3: `TestBuildRootingWeekFromFirstLoaded`.)
3. **Starters with no canonical ID and no platform ID (empty slots, odd data)** → skipped; never merged under an empty key. (Task 3: case inside `TestBuildRooting`.)
4. **Sleeper username that doesn't resolve** → `League()` fails as an upstream error instead of silently marking no team. (Task 2: `TestLeagueUnknownUserFails`.)
5. **Concurrent `League()` calls on a Lambda-warm client** → no data race on the cached user ID; lookup happens once after the first success. (Task 2: `TestUserIDLookedUpOnce`, run with `-race`.)

---

### Task 1: `Team.Mine` and ESPN SWID matching

**Files:**
- Modify: `internal/domain/domain.go` (`Team`)
- Modify: `internal/providers/espn/client.go` (`Client`, `New`)
- Modify: `internal/providers/espn/league.go` (`League` team loop; add `normSWID`)
- Test: `internal/providers/espn/espn_test.go`

**Interfaces:**
- Produces: `domain.Team.Mine bool` (JSON `mine`); `espn.normSWID(string) string`.

- [ ] **Step 1: Write the failing tests**

In `internal/providers/espn/espn_test.go`:

1. In `newTestClient`, change the cookie check line to
   `if r.Header.Get("Cookie") != "espn_s2=S2; SWID=aaa" {`
   and the return line's SWID argument from `"{SWID}"` to `"aaa"`:
   `return New(httpx.New("espn", 2*time.Second), srv.URL, s2, "aaa", leagueIDs), rec`
   (The fixture's team 1 owner is `{AAA}`: braces and case differ on purpose.)
2. In `TestLeague`, change `wantTeams` to
   `wantTeams := []domain.Team{{ID: "1", Name: "Team Varun", Owner: "varun", Mine: true}, {ID: "2", Name: "Alex's Aces", Owner: "alex"}}`
3. Append:

```go
func TestNormSWID(t *testing.T) {
	for _, s := range []string{"{AAA-1}", "aaa-1", " {aAa-1} ", "AAA-1"} {
		if got := normSWID(s); got != "aaa-1" {
			t.Errorf("normSWID(%q) = %q, want aaa-1", s, got)
		}
	}
}

func TestLeagueDoesNotExposeSWID(t *testing.T) {
	c, _ := newTestClient(t, "S2", "123456")
	l, err := c.League(context.Background(), "123456", 2026)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(b)), "aaa") {
		t.Errorf("league JSON contains the SWID: %s", b)
	}
	if !strings.Contains(string(b), `"mine":true`) {
		t.Errorf("league JSON missing mine flag: %s", b)
	}
}
```

Add `"encoding/json"` to the test file's imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/providers/espn/`
Expected: FAIL — compile errors `unknown field Mine in struct literal` and `undefined: normSWID`.

- [ ] **Step 3: Implement**

`internal/domain/domain.go` — replace `Team`:

```go
type Team struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Owner string `json:"owner"`
	Mine  bool   `json:"mine"` // the configured user's team
}
```

`internal/providers/espn/client.go` — add a field and set it in `New`:

```go
type Client struct {
	http      *httpx.Client
	base      string
	cookie    string
	swid      string // normalized (normSWID) for matching team owners; never logged
	leagueIDs []string
}

func New(hc *httpx.Client, base, espnS2, swid string, leagueIDs []string) *Client {
	return &Client{http: hc, base: base, cookie: fmt.Sprintf("espn_s2=%s; SWID=%s", espnS2, swid), swid: normSWID(swid), leagueIDs: leagueIDs}
}
```

`internal/providers/espn/league.go` — in `League`, replace the team loop:

```go
	for _, t := range l.Teams {
		owner := ""
		if len(t.Owners) > 0 {
			owner = names[t.Owners[0]]
		}
		mine := c.swid != "" && slices.ContainsFunc(t.Owners, func(o string) bool { return normSWID(o) == c.swid })
		teams = append(teams, domain.Team{ID: strconv.Itoa(t.ID), Name: t.displayName(), Owner: owner, Mine: mine})
	}
```

and add at the end of `league.go`:

```go
// normSWID drops the braces ESPN wraps GUIDs in and lowercases, so the SWID cookie and a
// team owner's member ID compare equal however either is written.
func normSWID(s string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(s), "{}"))
}
```

(`league.go` already imports `slices` and `strings`.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `gofmt -l . ; go vet ./... && go test -race ./...`
Expected: no gofmt output; all packages `ok`. (The Sleeper `TestLeague` still passes: its want has no `Mine` and nothing sets it yet.)

- [ ] **Step 5: Commit**

```bash
git add internal/domain/domain.go internal/providers/espn
git commit -m "feat(espn): mark the configured user's team with mine"
```

---

### Task 2: Sleeper `mine` via owner/co-owners

**Files:**
- Modify: `internal/providers/sleeper/client.go` (`Client` fields, `sync` import)
- Modify: `internal/providers/sleeper/leagues.go` (`rosterJSON`, `ListLeagues`, `League`; add `myUserID`)
- Create: `internal/providers/sleeper/testdata/null.json`, `testdata/rosters_coowned.json`, `testdata/rosters_others.json`
- Test: `internal/providers/sleeper/sleeper_test.go`

**Interfaces:**
- Consumes: `domain.Team.Mine` (Task 1).
- Produces: `(*sleeper.Client).myUserID(ctx) (string, error)`.

- [ ] **Step 1: Write the failing tests**

Create fixtures:

`internal/providers/sleeper/testdata/null.json`:
```json
null
```

`internal/providers/sleeper/testdata/rosters_coowned.json`:
```json
[
  {"roster_id": 1, "owner_id": "u2", "co_owners": ["u1"], "starters": [], "players": [], "reserve": null, "taxi": null},
  {"roster_id": 2, "owner_id": "u3", "co_owners": null, "starters": [], "players": [], "reserve": null, "taxi": null}
]
```

`internal/providers/sleeper/testdata/rosters_others.json`:
```json
[
  {"roster_id": 1, "owner_id": "u2", "starters": [], "players": [], "reserve": null, "taxi": null},
  {"roster_id": 2, "owner_id": "u3", "starters": [], "players": [], "reserve": null, "taxi": null}
]
```

In `sleeper_test.go`, add `"sync"` to the imports and replace the whole `newTestClient` function with:

```go
func newTestClient(t *testing.T) *Client {
	t.Helper()
	c, _ := newCountingClient(t, "varun")
	return c
}

// newCountingClient serves testdata and reports how many times each path was requested.
func newCountingClient(t *testing.T, username string) (*Client, func(path string) int) {
	t.Helper()
	routes := map[string]string{
		"/v1/state/nfl":                "state.json",
		"/v1/user/varun":               "user.json",
		"/v1/user/ghost":               "null.json",
		"/v1/user/u1/leagues/nfl/2026": "user_leagues.json",
		"/v1/league/111":               "league.json",
		"/v1/league/111/users":         "users.json",
		"/v1/league/111/rosters":       "rosters.json",
		"/v1/league/111/matchups/3":    "matchups.json",
		"/v1/league/444":               "league.json",
		"/v1/league/444/users":         "users.json",
		"/v1/league/444/rosters":       "rosters_coowned.json",
		"/v1/league/555":               "league.json",
		"/v1/league/555/users":         "users.json",
		"/v1/league/555/rosters":       "rosters_others.json",
		"/v1/players/nfl":              "players.json",
		"/stats/nfl/2026/3":            "week_stats.json",
		"/stats/nfl/player/6794":       "gamelog.json",
	}
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
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
	count := func(path string) int {
		mu.Lock()
		defer mu.Unlock()
		return hits[path]
	}
	return New(httpx.New("sleeper", 2*time.Second), srv.URL+"/v1", srv.URL, username), count
}
```

In `TestLeague`, change `wantTeams` to
`wantTeams := []domain.Team{{ID: "1", Name: "Gridiron Gurus", Owner: "Varun", Mine: true}, {ID: "2", Name: "Sam", Owner: "Sam"}}`

Append:

```go
func mineIDs(ts []domain.Team) []string {
	out := []string{}
	for _, t := range ts {
		if t.Mine {
			out = append(out, t.ID)
		}
	}
	return out
}

func TestLeagueMineViaCoOwner(t *testing.T) {
	l, err := newTestClient(t).League(context.Background(), "444", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := mineIDs(l.Teams); !reflect.DeepEqual(got, []string{"1"}) {
		t.Fatalf("mine teams = %v, want [1]", got)
	}
}

func TestLeagueMineNone(t *testing.T) {
	l, err := newTestClient(t).League(context.Background(), "555", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := mineIDs(l.Teams); len(got) != 0 {
		t.Fatalf("mine teams = %v, want none", got)
	}
}

func TestLeagueUnknownUserFails(t *testing.T) {
	c, _ := newCountingClient(t, "ghost")
	_, err := c.League(context.Background(), "111", 0)
	var ue *domain.UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v, want *domain.UpstreamError", err)
	}
}

func TestUserIDLookedUpOnce(t *testing.T) {
	c, hits := newCountingClient(t, "varun")
	ctx := context.Background()
	if _, err := c.League(ctx, "111", 0); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.League(ctx, "111", 0); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if _, err := c.ListLeagues(ctx, 2026); err != nil {
		t.Fatal(err)
	}
	if n := hits("/v1/user/varun"); n != 1 {
		t.Fatalf("user lookups = %d, want 1", n)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -race ./internal/providers/sleeper/`
Expected: FAIL — `TestLeague` (team 1 not mine), `TestLeagueMineViaCoOwner` (mine teams = [], want [1]), `TestLeagueUnknownUserFails` (err = <nil>). `TestUserIDLookedUpOnce` passes already (only `ListLeagues` looks the user up today); it guards the cache once `League()` also needs the ID.

- [ ] **Step 3: Implement**

`internal/providers/sleeper/client.go` — add `"sync"` to imports and replace `Client`:

```go
type Client struct {
	http      *httpx.Client
	apiBase   string
	statsBase string
	username  string

	mu     sync.Mutex
	userID string // resolved from username on first successful lookup
}
```

`internal/providers/sleeper/leagues.go`:

Add `CoOwners` to `rosterJSON`:

```go
type rosterJSON struct {
	RosterID int      `json:"roster_id"`
	OwnerID  string   `json:"owner_id"`
	CoOwners []string `json:"co_owners"`
	Starters []string `json:"starters"`
	Players  []string `json:"players"`
	Reserve  []string `json:"reserve"`
	Taxi     []string `json:"taxi"`
}
```

Add `myUserID`:

```go
// myUserID resolves the configured username to a Sleeper user ID, once per client.
// Concurrent first calls may each look it up; the result is the same.
func (c *Client) myUserID(ctx context.Context) (string, error) {
	c.mu.Lock()
	id := c.userID
	c.mu.Unlock()
	if id != "" {
		return id, nil
	}
	var u *struct {
		UserID string `json:"user_id"`
	}
	if err := c.get(ctx, c.apiBase+"/user/"+url.PathEscape(c.username), &u); err != nil {
		return "", err
	}
	if u == nil || u.UserID == "" {
		// Misconfiguration, not a client error: surface as 502.
		return "", &domain.UpstreamError{Provider: "sleeper", Err: fmt.Errorf("user %q not found", c.username)}
	}
	c.mu.Lock()
	c.userID = u.UserID
	c.mu.Unlock()
	return u.UserID, nil
}
```

In `ListLeagues`, replace everything from `var u *struct {` through the closing `}` of the `if u == nil { ... }` block with:

```go
	uid, err := c.myUserID(ctx)
	if err != nil {
		return nil, err
	}
```

and in the next line change `u.UserID` to `uid`.

In `League`, after the `rosters, err := c.rosters(ctx, nativeID)` error check, add:

```go
	uid, err := c.myUserID(ctx)
	if err != nil {
		return domain.League{}, err
	}
```

and change the team append to:

```go
		mine := r.OwnerID == uid || slices.Contains(r.CoOwners, uid)
		teams = append(teams, domain.Team{ID: strconv.Itoa(r.RosterID), Name: name, Owner: u.DisplayName, Mine: mine})
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `gofmt -l . ; go vet ./... && go test -race ./...`
Expected: no gofmt output; all packages `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/providers/sleeper
git commit -m "feat(sleeper): mark the configured user's team with mine (owner or co-owner)"
```

---

### Task 3: Rooting aggregation (pure)

**Files:**
- Create: `internal/viewer/rooting.go`
- Modify: `internal/viewer/view.go` (`buildLeagueView` uses `weekNav`)
- Test: `internal/viewer/rooting_test.go`

**Interfaces:**
- Consumes: `domain.Team.Mine`; existing `playerImage`, `Initials`, `APIError`.
- Produces:
  - `type leagueWeek struct { ID, Name string; Platform domain.Platform; Week int; League domain.League; Matchups []domain.Matchup; Err error }`
  - `type appearance struct { League, Href string; For bool; Points *float64 }`
  - `type rootingPlayer struct { Name, NFLTeam, Image, Initials string; IsDEF bool; For, Against, Net int; Apps []appearance; key string }`
  - `type rootingView struct { Season, Week, Prev, Next int; Weeks []int; Warnings, NotPlaying []string; Counted int; Players []rootingPlayer }`
  - `func buildRooting(season, reqWeek int, lws []leagueWeek, warnings []string) rootingView`
  - `func weekNav(week int) (prev, next int, weeks []int)`
  - `func errText(err error) string`

- [ ] **Step 1: Write the failing tests**

Create `internal/viewer/rooting_test.go`:

```go
package viewer

import (
	"reflect"
	"testing"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

func pf(v float64) *float64 { return &v }

func starter(id *string, name string, ids map[string]string, pts float64) domain.RosterEntry {
	return domain.RosterEntry{Slot: "QB", Player: domain.Player{ID: id, Name: name, Position: "QB", NFLTeam: "KC", PlatformIDs: ids}, Points: pf(pts)}
}

func side(team string, starters, bench []domain.RosterEntry) domain.MatchupSide {
	return domain.MatchupSide{TeamID: team, Roster: domain.Roster{TeamID: team, Starters: starters, Bench: bench}}
}

func mkLeague(id, name string, p domain.Platform, week int, mine string, ms ...domain.Matchup) leagueWeek {
	teams := []domain.Team{{ID: "1"}, {ID: "2"}, {ID: "5"}, {ID: "6"}, {ID: "9"}}
	for i := range teams {
		teams[i].Mine = teams[i].ID == mine
	}
	return leagueWeek{ID: id, Name: name, Platform: p, Week: week, League: domain.League{Teams: teams}, Matchups: ms}
}

func TestBuildRooting(t *testing.T) {
	mahomes := func(pts float64) domain.RosterEntry { return starter(sid("4046"), "Patrick Mahomes", nil, pts) }
	kelce := func(pts float64) domain.RosterEntry { return starter(sid("1466"), "Travis Kelce", nil, pts) }
	chase := starter(sid("7564"), "Ja'Marr Chase", nil, 11)
	jj := func(pts float64) domain.RosterEntry {
		return starter(nil, "Justin Jefferson", map[string]string{"espn": "4262921"}, pts)
	}
	ghost := starter(nil, "Empty", map[string]string{}, 0)

	lws := []leagueWeek{
		mkLeague("espn:1", "A", domain.PlatformESPN, 3, "1",
			domain.Matchup{Home: side("1", []domain.RosterEntry{mahomes(18), jj(20), ghost}, []domain.RosterEntry{chase}), Away: side("2", []domain.RosterEntry{kelce(7), ghost}, nil)}),
		mkLeague("sleeper:2", "B", domain.PlatformSleeper, 3, "5",
			domain.Matchup{Home: side("6", []domain.RosterEntry{kelce(9), chase}, nil), Away: side("5", []domain.RosterEntry{mahomes(19)}, nil)}),
		mkLeague("espn:3", "C", domain.PlatformESPN, 3, "1",
			domain.Matchup{Home: side("1", []domain.RosterEntry{jj(21)}, nil), Away: side("2", []domain.RosterEntry{mahomes(17)}, nil)}),
		mkLeague("espn:4", "D", domain.PlatformESPN, 3, "",
			domain.Matchup{Home: side("1", []domain.RosterEntry{mahomes(18)}, nil), Away: side("2", nil, nil)}),
		mkLeague("sleeper:5", "E", domain.PlatformSleeper, 3, "9",
			domain.Matchup{Home: side("1", []domain.RosterEntry{mahomes(18)}, nil), Away: side("2", nil, nil)}),
		{ID: "espn:6", Name: "F", Platform: domain.PlatformESPN, Err: &APIError{Status: 502, Code: "upstream_error", Message: "boom"}},
		mkLeague("espn:7", "G", domain.PlatformESPN, 4, "1",
			domain.Matchup{Home: side("1", []domain.RosterEntry{kelce(3)}, nil), Away: side("2", nil, nil)}),
	}
	v := buildRooting(2026, 0, lws, []string{"upstream note"})

	if v.Season != 2026 || v.Week != 3 || v.Prev != 2 || v.Next != 4 || len(v.Weeks) != 18 {
		t.Fatalf("header = season %d week %d prev %d next %d weeks %d", v.Season, v.Week, v.Prev, v.Next, len(v.Weeks))
	}
	wantWarn := []string{"upstream note", "D: couldn't find your team", "F: boom", "G: returned week 4"}
	if !reflect.DeepEqual(v.Warnings, wantWarn) {
		t.Errorf("warnings = %q, want %q", v.Warnings, wantWarn)
	}
	if !reflect.DeepEqual(v.NotPlaying, []string{"E"}) || v.Counted != 3 {
		t.Errorf("notPlaying = %v counted = %d, want [E] 3", v.NotPlaying, v.Counted)
	}
	type row struct {
		Name              string
		For, Against, Net int
		Apps              int
	}
	var got []row
	for _, p := range v.Players {
		got = append(got, row{p.Name, p.For, p.Against, p.Net, len(p.Apps)})
	}
	want := []row{
		{"Justin Jefferson", 2, 0, 2, 2}, // unmapped: merged by espn:4262921 across A and C
		{"Travis Kelce", 0, 2, -2, 2},
		{"Patrick Mahomes", 2, 1, 1, 3},
	} // Chase: 1 starter appearance (bench in A ignored) → excluded; "Empty" has no key → skipped
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("players = %+v, want %+v", got, want)
	}
	m := v.Players[2]
	wantApps := []appearance{
		{League: "A", Href: "/league/espn:1?week=3", For: true, Points: pf(18)},
		{League: "B", Href: "/league/sleeper:2?week=3", For: true, Points: pf(19)},
		{League: "C", Href: "/league/espn:3?week=3", For: false, Points: pf(17)},
	}
	if !reflect.DeepEqual(m.Apps, wantApps) {
		t.Errorf("Mahomes apps = %+v", m.Apps)
	}
	if m.Image != "https://sleepercdn.com/content/nfl/players/thumb/4046.jpg" || m.Initials != "PM" || m.NFLTeam != "KC" {
		t.Errorf("Mahomes view = %+v", m)
	}
}

func TestBuildRootingWeekFromFirstLoaded(t *testing.T) {
	lws := []leagueWeek{
		{ID: "espn:6", Name: "F", Err: &APIError{Status: 502, Message: "boom"}},
		mkLeague("espn:1", "A", domain.PlatformESPN, 5, "1", domain.Matchup{Home: side("1", nil, nil), Away: side("2", nil, nil)}),
	}
	v := buildRooting(2026, 0, lws, nil)
	if v.Week != 5 || v.Counted != 1 {
		t.Fatalf("week = %d counted = %d, want 5 and 1", v.Week, v.Counted)
	}
}

func TestBuildRootingRequestedWeek(t *testing.T) {
	lws := []leagueWeek{mkLeague("espn:1", "A", domain.PlatformESPN, 2, "1", domain.Matchup{Home: side("1", nil, nil), Away: side("2", nil, nil)})}
	v := buildRooting(2026, 2, lws, nil)
	if v.Week != 2 || v.Counted != 1 || len(v.Warnings) != 0 || v.Players != nil {
		t.Fatalf("view = %+v", v)
	}
}

func TestWeekNav(t *testing.T) {
	if p, n, w := weekNav(1); p != 0 || n != 2 || len(w) != 18 || w[0] != 1 || w[17] != 18 {
		t.Errorf("weekNav(1) = %d %d %v", p, n, w)
	}
	if p, n, _ := weekNav(18); p != 17 || n != 0 {
		t.Errorf("weekNav(18) = %d %d", p, n)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/viewer/`
Expected: FAIL — compile errors `undefined: leagueWeek`, `undefined: buildRooting`, `undefined: appearance`, `undefined: weekNav`.

- [ ] **Step 3: Implement**

Create `internal/viewer/rooting.go`:

```go
package viewer

import (
	"cmp"
	"errors"
	"fmt"
	"net/url"
	"slices"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

// leagueWeek is one league's data for the rooting guide; Err is set when it failed to load.
type leagueWeek struct {
	ID, Name string
	Platform domain.Platform
	Week     int // meta.week of the matchups response
	League   domain.League
	Matchups []domain.Matchup
	Err      error
}

// appearance is one of my matchups a player starts in, for me or against me.
type appearance struct {
	League, Href string
	For          bool
	Points       *float64
}

type rootingPlayer struct {
	Name, NFLTeam, Image, Initials string
	IsDEF                          bool
	For, Against, Net              int
	Apps                           []appearance
	key                            string
}

type rootingView struct {
	Season, Week, Prev, Next int
	Weeks                    []int
	Warnings, NotPlaying     []string
	Counted                  int
	Players                  []rootingPlayer
}

// buildRooting counts every starter in my matchup of each league: my starters are "for",
// my opponent's "against". Players with 2+ appearances are listed, biggest |net| first.
// reqWeek 0 means the API's current week, taken from the first league that loaded.
func buildRooting(season, reqWeek int, lws []leagueWeek, warnings []string) rootingView {
	v := rootingView{Season: season, Week: reqWeek, Warnings: slices.Clone(warnings)}
	if v.Week == 0 {
		for _, lw := range lws {
			if lw.Err == nil {
				v.Week = lw.Week
				break
			}
		}
	}
	v.Prev, v.Next, v.Weeks = weekNav(v.Week)
	byKey := map[string]*rootingPlayer{}
	for _, lw := range lws {
		if lw.Err != nil {
			v.Warnings = append(v.Warnings, lw.Name+": "+errText(lw.Err))
			continue
		}
		if lw.Week != v.Week {
			v.Warnings = append(v.Warnings, fmt.Sprintf("%s: returned week %d", lw.Name, lw.Week))
			continue
		}
		mine := ""
		for _, t := range lw.League.Teams {
			if t.Mine {
				mine = t.ID
				break
			}
		}
		if mine == "" {
			v.Warnings = append(v.Warnings, lw.Name+": couldn't find your team")
			continue
		}
		var me, opp domain.MatchupSide
		found := false
		for _, m := range lw.Matchups {
			if m.Home.TeamID == mine {
				me, opp, found = m.Home, m.Away, true
				break
			}
			if m.Away.TeamID == mine {
				me, opp, found = m.Away, m.Home, true
				break
			}
		}
		if !found {
			v.NotPlaying = append(v.NotPlaying, lw.Name)
			continue
		}
		v.Counted++
		href := fmt.Sprintf("/league/%s?week=%d", url.PathEscape(lw.ID), v.Week)
		add := func(s domain.MatchupSide, isFor bool) {
			for _, e := range s.Roster.Starters {
				key := playerKey(e.Player, lw.Platform)
				if key == "" {
					continue
				}
				p := byKey[key]
				if p == nil {
					p = &rootingPlayer{Name: e.Player.Name, NFLTeam: e.Player.NFLTeam, Image: playerImage(e.Player),
						Initials: Initials(e.Player.Name), IsDEF: e.Player.Position == "DEF", key: key}
					byKey[key] = p
				}
				p.Apps = append(p.Apps, appearance{League: lw.Name, Href: href, For: isFor, Points: e.Points})
				if isFor {
					p.For++
				} else {
					p.Against++
				}
			}
		}
		add(me, true)
		add(opp, false)
	}
	for _, p := range byKey {
		if p.For+p.Against < 2 {
			continue
		}
		p.Net = p.For - p.Against
		v.Players = append(v.Players, *p)
	}
	slices.SortFunc(v.Players, func(a, b rootingPlayer) int {
		return cmp.Or(
			cmp.Compare(absInt(b.Net), absInt(a.Net)),
			cmp.Compare(b.For+b.Against, a.For+a.Against),
			cmp.Compare(a.Name, b.Name),
			cmp.Compare(a.key, b.key),
		)
	})
	return v
}

// playerKey merges a player across leagues by canonical ID, falling back to the
// league platform's own ID; "" when neither exists.
func playerKey(p domain.Player, platform domain.Platform) string {
	if p.ID != nil && *p.ID != "" {
		return *p.ID
	}
	if id := p.PlatformIDs[string(platform)]; id != "" {
		return string(platform) + ":" + id
	}
	return ""
}

func weekNav(week int) (prev, next int, weeks []int) {
	if week > 1 {
		prev = week - 1
	}
	if week < 18 {
		next = week + 1
	}
	for w := 1; w <= 18; w++ {
		weeks = append(weeks, w)
	}
	return prev, next, weeks
}

// errText is the human part of an API error, for one-line warnings.
func errText(err error) string {
	var ae *APIError
	if errors.As(err, &ae) && ae.Message != "" {
		return ae.Message
	}
	return err.Error()
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
```

In `internal/viewer/view.go` `buildLeagueView`, replace

```go
	if v.Week > 1 {
		v.Prev = v.Week - 1
	}
	if v.Week < 18 {
		v.Next = v.Week + 1
	}
	for w := 1; w <= 18; w++ {
		v.Weeks = append(v.Weeks, w)
	}
```

with

```go
	v.Prev, v.Next, v.Weeks = weekNav(v.Week)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `gofmt -l . ; go vet ./... && go test -race ./internal/viewer/`
Expected: no gofmt output; `ok` (all existing viewer tests, including week-bounds tests, still pass).

- [ ] **Step 5: Commit**

```bash
git add internal/viewer/rooting.go internal/viewer/rooting_test.go internal/viewer/view.go
git commit -m "feat(viewer): aggregate starters across leagues for the rooting guide"
```

---

### Task 4: `/rooting` page

**Files:**
- Modify: `internal/viewer/handlers.go` (route, `weekParam`, `rooting`, `net` func, `league` uses `weekParam`)
- Create: `internal/viewer/templates/rooting.html`
- Modify: `internal/viewer/templates/base.html` (CSS), `internal/viewer/templates/leagues.html` (link)
- Test: `internal/viewer/rooting_test.go` (append)

**Interfaces:**
- Consumes: `leagueWeek`, `buildRooting`, `rootingView`, `rootingPlayer`, `appearance` (Task 3); `avatar` template (league.html).
- Produces: `GET /rooting`; template func `net(int) string`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/viewer/rooting_test.go` (add imports `"io"`, `"net/http"`, `"net/http/httptest"`, `"strings"`, `"time"`):

```go
const rootingLeagues = `{"data":[{"id":"espn:1","platform":"espn","season":2026,"name":"Office League"},{"id":"sleeper:2","platform":"sleeper","season":2026,"name":"Dynasty"},{"id":"espn:9","platform":"espn","season":2026,"name":"Broken"}],"meta":{"season":2026,"warnings":[]}}`

func rootingPlayerJSON(id, name string, pts float64) string {
	return fmt.Sprintf(`{"slot":"QB","player":{"id":%q,"name":%q,"position":"QB","nflTeam":"KC","platformIds":{}},"stats":{},"points":%g}`, id, name, pts)
}

var rootingRoutes = map[string]string{
	"/v1/nfl/leagues": rootingLeagues,
	"/v1/nfl/leagues/espn:1": `{"data":{"id":"espn:1","name":"Office League","teams":[{"id":"1","name":"Team Varun","owner":"v","mine":true},{"id":"2","name":"Alex","owner":"a"}]},"meta":{"warnings":[]}}`,
	"/v1/nfl/leagues/espn:1/matchups": `{"data":[{"week":WEEK,"home":{"teamId":"1","points":18,"roster":{"teamId":"1","starters":[` + rootingPlayerJSON("4046", "Patrick Mahomes", 18) + `],"bench":[],"reserve":[]}},
 "away":{"teamId":"2","points":7,"roster":{"teamId":"2","starters":[` + rootingPlayerJSON("1466", "Travis Kelce", 7) + `],"bench":[],"reserve":[]}}}],"meta":{"season":2026,"week":WEEK,"warnings":[]}}`,
	"/v1/nfl/leagues/sleeper:2": `{"data":{"id":"sleeper:2","name":"Dynasty","teams":[{"id":"5","name":"Mine","owner":"v","mine":true},{"id":"6","name":"Sam","owner":"s"}]},"meta":{"warnings":[]}}`,
	"/v1/nfl/leagues/sleeper:2/matchups": `{"data":[{"week":WEEK,"home":{"teamId":"6","points":9.5,"roster":{"teamId":"6","starters":[` + rootingPlayerJSON("1466", "Travis Kelce", 9.5) + `],"bench":[],"reserve":[]}},
 "away":{"teamId":"5","points":20,"roster":{"teamId":"5","starters":[` + rootingPlayerJSON("4046", "Patrick Mahomes", 20) + `],"bench":[],"reserve":[]}}}],"meta":{"season":2026,"week":WEEK,"warnings":[]}}`,
}

// newRootingHandler serves routes (WEEK replaced by ?week= or 3); any other path is a 502.
// Matchups without include=stats are rejected, since points need stats.
func newRootingHandler(t *testing.T, routes map[string]string) http.Handler {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, `{"error":{"code":"upstream_error","message":"upstream down"}}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/matchups") && r.URL.Query().Get("include") != "stats" {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"code":"invalid_param","message":"include=stats missing"}}`)
			return
		}
		week := r.URL.Query().Get("week")
		if week == "" {
			week = "3"
		}
		io.WriteString(w, strings.ReplaceAll(body, "WEEK", week))
	}))
	t.Cleanup(srv.Close)
	return NewHandler(&Client{BaseURL: srv.URL, APIKey: "k", HTTP: &http.Client{Timeout: 5 * time.Second}})
}

func TestRootingPage(t *testing.T) {
	code, body := get(t, newRootingHandler(t, rootingRoutes), "/rooting")
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	mustContain(t, body,
		"<h1>Rooting guide</h1>",
		"2026 · week 3 · 2 leagues counted",
		`<div class="warn">Broken: upstream down</div>`,
		`class="net pos">&#43;2<small>2 for · 0 against</small>`,
		`class="net neg">−2<small>0 for · 2 against</small>`,
		`class="chip for" href="/league/espn:1?week=3">Office League<b>18.00</b>`,
		`class="chip against" href="/league/sleeper:2?week=3">Dynasty<b>9.50</b>`,
		`PM<img src="https://sleepercdn.com/content/nfl/players/thumb/4046.jpg"`,
		`href="?week=2"`, `href="?week=4"`,
	)
	if strings.Index(body, "Patrick Mahomes") > strings.Index(body, "Travis Kelce") {
		t.Error("equal |net| and appearances should sort by name")
	}
}

func TestRootingPassesWeek(t *testing.T) {
	_, body := get(t, newRootingHandler(t, rootingRoutes), "/rooting?week=2")
	mustContain(t, body, "2026 · week 2 · 2 leagues counted", `href="/league/espn:1?week=2"`)
}

func TestRootingInvalidWeek(t *testing.T) {
	code, body := get(t, newRootingHandler(t, rootingRoutes), "/rooting?week=0")
	if code != http.StatusBadRequest {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "Invalid week", "week must be a number from 1 to 18")
}

func TestRootingAllLeaguesFail(t *testing.T) {
	routes := map[string]string{"/v1/nfl/leagues": `{"data":[{"id":"espn:9","platform":"espn","season":2026,"name":"Broken"}],"meta":{"season":2026,"warnings":[]}}`}
	code, body := get(t, newRootingHandler(t, routes), "/rooting")
	if code != http.StatusBadGateway {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "upstream down")
}

func TestRootingNoSharedPlayers(t *testing.T) {
	routes := map[string]string{
		"/v1/nfl/leagues":                 `{"data":[{"id":"espn:1","platform":"espn","season":2026,"name":"Office League"}],"meta":{"season":2026,"warnings":[]}}`,
		"/v1/nfl/leagues/espn:1":          rootingRoutes["/v1/nfl/leagues/espn:1"],
		"/v1/nfl/leagues/espn:1/matchups": rootingRoutes["/v1/nfl/leagues/espn:1/matchups"],
	}
	code, body := get(t, newRootingHandler(t, routes), "/rooting")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "No shared players this week.", "1 leagues counted")
}

func TestRootingBeforeRedeploy(t *testing.T) {
	_, h := newTestHandler(t) // its leagues carry no mine flag; sleeper:2 is a 404
	code, body := get(t, h, "/rooting")
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	mustContain(t, body, "Office League: couldn&#39;t find your team", "Dynasty: route not found", "No shared players this week.")
}

func TestLeaguesPageLinksRooting(t *testing.T) {
	_, h := newTestHandler(t)
	_, body := get(t, h, "/")
	mustContain(t, body, `href="/rooting"`, "Rooting guide")
}
```

Also add `"fmt"` to the imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/viewer/ -run 'TestRooting|TestLeaguesPageLinksRooting'`
Expected: FAIL — `/rooting` returns 404 (status 404 / page missing strings); leagues page missing `href="/rooting"`.

- [ ] **Step 3: Implement the handler**

In `internal/viewer/handlers.go`:

Add import `"golang.org/x/sync/errgroup"`.

Add to `funcs`:

```go
	"net": func(n int) string {
		switch {
		case n > 0:
			return fmt.Sprintf("+%d", n)
		case n < 0:
			return fmt.Sprintf("−%d", -n)
		}
		return "0"
	},
```

In `NewHandler`, add after the league route:

```go
	mux.HandleFunc("GET /rooting", s.rooting)
```

and update its doc comment to: `// NewHandler serves the leagues list at /, one league at /league/{id}, and the rooting guide at /rooting.`

Add:

```go
// weekParam validates ?week= (absent → 0). On a bad value it renders the error page and
// returns ok=false.
func (s *server) weekParam(w http.ResponseWriter, r *http.Request) (week int, ok bool) {
	wk := r.URL.Query().Get("week")
	if wk == "" {
		return 0, true
	}
	n, err := strconv.Atoi(wk)
	if err != nil || n < 1 || n > 18 {
		s.render(w, http.StatusBadRequest, "error.html", errorView{Title: "Invalid week", Message: "week must be a number from 1 to 18"})
		return 0, false
	}
	return n, true
}

func (s *server) rooting(w http.ResponseWriter, r *http.Request) {
	week, ok := s.weekParam(w, r)
	if !ok {
		return
	}
	var ls []domain.LeagueSummary
	meta, err := s.c.Get(r.Context(), "/v1/nfl/leagues", nil, &ls)
	if err != nil {
		s.fail(w, err)
		return
	}
	q := url.Values{"include": {"stats"}}
	if week > 0 {
		q.Set("week", strconv.Itoa(week))
	}
	lws := make([]leagueWeek, len(ls))
	var g errgroup.Group
	for i, l := range ls {
		g.Go(func() error {
			lw := leagueWeek{ID: l.ID, Name: l.Name, Platform: l.Platform}
			base := "/v1/nfl/leagues/" + url.PathEscape(l.ID)
			if _, lw.Err = s.c.Get(r.Context(), base, nil, &lw.League); lw.Err == nil {
				var m Meta
				m, lw.Err = s.c.Get(r.Context(), base+"/matchups", q, &lw.Matchups)
				lw.Week = m.Week
			}
			lws[i] = lw
			return nil // per-league failures become warnings
		})
	}
	_ = g.Wait()
	var firstErr error
	loaded := 0
	for _, lw := range lws {
		if lw.Err == nil {
			loaded++
		} else if firstErr == nil {
			firstErr = lw.Err
		}
	}
	if loaded == 0 && firstErr != nil {
		s.fail(w, firstErr)
		return
	}
	s.render(w, http.StatusOK, "rooting.html", buildRooting(meta.Season, week, lws, meta.Warnings))
}
```

In `league`, replace

```go
	if wk := r.URL.Query().Get("week"); wk != "" {
		n, err := strconv.Atoi(wk)
		if err != nil || n < 1 || n > 18 {
			s.render(w, http.StatusBadRequest, "error.html", errorView{Title: "Invalid week", Message: "week must be a number from 1 to 18"})
			return
		}
		q.Set("week", wk)
	}
```

with

```go
	week, ok := s.weekParam(w, r)
	if !ok {
		return
	}
	if week > 0 {
		q.Set("week", strconv.Itoa(week))
	}
```

- [ ] **Step 4: Implement the templates**

Create `internal/viewer/templates/rooting.html`:

```html
{{template "head" "Rooting guide"}}
<div class="card">
<a href="/">← All leagues</a>
<h1>Rooting guide</h1>
<div class="muted">{{.Season}} · week {{.Week}} · {{.Counted}} leagues counted{{if .NotPlaying}} · not playing this week: {{range $i, $n := .NotPlaying}}{{if $i}}, {{end}}{{$n}}{{end}}{{end}}</div>
<div class="nav">
{{if .Prev}}<a href="?week={{.Prev}}">‹ Week {{.Prev}}</a>{{end}}
<form method="get"><select name="week">{{range .Weeks}}<option value="{{.}}"{{if eq . $.Week}} selected{{end}}>Week {{.}}</option>{{end}}</select> <button>Go</button></form>
{{if .Next}}<a href="?week={{.Next}}">Week {{.Next}} ›</a>{{end}}
<a href="?week={{.Week}}">⟳ Refresh</a>
</div>
</div>
{{range .Warnings}}<div class="warn">{{.}}</div>{{end}}
{{if not .Players}}<div class="card empty">No shared players this week.</div>{{end}}
{{range .Players}}<div class="card rp">
{{template "avatar" .}}
<span class="who"><span class="name">{{.Name}}</span><span class="sub">{{.NFLTeam}}</span></span>
<span class="net {{if gt .Net 0}}pos{{else if lt .Net 0}}neg{{else}}zero{{end}}">{{net .Net}}<small>{{.For}} for · {{.Against}} against</small></span>
<div class="apps">{{range .Apps}}<a class="chip {{if .For}}for{{else}}against{{end}}" href="{{.Href}}">{{.League}}<b>{{pts .Points}}</b></a>{{end}}</div>
</div>
{{end}}
{{template "foot"}}
```

In `internal/viewer/templates/base.html`, insert immediately before `</style>`:

```css
.rp{display:grid;grid-template-columns:auto 1fr auto;gap:.5rem .8rem;align-items:center}
.rp .apps{grid-column:1/-1;display:flex;flex-wrap:wrap;gap:.35rem}
.net{font-size:1.35rem;font-weight:800;text-align:right;font-variant-numeric:tabular-nums}
.net small{display:block;font-size:11px;font-weight:400;color:var(--muted)}
.net.pos{color:var(--win)} .net.neg{color:var(--loss)} .net.zero{color:var(--muted)}
a.chip{color:var(--text)} a.chip:hover{text-decoration:none;filter:brightness(1.25)}
.chip.for{background:#173a2a} .chip.for b{color:var(--win)}
.chip.against{background:var(--loss-bg)} .chip.against b{color:var(--loss)}
```

In `internal/viewer/templates/leagues.html`, after the `<h1>` line add:

```html
<p><a href="/rooting">Rooting guide →</a></p>
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `gofmt -l . ; go vet ./... && go test -race ./...`
Expected: no gofmt output; all packages `ok`, including every pre-existing viewer test unchanged.

- [ ] **Step 6: Commit**

```bash
git add internal/viewer
git commit -m "feat(viewer): rooting guide page across all leagues"
```

- [ ] **Step 7 (controller): Deploy and live check**

This needs the owner's go-ahead (it pushes and deploys). After merge/deploy of the API change: `make viewer` (the owner's running viewer must be restarted to pick up the new route), open `/rooting` for weeks 2 and 3, confirm every league is counted (no "couldn't find your team"), and spot-check two players' chips against their league pages.
