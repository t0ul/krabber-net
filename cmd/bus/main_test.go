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
	long := strings.Repeat("M11 buses are detoured around 34th St because of a water main break. ", 6)
	for _, tc := range []struct {
		name     string
		routes   []string
		headline string
		want     []string
	}{
		{"routes and headline", []string{"M11", "M23"}, "Buses rerouted.", []string{"\nM11, M23, ", "\nBuses rerouted"}},
		{"no headline", []string{"M20"}, "", []string{"\nM20, ", "check before you board"}},
		{"many routes", []string{"M11", "M12", "M20", "M23", "M42", "M50", "M57", "M66"}, "Delays.", []string{"M50 +2 more", "Delays"}},
		{"long headline", []string{"M11"}, long, []string{"\nM11 buses are, "}},
	} {
		m := announce(tc.routes, tc.headline)
		if err := botkit.Check(m, form); err != nil {
			t.Errorf("%s: %v\n%s", tc.name, err, m)
		}
		if n := utf8.RuneCountInString(m); n > botkit.MoltMax {
			t.Errorf("%s: %d characters", tc.name, n)
		}
		if !strings.HasPrefix(m, "🚌 MTA %bus service change\n") {
			t.Errorf("%s: no tag line: %q", tc.name, m)
		}
		for _, w := range tc.want {
			if !strings.Contains(m, w) {
				t.Errorf("%s: missing %q in\n%s", tc.name, w, m)
			}
		}
	}
}
