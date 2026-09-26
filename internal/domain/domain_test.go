package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestParseLeagueID(t *testing.T) {
	tests := []struct {
		in       string
		platform Platform
		native   string
		ok       bool
	}{
		{"espn:123456", PlatformESPN, "123456", true},
		{"sleeper:987654321012345678", PlatformSleeper, "987654321012345678", true},
		{"yahoo:1", "", "", false},
		{"espn:", "", "", false},
		{"espn", "", "", false},
		{"espn:12/../34", "", "", false},
		{"sleeper:12a", "", "", false},
		{"espn:1:2", "", "", false},
		{"espn:123456789012345678901", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			p, native, err := ParseLeagueID(tt.in)
			if !tt.ok {
				var ip *InvalidParamError
				if !errors.As(err, &ip) {
					t.Fatalf("ParseLeagueID(%q) err = %v, want *InvalidParamError", tt.in, err)
				}
				return
			}
			if err != nil || p != tt.platform || native != tt.native {
				t.Fatalf("ParseLeagueID(%q) = %q, %q, %v", tt.in, p, native, err)
			}
			if LeagueID(p, native) != tt.in {
				t.Fatalf("LeagueID round trip = %q, want %q", LeagueID(p, native), tt.in)
			}
		})
	}
}

func TestValidPlayerID(t *testing.T) {
	tests := map[string]bool{
		"4046":          true,
		"KC":            true,
		"":              false,
		"../x":          false,
		"4046?x=1":      false,
		"1234567890123": false,
	}
	for id, want := range tests {
		if got := ValidPlayerID(id); got != want {
			t.Errorf("ValidPlayerID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestUpstreamErrorMessage(t *testing.T) {
	msg := (&UpstreamError{Provider: "espn", Status: 500}).Error()
	if !strings.Contains(msg, "espn") || !strings.Contains(msg, "500") {
		t.Fatalf("message %q should name provider and status", msg)
	}
	wrapped := &UpstreamError{Provider: "sleeper", Err: ErrNotFound}
	if !errors.Is(wrapped, ErrNotFound) {
		t.Fatal("UpstreamError should unwrap to its Err")
	}
}
