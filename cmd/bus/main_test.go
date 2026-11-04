package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/t0ul/krabber-net/internal/botkit"
)

// Every alert molt is an envelope that fits a molt, whatever the feed says.
func TestAnnounceIsAnEnvelope(t *testing.T) {
	long := strings.Repeat("M11 buses are detoured around 34th St because of a water main break. ", 6)
	for _, tc := range []struct {
		name     string
		routes   []string
		headline string
		want     []string
	}{
		{"routes and headline", []string{"M11", "M23"}, "M11 buses are detoured.", []string{"M11, M23.", "detoured. %bus"}},
		{"no headline", []string{"M20"}, "", []string{"M20.", "Check before you board. %bus"}},
		{"many routes", []string{"M11", "M12", "M20", "M23", "M42", "M50", "M57", "M66"}, "Delays.", []string{"M50 +2 more.", "Delays. %bus"}},
		{"long headline", []string{"M11"}, long, []string{"… %bus"}},
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
