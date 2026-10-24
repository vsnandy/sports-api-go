package viewer

import (
	"net/url"
	"strconv"
)

// linker builds the hrefs a page uses to link to itself (week nav) or to other pages
// (the leagues list, a league page). liveLinker is what cmd/viewer uses today;
// staticLinker is used only when exporting the site to static files for GitHub Pages.
type linker interface {
	leagues() string
	league(id string, week int) string // week 0 means the current/default week
	rooting(week int) string           // week 0 means the current/default week
}

// liveLinker reproduces the handler's existing live-server URLs unchanged.
type liveLinker struct{}

func (liveLinker) leagues() string { return "/" }

func (liveLinker) league(id string, week int) string {
	p := "/league/" + url.PathEscape(id)
	if week == 0 {
		return p
	}
	return p + "?week=" + strconv.Itoa(week)
}

func (liveLinker) rooting(week int) string {
	if week == 0 {
		return "/rooting"
	}
	return "/rooting?week=" + strconv.Itoa(week)
}

// staticLinker builds hrefs as real file paths under base, for a statically exported
// copy of the site (base must start and end with "/", e.g. "/sports-api-go/" or "/").
type staticLinker struct{ base string }

func (l staticLinker) leagues() string { return l.base + "index.html" }

func (l staticLinker) league(id string, week int) string {
	p := l.base + "league/" + url.PathEscape(id) + "/"
	if week == 0 {
		return p + "index.html"
	}
	return p + "week-" + strconv.Itoa(week) + ".html"
}

func (l staticLinker) rooting(week int) string {
	if week == 0 {
		return l.base + "rooting/index.html"
	}
	return l.base + "rooting/week-" + strconv.Itoa(week) + ".html"
}

// weekOption is one entry in a week <select>: its display number and the href that
// week's page lives at under the active linker.
type weekOption struct {
	Week int
	Href string
}

// weekLinks wraps weekNav with hrefs built by linkFor, so both leagueView and
// rootingView can share one implementation of the week-nav row.
func weekLinks(week int, linkFor func(int) string) (prev, next int, prevHref, nextHref, refreshHref string, weeks []weekOption) {
	prev, next, ws := weekNav(week)
	if prev > 0 {
		prevHref = linkFor(prev)
	}
	if next > 0 {
		nextHref = linkFor(next)
	}
	refreshHref = linkFor(week)
	weeks = make([]weekOption, len(ws))
	for i, w := range ws {
		weeks[i] = weekOption{Week: w, Href: linkFor(w)}
	}
	return
}
