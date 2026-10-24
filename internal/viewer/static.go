package viewer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// RenderSite renders every page of the viewer to static HTML files under outDir, for
// publishing to GitHub Pages. basePath is passed to NewStaticHandler (see its doc).
// It fails fast on the first page that doesn't render with a 2xx status, or if the
// leagues list itself reports a provider unavailable (e.g. an expired ESPN cookie),
// rather than publishing a site that's silently missing that provider's leagues.
func RenderSite(ctx context.Context, c *Client, outDir, basePath string) error {
	var ls []struct {
		ID string `json:"id"`
	}
	meta, err := c.Get(ctx, "/v1/nfl/leagues", nil, &ls)
	if err != nil {
		return fmt.Errorf("listing leagues: %w", err)
	}
	for _, w := range meta.Warnings {
		if strings.Contains(w, "_leagues_unavailable") {
			return fmt.Errorf("listing leagues: %s", w)
		}
	}

	h := NewStaticHandler(c, basePath)
	// outPath is always relative to outDir (the site's root on disk); basePath only
	// controls the hrefs the handler embeds inside each rendered page (via h), not where
	// files land, so it plays no part in outPath.
	fileLinks := staticLinker{base: "/"}

	type page struct{ urlPath, outPath string }
	var pages []page
	pages = append(pages, page{"/", fileLinks.leagues()})
	// League pages before rooting pages: if a league's API call fails, that failure should
	// surface on its own page (whose URL names it), not on the rooting guide, which still
	// 502s whole when every league it aggregates has failed.
	for _, l := range ls {
		for w := 0; w <= 18; w++ {
			pages = append(pages, page{urlPath: leagueURLPath(l.ID, w), outPath: fileLinks.league(l.ID, w)})
		}
	}
	for w := 0; w <= 18; w++ {
		pages = append(pages, page{urlPath: rootingURLPath(w), outPath: fileLinks.rooting(w)})
	}

	for _, p := range pages {
		if err := renderPage(ctx, h, outDir, p.urlPath, p.outPath); err != nil {
			return err
		}
	}
	return nil
}

func leagueURLPath(id string, week int) string {
	p := "/league/" + id
	if week == 0 {
		return p
	}
	return p + "?week=" + strconv.Itoa(week)
}

func rootingURLPath(week int) string {
	if week == 0 {
		return "/rooting"
	}
	return "/rooting?week=" + strconv.Itoa(week)
}

// renderPage serves one page in-process and writes its body to outDir/outPath. It fails
// if the response isn't 2xx, naming the page's URL path so a build failure is traceable.
func renderPage(ctx context.Context, h http.Handler, outDir, urlPath, outPath string) error {
	req := httptest.NewRequest(http.MethodGet, urlPath, nil).WithContext(ctx)
	req.Host = "127.0.0.1"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code < 200 || rec.Code >= 300 {
		return fmt.Errorf("rendering %s: status %d: %s", urlPath, rec.Code, rec.Body.String())
	}
	full := filepath.Join(outDir, outPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("creating directory for %s: %w", outPath, err)
	}
	if err := os.WriteFile(full, rec.Body.Bytes(), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", outPath, err)
	}
	return nil
}
