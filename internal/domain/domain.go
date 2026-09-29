// Package domain holds the platform-neutral types shared by every layer.
package domain

import "strings"

type Sport string

type Platform string

const (
	SportNFL        Sport    = "nfl"
	PlatformESPN    Platform = "espn"
	PlatformSleeper Platform = "sleeper"
)

// SeasonState is the current NFL season and week according to Sleeper.
// Week is 0 in the offseason.
type SeasonState struct {
	Season int
	Week   int
}

type LeagueSummary struct {
	ID       string   `json:"id"`
	Platform Platform `json:"platform"`
	Season   int      `json:"season"`
	Name     string   `json:"name"`
}

type League struct {
	ID                string                  `json:"id"`
	Platform          Platform                `json:"platform"`
	Sport             Sport                   `json:"sport"`
	Season            int                     `json:"season"`
	Name              string                  `json:"name"`
	Teams             []Team                  `json:"teams"`
	Scoring           ScoringRules            `json:"scoring"`
	ScoringByPosition map[string]ScoringRules `json:"scoringByPosition"`
	DerivedStats      []DerivedStat           `json:"derivedStats"`
	UnsupportedRules  []string                `json:"unsupportedRules"`
	RosterSlots       []string                `json:"rosterSlots"`
}

type Team struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Owner string `json:"owner"`
}

// ScoringRules maps a Sleeper stat key to points per unit of that stat.
type ScoringRules map[string]float64

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
	// Step, when > 0, sets Key to the number of whole Steps in From (e.g. every
	// 25 passing yards) instead of an in-range indicator; Min and Max are ignored.
	Step float64 `json:"step,omitempty"`
}

// ScoringModel assembles the league's scoring for the engine.
func (l League) ScoringModel() Scoring {
	return Scoring{Rules: l.Scoring, ByPosition: l.ScoringByPosition, Derived: l.DerivedStats}
}

type Player struct {
	ID          *string           `json:"id"` // Sleeper ID; nil when an ESPN player could not be mapped
	Name        string            `json:"name"`
	Position    string            `json:"position"`
	NFLTeam     string            `json:"nflTeam"`
	PlatformIDs map[string]string `json:"platformIds"`
}

// RosterEntry is a rostered player. Stats, Points and PointsSource are unset unless stats were
// requested and available for that player.
type RosterEntry struct {
	Slot         string             `json:"slot"`
	Player       Player             `json:"player"`
	Stats        map[string]float64 `json:"stats"`
	Points       *float64           `json:"points"`
	PointsSource string             `json:"pointsSource,omitempty"` // "platform" or "computed" when Points is set
}

type Roster struct {
	TeamID   string        `json:"teamId"`
	Starters []RosterEntry `json:"starters"`
	Bench    []RosterEntry `json:"bench"`
	Reserve  []RosterEntry `json:"reserve"`
}

type MatchupSide struct {
	TeamID string  `json:"teamId"`
	Points float64 `json:"points"` // platform-reported; source of truth for team totals
	Roster Roster  `json:"roster"`
}

type Matchup struct {
	Week int         `json:"week"`
	Home MatchupSide `json:"home"`
	Away MatchupSide `json:"away"`
}

type StatLine struct {
	PlayerID string             `json:"playerId"`
	Season   int                `json:"season"`
	Week     int                `json:"week"`
	Opponent string             `json:"opponent"`
	Stats    map[string]float64 `json:"stats"`
}

type GamelogEntry struct {
	Season   int                `json:"season"`
	Week     int                `json:"week"`
	Opponent string             `json:"opponent"`
	Stats    map[string]float64 `json:"stats"`
	Points   *float64           `json:"points"`
}

type Meta struct {
	Season   int      `json:"season,omitempty"`
	Week     int      `json:"week,omitempty"`
	Warnings []string `json:"warnings"`
}

// PlayerRef is a provider's reference to a player, resolved to a Player by the service.
// Name, Position, and NFLTeam are fallbacks for players that cannot be mapped.
type PlayerRef struct {
	Platform Platform
	ID       string
	Name     string
	Position string
	NFLTeam  string
}

type RosterEntryRef struct {
	Slot string
	Ref  PlayerRef
	// PlatformPoints is the platform's own points for the requested week, when it reports them.
	PlatformPoints *float64
}

type RosterRef struct {
	TeamID   string
	Starters []RosterEntryRef
	Bench    []RosterEntryRef
	Reserve  []RosterEntryRef
}

type MatchupSideRef struct {
	TeamID string
	Points float64
	Roster RosterRef
}

type MatchupRef struct {
	Week int
	Home MatchupSideRef
	Away MatchupSideRef
}

func LeagueID(p Platform, nativeID string) string { return string(p) + ":" + nativeID }

// ParseLeagueID splits "espn:123" into its platform and native ID. Native IDs must be
// digits so they are safe to interpolate into upstream URLs.
func ParseLeagueID(id string) (Platform, string, error) {
	p, native, ok := strings.Cut(id, ":")
	platform := Platform(p)
	if !ok || (platform != PlatformESPN && platform != PlatformSleeper) || !isDigits(native) {
		return "", "", &InvalidParamError{Param: "leagueId", Reason: "must be espn:<digits> or sleeper:<digits>"}
	}
	return platform, native, nil
}

// ValidPlayerID reports whether id looks like a Sleeper player ID: digits, or a team
// abbreviation for defenses.
func ValidPlayerID(id string) bool {
	if len(id) == 0 || len(id) > 12 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z') {
			return false
		}
	}
	return true
}

func isDigits(s string) bool {
	if len(s) == 0 || len(s) > 20 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
