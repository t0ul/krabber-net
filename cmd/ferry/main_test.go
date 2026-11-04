package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/t0ul/krabber-net/internal/botkit"
)

// Every alert molt is a haiku the site counts as five-seven-five, whatever
// the feed says, and keeps the tag.
func TestAnnounceIsAHaiku(t *testing.T) {
	long := strings.Repeat("Service on the East River route is suspended between Pier 11 and Dumbo. ", 6)
	for _, tc := range []struct {
		name     string
		routes   []string
		headline string
		want     []string
	}{
		{"routes and headline", []string{"ER", "AS"}, "Boats are running late.", []string{"\nEast River, Astoria\n", "Boats are running late"}},
		{"headline only", nil, "Service suspended.", []string{"\nevery route", "\nService suspended"}},
		{"routes only", []string{"SB"}, "", []string{"\nSouth Brooklyn, ", "check before you board"}},
		{"many routes", []string{"ER", "AS", "SB", "RW", "SV", "GI"}, "Delays.", []string{"\nEast River, Astoria", "Delays"}},
		{"long headline", []string{"ER"}, long, []string{"\nService on the East"}},
		{"unknown route", []string{"XX"}, "Delays.", []string{"\nXX, "}},
	} {
		m := announce(tc.routes, tc.headline)
		if err := botkit.Check(m, form); err != nil {
			t.Errorf("%s: %v\n%s", tc.name, err, m)
		}
		if n := utf8.RuneCountInString(m); n > botkit.MoltMax {
			t.Errorf("%s: %d characters", tc.name, n)
		}
		if !strings.HasPrefix(m, "⛴️ %ferry service change\n") {
			t.Errorf("%s: no tag line: %q", tc.name, m)
		}
		for _, w := range tc.want {
			if !strings.Contains(m, w) {
				t.Errorf("%s: missing %q in\n%s", tc.name, w, m)
			}
		}
	}
}
