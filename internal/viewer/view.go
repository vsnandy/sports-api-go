package viewer

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

type statRow struct {
	Label  string
	Points float64
}

type playerView struct {
	Slot, Name, NFLTeam string
	Points              *float64
	Source              string // "ESPN", "computed", or ""
	Breakdown           []statRow
	Total               float64
	Stats               string
}

type sideView struct {
	Name     string
	Points   float64
	Starters []playerView
	Bench    []playerView
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
	if v.Week > 1 {
		v.Prev = v.Week - 1
	}
	if v.Week < 18 {
		v.Next = v.Week + 1
	}
	for w := 1; w <= 18; w++ {
		v.Weeks = append(v.Weeks, w)
	}
	for _, m := range ms {
		v.Matchups = append(v.Matchups, matchupView{Home: buildSide(m.Home, names), Away: buildSide(m.Away, names)})
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
		sv.Bench = append(sv.Bench, buildPlayer(e))
	}
	return sv
}

func buildPlayer(e domain.RosterEntry) playerView {
	pv := playerView{Slot: e.Slot, Name: e.Player.Name, NFLTeam: e.Player.NFLTeam, Points: e.Points}
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
		rows = append(rows, statRow{Label: Label(k), Points: v})
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
