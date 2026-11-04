package main

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/t0ul/krabber-net/internal/botkit"
)

// Every Daily Scuttle is a haiku of three lines, whatever the feeds return.
func TestComposeIsAHaiku(t *testing.T) {
	day := time.Date(2026, time.November, 4, 0, 0, 0, 0, time.UTC)
	at := func(h, m int) time.Time { return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }
	tides := []tideEvent{{at(6, 42), false}, {at(12, 58), true}, {at(19, 3), false}, {at(1, 20), true}}
	sky := &weather{period: "Today", temp: 72, unit: "F", forecast: "Partly Sunny", windSpeed: "8 mph"}
	gale := &weather{period: "Today", temp: 41, unit: "F", forecast: strings.Repeat("Scattered showers and thunderstorms, then ", 8), windSpeed: "40 to 60 mph with gusts"}

	for _, tc := range []struct {
		name  string
		tides []tideEvent
		wx    *weather
		want  []string
	}{
		{"tides and sky", tides, sky, []string{"low 6:42a, high 12:58p, low 7:03p, high 1:20a", "Partly Sunny, 72F, wind 8 mph", "Daily Scuttle %nyc"}},
		{"no sky", tides, nil, []string{"the sky keeps its own counsel"}},
		{"one tide", tides[:1], sky, []string{"low 6:42a\n"}},
		{"long forecast", tides, gale, []string{"Daily Scuttle %nyc"}},
	} {
		m := compose(tc.tides, tc.wx)
		if err := botkit.Check(m, moltForm); err != nil {
			t.Errorf("%s: %v\n%s", tc.name, err, m)
		}
		if n := utf8.RuneCountInString(m); n > moltMax {
			t.Errorf("%s: %d characters", tc.name, n)
		}
		for _, w := range tc.want {
			if !strings.Contains(m, w) {
				t.Errorf("%s: missing %q in\n%s", tc.name, w, m)
			}
		}
	}
}
