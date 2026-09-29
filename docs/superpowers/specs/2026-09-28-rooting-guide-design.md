# Rooting Guide Design

Date: 2026-09-28
Status: Approved design, pending spec review
Builds on: `docs/superpowers/specs/2026-09-28-league-viewer-design.md`,
`docs/superpowers/specs/2026-09-28-viewer-visuals-design.md`

## 1. Goal

A game-day "rooting guide" in the local viewer: for one week, across all of the owner's
leagues (4 ESPN, 3 Sleeper), which players appear in two or more of the owner's matchups —
in the owner's starting lineups ("for") or the opponents' ("against") — and the net
exposure, so the owner sees at a glance whose big game helps or hurts most.

**Decisions (owner):** purpose is a rooting guide (starters only, not bench exposure);
list only players with 2+ appearances, sorted by net exposure; "my team" is identified by
the API (not by display-name matching in the viewer).

**Out of scope:** bench/roster exposure analysis, highlighting the owner's team on league
pages (possible later with the same flag), a server-side aggregation endpoint, standings.

## 2. API Change: `Team.mine`

`domain.Team` gains `Mine bool` (JSON `mine`), true for the owner's team. Additive; no
existing client breaks.

- **Sleeper.** `League()` needs the owner's Sleeper user ID. The client resolves it from
  `SLEEPER_USERNAME` with the same `/user/<username>` lookup `ListLeagues` uses and caches
  it for the life of the client (one extra upstream call per cold start; concurrent first
  calls may both fetch, which is harmless). `rosterJSON` gains `CoOwners []string`
  (`co_owners`). A roster is mine when `owner_id` equals my ID or `co_owners` contains it.
  If the user lookup fails, `League()` fails the same way `ListLeagues` would.
- **ESPN.** A team is mine when any entry in `owners` equals the configured SWID,
  compared after trimming `{`/`}` and ignoring case. The client already holds the SWID
  (for its cookie); it keeps a normalized copy for this comparison. The SWID is never
  written to responses or logs — only the boolean.
- If no team matches, no team is marked; the league response is otherwise unchanged.
- Rollout: ships with the normal CI deploy. The viewer tolerates an API that has not been
  redeployed yet (every league then reports "couldn't find your team", §3.4).

## 3. Viewer: Rooting Guide Page

### 3.1 Route and navigation

- `GET /rooting?week=N` (week optional; same 1–18 validation and error page as
  `/league/{id}`). Linked from the leagues page ("Rooting guide →") and back to it
  ("← All leagues").
- Header card: "Rooting guide", "<season> · week N", and the same nav row as league
  pages (`‹ Week N-1`, week `<select>` + Go, `Week N+1 ›`, `⟳ Refresh`).

### 3.2 Data flow

1. `GET /v1/nfl/leagues` (its warnings pass through).
2. For every league, in parallel (errgroup, one goroutine per league):
   `GET /v1/nfl/leagues/{id}` and `GET /v1/nfl/leagues/{id}/matchups?include=stats&week=N`
   (`week` omitted when the request has none, so the API picks the current week).
3. Week shown = the request's week, else the `meta.week` of the first league (in list
   order) whose matchups loaded. Leagues whose `meta.week` differs from it get a warning
   ("<league>: returned week X") and are left out of the counts.
4. Matchup warnings are not repeated per league (they are noise at this level); league
   load failures are (§3.4).

### 3.3 Aggregation (pure function, `internal/viewer/rooting.go`)

For each league with a `mine` team and a matchup containing it that week:

- My side = the side whose `teamId` is mine; the other side is the opponent.
- Each **starter** on my side adds an appearance "for" (+1); each starter on the
  opponent's side adds an appearance "against" (−1). Bench and reserve never count.
- Player key: the canonical player ID when present (so a player merges across ESPN and
  Sleeper leagues); otherwise `<platform>:<platform id>` from `platformIds`; entries with
  neither are skipped.
- Each appearance records league name, league href, side, and that league's points for the
  player (`points`, may be null).

A player's **net** = for − against. Listed players: appearances (for + against) ≥ 2. Sort:
|net| descending, then appearances descending, then name ascending. A player can be for me
in one league and against me in another; each appearance counts on its own side (a player
is on at most one roster per league, so never both within one league).

### 3.4 Missing data and failures

| Condition | Result |
|---|---|
| League detail or matchups call fails | warning "<league>: <error message>", league skipped |
| No team marked `mine` | warning "<league>: couldn't find your team", league skipped |
| My team has no matchup this week (bye/playoffs) | league skipped silently, listed under "Not playing this week" in the header card |
| `/leagues` fails, or every league fails | error page (same as today) |
| No player reaches 2 appearances | muted empty-state card "No shared players this week." |

### 3.5 Layout (dark "game day" theme, existing tokens)

One card per listed player, in sort order:

- Avatar (existing `avatar` template: headshot / D/ST logo / initials) and name + NFL team.
- Net badge: `+3` (green), `−2` (red), `0` (muted), with "3 for · 1 against" beneath.
- League chips: one per appearance — league name and that league's points for him
  (`—` when null); class `for` (green-tinted) or `against` (red-tinted); each links to the
  league page for that week.

Summary line above the list: "N leagues counted" and the skipped/not-playing leagues.

## 4. Testing

- **Sleeper provider:** `mine` set via `owner_id`, via `co_owners`, and on no roster when
  neither matches; user ID looked up once across two `League()` calls.
- **ESPN provider:** `mine` set when owners contain the SWID with different case and with
  and without braces; unset otherwise; SWID does not appear in the marshalled league.
- **Aggregation:** merge across platforms by canonical ID; unmapped key fallback; bench
  ignored; for/against/net math; threshold of 2; sort order and ties; league with no
  `mine` team; my team with no matchup.
- **Rendering (fake API):** page lists a shared player with correct badge and chips, links
  to `/league/{id}?week=N`, shows the "couldn't find your team" and failed-league
  warnings, week validation error for `?week=0`, leagues page links to `/rooting`.
- Existing API and viewer tests stay green unchanged (the new JSON field is additive).
- Manual: after deploy, `make viewer`, open the rooting guide for weeks 2–3 and check a
  few players against the league pages.
