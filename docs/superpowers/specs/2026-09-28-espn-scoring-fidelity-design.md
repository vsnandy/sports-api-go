# ESPN Scoring Fidelity Design

Date: 2026-09-28
Status: Approved design, pending spec review
Builds on: `docs/superpowers/specs/2026-09-25-sports-api-v1-design.md`

## 1. Problem and Goal

Computed ESPN fantasy points are too low. Against ESPN's own team totals for week 2 of
2026, our starter sums are 2–16 points low on average (max 27) in all four of the
owner's ESPN leagues, while Sleeper leagues match exactly (38/38 sides).

Causes, verified against live ESPN responses:
1. **D/ST scores 0.** ESPN defines defensive scoring as `pointsOverrides` keyed `"16"`
   (the D/ST lineup slot) on items whose base value is 0. Our flat rules cannot express
   per-position values.
2. **Unmapped stat IDs**: return/defensive TDs and a few bonuses (63, 93, 101–104, 120,
   127, 201, 206, 8, 79, 82, plus unknown 198/209).
3. **Tier mismatch**: ESPN points-allowed (0/1–6/7–13/14–17/18–21/22–27/28–34/35–45/46+)
   and yards-allowed tiers don't align with Sleeper's `pts_allow_*`/`yds_allow_*` buckets.

ESPN's boxscore entries already carry each player's week total under league rules
(`stats[].appliedTotal` for the actual-stats row, equal to `appliedStatTotal`) and a
per-stat breakdown (`appliedStats`). Starter totals sum exactly to ESPN's team totals.

**Goal (owner's option C):**
- ESPN league matchups show ESPN's own per-player points (exact by construction).
- Applying an ESPN league's rules to any player (e.g. `gamelog?scoring=espn:<id>`) uses an
  upgraded rules engine.

**Success criteria:**
- Unit tests pass (offline).
- `cmd/scoreaudit` over the owner's 4 ESPN leagues, 2026 weeks 1–3, reports ≥ 98% of
  compared player-weeks exact (|engine − ESPN| < 0.01). Unmapped players are excluded and
  counted separately. Every remaining mismatch is attributed to a stat ID.
- Sleeper leagues and presets produce identical points to today.

**Out of scope:** projections; Sleeper leagues with exotic bonus rules; IDP leagues;
infrastructure changes.

## 2. Scoring Model (`internal/domain`, `internal/scoring`)

```go
// domain
type Scoring struct {
    Rules      ScoringRules            // base points per unit, Sleeper stat keys (existing type)
    ByPosition map[string]ScoringRules // position → rules replacing the base value for that key
    Derived    []DerivedStat           // indicator stats computed from raw stats before scoring
}

type DerivedStat struct {
    Key  string   `json:"key"`  // e.g. "espn_pa_14_17"
    From string   `json:"from"` // raw stat key, e.g. "pts_allow"
    Min  float64  `json:"min"`  // inclusive
    Max  *float64 `json:"max"`  // inclusive; nil = unbounded
}
```

- `domain.League` keeps `Scoring ScoringRules \`json:"scoring"\`` and gains
  `ScoringByPosition map[string]ScoringRules \`json:"scoringByPosition"\`` and
  `DerivedStats []DerivedStat \`json:"derivedStats"\`` (non-breaking API change; both
  always non-nil in responses). `func (l League) ScoringModel() Scoring` assembles the
  three.
- Positions are Sleeper position strings: `QB RB WR TE K DEF`.

**Engine (`internal/scoring`):**
- `func Points(stats map[string]float64, s domain.Scoring, position string) float64`
- `func Breakdown(stats map[string]float64, s domain.Scoring, position string) map[string]float64`
  — points contributed per stat key (derived keys included), unrounded; `Points` is the
  rounded (2 dp) sum of `Breakdown`.
- Algorithm: copy `stats`; for each `DerivedStat`, if `stats[From]` exists and
  `Min ≤ v ≤ Max` (or `Max == nil`), set `stats[Key] = 1`. Then for each stat key, weight =
  `ByPosition[position][key]` if present, else `Rules[key]` if present, else skip;
  contribution = value × weight.
- `Preset(name)` returns `(domain.Scoring, bool)` with only `Rules` set (values unchanged).

## 3. Points Sources

**ESPN adapter (`internal/providers/espn`):**
- `entryJSON.playerPoolEntry.player` gains
  `stats []{ scoringPeriodId, statSourceId, statSplitTypeId, appliedTotal, appliedStats map[string]float64 }`.
- `Matchups(…, week)`: for each entry, the actual-stats row is the one with
  `statSourceId == 0 && statSplitTypeId == 1 && scoringPeriodId == week`. If present,
  `RosterEntryRef.PlatformPoints = &appliedTotal`; otherwise nil.
- `Rosters` does not set `PlatformPoints` (no week).
- New method for the audit:
  `func (c *Client) WeekPlayerPoints(ctx, nativeID string, season, week int) ([]PlayerWeekPoints, error)`
  with `PlayerWeekPoints{Ref domain.PlayerRef; Total float64; ByStat map[int]float64}`,
  covering every rostered entry (starters, bench, IR) that has an actual-stats row.

**Domain:** `RosterEntryRef` gains `PlatformPoints *float64`; `RosterEntry` gains
`PointsSource string \`json:"pointsSource,omitempty"\`` (`"platform"` | `"computed"`).

**Service (`internal/service`):**
- `Matchups(…, withStats=true)`: per entry, if `PlatformPoints != nil` →
  `Points = PlatformPoints`, `PointsSource = "platform"`; else if a Sleeper stat line
  exists → `Points = scoring.Points(line.Stats, league.ScoringModel(), player.Position)`,
  `PointsSource = "computed"`; else both unset. `Stats` is still attached from Sleeper
  whenever a line exists (unmapped players keep `Stats: null` but may have platform points).
- `Gamelog(…, scoring)`: `rulesFor` returns `domain.Scoring`; points use the player's
  Sleeper position.
- Without `include=stats`, no points or source are attached (unchanged).

## 4. ESPN Translation (`internal/providers/espn`)

`convertScoring(items) (rules ScoringRules, byPosition map[string]ScoringRules, derived []DerivedStat, unsupported []string)`:

- **Direct stat IDs** (per unit → Sleeper key): existing table, plus 120 → `pts_allow`,
  127 → `yds_allow`, and candidates for 8, 63, 79, 82, 93, 101–104, 201, 206. Each new
  mapping is kept only once `cmd/scoreaudit` confirms it (§5); unconfirmed IDs stay in
  `unsupported`.
- **Points-allowed tiers** → `DerivedStat{From: "pts_allow"}`, keys `espn_pa_<range>`:
  89 [0,0] · 90 [1,6] · 91 [7,13] · 92 [14,17] · 121 [18,21] · 122 [22,27] ·
  123 [28,34] · 124 [35,45] · 125 [46,∞).
- **Yards-allowed tiers** → `DerivedStat{From: "yds_allow"}`, keys `espn_ya_<range>`:
  128 [0,99] · 129 [100,199] · 130 [200,299] · 131 [300,349] · 132 [350,399] ·
  133 [400,449] · 134 [450,499] · 135 [500,549] · 136 [550,∞).
  A tier item's `points` becomes `Rules[key]`; its overrides become `ByPosition`.
- **`pointsOverrides`**: keys are lineup slot IDs, translated to positions
  (0→QB, 2→RB, 4→WR, 6→TE, 16→DEF, 17→K) into `ByPosition[pos][key]`. Other slot keys
  → `unsupported` entry `espn stat <id> override for slot <slot>`.
- Unknown stat IDs (e.g. 198, 209) → `unsupported` as today.
- Exported for the audit: `func ESPNStatIDForKey(key string) (int, bool)` covering direct
  and derived keys.

## 5. Audit Tool

**`internal/scoreaudit`** (pure, unit-tested):
- Input: ESPN `PlayerWeekPoints` per league/week, resolved Sleeper players, Sleeper week
  stat lines, league `Scoring`.
- Per player-week: unmapped → excluded; else engine points vs ESPN total; exact if
  |diff| < 0.01. For mismatches, compare `Breakdown` (mapped back via
  `ESPNStatIDForKey`) against ESPN `ByStat`, attributing the difference to stat IDs.
- Output: `Report{Compared, Exact, Unmapped int; Mismatches []StatMismatch}` with
  `StatMismatch{StatID int; Count int; Expected, Got float64; Example string}` sorted by
  count; `Report.Pass(threshold float64) bool`.

**`cmd/scoreaudit`** (thin wiring, run locally):
```
go run ./cmd/scoreaudit -season 2026 -weeks 1-3 \
  -league espn:1214655831 -league espn:755035945 -league espn:1422028 -league espn:1215124
```
- `-league` repeatable; omitted → all ESPN leagues for the season from
  `ListLeagues`. `-weeks` accepts `N` or `N-M`. `-threshold` default `0.98`.
- Cookies from `ESPN_S2`/`ESPN_SWID`, else SSM `/sports-api/espn-s2` and
  `/sports-api/espn-swid` (region `AWS_REGION`, default `us-east-1`). Players index uses
  `players.NoStore{}` (fetches Sleeper's dump directly).
- Prints the summary and top 10 mismatching stat IDs; exit 1 below threshold.
- `make scoreaudit` runs it with `-season 2026 -weeks 1-3`.

## 6. Order of Work

1. Scoring model + engine (Sleeper/presets unchanged).
2. ESPN translation: overrides, tiers, direct IDs already in the community map.
3. ESPN platform points + `WeekPlayerPoints`; service `pointsSource`.
4. `internal/scoreaudit` + `cmd/scoreaudit`.
5. Run the audit; confirm or drop candidate stat-ID mappings from its per-stat output
   until ≥ 98% exact (owner runs it if cookies/network aren't available to the
   implementer).

## 7. Testing

- Engine: override replaces base only for its position; derived boundaries inclusive at
  both ends; unbounded max; missing raw stat → no derived stat; `Breakdown` sums to
  `Points`; presets/Sleeper unchanged (existing tests stay green).
- ESPN conversion: fixture of the owner's real `scoringItems` (league 1214655831; no
  secrets) asserts base rules, `ByPosition["DEF"]` values, tier ranges and points, and
  `unsupported` contents.
- ESPN adapter: trimmed real boxscore fixture — `PlatformPoints` from the actual-stats
  row of the requested week, not projections (`statSourceId == 1`) or other weeks;
  `WeekPlayerPoints` totals and `ByStat`.
- Service: ESPN entries → `"platform"`; missing platform points → `"computed"`; Sleeper →
  `"computed"`; unmapped ESPN player keeps platform points with `Stats: null`.
- Audit logic: exact match, mismatch attributed to the right stat ID, unmapped excluded,
  `Pass` threshold.
