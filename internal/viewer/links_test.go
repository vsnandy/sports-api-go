package viewer

import (
	"testing"
)

func TestLiveLinker(t *testing.T) {
	var l liveLinker
	if got := l.leagues(); got != "/" {
		t.Errorf("leagues() = %q, want /", got)
	}
	cases := []struct {
		id   string
		week int
		want string
	}{
		{"espn:1", 0, "/league/espn:1"},
		{"espn:1", 5, "/league/espn:1?week=5"},
		{"sleeper:2", 1, "/league/sleeper:2?week=1"},
	}
	for _, c := range cases {
		if got := l.league(c.id, c.week); got != c.want {
			t.Errorf("league(%q, %d) = %q, want %q", c.id, c.week, got, c.want)
		}
	}
	if got := l.rooting(0); got != "/rooting" {
		t.Errorf("rooting(0) = %q, want /rooting", got)
	}
	if got := l.rooting(7); got != "/rooting?week=7" {
		t.Errorf("rooting(7) = %q, want /rooting?week=7", got)
	}
}

func TestStaticLinker(t *testing.T) {
	l := staticLinker{base: "/sports-api-go/"}
	if got := l.leagues(); got != "/sports-api-go/index.html" {
		t.Errorf("leagues() = %q", got)
	}
	cases := []struct {
		id   string
		week int
		want string
	}{
		{"espn:1", 0, "/sports-api-go/league/espn:1/index.html"},
		{"espn:1", 5, "/sports-api-go/league/espn:1/week-5.html"},
		{"sleeper:2", 18, "/sports-api-go/league/sleeper:2/week-18.html"},
	}
	for _, c := range cases {
		if got := l.league(c.id, c.week); got != c.want {
			t.Errorf("league(%q, %d) = %q, want %q", c.id, c.week, got, c.want)
		}
	}
	if got := l.rooting(0); got != "/sports-api-go/rooting/index.html" {
		t.Errorf("rooting(0) = %q", got)
	}
	if got := l.rooting(3); got != "/sports-api-go/rooting/week-3.html" {
		t.Errorf("rooting(3) = %q", got)
	}
}

func TestStaticLinkerRootBase(t *testing.T) {
	l := staticLinker{base: "/"}
	if got := l.league("espn:1", 2); got != "/league/espn:1/week-2.html" {
		t.Errorf("league() = %q", got)
	}
}

func TestWeekLinks(t *testing.T) {
	linkFor := func(w int) string { return "W" + itoaForTest(w) }
	prev, next, prevHref, nextHref, refreshHref, weeks := weekLinks(1, linkFor)
	if prev != 0 || prevHref != "" {
		t.Errorf("week 1: prev=%d prevHref=%q, want 0 and empty", prev, prevHref)
	}
	if next != 2 || nextHref != "W2" {
		t.Errorf("week 1: next=%d nextHref=%q, want 2 and W2", next, nextHref)
	}
	if refreshHref != "W1" {
		t.Errorf("refreshHref = %q, want W1", refreshHref)
	}
	if len(weeks) != 18 || weeks[0] != (weekOption{1, "W1"}) || weeks[17] != (weekOption{18, "W18"}) {
		t.Errorf("weeks = %+v", weeks)
	}

	prev, next, _, nextHref, _, _ = weekLinks(18, linkFor)
	if prev != 17 || next != 0 || nextHref != "" {
		t.Errorf("week 18: prev=%d next=%d nextHref=%q, want 17, 0, empty", prev, next, nextHref)
	}
}

func itoaForTest(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}
