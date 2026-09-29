package viewer

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

type statRow struct {
	Label  string
	Points float64
	Gain   bool
}

type playerView struct {
	Slot, Name, NFLTeam string
	Points              *float64
	Source              string // "ESPN", "computed", or ""
	Breakdown           []statRow
	Total               float64
	Stats               string
	Image               string // headshot or team logo URL; empty when unknown
	Initials            string // shown under the image, and alone when there is none
	IsDEF               bool
	Bench               bool
}

type sideView struct {
	Name     string
	Points   float64
	Starters []playerView
	Bench    []playerView
	Leading  bool
}

type matchupView struct{ Home, Away sideView }

type ruleView struct {
	Label string
	Value float64
}

type positionRules struct {
	Position string
	Rules    []ruleView
}

type derivedView struct{ Label, From, Range string }

type leagueView struct {
	ID, Name                 string
	Season, Week, Prev, Next int
	Weeks                    []int
	Warnings                 []string
	Matchups                 []matchupView
	Rules                    []ruleView
	ByPosition               []positionRules
	Derived                  []derivedView
	Unsupported              []string
}

func buildLeagueView(lg domain.League, ms []domain.Matchup, meta Meta) leagueView {
	names := map[string]string{}
	for _, t := range lg.Teams {
		names[t.ID] = t.Name
	}
	v := leagueView{ID: lg.ID, Name: lg.Name, Season: meta.Season, Week: meta.Week, Warnings: meta.Warnings, Unsupported: lg.UnsupportedRules}
	v.Prev, v.Next, v.Weeks = weekNav(v.Week)
	for _, m := range ms {
		home, away := buildSide(m.Home, names), buildSide(m.Away, names)
		home.Leading = home.Points >= away.Points
		away.Leading = away.Points >= home.Points
		v.Matchups = append(v.Matchups, matchupView{Home: home, Away: away})
	}
	v.Rules = rules(lg.Scoring)
	positions := make([]string, 0, len(lg.ScoringByPosition))
	for p := range lg.ScoringByPosition {
		positions = append(positions, p)
	}
	sort.Strings(positions)
	for _, p := range positions {
		v.ByPosition = append(v.ByPosition, positionRules{Position: p, Rules: rules(lg.ScoringByPosition[p])})
	}
	for _, d := range lg.DerivedStats {
		v.Derived = append(v.Derived, derivedView{Label: Label(d.Key), From: Label(d.From), Range: rangeText(d)})
	}
	return v
}

func buildSide(s domain.MatchupSide, names map[string]string) sideView {
	name := names[s.TeamID]
	if name == "" {
		name = "Team " + s.TeamID
	}
	sv := sideView{Name: name, Points: s.Points}
	for _, e := range s.Roster.Starters {
		sv.Starters = append(sv.Starters, buildPlayer(e))
	}
	for _, e := range append(slices.Clone(s.Roster.Bench), s.Roster.Reserve...) {
		pv := buildPlayer(e)
		pv.Bench = true
		sv.Bench = append(sv.Bench, pv)
	}
	return sv
}

func buildPlayer(e domain.RosterEntry) playerView {
	pv := playerView{Slot: e.Slot, Name: e.Player.Name, NFLTeam: e.Player.NFLTeam, Points: e.Points}
	pv.Image, pv.Initials, pv.IsDEF = playerImage(e.Player), Initials(e.Player.Name), e.Player.Position == "DEF"
	switch e.PointsSource {
	case "platform":
		pv.Source = "ESPN"
	case "computed":
		pv.Source = "computed"
	}
	pv.Breakdown, pv.Total = breakdownRows(e.PointsBreakdown)
	keys := make([]string, 0, len(e.Stats))
	for k := range e.Stats {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %g", k, e.Stats[k]))
	}
	pv.Stats = strings.Join(parts, " · ")
	return pv
}

// breakdownRows labels a breakdown, sorts by absolute points (then label), and totals it.
func breakdownRows(b map[string]float64) ([]statRow, float64) {
	rows := make([]statRow, 0, len(b))
	var total float64
	for k, v := range b {
		rows = append(rows, statRow{Label: Label(k), Points: v, Gain: v > 0})
		total += v
	}
	slices.SortFunc(rows, func(a, b statRow) int {
		if c := cmp.Compare(math.Abs(b.Points), math.Abs(a.Points)); c != 0 {
			return c
		}
		return cmp.Compare(a.Label, b.Label)
	})
	return rows, math.Round(total*100) / 100
}

// rules lists non-zero rules by label.
func rules(r domain.ScoringRules) []ruleView {
	out := []ruleView{}
	for k, v := range r {
		if v != 0 {
			out = append(out, ruleView{Label: Label(k), Value: v})
		}
	}
	slices.SortFunc(out, func(a, b ruleView) int { return cmp.Compare(a.Label, b.Label) })
	return out
}

func rangeText(d domain.DerivedStat) string {
	switch {
	case d.Step > 0:
		return fmt.Sprintf("every %g", d.Step)
	case d.Max == nil:
		return fmt.Sprintf("%g+", d.Min)
	case *d.Max == d.Min:
		return fmt.Sprintf("%g", d.Min)
	default:
		return fmt.Sprintf("%g–%g", d.Min, *d.Max)
	}
}

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
