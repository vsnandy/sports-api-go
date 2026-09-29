package viewer

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strconv"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

//go:embed templates/*.html
var templateFS embed.FS

var funcs = template.FuncMap{
	"pts": func(v any) string {
		switch x := v.(type) {
		case float64:
			return fmt.Sprintf("%.2f", x)
		case *float64:
			if x == nil {
				return "—"
			}
			return fmt.Sprintf("%.2f", *x)
		}
		return ""
	},
	"num": func(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) },
	"signed": func(v float64) string {
		if v < 0 {
			return fmt.Sprintf("−%.2f", -v)
		}
		return fmt.Sprintf("+%.2f", v)
	},
}

type server struct {
	c    *Client
	tmpl *template.Template
}

// NewHandler serves the leagues list at / and one league at /league/{id}.
func NewHandler(c *Client) http.Handler {
	s := &server{c: c, tmpl: template.Must(template.New("").Funcs(funcs).ParseFS(templateFS, "templates/*.html"))}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.leagues)
	mux.HandleFunc("GET /league/{id}", s.league)
	return loopbackOnly(mux)
}

// loopbackOnly rejects requests whose Host isn't a loopback name, so a web page using
// DNS rebinding can't read league data through the running viewer.
func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		switch host {
		case "127.0.0.1", "localhost", "::1":
			next.ServeHTTP(w, r)
		default:
			http.Error(w, "forbidden host", http.StatusForbidden)
		}
	})
}

type leagueLink struct{ Name, Href, Platform string }

type leaguesView struct {
	Season   int
	Warnings []string
	Leagues  []leagueLink // ESPN first, then Sleeper
}

type errorView struct{ Title, Code, Message string }

func (s *server) leagues(w http.ResponseWriter, r *http.Request) {
	var ls []domain.LeagueSummary
	meta, err := s.c.Get(r.Context(), "/v1/nfl/leagues", nil, &ls)
	if err != nil {
		s.fail(w, err)
		return
	}
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
	s.render(w, http.StatusOK, "leagues.html", v)
}

func (s *server) league(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	q := url.Values{"include": {"stats"}}
	if wk := r.URL.Query().Get("week"); wk != "" {
		n, err := strconv.Atoi(wk)
		if err != nil || n < 1 || n > 18 {
			s.render(w, http.StatusBadRequest, "error.html", errorView{Title: "Invalid week", Message: "week must be a number from 1 to 18"})
			return
		}
		q.Set("week", wk)
	}
	base := "/v1/nfl/leagues/" + url.PathEscape(id)
	var lg domain.League
	if _, err := s.c.Get(r.Context(), base, nil, &lg); err != nil {
		s.fail(w, err)
		return
	}
	var ms []domain.Matchup
	meta, err := s.c.Get(r.Context(), base+"/matchups", q, &ms)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, http.StatusOK, "league.html", buildLeagueView(lg, ms, meta))
}

func (s *server) fail(w http.ResponseWriter, err error) {
	var ae *APIError
	if errors.As(err, &ae) {
		status := ae.Status
		if status < 400 {
			status = http.StatusBadGateway
		}
		s.render(w, status, "error.html", errorView{Title: fmt.Sprintf("API error %d", ae.Status), Code: ae.Code, Message: ae.Message})
		return
	}
	s.render(w, http.StatusBadGateway, "error.html", errorView{Title: "API unreachable", Message: err.Error()})
}

// render executes into a buffer first so a template error never sends a half page.
func (s *server) render(w http.ResponseWriter, status int, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}
