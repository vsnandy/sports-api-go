package main

import (
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	c, err := loadConfig(env(map[string]string{
		"API_URL": " https://x.example ", "API_KEY": "k", "OUT_DIR": "dist/pages",
	}))
	if err != nil || c.apiURL != "https://x.example" || c.apiKey != "k" || c.outDir != "dist/pages" || c.basePath != "/" {
		t.Fatalf("config = %+v, %v, want basePath defaulted to /", c, err)
	}

	for name, m := range map[string]map[string]string{
		"API_URL": {"API_KEY": "k", "OUT_DIR": "d"},
		"API_KEY": {"API_URL": "https://x.example", "OUT_DIR": "d"},
		"OUT_DIR": {"API_URL": "https://x.example", "API_KEY": "k"},
	} {
		if _, err := loadConfig(env(m)); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("missing %s: err = %v", name, err)
		}
	}

	for in, want := range map[string]string{
		"/sports-api-go":  "/sports-api-go/",
		"/sports-api-go/": "/sports-api-go/",
		"sports-api-go/":  "/sports-api-go/",
		"":                "/",
		"/":               "/",
	} {
		c, err := loadConfig(env(map[string]string{
			"API_URL": "https://x.example", "API_KEY": "k", "OUT_DIR": "d", "SITE_BASE_PATH": in,
		}))
		if err != nil || c.basePath != want {
			t.Errorf("SITE_BASE_PATH=%q: basePath = %q, err = %v, want %q", in, c.basePath, err, want)
		}
	}
}
