# Viewer Visuals Design

Date: 2026-09-28
Status: Approved design, pending spec review
Builds on: `docs/superpowers/specs/2026-09-28-league-viewer-design.md`

## 1. Goal

Make the local league viewer visually appealing: player headshots, team logos for D/ST,
a dark "game day" theme, and a stacked scoreboard layout. Chosen with the owner via
mockups (style **B · Dark "game day"**, layout **A · Stacked scoreboard list**).

**Constraints:** viewer-only change (templates, CSS, view model); no API change; still no
JavaScript; images load directly from Sleeper/ESPN CDNs in the browser (owner's choice);
existing behavior (routes, escaping, errors, key handling, loopback-only) unchanged.

**Out of scope:** win–loss records/standings (the API has none), "my team" highlighting,
image proxying/caching, mobile-specific layout.

## 2. Theme

CSS custom properties on `:root`, used by all templates:

| Token | Value | Use |
|---|---|---|
| `--bg` | `#0d1321` | page background |
| `--card` | `#151d2e` | cards, strips |
| `--card-2` | `#1e2a42` | stat chips, hover |
| `--border` | `#243049` | card borders, row dividers |
| `--text` | `#e8ecf4` | primary text |
| `--muted` | `#7c8aa5` | secondary text, slots, bench divider |
| `--win` | `#3ee07f` | points, leading score, gain chips |
| `--loss` | `#ff6b81` | loss chips |
| `--loss-bg` | `#3a1e28` | loss chip background |
| `--link` | `#7cc4ff` | links, nav |
| `--warn` | `#f0b429` | warning callouts (on `#2b2412`) |

Font: `system-ui, sans-serif`; numbers use `font-variant-numeric: tabular-nums`.

## 3. Images

The view model sets `playerView.Image` (URL or empty) and `playerView.Initials`:

1. Position `DEF` (Sleeper D/ST player, ID = team abbreviation) →
   `https://sleepercdn.com/images/team_logos/nfl/<lowercase NFL team>.png`
   (team from `player.nflTeam`, falling back to the player ID).
2. Player with a numeric Sleeper ID →
   `https://sleepercdn.com/content/nfl/players/thumb/<id>.jpg`.
3. Otherwise, a numeric ESPN ID in `platformIds.espn` →
   `https://a.espncdn.com/i/headshots/nfl/players/full/<espnId>.png`.
4. Otherwise no image.

`Initials(name)`: first letter of the first and last words, uppercase, letters only
(`"Patrick Mahomes"` → `PM`, `"A.J. Brown"` → `AB`, `"Steelers D/ST"` → `SD`,
`"Pelé"` → `P`); empty name → `?`.

**No-JavaScript fallback:** each avatar is a fixed-size circle showing the initials, with the
`<img alt="" loading="lazy">` absolutely positioned on top. A failed image renders nothing
(empty alt), so the initials show through. D/ST logos use `object-fit: contain` with no
circle crop. URLs are built only from digits and lowercased letters (IDs validated), so no
untrusted text reaches an `src` attribute.

## 4. Pages

**Leagues (`/`):** header "Leagues 2026"; a responsive grid of league cards, each with the
league name and a platform badge (`ESPN` / `Sleeper`); warnings as amber callouts.

**League (`/league/{id}`):**
- Header card: `← All leagues`, league name, "2026 · week N" (existing copy), and a nav row:
  `‹ Week N-1` (hidden at 1), week `<select>` + Go button, `Week N+1 ›` (hidden at 18),
  `⟳ Refresh`.
- Warnings: amber callouts.
- Matchups: one `<details class="matchup">` per matchup. The `<summary>` is a full-width
  scoreboard strip: home name, home score, "–", away score, away name, and a chevron.
  The higher score gets class `leading` (green); the other `trailing` (muted); ties both
  `leading`. Open matchups get a green-tinted border.
- Expanded: two columns (home, away), each with the team name and total, then roster rows:
  slot (muted, fixed width), avatar, name + NFL team (muted), source tag
  (`ESPN` blue / `computed` green), points (green, bold, `—` when null).
  A dimmed "Bench" divider precedes bench/IR rows (bench rows at reduced opacity).
- Player rows with a breakdown are nested `<details>`; opening one shows stat chips —
  label and signed points (`+12.00` / `−2.00`), class `gain` (green on `--card-2`) or
  `loss` (red on `--loss-bg`), ordered as today (by absolute points) — then a total and
  the raw stat line in muted text.
- Scoring rules: a card-styled `<details>` with the same sections as today.
- "No matchups this week." styled as a muted empty-state card.

**Error page:** dark card with a red left border; title, code, message; link back.

## 5. View Model Changes (`internal/viewer/view.go`)

- `playerView` gains `Image string`, `Initials string`, `IsDEF bool`, `Bench bool`.
- `statRow` gains `Gain bool` (points > 0).
- `sideView` gains `Leading bool` (set per matchup: home ≥ away → home leading; away ≥
  home → away leading).
- New pure functions: `playerImage(p domain.Player) string`, `Initials(name string) string`.

## 6. Testing

- `playerImage`: DEF → lowercase team logo URL; numeric Sleeper ID → Sleeper headshot;
  unmapped with numeric ESPN ID → ESPN headshot; non-numeric/absent IDs → empty.
- `Initials`: the examples in §3 plus empty and single-word names.
- Rendering (fake API): league page contains the Sleeper headshot `src` for a Sleeper-ID
  player, the logo `src` for D/ST, the ESPN headshot for an unmapped player with an ESPN
  ID, initials for every avatar; the leading side has class `leading`; chips carry `gain`
  / `loss`; leagues page has platform badges.
- All existing viewer tests (escaping, errors, week bounds, loopback, redirects) stay
  green unchanged.
- Manual: `make viewer` against the owner's leagues; check all 7 leagues visually.
