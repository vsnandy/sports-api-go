# ESPN Scoring Fidelity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** ESPN league matchups report ESPN's own per-player points, and the scoring engine reproduces ESPN league rules (per-position overrides, points/yards-allowed tiers, more stat IDs) to ≥ 98% exact, proven by a local audit tool.

**Architecture:** `domain.Scoring` adds per-position rule replacements and derived indicator stats on top of the existing flat `ScoringRules`; `scoring.Points`/`Breakdown` take a `Scoring` and a position. The ESPN adapter translates `pointsOverrides` and tier stat IDs into that model and exposes each rostered player's ESPN week total (`PlatformPoints`), which the service prefers over computed points. `internal/scoreaudit` + `cmd/scoreaudit` compare engine vs ESPN per stat ID.

**Tech Stack:** Go (stdlib `testing`, existing deps only: aws-sdk-go-v2 config/ssm for the audit's cookie lookup).

**Spec:** `docs/superpowers/specs/2026-09-28-espn-scoring-fidelity-design.md`

## Global Constraints

- No new third-party dependencies.
- Sleeper leagues and presets (`ppr`/`half`/`std`) must produce exactly the same points as before; existing value assertions stay unchanged.
- API stays non-breaking: `League.scoring` remains the flat base table; new `scoringByPosition` (object, never `null`) and `derivedStats` (array, never `null`); `RosterEntry.pointsSource` is `"platform"` or `"computed"` and omitted when `points` is null.
- Positions are Sleeper strings `QB RB WR TE K DEF`. ESPN override slot keys map `0→QB 2→RB 4→WR 6→TE 16→DEF 17→K`.
- Derived keys: points allowed `espn_pa_0` [0,0], `espn_pa_1_6` [1,6], `espn_pa_7_13` [7,13], `espn_pa_14_17` [14,17], `espn_pa_18_21` [18,21], `espn_pa_22_27` [22,27], `espn_pa_28_34` [28,34], `espn_pa_35_45` [35,45], `espn_pa_46p` [46,∞) from `pts_allow` (ESPN 89,90,91,92,121,122,123,124,125); yards allowed `espn_ya_0_99`, `espn_ya_100_199`, `espn_ya_200_299`, `espn_ya_300_349`, `espn_ya_350_399`, `espn_ya_400_449`, `espn_ya_450_499`, `espn_ya_500_549`, `espn_ya_550p` from `yds_allow` (ESPN 128–136). Ranges inclusive.
- Exact means |engine − ESPN| < 0.01; audit pass threshold 0.98.
- Tests make no network calls. Only Task 5 contacts live ESPN, run by the controller with the owner's approval; secrets are never printed.

## Review Focus

1. **ESPN entry carrying projection, actual, and other-week stat rows** → only the actual (`statSourceId 0`, `statSplitTypeId 1`) row for the requested week is used. (Task 3: `TestMatchupsPlatformPoints`.)
2. **Non-DEF stat lines (no `pts_allow`/`yds_allow`) and nil stat maps with derived stats configured** → no phantom tier points, no panic. (Task 1: `TestDerivedStats`.)
3. **Tier boundary values (17 vs 18, 99 vs 100, 46 and above)** → the inclusive range picks exactly one tier. (Task 1: `TestDerivedStats`; Task 2: `TestTierTable`.)
4. **Override keyed by a slot we don't translate (e.g. FLEX `"23"`)** → reported in `unsupportedRules`, no crash. (Task 2: `TestConvertScoringUnknownOverrideSlot`.)
5. **Week stats unavailable while ESPN platform points exist** → players still get ESPN points with `pointsSource: "platform"` alongside the `stats_unavailable` warning. (Task 3: `TestMatchupsPlatformPointsSurviveStatsFailure`.)

---

## File Map

```
internal/domain/domain.go                 # MODIFY: Scoring, DerivedStat, League fields + ScoringModel, RosterEntryRef.PlatformPoints, RosterEntry.PointsSource
internal/scoring/scoring.go               # MODIFY: Points/Breakdown(stats, Scoring, position), Preset → Scoring
internal/scoring/scoring_test.go          # MODIFY
internal/providers/sleeper/leagues.go     # MODIFY: non-nil ScoringByPosition/DerivedStats
internal/providers/sleeper/sleeper_test.go# MODIFY
internal/service/leagues.go               # MODIFY: attachStats → attachPoints
internal/service/players.go               # MODIFY: rulesFor → *domain.Scoring
internal/service/service_test.go          # MODIFY
internal/service/fakes_test.go            # MODIFY: ptr helpers
internal/providers/espn/tables.go         # MODIFY: tiers, overrideSlots, statKeys, ESPNStatIDForKey
internal/providers/espn/league.go         # MODIFY: convertScoring, stat rows, PlatformPoints, WeekPlayerPoints
internal/providers/espn/scoring_test.go   # CREATE
internal/providers/espn/testdata/scoring_items.json  # CREATE
internal/providers/espn/testdata/league.json          # MODIFY: stat rows on week-3 away entries
internal/providers/espn/espn_test.go      # MODIFY
internal/scoreaudit/scoreaudit.go         # CREATE
internal/scoreaudit/scoreaudit_test.go    # CREATE
cmd/scoreaudit/main.go                    # CREATE
cmd/scoreaudit/main_test.go               # CREATE
Makefile                                  # MODIFY: scoreaudit target
docs/superpowers/specs/2026-09-28-espn-scoring-fidelity-design.md  # MODIFY (§5 default leagues)
```

---

### Task 1: Scoring model and engine

**Files:**
- Modify: `internal/domain/domain.go`, `internal/scoring/scoring.go`, `internal/scoring/scoring_test.go`, `internal/providers/sleeper/leagues.go`, `internal/providers/sleeper/sleeper_test.go`, `internal/service/leagues.go`, `internal/service/players.go`

**Interfaces:**
- Consumes: existing `domain.ScoringRules`, `domain.League`.
- Produces:
  - `domain.Scoring{Rules ScoringRules; ByPosition map[string]ScoringRules; Derived []DerivedStat}`
  - `domain.DerivedStat{Key, From string; Min float64; Max *float64}` (JSON `key`,`from`,`min`,`max`)
  - `domain.League` fields `ScoringByPosition map[string]ScoringRules` (`scoringByPosition`), `DerivedStats []DerivedStat` (`derivedStats`); `func (l League) ScoringModel() Scoring`
  - `scoring.Points(stats map[string]float64, s domain.Scoring, position string) float64`
  - `scoring.Breakdown(stats map[string]float64, s domain.Scoring, position string) map[string]float64` (non-zero contributions only, unrounded)
  - `scoring.Preset(name string) (domain.Scoring, bool)`
  - service `rulesFor(...) (*domain.Scoring, []string, error)`; `attachStats(r domain.Roster, lines map[string]domain.StatLine, s domain.Scoring)` (replaced in Task 3)

- [ ] **Step 1: Write the failing tests**

Replace `internal/scoring/scoring_test.go` with:

```go
package scoring

import (
	"math"
	"testing"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

func flat(r domain.ScoringRules) domain.Scoring { return domain.Scoring{Rules: r} }

func upTo(v float64) *float64 { return &v }

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
			if got := Points(tt.stats, flat(rules), "QB"); got != tt.want {
				t.Fatalf("Points = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPresets(t *testing.T) {
	wr := map[string]float64{"rec": 6, "rec_yd": 85, "rec_td": 1}
	for name, want := range map[string]float64{"ppr": 20.5, "half": 17.5, "std": 14.5} {
		s, ok := Preset(name)
		if !ok {
			t.Fatalf("Preset(%q) missing", name)
		}
		if got := Points(wr, s, "WR"); got != want {
			t.Errorf("%s points = %v, want %v", name, got, want)
		}
	}
	if _, ok := Preset("bogus"); ok {
		t.Fatal("unknown preset should not resolve")
	}
	a, _ := Preset("ppr")
	a.Rules["rec"] = 99
	b, _ := Preset("ppr")
	if b.Rules["rec"] != 1 {
		t.Fatal("Preset must return a fresh copy")
	}
}

func TestTwoPointConversions(t *testing.T) {
	s, _ := Preset("std")
	stats := map[string]float64{"pass_2pt": 1, "rush_2pt": 1, "rec_2pt": 1}
	if got := Points(stats, s, "QB"); got != 6 {
		t.Fatalf("2pt conversions = %v, want 6", got)
	}
}

func TestByPositionReplacesBase(t *testing.T) {
	s := domain.Scoring{
		Rules:      domain.ScoringRules{"rec": 0.5, "sack": 0},
		ByPosition: map[string]domain.ScoringRules{"TE": {"rec": 1.5}, "DEF": {"sack": 1}},
	}
	line := map[string]float64{"rec": 4, "sack": 3}
	if got := Points(line, s, "TE"); got != 6 {
		t.Errorf("TE = %v, want 6 (rec replaced, sack base 0)", got)
	}
	if got := Points(line, s, "WR"); got != 2 {
		t.Errorf("WR = %v, want 2 (base rules only)", got)
	}
	if got := Points(line, s, "DEF"); got != 5 {
		t.Errorf("DEF = %v, want 5 (rec base 0.5×4 + sack override 1×3)", got)
	}
}

func TestDerivedStats(t *testing.T) {
	s := domain.Scoring{
		Rules: domain.ScoringRules{},
		ByPosition: map[string]domain.ScoringRules{"DEF": {
			"espn_pa_14_17": 1, "espn_pa_18_21": 0.5, "espn_pa_46p": -5,
		}},
		Derived: []domain.DerivedStat{
			{Key: "espn_pa_14_17", From: "pts_allow", Min: 14, Max: upTo(17)},
			{Key: "espn_pa_18_21", From: "pts_allow", Min: 18, Max: upTo(21)},
			{Key: "espn_pa_46p", From: "pts_allow", Min: 46},
		},
	}
	tests := []struct {
		name  string
		stats map[string]float64
		want  float64
	}{
		{"lower bound inclusive", map[string]float64{"pts_allow": 14}, 1},
		{"upper bound inclusive", map[string]float64{"pts_allow": 17}, 1},
		{"next tier starts at 18", map[string]float64{"pts_allow": 18}, 0.5},
		{"unbounded tier", map[string]float64{"pts_allow": 70}, -5},
		{"gap between tiers scores nothing", map[string]float64{"pts_allow": 30}, 0},
		{"no raw stat, no tier", map[string]float64{"sack": 2}, 0},
		{"nil stats", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Points(tt.stats, s, "DEF"); got != tt.want {
				t.Fatalf("Points = %v, want %v", got, tt.want)
			}
		})
	}
	if got := Points(map[string]float64{"pts_allow": 14}, s, "QB"); got != 0 {
		t.Errorf("tier rule is DEF-only; QB got %v", got)
	}
	in := map[string]float64{"pts_allow": 14}
	Points(in, s, "DEF")
	if _, leaked := in["espn_pa_14_17"]; leaked {
		t.Error("Points must not write derived stats into the caller's map")
	}
}

func TestBreakdownSumsToPoints(t *testing.T) {
	s := domain.Scoring{
		Rules:      domain.ScoringRules{"pass_yd": 0.04, "pass_td": 4},
		ByPosition: map[string]domain.ScoringRules{"DEF": {"sack": 1, "espn_pa_14_17": 1}},
		Derived:    []domain.DerivedStat{{Key: "espn_pa_14_17", From: "pts_allow", Min: 14, Max: upTo(17)}},
	}
	line := map[string]float64{"sack": 3, "pts_allow": 14, "pass_yd": 0}
	b := Breakdown(line, s, "DEF")
	if b["sack"] != 3 || b["espn_pa_14_17"] != 1 || len(b) != 2 {
		t.Fatalf("Breakdown = %v, want sack 3 and espn_pa_14_17 1 only", b)
	}
	var sum float64
	for _, v := range b {
		sum += v
	}
	if math.Round(sum*100)/100 != Points(line, s, "DEF") {
		t.Fatalf("Breakdown sum %v != Points %v", sum, Points(line, s, "DEF"))
	}
}
```

In `internal/providers/sleeper/sleeper_test.go`, add at the end of `TestLeague` (after the `UnsupportedRules` check):

```go
	if l.ScoringByPosition == nil || len(l.ScoringByPosition) != 0 {
		t.Errorf("ScoringByPosition = %#v, want empty non-nil", l.ScoringByPosition)
	}
	if l.DerivedStats == nil || len(l.DerivedStats) != 0 {
		t.Errorf("DerivedStats = %#v, want empty non-nil", l.DerivedStats)
	}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/scoring/ ./internal/providers/sleeper/`
Expected: FAIL — build errors such as `cannot use flat(rules) (value of type domain.Scoring)` / `undefined: domain.Scoring` / `l.ScoringByPosition undefined`.

- [ ] **Step 3: Implement the model**

In `internal/domain/domain.go`, add two fields to `League` right after `Scoring`:

```go
	Scoring           ScoringRules            `json:"scoring"`
	ScoringByPosition map[string]ScoringRules `json:"scoringByPosition"`
	DerivedStats      []DerivedStat           `json:"derivedStats"`
```

and after the `ScoringRules` type declaration add:

```go
// Scoring is a league's full scoring model: base rules, per-position replacements
// (e.g. ESPN's D/ST values), and indicator stats derived from raw stats (tiers).
type Scoring struct {
	Rules      ScoringRules
	ByPosition map[string]ScoringRules
	Derived    []DerivedStat
}

// DerivedStat sets Key to 1 when the raw stat From lies in [Min, Max]; nil Max is unbounded.
type DerivedStat struct {
	Key  string   `json:"key"`
	From string   `json:"from"`
	Min  float64  `json:"min"`
	Max  *float64 `json:"max"`
}

// ScoringModel assembles the league's scoring for the engine.
func (l League) ScoringModel() Scoring {
	return Scoring{Rules: l.Scoring, ByPosition: l.ScoringByPosition, Derived: l.DerivedStats}
}
```

Replace the `Points` function and `Preset` in `internal/scoring/scoring.go` (keep `base` unchanged):

```go
// Breakdown returns the points each stat contributes (derived stats included),
// unrounded, omitting zero contributions. A stat's weight is the position's
// replacement rule if present, otherwise the base rule.
func Breakdown(stats map[string]float64, s domain.Scoring, position string) map[string]float64 {
	all := withDerived(stats, s.Derived)
	pos := s.ByPosition[position]
	out := map[string]float64{}
	for k, v := range all {
		w, ok := pos[k]
		if !ok {
			w, ok = s.Rules[k]
		}
		if ok && v*w != 0 {
			out[k] = v * w
		}
	}
	return out
}

// Points returns the sum of Breakdown rounded to 2 decimals.
func Points(stats map[string]float64, s domain.Scoring, position string) float64 {
	var total float64
	for _, p := range Breakdown(stats, s, position) {
		total += p
	}
	return math.Round(total*100) / 100
}

// withDerived returns stats plus a 1 for every derived stat whose raw value is in range.
// The caller's map is never modified.
func withDerived(stats map[string]float64, derived []domain.DerivedStat) map[string]float64 {
	if len(derived) == 0 {
		return stats
	}
	out := make(map[string]float64, len(stats)+len(derived))
	maps.Copy(out, stats)
	for _, d := range derived {
		v, ok := stats[d.From]
		if ok && v >= d.Min && (d.Max == nil || v <= *d.Max) {
			out[d.Key] = 1
		}
	}
	return out
}
```

```go
// Preset returns a fresh copy of the named preset: "ppr", "half", or "std".
func Preset(name string) (domain.Scoring, bool) {
	var rec float64
	switch name {
	case "ppr":
		rec = 1
	case "half":
		rec = 0.5
	case "std":
		rec = 0
	default:
		return domain.Scoring{}, false
	}
	r := maps.Clone(base)
	r["rec"] = rec
	return domain.Scoring{Rules: r}, true
}
```

In `internal/providers/sleeper/leagues.go`, change the `League` return to include the new fields:

```go
		Scoring: scoring, ScoringByPosition: map[string]domain.ScoringRules{},
		DerivedStats: []domain.DerivedStat{}, UnsupportedRules: []string{}, RosterSlots: l.RosterPositions,
```

- [ ] **Step 4: Update the service call sites**

In `internal/service/leagues.go`, change `attachStats` to take the full scoring model and the player's position:

```go
// attachStats fills Stats and Points in place for players with a stat line.
func attachStats(r domain.Roster, lines map[string]domain.StatLine, s domain.Scoring) {
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
			pts := scoring.Points(line.Stats, s, e.Player.Position)
			e.Stats, e.Points = line.Stats, &pts
		}
	}
}
```

and its two callers in `Matchups` to `attachStats(home, lines, league.ScoringModel())` / `attachStats(away, lines, league.ScoringModel())`.

In `internal/service/players.go`, in `Gamelog` capture the player and use the pointer scoring:

```go
	p, err := s.player(ctx, playerID)
	if err != nil {
		return nil, domain.Meta{}, err
	}
```

(replacing `if _, err := s.player(ctx, playerID); err != nil { … }`), change the `rulesFor` call to `sc, warnings, err := s.rulesFor(ctx, scoringParam, season)`, and in the loop:

```go
		if sc != nil {
			pts := scoring.Points(l.Stats, *sc, p.Position)
			e.Points = &pts
		}
```

Replace `rulesFor` with:

```go
func (s *Service) rulesFor(ctx context.Context, param string, season int) (*domain.Scoring, []string, error) {
	if param == "" {
		return nil, nil, nil
	}
	if sc, ok := scoring.Preset(param); ok {
		return &sc, nil, nil
	}
	_, _, league, err := s.resolveLeague(ctx, param, season)
	if err != nil {
		return nil, nil, err
	}
	var warnings []string
	for _, u := range league.UnsupportedRules {
		warnings = append(warnings, "unsupported_rule: "+u)
	}
	sc := league.ScoringModel()
	return &sc, warnings, nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `gofmt -l . ; go vet ./... && go test -race ./...`
Expected: no gofmt output; all packages `ok` (ESPN tests still pass — conversion changes in Task 2).

- [ ] **Step 6: Commit**

```bash
git add internal/domain internal/scoring internal/providers/sleeper internal/service
git commit -m "feat(scoring): add per-position rules and derived stats to the scoring model"
```

---

### Task 2: ESPN scoring translation

**Files:**
- Modify: `internal/providers/espn/tables.go`, `internal/providers/espn/league.go` (`League`, `convertScoring`), `internal/providers/espn/espn_test.go` (`TestLeague`)
- Create: `internal/providers/espn/testdata/scoring_items.json`, `internal/providers/espn/scoring_test.go`

**Interfaces:**
- Consumes: `domain.Scoring`, `domain.DerivedStat`, `scoring.Points` (Task 1).
- Produces:
  - `convertScoring(items []scoringItemJSON) convertedScoring` with fields `rules domain.ScoringRules`, `byPosition map[string]domain.ScoringRules`, `derived []domain.DerivedStat` (sorted by Key), `unsupported []string` (sorted); all non-nil.
  - `func ESPNStatIDForKey(key string) (int, bool)` (exported; used by Task 4).
  - `League()` populates `Scoring`, `ScoringByPosition`, `DerivedStats`, `UnsupportedRules`.

- [ ] **Step 1: Add the real scoring fixture**

`internal/providers/espn/testdata/scoring_items.json` (league 1214655831, 2026; scoring settings only, no secrets):

```json
{"scoringItems":[{"statId":3,"points":0.04},
    {"statId":4,"points":4.0},
    {"statId":19,"points":2.0},
    {"statId":20,"points":-2.0},
    {"statId":24,"points":0.1},
    {"statId":25,"points":6.0},
    {"statId":26,"points":2.0},
    {"statId":42,"points":0.1},
    {"statId":43,"points":6.0},
    {"statId":44,"points":2.0},
    {"statId":53,"points":0.5},
    {"statId":63,"points":6.0},
    {"statId":72,"points":-2.0},
    {"statId":77,"points":4.0},
    {"statId":80,"points":3.0},
    {"statId":85,"points":-1.0},
    {"statId":86,"points":1.0},
    {"statId":89,"points":0.0,"pointsOverrides":{"16":5.0}},
    {"statId":90,"points":0.0,"pointsOverrides":{"16":4.0}},
    {"statId":91,"points":0.0,"pointsOverrides":{"16":3.0}},
    {"statId":92,"points":0.0,"pointsOverrides":{"16":1.0}},
    {"statId":93,"points":6.0,"pointsOverrides":{"16":6.0}},
    {"statId":95,"points":0.0,"pointsOverrides":{"16":2.0}},
    {"statId":96,"points":0.0,"pointsOverrides":{"16":2.0}},
    {"statId":97,"points":0.0,"pointsOverrides":{"16":2.0}},
    {"statId":98,"points":0.0,"pointsOverrides":{"16":2.0}},
    {"statId":99,"points":0.0,"pointsOverrides":{"16":1.0}},
    {"statId":101,"points":6.0,"pointsOverrides":{"16":6.0}},
    {"statId":102,"points":6.0,"pointsOverrides":{"16":6.0}},
    {"statId":103,"points":6.0,"pointsOverrides":{"16":6.0}},
    {"statId":104,"points":6.0,"pointsOverrides":{"16":6.0}},
    {"statId":123,"points":0.0,"pointsOverrides":{"16":-1.0}},
    {"statId":124,"points":0.0,"pointsOverrides":{"16":-3.0}},
    {"statId":125,"points":0.0,"pointsOverrides":{"16":-5.0}},
    {"statId":128,"points":0.0,"pointsOverrides":{"16":5.0}},
    {"statId":129,"points":0.0,"pointsOverrides":{"16":3.0}},
    {"statId":130,"points":0.0,"pointsOverrides":{"16":2.0}},
    {"statId":132,"points":0.0,"pointsOverrides":{"16":-1.0}},
    {"statId":133,"points":0.0,"pointsOverrides":{"16":-3.0}},
    {"statId":134,"points":0.0,"pointsOverrides":{"16":-5.0}},
    {"statId":135,"points":0.0,"pointsOverrides":{"16":-6.0}},
    {"statId":136,"points":0.0,"pointsOverrides":{"16":-7.0}},
    {"statId":198,"points":5.0},
    {"statId":201,"points":6.0},
    {"statId":206,"points":2.0,"pointsOverrides":{"16":2.0}},
    {"statId":209,"points":1.0,"pointsOverrides":{"16":1.0}}]}
```

- [ ] **Step 2: Write the failing tests**

`internal/providers/espn/scoring_test.go`:

```go
package espn

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/vsnandy/sports-api-go/internal/domain"
	"github.com/vsnandy/sports-api-go/internal/scoring"
)

func loadItems(t *testing.T) []scoringItemJSON {
	t.Helper()
	raw, err := os.ReadFile("testdata/scoring_items.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		ScoringItems []scoringItemJSON `json:"scoringItems"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v.ScoringItems
}

func TestConvertScoringRealLeague(t *testing.T) {
	c := convertScoring(loadItems(t))

	for k, want := range map[string]float64{
		"pass_yd": 0.04, "pass_td": 4, "pass_int": -2, "rec": 0.5, "rush_td": 6,
		"fgm_0_19": 3, "fgm_40_49": 4, "xpm": 1, "sack": 0, "espn_pa_0": 0,
	} {
		if got, ok := c.rules[k]; !ok || got != want {
			t.Errorf("rules[%s] = %v (present %v), want %v", k, got, ok, want)
		}
	}
	wantDEF := domain.ScoringRules{
		"espn_pa_0": 5, "espn_pa_1_6": 4, "espn_pa_7_13": 3, "espn_pa_14_17": 1,
		"espn_pa_28_34": -1, "espn_pa_35_45": -3, "espn_pa_46p": -5,
		"int": 2, "fum_rec": 2, "blk_kick": 2, "safe": 2, "sack": 1,
		"espn_ya_0_99": 5, "espn_ya_100_199": 3, "espn_ya_200_299": 2, "espn_ya_350_399": -1,
		"espn_ya_400_449": -3, "espn_ya_450_499": -5, "espn_ya_500_549": -6, "espn_ya_550p": -7,
	}
	if !reflect.DeepEqual(c.byPosition, map[string]domain.ScoringRules{"DEF": wantDEF}) {
		t.Errorf("byPosition = %v", c.byPosition)
	}
	if len(c.derived) != 15 {
		t.Errorf("derived = %d stats, want 15 (only tiers present in the league)", len(c.derived))
	}
	wantUnsupported := []string{
		"espn stat 101 (6 pts)", "espn stat 102 (6 pts)", "espn stat 103 (6 pts)", "espn stat 104 (6 pts)",
		"espn stat 198 (5 pts)", "espn stat 201 (6 pts)", "espn stat 206 (2 pts)", "espn stat 209 (1 pts)",
		"espn stat 63 (6 pts)", "espn stat 93 (6 pts)",
	}
	if !reflect.DeepEqual(c.unsupported, wantUnsupported) {
		t.Errorf("unsupported = %v", c.unsupported)
	}
}

// The Steelers D/ST in week 2 scored 8 on ESPN: 3 sacks, 1 INT, 1 fumble recovery,
// 14 points allowed, 327 yards allowed (a tier this league doesn't score).
func TestConvertedScoringMatchesESPNForDST(t *testing.T) {
	c := convertScoring(loadItems(t))
	s := domain.Scoring{Rules: c.rules, ByPosition: c.byPosition, Derived: c.derived}
	pit := map[string]float64{"sack": 3, "int": 1, "fum_rec": 1, "pts_allow": 14, "yds_allow": 327, "int_ret_yd": 3}
	if got := scoring.Points(pit, s, "DEF"); got != 8 {
		t.Fatalf("PIT D/ST = %v, want 8", got)
	}
}

func TestTierTable(t *testing.T) {
	cases := []struct {
		id       int
		key      string
		from     string
		min      float64
		max      float64 // -1 = unbounded
	}{
		{89, "espn_pa_0", "pts_allow", 0, 0}, {90, "espn_pa_1_6", "pts_allow", 1, 6},
		{91, "espn_pa_7_13", "pts_allow", 7, 13}, {92, "espn_pa_14_17", "pts_allow", 14, 17},
		{121, "espn_pa_18_21", "pts_allow", 18, 21}, {122, "espn_pa_22_27", "pts_allow", 22, 27},
		{123, "espn_pa_28_34", "pts_allow", 28, 34}, {124, "espn_pa_35_45", "pts_allow", 35, 45},
		{125, "espn_pa_46p", "pts_allow", 46, -1},
		{128, "espn_ya_0_99", "yds_allow", 0, 99}, {129, "espn_ya_100_199", "yds_allow", 100, 199},
		{130, "espn_ya_200_299", "yds_allow", 200, 299}, {131, "espn_ya_300_349", "yds_allow", 300, 349},
		{132, "espn_ya_350_399", "yds_allow", 350, 399}, {133, "espn_ya_400_449", "yds_allow", 400, 449},
		{134, "espn_ya_450_499", "yds_allow", 450, 499}, {135, "espn_ya_500_549", "yds_allow", 500, 549},
		{136, "espn_ya_550p", "yds_allow", 550, -1},
	}
	if len(tiers) != len(cases) {
		t.Fatalf("tiers has %d entries, want %d", len(tiers), len(cases))
	}
	for _, c := range cases {
		tr, ok := tiers[c.id]
		if !ok || tr.key != c.key || tr.from != c.from || tr.min != c.min {
			t.Errorf("tier %d = %+v, want %s %s min %v", c.id, tr, c.key, c.from, c.min)
			continue
		}
		if (c.max < 0) != (tr.max == nil) || (tr.max != nil && *tr.max != c.max) {
			t.Errorf("tier %d max = %v, want %v", c.id, tr.max, c.max)
		}
		if id, ok := ESPNStatIDForKey(c.key); !ok || id != c.id {
			t.Errorf("ESPNStatIDForKey(%s) = %d, %v", c.key, id, ok)
		}
	}
}

func TestConvertScoringUnknownOverrideSlot(t *testing.T) {
	c := convertScoring([]scoringItemJSON{
		{StatID: 53, Points: 1, PointsOverrides: map[string]float64{"23": 2, "6": 1.5}},
	})
	if c.byPosition["TE"]["rec"] != 1.5 {
		t.Errorf("TE override lost: %v", c.byPosition)
	}
	if !reflect.DeepEqual(c.unsupported, []string{"espn stat 53 override for slot 23"}) {
		t.Errorf("unsupported = %v", c.unsupported)
	}
}

func TestESPNStatIDForKey(t *testing.T) {
	for key, want := range map[string]int{"pass_yd": 3, "fgm_20_29": 80, "sack": 99, "pts_allow": 120, "yds_allow": 127} {
		if id, ok := ESPNStatIDForKey(key); !ok || id != want {
			t.Errorf("ESPNStatIDForKey(%s) = %d, %v; want %d", key, id, ok, want)
		}
	}
	if _, ok := ESPNStatIDForKey("pts_allow_14_20"); ok {
		t.Error("Sleeper bucket keys are no longer ESPN mappings")
	}
}
```

In `internal/providers/espn/espn_test.go`, replace the scoring assertions in `TestLeague` (the `wantRules` … `wantUnsupported` block) with:

```go
	wantRules := domain.ScoringRules{"pass_yd": 0.04, "pass_td": 4, "rec": 1, "fgm_0_19": 3, "fgm_20_29": 3, "fgm_30_39": 3, "espn_pa_14_17": 1}
	if !reflect.DeepEqual(l.Scoring, wantRules) {
		t.Errorf("Scoring = %v", l.Scoring)
	}
	if !reflect.DeepEqual(l.ScoringByPosition, map[string]domain.ScoringRules{"TE": {"rec": 1.5}}) {
		t.Errorf("ScoringByPosition = %v", l.ScoringByPosition)
	}
	seventeen := 17.0
	wantDerived := []domain.DerivedStat{{Key: "espn_pa_14_17", From: "pts_allow", Min: 14, Max: &seventeen}}
	if !reflect.DeepEqual(l.DerivedStats, wantDerived) {
		t.Errorf("DerivedStats = %+v", l.DerivedStats)
	}
	if l.UnsupportedRules == nil || len(l.UnsupportedRules) != 0 {
		t.Errorf("UnsupportedRules = %#v, want empty non-nil", l.UnsupportedRules)
	}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/providers/espn/`
Expected: FAIL — `c.rules undefined (type domain.ScoringRules has no field or method rules)` / `undefined: tiers` / `undefined: ESPNStatIDForKey`.

- [ ] **Step 4: Implement**

In `internal/providers/espn/tables.go`, in `statKeys` remove the entries for 89, 90 and 91 and add `120: {"pts_allow"}, 127: {"yds_allow"},`. Then append:

```go
// tier is an ESPN stat that scores a range of a raw stat, as a derived indicator.
type tier struct {
	key  string
	from string
	min  float64
	max  *float64 // nil = unbounded
}

func upTo(v float64) *float64 { return &v }

// tiers maps ESPN points-allowed and yards-allowed stat IDs to inclusive ranges of
// Sleeper's raw pts_allow / yds_allow.
var tiers = map[int]tier{
	89: {"espn_pa_0", "pts_allow", 0, upTo(0)},
	90: {"espn_pa_1_6", "pts_allow", 1, upTo(6)},
	91: {"espn_pa_7_13", "pts_allow", 7, upTo(13)},
	92: {"espn_pa_14_17", "pts_allow", 14, upTo(17)},
	121: {"espn_pa_18_21", "pts_allow", 18, upTo(21)},
	122: {"espn_pa_22_27", "pts_allow", 22, upTo(27)},
	123: {"espn_pa_28_34", "pts_allow", 28, upTo(34)},
	124: {"espn_pa_35_45", "pts_allow", 35, upTo(45)},
	125: {"espn_pa_46p", "pts_allow", 46, nil},
	128: {"espn_ya_0_99", "yds_allow", 0, upTo(99)},
	129: {"espn_ya_100_199", "yds_allow", 100, upTo(199)},
	130: {"espn_ya_200_299", "yds_allow", 200, upTo(299)},
	131: {"espn_ya_300_349", "yds_allow", 300, upTo(349)},
	132: {"espn_ya_350_399", "yds_allow", 350, upTo(399)},
	133: {"espn_ya_400_449", "yds_allow", 400, upTo(449)},
	134: {"espn_ya_450_499", "yds_allow", 450, upTo(499)},
	135: {"espn_ya_500_549", "yds_allow", 500, upTo(549)},
	136: {"espn_ya_550p", "yds_allow", 550, nil},
}

// overrideSlots maps pointsOverrides keys (lineup slot IDs) to Sleeper positions.
var overrideSlots = map[string]string{"0": "QB", "2": "RB", "4": "WR", "6": "TE", "16": "DEF", "17": "K"}

// ESPNStatIDForKey returns the ESPN stat ID a Sleeper or derived stat key came from.
func ESPNStatIDForKey(key string) (int, bool) {
	for id, t := range tiers {
		if t.key == key {
			return id, true
		}
	}
	for id, keys := range statKeys {
		if slices.Contains(keys, key) {
			return id, true
		}
	}
	return 0, false
}
```

In `internal/providers/espn/league.go`, replace `convertScoring` with:

```go
type convertedScoring struct {
	rules       domain.ScoringRules
	byPosition  map[string]domain.ScoringRules
	derived     []domain.DerivedStat
	unsupported []string
}

// convertScoring translates ESPN scoring items into the engine's model: per-unit
// stats become base rules, tier stats become derived indicators, and pointsOverrides
// become per-position replacements. Anything untranslatable is reported.
func convertScoring(items []scoringItemJSON) convertedScoring {
	c := convertedScoring{
		rules:       domain.ScoringRules{},
		byPosition:  map[string]domain.ScoringRules{},
		derived:     []domain.DerivedStat{},
		unsupported: []string{},
	}
	for _, it := range items {
		var keys []string
		if t, ok := tiers[it.StatID]; ok {
			keys = []string{t.key}
			c.derived = append(c.derived, domain.DerivedStat{Key: t.key, From: t.from, Min: t.min, Max: t.max})
		} else if ks, ok := statKeys[it.StatID]; ok {
			keys = ks
		} else {
			c.unsupported = append(c.unsupported, fmt.Sprintf("espn stat %d (%g pts)", it.StatID, it.Points))
			continue
		}
		for _, k := range keys {
			c.rules[k] = it.Points
		}
		for slot, pts := range it.PointsOverrides {
			pos, ok := overrideSlots[slot]
			if !ok {
				c.unsupported = append(c.unsupported, fmt.Sprintf("espn stat %d override for slot %s", it.StatID, slot))
				continue
			}
			if c.byPosition[pos] == nil {
				c.byPosition[pos] = domain.ScoringRules{}
			}
			for _, k := range keys {
				c.byPosition[pos][k] = pts
			}
		}
	}
	sort.Strings(c.unsupported)
	slices.SortFunc(c.derived, func(a, b domain.DerivedStat) int { return cmp.Compare(a.Key, b.Key) })
	return c
}
```

and in `League()` replace `rules, unsupported := convertScoring(...)` and the `Scoring: rules, UnsupportedRules: unsupported,` line with:

```go
	conv := convertScoring(l.Settings.ScoringSettings.ScoringItems)
```

```go
		Scoring: conv.rules, ScoringByPosition: conv.byPosition, DerivedStats: conv.derived,
		UnsupportedRules: conv.unsupported,
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `gofmt -l . ; go vet ./... && go test -race ./...`
Expected: no gofmt output; all packages `ok`.

- [ ] **Step 6: Commit**

```bash
git add internal/providers/espn
git commit -m "feat(espn): translate position overrides and points/yards-allowed tiers"
```

---

### Task 3: ESPN platform points in matchups

**Files:**
- Modify: `internal/domain/domain.go`, `internal/providers/espn/league.go`, `internal/providers/espn/testdata/league.json`, `internal/providers/espn/espn_test.go`, `internal/service/leagues.go`, `internal/service/fakes_test.go`, `internal/service/service_test.go`

**Interfaces:**
- Consumes: `domain.Scoring`, `League.ScoringModel()`, `scoring.Points` (Task 1).
- Produces:
  - `domain.RosterEntryRef.PlatformPoints *float64`; `domain.RosterEntry.PointsSource string` (`json:"pointsSource,omitempty"`)
  - `espn.PlayerWeekPoints{Ref domain.PlayerRef; Total float64; ByStat map[int]float64}`
  - `func (c *espn.Client) WeekPlayerPoints(ctx context.Context, nativeID string, season, week int) ([]PlayerWeekPoints, error)` (used by Task 4)
  - service `attachPoints(r domain.Roster, ref domain.RosterRef, lines map[string]domain.StatLine, s domain.Scoring)` replaces `attachStats`

- [ ] **Step 1: Extend the ESPN fixture**

In `internal/providers/espn/testdata/league.json`, inside the week-3 schedule entry's **away** side (`"away": {"teamId": 1, "totalPoints": 120.5, ...}`), replace its single Mahomes entry with these two entries (leave the Mahomes entry under `teams` untouched):

```json
        {"playerId": 3139477, "lineupSlotId": 0, "playerPoolEntry": {"player": {"fullName": "Patrick Mahomes", "defaultPositionId": 1, "proTeamId": 12,
          "stats": [
            {"scoringPeriodId": 3, "statSourceId": 1, "statSplitTypeId": 1, "appliedTotal": 99.9, "appliedStats": {"3": 99.9}},
            {"scoringPeriodId": 3, "statSourceId": 0, "statSplitTypeId": 1, "appliedTotal": 24.5, "appliedStats": {"3": 10.0, "4": 12.0, "20": -2.0, "24": 4.5}},
            {"scoringPeriodId": 2, "statSourceId": 0, "statSplitTypeId": 1, "appliedTotal": 7.0, "appliedStats": {"3": 7.0}},
            {"scoringPeriodId": 0, "statSourceId": 0, "statSplitTypeId": 0, "appliedTotal": 31.5, "appliedStats": {"3": 17.0}}
          ]}}},
        {"playerId": 4262921, "lineupSlotId": 20, "playerPoolEntry": {"player": {"fullName": "Justin Jefferson", "defaultPositionId": 3, "proTeamId": 16,
          "stats": [
            {"scoringPeriodId": 3, "statSourceId": 1, "statSplitTypeId": 1, "appliedTotal": 15.2, "appliedStats": {"42": 9.0}}
          ]}}}
```

- [ ] **Step 2: Write the failing tests**

Append to `internal/providers/espn/espn_test.go`:

```go
func TestMatchupsPlatformPoints(t *testing.T) {
	c, _ := newTestClient(t, "S2")
	ms, err := c.Matchups(context.Background(), "123456", 2026, 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	away := ms[0].Away.Roster
	qb := away.Starters[0]
	if qb.PlatformPoints == nil || *qb.PlatformPoints != 24.5 {
		t.Fatalf("QB PlatformPoints = %v, want 24.5 from the actual week-3 row (not projection 99.9, week 2, or season)", qb.PlatformPoints)
	}
	if len(away.Bench) != 1 || away.Bench[0].PlatformPoints != nil {
		t.Fatalf("bench entry with only a projection row must have nil PlatformPoints: %+v", away.Bench)
	}
}

func TestRostersHaveNoPlatformPoints(t *testing.T) {
	c, _ := newTestClient(t, "S2")
	rs, err := c.Rosters(context.Background(), "123456", 2026, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range rs[0].Starters {
		if e.PlatformPoints != nil {
			t.Fatalf("rosters have no week; PlatformPoints = %v", *e.PlatformPoints)
		}
	}
}

func TestWeekPlayerPoints(t *testing.T) {
	c, _ := newTestClient(t, "S2")
	pts, err := c.WeekPlayerPoints(context.Background(), "123456", 2026, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 1 {
		t.Fatalf("got %d players, want 1 (only entries with an actual week-3 row)", len(pts))
	}
	p := pts[0]
	if p.Ref.ID != "3139477" || p.Ref.Name != "Patrick Mahomes" || p.Ref.Position != "QB" || p.Total != 24.5 {
		t.Fatalf("player = %+v", p)
	}
	if !reflect.DeepEqual(p.ByStat, map[int]float64{3: 10, 4: 12, 20: -2, 24: 4.5}) {
		t.Fatalf("ByStat = %v", p.ByStat)
	}
}
```

In `internal/service/fakes_test.go`, append helpers:

```go
func ptr(v float64) *float64 { return &v }

func withPlatformPoints(r domain.RosterEntryRef, pts float64) domain.RosterEntryRef {
	r.PlatformPoints = ptr(pts)
	return r
}
```

Add `"slices"` to the imports of `internal/service/service_test.go`, then append:

```go
func espnMatchups() []domain.MatchupRef {
	return []domain.MatchupRef{{
		Week: 3,
		Home: domain.MatchupSideRef{TeamID: "1", Points: 42.6, Roster: domain.RosterRef{
			TeamID: "1",
			Starters: []domain.RosterEntryRef{
				withPlatformPoints(espnRef("QB", "3139477", "Patrick Mahomes", "QB", "KC"), 24.5),
				withPlatformPoints(espnRef("RB", "9999999", "Rookie Guy", "RB", ""), 7),
				espnRef("WR", "4262921", "Justin Jefferson", "WR", "MIN"),
			},
		}},
		Away: domain.MatchupSideRef{TeamID: "2", Roster: domain.RosterRef{TeamID: "2"}},
	}}
}

func TestMatchupsPlatformPoints(t *testing.T) {
	f := newFixture()
	f.espn.matchups = espnMatchups()
	ms, _, err := f.svc.Matchups(ctx, "espn:123456", 0, 3, true)
	if err != nil {
		t.Fatal(err)
	}
	st := ms[0].Home.Roster.Starters
	if *st[0].Points != 24.5 || st[0].PointsSource != "platform" || st[0].Stats["pass_td"] != 2 {
		t.Errorf("mapped ESPN player: %+v (want platform 24.5 with Sleeper stats)", st[0])
	}
	if st[1].Player.ID != nil || *st[1].Points != 7 || st[1].PointsSource != "platform" || st[1].Stats != nil {
		t.Errorf("unmapped ESPN player: %+v (want platform 7, nil stats)", st[1])
	}
	// No platform points → engine with the ESPN league's rules: rec 6×0.5 + rec_yd 85×0.1.
	if *st[2].Points != 11.5 || st[2].PointsSource != "computed" {
		t.Errorf("fallback: %+v (want computed 11.5)", st[2])
	}
}

func TestMatchupsPlatformPointsSurviveStatsFailure(t *testing.T) {
	f := newFixture()
	f.espn.matchups = espnMatchups()
	f.stats.weekErr = &domain.UpstreamError{Provider: "sleeper", Status: 500}
	ms, meta, err := f.svc.Matchups(ctx, "espn:123456", 0, 3, true)
	if err != nil {
		t.Fatal(err)
	}
	st := ms[0].Home.Roster.Starters
	if *st[0].Points != 24.5 || st[0].PointsSource != "platform" || st[0].Stats != nil {
		t.Errorf("platform points should survive a stats failure: %+v", st[0])
	}
	if st[2].Points != nil || st[2].PointsSource != "" {
		t.Errorf("no platform points and no stats → no points: %+v", st[2])
	}
	if !slices.ContainsFunc(meta.Warnings, func(w string) bool { return strings.HasPrefix(w, "stats_unavailable:") }) {
		t.Errorf("warnings = %v, want a stats_unavailable warning", meta.Warnings)
	}
}

func TestSleeperMatchupsAreComputed(t *testing.T) {
	f := newFixture()
	ms, _, err := f.svc.Matchups(ctx, "sleeper:111", 0, 3, true)
	if err != nil {
		t.Fatal(err)
	}
	if src := ms[0].Home.Roster.Starters[0].PointsSource; src != "computed" {
		t.Fatalf("PointsSource = %q, want computed", src)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/providers/espn/ ./internal/service/`
Expected: FAIL — `qb.PlatformPoints undefined` / `c.WeekPlayerPoints undefined` / `st[0].PointsSource undefined`.

- [ ] **Step 4: Implement**

In `internal/domain/domain.go`:

```go
type RosterEntryRef struct {
	Slot string
	Ref  PlayerRef
	// PlatformPoints is the platform's own points for the requested week, when it reports them.
	PlatformPoints *float64
}
```

and add to `RosterEntry` (after `Points`):

```go
	PointsSource string             `json:"pointsSource,omitempty"` // "platform" or "computed" when Points is set
```

updating the `RosterEntry` doc comment's first sentence to: `// RosterEntry is a rostered player. Stats, Points and PointsSource are unset unless stats were`.

In `internal/providers/espn/league.go`, add a stats row type and extend `entryJSON`:

```go
type statRowJSON struct {
	ScoringPeriodID int                `json:"scoringPeriodId"`
	StatSourceID    int                `json:"statSourceId"` // 0 actual, 1 projected
	StatSplitTypeID int                `json:"statSplitTypeId"` // 1 single scoring period
	AppliedTotal    float64            `json:"appliedTotal"`
	AppliedStats    map[string]float64 `json:"appliedStats"`
}
```

In `entryJSON`'s `Player` struct add `Stats []statRowJSON \`json:"stats"\``, then add:

```go
// actualRow returns the entry's actual (not projected) stats row for one week.
func (e entryJSON) actualRow(week int) (statRowJSON, bool) {
	for _, r := range e.PlayerPoolEntry.Player.Stats {
		if r.StatSourceID == 0 && r.StatSplitTypeID == 1 && r.ScoringPeriodID == week {
			return r, true
		}
	}
	return statRowJSON{}, false
}

func (e entryJSON) ref() domain.PlayerRef {
	p := e.PlayerPoolEntry.Player
	return domain.PlayerRef{
		Platform: domain.PlatformESPN, ID: strconv.Itoa(e.PlayerID), Name: p.FullName,
		Position: positions[p.DefaultPositionID], NFLTeam: proTeams[p.ProTeamID],
	}
}
```

Change `toRoster` to take the week (0 = none) and use `ref()`:

```go
func toRoster(teamID string, entries []entryJSON, week int) domain.RosterRef {
	sorted := slices.Clone(entries)
	slices.SortStableFunc(sorted, func(a, b entryJSON) int { return cmp.Compare(slotRank(a.LineupSlotID), slotRank(b.LineupSlotID)) })
	r := domain.RosterRef{TeamID: teamID, Starters: []domain.RosterEntryRef{}, Bench: []domain.RosterEntryRef{}, Reserve: []domain.RosterEntryRef{}}
	for _, e := range sorted {
		ref := domain.RosterEntryRef{Slot: slotName(e.LineupSlotID), Ref: e.ref()}
		if week > 0 {
			if row, ok := e.actualRow(week); ok {
				total := row.AppliedTotal
				ref.PlatformPoints = &total
			}
		}
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
```

Update callers: in `Rosters`, `toRoster(strconv.Itoa(t.ID), entries, 0)`; change `side` to `func side(s sideJSON, week int) domain.MatchupSideRef` passing `week` to `toRoster`, and in `Matchups` call `side(*m.Home, week)` / `side(*m.Away, week)`.

Append:

```go
// PlayerWeekPoints is ESPN's own scoring of one rostered player for one week.
type PlayerWeekPoints struct {
	Ref    domain.PlayerRef
	Total  float64
	ByStat map[int]float64 // ESPN stat ID → points
}

// WeekPlayerPoints returns ESPN's points for every rostered player (starters, bench,
// IR) that has an actual-stats row for week. It backs cmd/scoreaudit.
func (c *Client) WeekPlayerPoints(ctx context.Context, nativeID string, season, week int) ([]PlayerWeekPoints, error) {
	l, err := c.fetch(ctx, nativeID, season, week, "mMatchupScore", "mBoxscore")
	if err != nil {
		return nil, err
	}
	out := []PlayerWeekPoints{}
	for _, m := range l.Schedule {
		if m.MatchupPeriodID != week {
			continue
		}
		for _, s := range []*sideJSON{m.Home, m.Away} {
			if s == nil || s.RosterForCurrentScoringPeriod == nil {
				continue
			}
			for _, e := range s.RosterForCurrentScoringPeriod.Entries {
				row, ok := e.actualRow(week)
				if !ok {
					continue
				}
				by := make(map[int]float64, len(row.AppliedStats))
				for k, v := range row.AppliedStats {
					if id, err := strconv.Atoi(k); err == nil {
						by[id] = v
					}
				}
				out = append(out, PlayerWeekPoints{Ref: e.ref(), Total: row.AppliedTotal, ByStat: by})
			}
		}
	}
	return out, nil
}
```

In `internal/service/leagues.go`, replace `attachStats` with:

```go
// attachPoints fills Stats, Points and PointsSource in place. The platform's own
// points win; otherwise points are computed from the Sleeper stat line with the
// league's scoring. Roster entries and refs are index-aligned (resolveEntries keeps order).
func attachPoints(r domain.Roster, ref domain.RosterRef, lines map[string]domain.StatLine, s domain.Scoring) {
	groups := []struct {
		entries []domain.RosterEntry
		refs    []domain.RosterEntryRef
	}{{r.Starters, ref.Starters}, {r.Bench, ref.Bench}, {r.Reserve, ref.Reserve}}
	for _, g := range groups {
		for i := range g.entries {
			e := &g.entries[i]
			var line domain.StatLine
			hasLine := false
			if e.Player.ID != nil {
				line, hasLine = lines[*e.Player.ID]
			}
			if hasLine {
				e.Stats = line.Stats
			}
			switch {
			case i < len(g.refs) && g.refs[i].PlatformPoints != nil:
				pts := *g.refs[i].PlatformPoints
				e.Points, e.PointsSource = &pts, "platform"
			case hasLine:
				pts := scoring.Points(line.Stats, s, e.Player.Position)
				e.Points, e.PointsSource = &pts, "computed"
			}
		}
	}
}
```

and in `Matchups` replace the `if lines != nil { attachStats(...) ... }` block with:

```go
		if withStats {
			sc := league.ScoringModel()
			attachPoints(home, m.Home.Roster, lines, sc)
			attachPoints(away, m.Away.Roster, lines, sc)
		}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `gofmt -l . ; go vet ./... && go test -race ./...`
Expected: no gofmt output; all packages `ok` (existing `TestMatchupsStatsFailureDegrades` still passes: Sleeper entries have no platform points).

- [ ] **Step 6: Commit**

```bash
git add internal/domain internal/providers/espn internal/service
git commit -m "feat: use ESPN's own per-player points in ESPN matchups"
```

---

### Task 4: Score audit package and command

**Files:**
- Create: `internal/scoreaudit/scoreaudit.go`, `internal/scoreaudit/scoreaudit_test.go`, `cmd/scoreaudit/main.go`, `cmd/scoreaudit/main_test.go`
- Modify: `Makefile`, `docs/superpowers/specs/2026-09-28-espn-scoring-fidelity-design.md` (§5)

**Interfaces:**
- Consumes: `scoring.Points`, `scoring.Breakdown` (Task 1); `espn.ESPNStatIDForKey` (Task 2); `espn.(*Client).WeekPlayerPoints`, `espn.PlayerWeekPoints` (Task 3); existing `espn.New`, `espn.DefaultBase`, `(*espn.Client).League`, `sleeper.New`, `sleeper.DefaultAPIBase`, `sleeper.DefaultStatsBase`, `(*sleeper.Client).WeekStats`, `players.New`, `players.NoStore`, `(*players.Index).Resolve`, `httpx.New`, `domain.ParseLeagueID`.
- Produces: `scoreaudit.Sample`, `scoreaudit.StatMismatch`, `scoreaudit.Report` (`ExactRate() float64`, `Pass(threshold float64) bool`), `scoreaudit.Compare(samples []Sample, scorings map[string]domain.Scoring, statID func(string) (int, bool)) Report`; command `cmd/scoreaudit`; `make scoreaudit`.

- [ ] **Step 1: Write the failing tests**

`internal/scoreaudit/scoreaudit_test.go`:

```go
package scoreaudit

import (
	"reflect"
	"testing"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

func TestCompare(t *testing.T) {
	seventeen := 17.0
	scorings := map[string]domain.Scoring{"espn:1": {
		Rules:      domain.ScoringRules{"sack": 0},
		ByPosition: map[string]domain.ScoringRules{"DEF": {"sack": 1, "espn_pa_14_17": 1}},
		Derived:    []domain.DerivedStat{{Key: "espn_pa_14_17", From: "pts_allow", Min: 14, Max: &seventeen}},
	}}
	ids := map[string]int{"sack": 99, "espn_pa_14_17": 92, "int": 95}
	statID := func(k string) (int, bool) { id, ok := ids[k]; return id, ok }

	samples := []Sample{
		{League: "espn:1", Week: 1, Name: "D1", Mapped: true, Position: "DEF",
			Stats: map[string]float64{"sack": 3, "pts_allow": 14}, Total: 4, ByStat: map[int]float64{99: 3, 92: 1}},
		{League: "espn:1", Week: 1, Name: "D2", Mapped: true, Position: "DEF",
			Stats: map[string]float64{"sack": 2, "int": 1, "pts_allow": 30}, Total: 4, ByStat: map[int]float64{99: 2, 95: 2}},
		{League: "espn:1", Week: 1, Name: "Nobody", Mapped: false, Total: 12, ByStat: map[int]float64{3: 12}},
		{League: "espn:1", Week: 2, Name: "Bye Guy", Mapped: true, Position: "WR", Total: 0, ByStat: map[int]float64{}},
	}
	r := Compare(samples, scorings, statID)

	if r.Compared != 3 || r.Exact != 2 || r.Unmapped != 1 {
		t.Fatalf("counts = compared %d exact %d unmapped %d", r.Compared, r.Exact, r.Unmapped)
	}
	want := []StatMismatch{{StatID: 95, Count: 1, Expected: 2, Got: 0, Example: "D2 (espn:1 wk 1)",
		ExampleStats: map[string]float64{"sack": 2, "int": 1, "pts_allow": 30}}}
	if !reflect.DeepEqual(r.Mismatches, want) {
		t.Fatalf("mismatches = %+v", r.Mismatches)
	}
	if !r.Pass(0.6) || r.Pass(0.98) {
		t.Fatalf("Pass: rate %.3f", r.ExactRate())
	}
}

func TestCompareAttributesEngineOnlyKeysToStatZero(t *testing.T) {
	scorings := map[string]domain.Scoring{"espn:1": {Rules: domain.ScoringRules{"bonus_x": 5}}}
	r := Compare([]Sample{{League: "espn:1", Week: 1, Name: "P", Mapped: true, Position: "QB",
		Stats: map[string]float64{"bonus_x": 1}, Total: 0, ByStat: map[int]float64{}}},
		scorings, func(string) (int, bool) { return 0, false })
	if len(r.Mismatches) != 1 || r.Mismatches[0].StatID != 0 || r.Mismatches[0].Got != 5 {
		t.Fatalf("mismatches = %+v", r.Mismatches)
	}
}

func TestEmptyReportFails(t *testing.T) {
	if (Report{}).Pass(0.98) {
		t.Fatal("a report with nothing compared must not pass")
	}
}
```

`cmd/scoreaudit/main_test.go`:

```go
package main

import (
	"reflect"
	"testing"
)

func TestParseWeeks(t *testing.T) {
	ok := map[string][]int{"3": {3}, "1-3": {1, 2, 3}, "18": {18}}
	for in, want := range ok {
		got, err := parseWeeks(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("parseWeeks(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "0", "19", "3-1", "a", "1-", "1-2-3"} {
		if _, err := parseWeeks(bad); err == nil {
			t.Errorf("parseWeeks(%q) should fail", bad)
		}
	}
}

func TestLeagueIDs(t *testing.T) {
	got, err := leagueIDs([]string{"espn:123", "espn:456"}, "")
	if err != nil || !reflect.DeepEqual(got, []string{"123", "456"}) {
		t.Fatalf("flags: %v, %v", got, err)
	}
	got, err = leagueIDs(nil, " 789 ,111")
	if err != nil || !reflect.DeepEqual(got, []string{"789", "111"}) {
		t.Fatalf("env: %v, %v", got, err)
	}
	for _, bad := range [][]string{{"sleeper:1"}, {"espn:1/2"}} {
		if _, err := leagueIDs(bad, ""); err == nil {
			t.Errorf("leagueIDs(%v) should fail", bad)
		}
	}
	if _, err := leagueIDs(nil, ""); err == nil {
		t.Error("no leagues anywhere should fail")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/scoreaudit/ ./cmd/scoreaudit/`
Expected: FAIL — `no non-test Go files` / `undefined: Compare` / `undefined: parseWeeks`.

- [ ] **Step 3: Implement the audit package**

`internal/scoreaudit/scoreaudit.go`:

```go
// Package scoreaudit compares the scoring engine against a platform's own per-player
// points and attributes each mismatch to the platform stat ID responsible.
package scoreaudit

import (
	"cmp"
	"fmt"
	"math"
	"slices"

	"github.com/vsnandy/sports-api-go/internal/domain"
	"github.com/vsnandy/sports-api-go/internal/scoring"
)

const tolerance = 0.01

// Sample is one player-week: the platform's points and the Sleeper stat line.
type Sample struct {
	League   string
	Week     int
	Name     string
	Mapped   bool               // false when the player has no Sleeper match (excluded)
	Position string             // Sleeper position, for position-specific rules
	Stats    map[string]float64 // Sleeper stat line; nil when the player has none
	Total    float64            // platform points
	ByStat   map[int]float64    // platform points per platform stat ID
}

// StatMismatch aggregates player-weeks whose points disagree for one platform stat.
// StatID 0 collects engine points from keys with no known platform stat ID.
type StatMismatch struct {
	StatID       int
	Count        int
	Expected     float64 // platform points in the first example
	Got          float64 // engine points in the first example
	Example      string  // "Name (league wk N)"
	ExampleStats map[string]float64
}

type Report struct {
	Compared   int
	Exact      int
	Unmapped   int
	Mismatches []StatMismatch // by Count desc, then StatID
}

func (r Report) ExactRate() float64 {
	if r.Compared == 0 {
		return 0
	}
	return float64(r.Exact) / float64(r.Compared)
}

// Pass reports whether at least threshold of compared player-weeks were exact.
func (r Report) Pass(threshold float64) bool {
	return r.Compared > 0 && r.ExactRate() >= threshold
}

// Compare scores every mapped sample with its league's scoring and compares it with
// the platform total, attributing differences per platform stat ID.
func Compare(samples []Sample, scorings map[string]domain.Scoring, statID func(key string) (int, bool)) Report {
	var r Report
	byID := map[int]*StatMismatch{}
	for _, s := range samples {
		if !s.Mapped {
			r.Unmapped++
			continue
		}
		r.Compared++
		sc := scorings[s.League]
		if math.Abs(scoring.Points(s.Stats, sc, s.Position)-s.Total) < tolerance {
			r.Exact++
			continue
		}
		ours := map[int]float64{}
		for k, p := range scoring.Breakdown(s.Stats, sc, s.Position) {
			id, ok := statID(k)
			if !ok {
				id = 0
			}
			ours[id] += p
		}
		ids := map[int]bool{}
		for id := range ours {
			ids[id] = true
		}
		for id := range s.ByStat {
			ids[id] = true
		}
		for id := range ids {
			if math.Abs(ours[id]-s.ByStat[id]) < tolerance {
				continue
			}
			m, ok := byID[id]
			if !ok {
				m = &StatMismatch{
					StatID: id, Expected: s.ByStat[id], Got: ours[id],
					Example: fmt.Sprintf("%s (%s wk %d)", s.Name, s.League, s.Week), ExampleStats: s.Stats,
				}
				byID[id] = m
			}
			m.Count++
		}
	}
	for _, m := range byID {
		r.Mismatches = append(r.Mismatches, *m)
	}
	slices.SortFunc(r.Mismatches, func(a, b StatMismatch) int {
		if c := cmp.Compare(b.Count, a.Count); c != 0 {
			return c
		}
		return cmp.Compare(a.StatID, b.StatID)
	})
	return r
}
```

- [ ] **Step 4: Implement the command**

`cmd/scoreaudit/main.go`:

```go
// Command scoreaudit compares the scoring engine's ESPN points with ESPN's own
// per-player points for the owner's leagues. Run locally: it needs ESPN cookies.
//
//	go run ./cmd/scoreaudit -season 2026 -weeks 1-3 -league espn:123456 [-league ...] [-v]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/vsnandy/sports-api-go/internal/domain"
	"github.com/vsnandy/sports-api-go/internal/players"
	"github.com/vsnandy/sports-api-go/internal/providers/espn"
	"github.com/vsnandy/sports-api-go/internal/providers/httpx"
	"github.com/vsnandy/sports-api-go/internal/providers/sleeper"
	"github.com/vsnandy/sports-api-go/internal/scoreaudit"
)

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	var leagues multiFlag
	season := flag.Int("season", 0, "NFL season (required)")
	weeks := flag.String("weeks", "", "week or range, e.g. 3 or 1-3 (required)")
	threshold := flag.Float64("threshold", 0.98, "minimum exact rate to pass")
	verbose := flag.Bool("v", false, "print each mismatch example's Sleeper stat line")
	flag.Var(&leagues, "league", "ESPN league like espn:123456 (repeatable; default ESPN_LEAGUE_IDS)")
	flag.Parse()

	ok, err := run(context.Background(), *season, *weeks, *threshold, *verbose, leagues)
	if err != nil {
		fmt.Fprintln(os.Stderr, "scoreaudit:", err)
		os.Exit(2)
	}
	if !ok {
		os.Exit(1)
	}
}

func run(ctx context.Context, season int, weeksArg string, threshold float64, verbose bool, leagueFlags []string) (bool, error) {
	if season < 2000 {
		return false, errors.New("-season is required")
	}
	weeks, err := parseWeeks(weeksArg)
	if err != nil {
		return false, err
	}
	natives, err := leagueIDs(leagueFlags, os.Getenv("ESPN_LEAGUE_IDS"))
	if err != nil {
		return false, err
	}
	s2, swid, err := cookies(ctx)
	if err != nil {
		return false, err
	}

	ec := espn.New(httpx.New("espn", 30*time.Second), espn.DefaultBase, s2, swid, natives)
	sl := sleeper.New(httpx.New("sleeper", 60*time.Second), sleeper.DefaultAPIBase, sleeper.DefaultStatsBase, "")
	idx := players.New(sl, players.NoStore{}, time.Now)

	scorings := map[string]domain.Scoring{}
	lines := map[int]map[string]domain.StatLine{}
	var samples []scoreaudit.Sample
	for _, native := range natives {
		lg, err := ec.League(ctx, native, season)
		if err != nil {
			return false, fmt.Errorf("league %s: %w", native, err)
		}
		scorings[lg.ID] = lg.ScoringModel()
		for _, w := range weeks {
			if _, ok := lines[w]; !ok {
				if lines[w], err = sl.WeekStats(ctx, season, w); err != nil {
					return false, fmt.Errorf("sleeper week %d stats: %w", w, err)
				}
			}
			pts, err := ec.WeekPlayerPoints(ctx, native, season, w)
			if err != nil {
				return false, fmt.Errorf("league %s week %d: %w", native, w, err)
			}
			for _, pp := range pts {
				p, ok, err := idx.Resolve(ctx, pp.Ref)
				if err != nil {
					return false, err
				}
				s := scoreaudit.Sample{League: lg.ID, Week: w, Name: pp.Ref.Name, Mapped: ok, Total: pp.Total, ByStat: pp.ByStat}
				if ok {
					s.Position = p.Position
					s.Stats = lines[w][*p.ID].Stats
				}
				samples = append(samples, s)
			}
		}
	}

	r := scoreaudit.Compare(samples, scorings, espn.ESPNStatIDForKey)
	fmt.Printf("compared %d player-weeks · exact %d (%.1f%%) · unmapped %d (excluded)\n",
		r.Compared, r.Exact, 100*r.ExactRate(), r.Unmapped)
	if len(r.Mismatches) > 0 {
		fmt.Println("top mismatches:")
	}
	for i, m := range r.Mismatches {
		if i == 10 {
			break
		}
		label := fmt.Sprintf("espn stat %d", m.StatID)
		if m.StatID == 0 {
			label = "engine-only keys"
		}
		fmt.Printf("  %-16s x%-4d expected %+.2f got %+.2f  e.g. %s\n", label, m.Count, m.Expected, m.Got, m.Example)
		if verbose {
			fmt.Printf("      sleeper stats: %v\n", m.ExampleStats)
		}
	}
	pass := r.Pass(threshold)
	if pass {
		fmt.Printf("PASS (>= %.0f%%)\n", 100*threshold)
	} else {
		fmt.Printf("FAIL (< %.0f%%)\n", 100*threshold)
	}
	return pass, nil
}

// parseWeeks accepts "N" or "N-M" with 1 <= N <= M <= 18.
func parseWeeks(s string) ([]int, error) {
	lo, hi, isRange := strings.Cut(s, "-")
	if !isRange {
		hi = lo
	}
	a, errA := strconv.Atoi(lo)
	b, errB := strconv.Atoi(hi)
	if errA != nil || errB != nil || a < 1 || b > 18 || a > b {
		return nil, fmt.Errorf("-weeks must be N or N-M within 1-18, got %q", s)
	}
	var out []int
	for w := a; w <= b; w++ {
		out = append(out, w)
	}
	return out, nil
}

// leagueIDs returns native ESPN league IDs from -league flags, else ESPN_LEAGUE_IDS.
func leagueIDs(flags []string, env string) ([]string, error) {
	var out []string
	for _, f := range flags {
		p, native, err := domain.ParseLeagueID(f)
		if err != nil || p != domain.PlatformESPN {
			return nil, fmt.Errorf("-league %q must look like espn:<digits>", f)
		}
		out = append(out, native)
	}
	if len(out) == 0 {
		for _, id := range strings.Split(env, ",") {
			if id = strings.TrimSpace(id); id != "" {
				out = append(out, id)
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no leagues: pass -league espn:<id> or set ESPN_LEAGUE_IDS")
	}
	return out, nil
}

// cookies returns ESPN_S2/ESPN_SWID from the environment, else from SSM.
func cookies(ctx context.Context) (string, string, error) {
	if s2, swid := os.Getenv("ESPN_S2"), os.Getenv("ESPN_SWID"); s2 != "" && swid != "" {
		return s2, swid, nil
	}
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = "us-east-1"
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return "", "", err
	}
	const s2Name, swidName = "/sports-api/espn-s2", "/sports-api/espn-swid"
	out, err := ssm.NewFromConfig(cfg).GetParameters(ctx, &ssm.GetParametersInput{
		Names: []string{s2Name, swidName}, WithDecryption: aws.Bool(true),
	})
	if err != nil {
		return "", "", fmt.Errorf("reading ESPN cookies from SSM: %w", err)
	}
	vals := map[string]string{}
	for _, p := range out.Parameters {
		vals[aws.ToString(p.Name)] = aws.ToString(p.Value)
	}
	if vals[s2Name] == "" || vals[swidName] == "" {
		return "", "", errors.New("ESPN cookies not found (set ESPN_S2/ESPN_SWID or create the SSM parameters)")
	}
	return vals[s2Name], vals[swidName], nil
}
```

In `Makefile`, add `scoreaudit` to `.PHONY` and this target (recipe line starts with a TAB):

```make
scoreaudit:
	go run ./cmd/scoreaudit -season 2026 -weeks 1-3 $(ARGS)
```

In the spec (§5), replace "`-league` repeatable; omitted → all ESPN leagues for the season from `ListLeagues`." with "`-league` repeatable; omitted → `ESPN_LEAGUE_IDS` (comma-separated). `-v` prints each top mismatch's Sleeper stat line." and "`make scoreaudit` runs it with `-season 2026 -weeks 1-3`." with "`make scoreaudit ARGS='-league espn:<id> …'` runs it with `-season 2026 -weeks 1-3`."

- [ ] **Step 5: Run the tests to verify they pass**

Run: `gofmt -l . ; go vet ./... && go test -race ./... && go build ./cmd/scoreaudit && rm -f scoreaudit`
Expected: no gofmt output; all packages `ok`; build succeeds.

- [ ] **Step 6: Commit**

```bash
git add internal/scoreaudit cmd/scoreaudit Makefile docs/superpowers/specs/2026-09-28-espn-scoring-fidelity-design.md
git commit -m "feat: add scoreaudit to compare engine scoring with ESPN per stat ID"
```

---

### Task 5: Run the audit and confirm stat-ID mappings (controller, with the owner's approval)

This task contacts live ESPN with the owner's cookies. It is run by the controller (not a subagent) after the owner approves, and never prints secrets.

**Files:**
- Modify: `internal/providers/espn/tables.go` (`statKeys`), `internal/providers/espn/scoring_test.go`

**Interfaces:**
- Consumes: `cmd/scoreaudit` (Task 4), `statKeys` (Task 2).
- Produces: confirmed additions to `statKeys`; audit result for the owner.

- [ ] **Step 1: Baseline audit**

Run: `go run ./cmd/scoreaudit -season 2026 -weeks 1-3 -league espn:1214655831 -league espn:755035945 -league espn:1422028 -league espn:1215124 -v`
Record the summary line and the top-mismatch list in the task report.

- [ ] **Step 2: Confirm candidate mappings from the output**

For each mismatching stat ID among 63, 93, 101, 102, 103, 104, 201, 206, 209, 198, 8, 79, 82: in every listed example, find the Sleeper stat key whose value × the league's per-unit value for that ID (base, or the DEF override for D/ST) equals `expected`. Candidates to check first: 63 `fum_rec_td`, 93 `blk_kick_ret_td`, 101 `kr_td`, 102 `pr_td`, 103 `int_ret_td` / `def_td`, 104 `fum_ret_td`, 201 `fgm_60p`, 79 `fgmiss_40_49`, 82 `fgmiss_0_19`+`fgmiss_20_29`+`fgmiss_30_39`, 206 `def_2pt`. A mapping is confirmed only when the key explains every example the audit shows for that ID. ID 8 (every 25 passing yards) is a floor of `pass_yd/25`, not a per-unit stat: leave it unsupported.

- [ ] **Step 3: Add confirmed mappings test-first**

For each confirmed ID, add a case to `TestESPNStatIDForKey`'s map (`"<key>": <id>`) and remove its string from `wantUnsupported` in `TestConvertScoringRealLeague` (only for IDs present in the fixture); run `go test ./internal/providers/espn/` and see it fail; add the entry to `statKeys`; run again and see it pass.

- [ ] **Step 4: Re-run the audit**

Run the Step 1 command again. Expected: `PASS (>= 98%)`. If it still fails after all confirmable mappings, stop and report the remaining top mismatches to the owner with their explanations (e.g. Sleeper lacks the stat) rather than guessing mappings.

- [ ] **Step 5: Final verification and commit**

Run: `gofmt -l . ; go vet ./... && go test -race ./...`
Expected: clean, all `ok`.

```bash
git add internal/providers/espn
git commit -m "feat(espn): map stat IDs confirmed by scoreaudit"
```
