package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/t0ul/krabber-net/internal/botkit"
)

// Every alert molt is a haiku that fits a molt, whatever the feed says.
func TestAnnounceIsAHaiku(t *testing.T) {
	long := strings.Repeat("Service on the East River route is suspended between Pier 11 and Dumbo. ", 6)
	for _, tc := range []struct {
		name     string
		routes   []string
		headline string
		want     []string
	}{
		{"routes and headline", []string{"ER", "AS"}, "East River boats are running 20 minutes late.", []string{"\nEast River, Astoria\n", "20 minutes late. %ferry"}},
		{"headline only", nil, "All service suspended for weather.", []string{"\nevery route\n", "weather. %ferry"}},
		{"routes only", []string{"SB"}, "", []string{"\nSouth Brooklyn\n", "check the app before you board %ferry"}},
		{"many routes", []string{"ER", "AS", "SB", "RW", "SV", "GI"}, "Delays.", []string{"+2 more\n", "Delays. %ferry"}},
		{"long headline", []string{"ER"}, long, []string{"… %ferry"}},
		{"unknown route", []string{"XX"}, "Delays.", []string{"\nXX\n"}},
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
