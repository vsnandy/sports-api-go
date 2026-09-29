# Viewer Visuals Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restyle the local league viewer with a dark "game day" theme, a stacked scoreboard layout, player headshots, D/ST team logos, and colored stat chips.

**Architecture:** The view model (`internal/viewer/view.go`) gains image URLs, initials, and display flags computed from data the viewer already receives; templates and inline CSS are rewritten to the chosen design. No API change and no JavaScript.

**Tech Stack:** Go `html/template`, inline CSS; images load in the browser from `sleepercdn.com` and `a.espncdn.com`.

**Spec:** `docs/superpowers/specs/2026-09-28-viewer-visuals-design.md`

## Global Constraints

- Viewer-only change (templates, CSS, view model, handlers' league list); no API change; no JavaScript.
- Theme tokens exactly: `--bg #0d1321`, `--card #151d2e`, `--card-2 #1e2a42`, `--border #243049`, `--text #e8ecf4`, `--muted #7c8aa5`, `--win #3ee07f`, `--loss #ff6b81`, `--loss-bg #3a1e28`, `--link #7cc4ff`, `--warn #f0b429` (on `#2b2412`).
- Image URLs: D/ST `https://sleepercdn.com/images/team_logos/nfl/<lowercase team>.png`; Sleeper-ID players `https://sleepercdn.com/content/nfl/players/thumb/<id>.jpg`; unmapped ESPN players `https://a.espncdn.com/i/headshots/nfl/players/full/<espnId>.png`. IDs must be all digits (team: all ASCII letters) or no image.
- Avatars: initials under an `<img alt="" loading="lazy">`; D/ST uses `object-fit: contain`, no circle crop.
- Leading score: home leads if home ≥ away; away leads if away ≥ home (ties: both).
- Chips: signed values `+8.00` / `−2.00` (U+2212 minus), class `gain` for positive, `loss` otherwise.
- Existing viewer tests stay green unchanged (routes, escaping, errors, week bounds, loopback, redirect guard, copy like `week 3`, `Refresh`, `href="?week=N"`).

## Review Focus

1. **Missing or broken images (rookies, retired IDs, CDN change)** → the initials show; no broken-image icon. (Task 2: avatars always render initials under the `<img>`; `alt=""`.)
2. **Hostile or odd IDs (`../`, spaces, negative ESPN D/ST IDs)** → never interpolated into an image URL. (Task 1: `TestPlayerImage` non-numeric/space cases.)
3. **Tied matchups and 0–0 weeks** → both scores styled the same, no crash. (Task 1: `TestBuildLeagueViewFlags` tie case.)
4. **Names with punctuation/accents or one word** → sensible initials, never empty. (Task 1: `TestInitials`.)
5. **Players with no points or no breakdown (bench, bye)** → plain row with `—`, no empty chip area. (Task 2: `TestLeaguePageVisuals` Rookie Guy bench row.)

---

### Task 1: View model — images, initials, display flags

**Files:**
- Modify: `internal/viewer/view.go`
- Create: `internal/viewer/view_test.go`

**Interfaces:**
- Consumes: `domain.Player`, `domain.Matchup`, `domain.RosterEntry`, existing `buildLeagueView`, `buildSide`, `buildPlayer`, `breakdownRows`.
- Produces: `playerView` fields `Image string`, `Initials string`, `IsDEF bool`, `Bench bool`; `statRow.Gain bool`; `sideView.Leading bool`; functions `playerImage(p domain.Player) string`, `Initials(name string) string`.

- [ ] **Step 1: Write the failing tests**

`internal/viewer/view_test.go`:

```go
package viewer

import (
	"testing"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

func sid(s string) *string { return &s }

func TestPlayerImage(t *testing.T) {
	cases := []struct {
		name string
		p    domain.Player
		want string
	}{
		{"sleeper id", domain.Player{ID: sid("4046"), Position: "QB"}, "https://sleepercdn.com/content/nfl/players/thumb/4046.jpg"},
		{"DEF uses team logo", domain.Player{ID: sid("PIT"), Position: "DEF", NFLTeam: "PIT"}, "https://sleepercdn.com/images/team_logos/nfl/pit.png"},
		{"DEF falls back to ID", domain.Player{ID: sid("KC"), Position: "DEF"}, "https://sleepercdn.com/images/team_logos/nfl/kc.png"},
		{"unmapped ESPN D/ST", domain.Player{Position: "DEF", NFLTeam: "MIN", PlatformIDs: map[string]string{"espn": "-16016"}}, "https://sleepercdn.com/images/team_logos/nfl/min.png"},
		{"unmapped ESPN player", domain.Player{Position: "RB", PlatformIDs: map[string]string{"espn": "4685720"}}, "https://a.espncdn.com/i/headshots/nfl/players/full/4685720.png"},
		{"hostile ids", domain.Player{ID: sid("1/../2"), Position: "WR", PlatformIDs: map[string]string{"espn": "12 3"}}, ""},
		{"DEF with bad team", domain.Player{Position: "DEF", NFLTeam: "K C"}, ""},
		{"nothing", domain.Player{Position: "WR"}, ""},
	}
	for _, c := range cases {
		if got := playerImage(c.p); got != c.want {
			t.Errorf("%s: playerImage = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestInitials(t *testing.T) {
	for in, want := range map[string]string{
		"Patrick Mahomes": "PM",
		"A.J. Brown":      "AB",
		"Steelers D/ST":   "SD",
		"Pelé":            "P",
		"  ":              "?",
		"":                "?",
		"ja'marr chase":   "JC",
	} {
		if got := Initials(in); got != want {
			t.Errorf("Initials(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildLeagueViewFlags(t *testing.T) {
	pts := func(v float64) *float64 { return &v }
	lg := domain.League{Teams: []domain.Team{{ID: "1", Name: "A"}, {ID: "2", Name: "B"}}}
	ms := []domain.Matchup{
		{Home: domain.MatchupSide{TeamID: "1", Points: 22, Roster: domain.Roster{
			Starters: []domain.RosterEntry{{Slot: "DEF", Player: domain.Player{ID: sid("PIT"), Name: "Pittsburgh Steelers", Position: "DEF", NFLTeam: "PIT"},
				Points: pts(4), PointsBreakdown: map[string]float64{"sack": 3, "pass_int": -1}}},
			Bench: []domain.RosterEntry{{Slot: "BN", Player: domain.Player{Name: "Rookie Guy", Position: "RB"}}},
		}}, Away: domain.MatchupSide{TeamID: "2", Points: 0}},
		{Home: domain.MatchupSide{TeamID: "1", Points: 10}, Away: domain.MatchupSide{TeamID: "2", Points: 10}},
	}
	v := buildLeagueView(lg, ms, Meta{Week: 3})
	m := v.Matchups[0]
	if !m.Home.Leading || m.Away.Leading {
		t.Errorf("22 vs 0: leading = %v/%v, want true/false", m.Home.Leading, m.Away.Leading)
	}
	if tie := v.Matchups[1]; !tie.Home.Leading || !tie.Away.Leading {
		t.Errorf("tie: leading = %v/%v, want both", tie.Home.Leading, tie.Away.Leading)
	}
	def := m.Home.Starters[0]
	if !def.IsDEF || def.Bench || def.Initials != "PS" || def.Image != "https://sleepercdn.com/images/team_logos/nfl/pit.png" {
		t.Errorf("DEF row = %+v", def)
	}
	if !def.Breakdown[0].Gain || def.Breakdown[1].Gain {
		t.Errorf("gain flags = %+v", def.Breakdown)
	}
	if b := m.Home.Bench[0]; !b.Bench || b.Initials != "RG" || b.Image != "" {
		t.Errorf("bench row = %+v", b)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/viewer/ -run 'TestPlayerImage|TestInitials|TestBuildLeagueViewFlags'`
Expected: FAIL — `undefined: playerImage`, `undefined: Initials`, `m.Home.Leading undefined`.

- [ ] **Step 3: Implement**

In `internal/viewer/view.go`:

Add `Gain bool` to `statRow`; add to `playerView` (after `Stats`):

```go
	Image    string // headshot or team logo URL; empty when unknown
	Initials string // shown under the image, and alone when there is none
	IsDEF    bool
	Bench    bool
```

and add `Leading bool` to `sideView`.

In `buildLeagueView`, replace the matchup loop with:

```go
	for _, m := range ms {
		home, away := buildSide(m.Home, names), buildSide(m.Away, names)
		home.Leading = home.Points >= away.Points
		away.Leading = away.Points >= home.Points
		v.Matchups = append(v.Matchups, matchupView{Home: home, Away: away})
	}
```

In `buildSide`, mark bench/reserve rows:

```go
	for _, e := range append(slices.Clone(s.Roster.Bench), s.Roster.Reserve...) {
		pv := buildPlayer(e)
		pv.Bench = true
		sv.Bench = append(sv.Bench, pv)
	}
```

In `buildPlayer`, after the `pv := playerView{...}` line add:

```go
	pv.Image, pv.Initials, pv.IsDEF = playerImage(e.Player), Initials(e.Player.Name), e.Player.Position == "DEF"
```

In `breakdownRows`, set the flag when building rows: `rows = append(rows, statRow{Label: Label(k), Points: v, Gain: v > 0})`.

Append (and add `"unicode"` to imports):

```go
const (
	sleeperHeadshot = "https://sleepercdn.com/content/nfl/players/thumb/"
	sleeperLogo     = "https://sleepercdn.com/images/team_logos/nfl/"
	espnHeadshot    = "https://a.espncdn.com/i/headshots/nfl/players/full/"
)

// playerImage picks a team logo for D/ST, else a Sleeper or ESPN headshot. IDs must be
// all digits (team codes all letters) so nothing untrusted reaches an image URL.
func playerImage(p domain.Player) string {
	if p.Position == "DEF" {
		team := p.NFLTeam
		if team == "" && p.ID != nil {
			team = *p.ID
		}
		if allLetters(team) {
			return sleeperLogo + strings.ToLower(team) + ".png"
		}
		return ""
	}
	if p.ID != nil && allDigits(*p.ID) {
		return sleeperHeadshot + *p.ID + ".jpg"
	}
	if e := p.PlatformIDs["espn"]; allDigits(e) {
		return espnHeadshot + e + ".png"
	}
	return ""
}

// Initials returns the first letters of a name's first and last words ("A.J. Brown" → "AB").
func Initials(name string) string {
	var words []string
	for _, w := range strings.Fields(name) {
		letters := strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) {
				return unicode.ToUpper(r)
			}
			return -1
		}, w)
		if letters != "" {
			words = append(words, letters)
		}
	}
	first := func(s string) string { return string([]rune(s)[0]) }
	switch len(words) {
	case 0:
		return "?"
	case 1:
		return first(words[0])
	}
	return first(words[0]) + first(words[len(words)-1])
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func allLetters(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `gofmt -l . ; go vet ./... && go test -race ./internal/viewer/`
Expected: no gofmt output; `ok` (existing viewer tests unchanged and passing).

- [ ] **Step 5: Commit**

```bash
git add internal/viewer/view.go internal/viewer/view_test.go
git commit -m "feat(viewer): add headshot/logo URLs, initials, and display flags"
```

---

### Task 2: Dark "game day" templates

**Files:**
- Modify: `internal/viewer/templates/base.html`, `internal/viewer/templates/leagues.html`, `internal/viewer/templates/league.html`, `internal/viewer/templates/error.html`, `internal/viewer/handlers.go` (`funcs`, `leagueLink`, `leaguesView`, `leagues`), `internal/viewer/viewer_test.go`, `docs/superpowers/specs/2026-09-28-viewer-visuals-design.md` (§4 header copy)

**Interfaces:**
- Consumes: Task 1's `playerView.Image/Initials/IsDEF/Bench`, `statRow.Gain`, `sideView.Leading`.
- Produces: rendered pages; template func `signed(float64) string`; `leagueLink{Name, Href, Platform string}`, `leaguesView.Leagues []leagueLink` (ESPN first, then Sleeper).

- [ ] **Step 1: Write the failing tests**

In `internal/viewer/viewer_test.go`, in `matchupsJSON`, change Mahomes's breakdown from `{"pass_td":8,"espn_pass_yd_per_25":10}` to `{"pass_td":8,"espn_pass_yd_per_25":10,"pass_int":-2}` (points stay 18). Then append:

```go
func TestLeaguePageVisuals(t *testing.T) {
	_, h := newTestHandler(t)
	code, body := get(t, h, "/league/espn:1")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body,
		`--bg:#0d1321`,
		`PM<img src="https://sleepercdn.com/content/nfl/players/thumb/4046.jpg" alt="" loading="lazy">`,
		`class="avatar def">PS<img src="https://sleepercdn.com/images/team_logos/nfl/pit.png"`,
		`RG<img src="https://a.espncdn.com/i/headshots/nfl/players/full/9.png"`,
		`class="score leading">22.00`,
		`class="score trailing">0.00`,
		`class="chip gain">Passing TDs<b>+8.00</b>`,
		`class="chip loss">Interceptions thrown<b>−2.00</b>`,
		`class="row bench"`,
		`class="pts none">—`,
	)
}

func TestLeaguesPageBadges(t *testing.T) {
	_, h := newTestHandler(t)
	_, body := get(t, h, "/")
	mustContain(t, body, `class="badge espn">ESPN`, `class="badge sleeper">Sleeper`, `class="league-card" href="/league/espn:1"`)
	if strings.Index(body, "Office League") > strings.Index(body, "Dynasty") {
		t.Error("ESPN leagues should be listed before Sleeper leagues")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/viewer/ -run 'TestLeaguePageVisuals|TestLeaguesPageBadges'`
Expected: FAIL — page missing `--bg:#0d1321`, image markup, and badge classes.

- [ ] **Step 3: Implement handlers changes**

In `internal/viewer/handlers.go`, add to `funcs`:

```go
	"signed": func(v float64) string {
		if v < 0 {
			return fmt.Sprintf("−%.2f", -v)
		}
		return fmt.Sprintf("+%.2f", v)
	},
```

Replace `leagueLink`, `leaguesView`, and the loop in `leagues`:

```go
type leagueLink struct{ Name, Href, Platform string }

type leaguesView struct {
	Season   int
	Warnings []string
	Leagues  []leagueLink // ESPN first, then Sleeper
}
```

```go
	v := leaguesView{Season: meta.Season, Warnings: meta.Warnings}
	var sleeperLinks []leagueLink
	for _, l := range ls {
		link := leagueLink{Name: l.Name, Href: "/league/" + url.PathEscape(l.ID), Platform: string(l.Platform)}
		if l.Platform == domain.PlatformESPN {
			v.Leagues = append(v.Leagues, link)
		} else {
			sleeperLinks = append(sleeperLinks, link)
		}
	}
	v.Leagues = append(v.Leagues, sleeperLinks...)
```

- [ ] **Step 4: Implement the templates**

`internal/viewer/templates/base.html`:

```html
{{define "head"}}<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.}}</title>
<style>
:root{--bg:#0d1321;--card:#151d2e;--card-2:#1e2a42;--border:#243049;--text:#e8ecf4;--muted:#7c8aa5;--win:#3ee07f;--loss:#ff6b81;--loss-bg:#3a1e28;--link:#7cc4ff;--warn:#f0b429;--warn-bg:#2b2412}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);font:14px/1.45 system-ui,sans-serif}
main{max-width:76rem;margin:0 auto;padding:1.25rem 1rem 3rem}
a{color:var(--link);text-decoration:none} a:hover{text-decoration:underline}
h1{font-size:1.5rem;margin:.15rem 0} h3{font-size:.95rem;margin:.2rem 0 .4rem}
.muted{color:var(--muted);font-size:12px}
.card{background:var(--card);border:1px solid var(--border);border-radius:12px;padding:.8rem 1rem;margin:.6rem 0}
.warn{background:var(--warn-bg);border:1px solid var(--warn);color:var(--warn);border-radius:10px;padding:.45rem .8rem;margin:.4rem 0;font-size:13px}
.err{border-left:4px solid var(--loss)}
.nav{display:flex;flex-wrap:wrap;gap:.8rem;align-items:center;margin-top:.6rem}
.nav form{display:inline-flex;gap:.3rem}
select,button{background:var(--card-2);color:var(--text);border:1px solid var(--border);border-radius:8px;padding:.25rem .5rem;font:inherit}
button{cursor:pointer}
summary{cursor:pointer;list-style:none} summary::-webkit-details-marker{display:none}
.matchup{background:var(--card);border:1px solid var(--border);border-radius:12px;margin:.5rem 0}
.matchup[open]{border-color:rgba(62,224,127,.45)}
.strip{display:grid;grid-template-columns:1fr auto auto auto 1fr auto;gap:.8rem;align-items:center;padding:.7rem 1rem}
.strip .away{text-align:right}
.team{font-weight:700}
.score{font-size:1.35rem;font-weight:800;font-variant-numeric:tabular-nums}
.score.leading{color:var(--win)} .score.trailing{color:#9aa7c2}
.dash{color:#4a5775}
.chev{color:var(--muted);transition:transform .15s} .matchup[open] .chev{transform:rotate(180deg)}
.sides{display:grid;grid-template-columns:1fr 1fr;gap:1.2rem;padding:0 1rem 1rem}
@media (max-width:760px){.sides{grid-template-columns:1fr}}
.side-head{display:flex;justify-content:space-between;align-items:baseline;border-bottom:1px solid var(--border);padding-bottom:.3rem}
.row{display:flex;align-items:center;gap:.6rem;padding:.35rem 0;border-bottom:1px solid var(--border)}
.row.bench{opacity:.7}
.slot{width:2.6rem;font-size:11px;color:var(--muted);flex:none}
.avatar{position:relative;width:34px;height:34px;flex:none;border-radius:50%;background:var(--card-2);display:flex;align-items:center;justify-content:center;font-size:11px;font-weight:700;color:var(--muted);overflow:hidden}
.avatar img{position:absolute;inset:0;width:100%;height:100%;object-fit:cover}
.avatar.def{border-radius:6px;background:transparent}
.avatar.def img{object-fit:contain}
.who{flex:1;min-width:0}
.who .name,.who .sub{display:block}
.who .name{font-weight:600;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.who .sub{color:var(--muted);font-size:11px}
.tag{font-size:10px;border-radius:4px;padding:1px 6px;flex:none}
.tag.ESPN{background:#1e3a5f;color:var(--link)} .tag.computed{background:#173a2a;color:var(--win)}
.pts{min-width:3.6rem;font-weight:700;color:var(--win);text-align:right;font-variant-numeric:tabular-nums;flex:none}
.pts.none{color:var(--muted);font-weight:400}
.benchlabel{font-size:11px;text-transform:uppercase;letter-spacing:.08em;color:var(--muted);padding:.7rem 0 .2rem}
.chips{display:flex;flex-wrap:wrap;gap:.35rem;padding:.45rem 0 .5rem 3.2rem}
.chip{background:var(--card-2);border-radius:999px;padding:2px 9px;font-size:12px}
.chip b{margin-left:.35rem;font-variant-numeric:tabular-nums}
.chip.gain b{color:var(--win)} .chip.loss{background:var(--loss-bg)} .chip.loss b{color:var(--loss)}
.chip.total{border:1px solid var(--border);background:transparent}
.rawstats{padding:0 0 .5rem 3.2rem;color:var(--muted);font-size:11px}
.leagues{display:grid;grid-template-columns:repeat(auto-fill,minmax(16rem,1fr));gap:.8rem;margin-top:1rem}
.league-card{display:flex;justify-content:space-between;align-items:center;gap:.6rem;background:var(--card);border:1px solid var(--border);border-radius:12px;padding:1rem;color:var(--text);font-weight:700}
.league-card:hover{border-color:var(--link);text-decoration:none}
.badge{font-size:10px;font-weight:700;border-radius:4px;padding:2px 6px;letter-spacing:.04em;flex:none}
.badge.espn{background:#5a1a1a;color:#ff8a80} .badge.sleeper{background:#2a2350;color:#b9a7ff}
table{border-collapse:collapse;width:100%} td,th{padding:.25rem .4rem;border-bottom:1px solid var(--border);text-align:left}
.num{text-align:right;font-variant-numeric:tabular-nums}
.empty{color:var(--muted);text-align:center;padding:1.5rem}
</style></head><body><main>{{end}}
{{define "foot"}}</main></body></html>{{end}}
```

`internal/viewer/templates/leagues.html`:

```html
{{template "head" "Leagues"}}
<h1>Leagues {{.Season}}</h1>
{{range .Warnings}}<div class="warn">{{.}}</div>{{end}}
<div class="leagues">
{{range .Leagues}}<a class="league-card" href="{{.Href}}"><span>{{.Name}}</span><span class="badge {{.Platform}}">{{if eq .Platform "espn"}}ESPN{{else}}Sleeper{{end}}</span></a>
{{end}}</div>
{{template "foot"}}
```

`internal/viewer/templates/league.html`:

```html
{{template "head" .Name}}
<div class="card">
<a href="/">← All leagues</a>
<h1>{{.Name}}</h1>
<div class="muted">{{.Season}} · week {{.Week}}</div>
<div class="nav">
{{if .Prev}}<a href="?week={{.Prev}}">‹ Week {{.Prev}}</a>{{end}}
<form method="get"><select name="week">{{range .Weeks}}<option value="{{.}}"{{if eq . $.Week}} selected{{end}}>Week {{.}}</option>{{end}}</select> <button>Go</button></form>
{{if .Next}}<a href="?week={{.Next}}">Week {{.Next}} ›</a>{{end}}
<a href="?week={{.Week}}">⟳ Refresh</a>
</div>
</div>
{{range .Warnings}}<div class="warn">{{.}}</div>{{end}}
{{if not .Matchups}}<div class="card empty">No matchups this week.</div>{{end}}
{{range .Matchups}}
<details class="matchup"><summary class="strip">
<span class="team">{{.Home.Name}}</span>
<span class="score {{if .Home.Leading}}leading{{else}}trailing{{end}}">{{pts .Home.Points}}</span>
<span class="dash">–</span>
<span class="score {{if .Away.Leading}}leading{{else}}trailing{{end}}">{{pts .Away.Points}}</span>
<span class="team away">{{.Away.Name}}</span>
<span class="chev">▾</span>
</summary>
<div class="sides">{{template "side" .Home}}{{template "side" .Away}}</div>
</details>
{{end}}
<details class="card"><summary><h3 style="display:inline">Scoring rules</h3></summary>
<h3>Base</h3>
<table>{{range .Rules}}<tr><td>{{.Label}}</td><td class="num">{{num .Value}}</td></tr>{{end}}</table>
{{range .ByPosition}}<h3>{{.Position}}</h3>
<table>{{range .Rules}}<tr><td>{{.Label}}</td><td class="num">{{num .Value}}</td></tr>{{end}}</table>{{end}}
{{if .Derived}}<h3>Computed stats</h3>
<table>{{range .Derived}}<tr><td>{{.Label}}</td><td>{{.From}}</td><td>{{.Range}}</td></tr>{{end}}</table>{{end}}
{{if .Unsupported}}<h3>Unsupported</h3><ul>{{range .Unsupported}}<li>{{.}}</li>{{end}}</ul>{{end}}
</details>
{{template "foot"}}

{{define "side"}}<div>
<div class="side-head"><h3>{{.Name}}</h3><span class="pts">{{pts .Points}}</span></div>
{{range .Starters}}{{template "player" .}}{{end}}
{{if .Bench}}<div class="benchlabel">Bench</div>{{range .Bench}}{{template "player" .}}{{end}}{{end}}
</div>{{end}}

{{define "avatar"}}<span class="avatar{{if .IsDEF}} def{{end}}">{{.Initials}}{{if .Image}}<img src="{{.Image}}" alt="" loading="lazy">{{end}}</span>{{end}}

{{define "rowbody"}}<span class="slot">{{.Slot}}</span>{{template "avatar" .}}<span class="who"><span class="name">{{.Name}}</span><span class="sub">{{.NFLTeam}}</span></span>{{if .Source}}<span class="tag {{.Source}}">{{.Source}}</span>{{end}}<span class="pts{{if not .Points}} none{{end}}">{{pts .Points}}</span>{{end}}

{{define "player"}}{{if .Breakdown}}<details class="player"><summary class="row{{if .Bench}} bench{{end}}">{{template "rowbody" .}}</summary>
<div class="chips">{{range .Breakdown}}<span class="chip {{if .Gain}}gain{{else}}loss{{end}}">{{.Label}}<b>{{signed .Points}}</b></span>{{end}}<span class="chip total">Total<b>{{pts .Total}}</b></span></div>
{{if .Stats}}<div class="rawstats">{{.Stats}}</div>{{end}}
</details>{{else}}<div class="row{{if .Bench}} bench{{end}}">{{template "rowbody" .}}</div>{{end}}{{end}}
```

`internal/viewer/templates/error.html`:

```html
{{template "head" .Title}}
<p><a href="/">← All leagues</a></p>
<div class="card err"><h1>{{.Title}}</h1>
{{if .Code}}<p><code>{{.Code}}</code></p>{{end}}
<p>{{.Message}}</p></div>
{{template "foot"}}
```

In the spec §4, change "League name, "Week N · 2026"," to "league name, "2026 · week N" (existing copy),".

- [ ] **Step 5: Run the tests to verify they pass**

Run: `gofmt -l . ; go vet ./... && go test -race ./...`
Expected: no gofmt output; all packages `ok`, including every pre-existing viewer test unchanged.

- [ ] **Step 6: Commit**

```bash
git add internal/viewer docs/superpowers/specs/2026-09-28-viewer-visuals-design.md
git commit -m "feat(viewer): dark game-day theme with headshots, logos, and stat chips"
```

- [ ] **Step 7 (controller): Visual check**

Run the viewer against the live API (key from SSM, never printed), render each of the 7 league pages for weeks 2–3, and confirm: images load (spot-check a few `src` URLs return 200), D/ST rows show logos, leading scores are green, chips color by sign. Share one rendered page with the owner via the visual companion for a look.
