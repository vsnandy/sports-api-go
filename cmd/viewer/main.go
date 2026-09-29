// Command viewer serves a local, read-only view of the owner's leagues at
// http://127.0.0.1:8081, calling the deployed API with API_KEY server-side.
package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/vsnandy/sports-api-go/internal/viewer"
)

const addr = "127.0.0.1:8081"

type config struct{ apiURL, apiKey string }

func loadConfig(getenv func(string) string) (config, error) {
	c := config{apiURL: strings.TrimSpace(getenv("API_URL")), apiKey: getenv("API_KEY")}
	if c.apiURL == "" {
		return config{}, errors.New("API_URL is required (see .env.example)")
	}
	if c.apiKey == "" {
		return config{}, errors.New("API_KEY is required (see .env.example)")
	}
	return c, nil
}

func main() {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "viewer:", err)
		os.Exit(2)
	}
	c := viewer.NewClient(cfg.apiURL, cfg.apiKey, 20*time.Second)
	fmt.Printf("league viewer on http://%s\n", addr)
	if err := http.ListenAndServe(addr, viewer.NewHandler(c)); err != nil {
		fmt.Fprintln(os.Stderr, "viewer:", err)
		os.Exit(1)
	}
}
