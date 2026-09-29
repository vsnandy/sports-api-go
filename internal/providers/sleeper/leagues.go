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
		Scoring: scoring, ScoringByPosition: map[string]domain.ScoringRules{},
		DerivedStats: []domain.DerivedStat{}, UnsupportedRules: []string{}, RosterSlots: l.RosterPositions,
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
