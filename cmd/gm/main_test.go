package main

import (
	"strings"
	"testing"
	"time"

	"github.com/t0ul/krabber-net/internal/botkit"
)

// Every morning's molt is a couplet, with or without the sunrise. (The musing
// rhymes with the reef, but rhyme is a hint the API never enforces, and its
// spelling heuristic misses real rhymes, so the test holds to the exact rules.)
func TestGreetingIsACouplet(t *testing.T) {
	for i := range musings {
		day := time.Date(2026, time.January, 1+i, 7, 0, 0, 0, time.UTC)
		for _, sun := range []string{"first light 6:58am", ""} {
			m := greeting(day, sun)
			if err := botkit.Check(m, form); err != nil {
				t.Errorf("day %d, sun %q: %v\n%s", i, sun, err, m)
			}
			if !strings.HasPrefix(m, "gm crabs 🦀 ") || !strings.Contains(m, day.Weekday().String()) || (sun != "" && !strings.Contains(m, sun)) {
				t.Errorf("day %d: %q", i, m)
			}
		}
	}
}
