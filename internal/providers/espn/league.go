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
	conv := convertScoring(l.Settings.ScoringSettings.ScoringItems)
	return domain.League{
		ID: domain.LeagueID(domain.PlatformESPN, nativeID), Platform: domain.PlatformESPN,
		Sport: domain.SportNFL, Season: season, Name: l.Settings.Name, Teams: teams,
		Scoring: conv.rules, ScoringByPosition: conv.byPosition, DerivedStats: conv.derived,
		UnsupportedRules: conv.unsupported,
		RosterSlots:      rosterSlots(l.Settings.RosterSettings.LineupSlotCounts),
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
