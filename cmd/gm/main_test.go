package main

import (
	"strings"
	"testing"
	"time"

	"github.com/t0ul/krabber-net/internal/botkit"
)

// Every morning's molt is a haiku the site counts as five-seven-five,
// whatever the weekday, with or without the sunrise.
func TestGreetingIsAHaiku(t *testing.T) {
	for i := range 2 * len(musings) { // every musing on every weekday
		day := time.Date(2026, time.January, 1+i, 7, 0, 0, 0, time.UTC)
		for _, sun := range []string{"first light 6:58am", ""} {
			m := greeting(day, sun)
			if err := botkit.Check(m, form); err != nil {
				t.Errorf("day %d, sun %q: %v\n%s", i, sun, err, m)
			}
			if !strings.HasPrefix(m, "gm crabs 🦀 ") || !strings.Contains(m, day.Weekday().String()+"\n") || !strings.Contains(m, "on the reef\n") || (sun != "" && !strings.Contains(m, "6:58am")) {
				t.Errorf("day %d: %q", i, m)
			}
		}
	}
}
