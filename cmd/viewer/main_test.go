package main

import (
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	c, err := loadConfig(env(map[string]string{"API_URL": " https://x.example ", "API_KEY": "k"}))
	if err != nil || c.apiURL != "https://x.example" || c.apiKey != "k" {
		t.Fatalf("config = %+v, %v", c, err)
	}
	for name, m := range map[string]map[string]string{
		"API_URL": {"API_KEY": "k"},
		"API_KEY": {"API_URL": "https://x.example"},
	} {
		if _, err := loadConfig(env(m)); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("missing %s: err = %v", name, err)
		}
	}
}
