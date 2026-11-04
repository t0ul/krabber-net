package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/t0ul/krabber-net/internal/botkit"
)

// Every alert molt is an envelope that fits a molt, whatever the feed says.
func TestAnnounceIsAnEnvelope(t *testing.T) {
	long := strings.Repeat("Service on the East River route is suspended between Pier 11 and Dumbo. ", 6)
	for _, tc := range []struct {
		name     string
		routes   []string
		headline string
		want     []string
	}{
		{"routes and headline", []string{"ER", "AS"}, "East River boats are running 20 minutes late.", []string{"East River, Astoria.", "20 minutes late. %ferry"}},
		{"headline only", nil, "All service suspended for weather.", []string{"Every route.", "weather. %ferry"}},
		{"routes only", []string{"SB"}, "", []string{"South Brooklyn.", "Check the app before you board. %ferry"}},
		{"many routes", []string{"ER", "AS", "SB", "RW", "SV", "GI"}, "Delays.", []string{"+2 more.", "Delays. %ferry"}},
		{"long headline", []string{"ER"}, long, []string{"… %ferry"}},
		{"unknown route", []string{"XX"}, "Delays.", []string{"XX."}},
	} {
		m := announce(tc.routes, tc.headline)
		if err := botkit.Check(m, form); err != nil {
			t.Errorf("%s: %v\n%s", tc.name, err, m)
		}
		if n := utf8.RuneCountInString(m); n > botkit.MoltMax {
			t.Errorf("%s: %d characters", tc.name, n)
		}
		for _, w := range tc.want {
			if !strings.Contains(m, w) {
				t.Errorf("%s: missing %q in\n%s", tc.name, w, m)
			}
		}
	}
}
