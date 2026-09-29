package viewer

import (
	"cmp"
	"errors"
	"fmt"
	"net/url"
	"slices"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

// leagueWeek is one league's data for the rooting guide; Err is set when it failed to load.
type leagueWeek struct {
	ID, Name string
	Platform domain.Platform
	Week     int // meta.week of the matchups response
	League   domain.League
	Matchups []domain.Matchup
	Err      error
}

// appearance is one of my matchups a player starts in, for me or against me.
type appearance struct {
	League, Href string
	For          bool
	Points       *float64
}

type rootingPlayer struct {
	Name, NFLTeam, Image, Initials string
	IsDEF                          bool
	For, Against, Net              int
	Apps                           []appearance
	key                            string
}

type rootingView struct {
	Season, Week, Prev, Next int
	Weeks                    []int
	Warnings, NotPlaying     []string
	Counted                  int
	Players                  []rootingPlayer
}

// buildRooting counts every starter in my matchup of each league: my starters are "for",
// my opponent's "against". Players with 2+ appearances are listed, biggest |net| first.
// reqWeek 0 means the API's current week, taken from the first league that loaded.
func buildRooting(season, reqWeek int, lws []leagueWeek, warnings []string) rootingView {
	v := rootingView{Season: season, Week: reqWeek, Warnings: slices.Clone(warnings)}
	if v.Week == 0 {
		for _, lw := range lws {
			if lw.Err == nil {
				v.Week = lw.Week
				break
			}
		}
	}
	v.Prev, v.Next, v.Weeks = weekNav(v.Week)
	byKey := map[string]*rootingPlayer{}
	for _, lw := range lws {
		if lw.Err != nil {
			v.Warnings = append(v.Warnings, lw.Name+": "+errText(lw.Err))
			continue
		}
		if lw.Week != v.Week {
			v.Warnings = append(v.Warnings, fmt.Sprintf("%s: returned week %d", lw.Name, lw.Week))
			continue
		}
		mine := ""
		for _, t := range lw.League.Teams {
			if t.Mine {
				mine = t.ID
				break
			}
		}
		if mine == "" {
			v.Warnings = append(v.Warnings, lw.Name+": couldn't find your team")
			continue
		}
		var me, opp domain.MatchupSide
		found := false
		for _, m := range lw.Matchups {
			if m.Home.TeamID == mine {
				me, opp, found = m.Home, m.Away, true
				break
			}
			if m.Away.TeamID == mine {
				me, opp, found = m.Away, m.Home, true
				break
			}
		}
		if !found {
			v.NotPlaying = append(v.NotPlaying, lw.Name)
			continue
		}
		v.Counted++
		href := fmt.Sprintf("/league/%s?week=%d", url.PathEscape(lw.ID), v.Week)
		add := func(s domain.MatchupSide, isFor bool) {
			for _, e := range s.Roster.Starters {
				key := playerKey(e.Player, lw.Platform)
				if key == "" {
					continue
				}
				p := byKey[key]
				if p == nil {
					p = &rootingPlayer{Name: e.Player.Name, NFLTeam: e.Player.NFLTeam, Image: playerImage(e.Player),
						Initials: Initials(e.Player.Name), IsDEF: e.Player.Position == "DEF", key: key}
					byKey[key] = p
				}
				p.Apps = append(p.Apps, appearance{League: lw.Name, Href: href, For: isFor, Points: e.Points})
				if isFor {
					p.For++
				} else {
					p.Against++
				}
			}
		}
		add(me, true)
		add(opp, false)
	}
	for _, p := range byKey {
		if p.For+p.Against < 2 {
			continue
		}
		p.Net = p.For - p.Against
		v.Players = append(v.Players, *p)
	}
	slices.SortFunc(v.Players, func(a, b rootingPlayer) int {
		return cmp.Or(
			cmp.Compare(absInt(b.Net), absInt(a.Net)),
			cmp.Compare(b.For+b.Against, a.For+a.Against),
			cmp.Compare(a.Name, b.Name),
			cmp.Compare(a.key, b.key),
		)
	})
	return v
}

// playerKey merges a player across leagues by canonical ID, falling back to the
// league platform's own ID; "" when neither exists.
func playerKey(p domain.Player, platform domain.Platform) string {
	if p.ID != nil && *p.ID != "" {
		return *p.ID
	}
	if id := p.PlatformIDs[string(platform)]; id != "" {
		return string(platform) + ":" + id
	}
	return ""
}

func weekNav(week int) (prev, next int, weeks []int) {
	if week > 1 {
		prev = week - 1
	}
	if week < 18 {
		next = week + 1
	}
	for w := 1; w <= 18; w++ {
		weeks = append(weeks, w)
	}
	return prev, next, weeks
}

// errText is the human part of an API error, for one-line warnings.
func errText(err error) string {
	var ae *APIError
	if errors.As(err, &ae) && ae.Message != "" {
		return ae.Message
	}
	return err.Error()
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
