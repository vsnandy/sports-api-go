// Command viewer-static renders the league viewer to static HTML for GitHub Pages,
// calling the deployed API with API_KEY server-side; the rendered output never
// contains the API key.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/vsnandy/sports-api-go/internal/viewer"
)

type config struct {
	apiURL, apiKey, outDir, basePath string
}

func loadConfig(getenv func(string) string) (config, error) {
	c := config{
		apiURL: strings.TrimSpace(getenv("API_URL")),
		apiKey: getenv("API_KEY"),
		outDir: strings.TrimSpace(getenv("OUT_DIR")),
	}
	if c.apiURL == "" {
		return config{}, errors.New("API_URL is required")
	}
	if c.apiKey == "" {
		return config{}, errors.New("API_KEY is required")
	}
	if c.outDir == "" {
		return config{}, errors.New("OUT_DIR is required")
	}
	c.basePath = normalizeBasePath(getenv("SITE_BASE_PATH"))
	return c, nil
}

// normalizeBasePath ensures a leading and trailing "/", so an operator typo (a missing
// slash either side) can't produce a malformed href like "/sports-api-goleague/...".
func normalizeBasePath(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

func main() {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "viewer-static:", err)
		os.Exit(2)
	}
	c := viewer.NewClient(cfg.apiURL, cfg.apiKey, 20*time.Second)
	if err := viewer.RenderSite(context.Background(), c, cfg.outDir, cfg.basePath); err != nil {
		fmt.Fprintln(os.Stderr, "viewer-static:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote static site to %s\n", cfg.outDir)
}
