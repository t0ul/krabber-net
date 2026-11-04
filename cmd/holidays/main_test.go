package main

import (
	"strings"
	"testing"
	"time"

	"github.com/t0ul/krabber-net/internal/botkit"
)

// Observed federal-holiday dates for 2026, verified against the official OPM
// federal holiday schedule.
func TestFederalHolidays2026(t *testing.T) {
	want := map[string]string{
		"2026-01-01": "New Year's Day",
		"2026-01-19": "Martin Luther King Jr. Day", // 3rd Mon Jan
		"2026-02-16": "Presidents' Day",            // 3rd Mon Feb
		"2026-05-25": "Memorial Day",               // last Mon May
		"2026-06-19": "Juneteenth",                 // Fri
		"2026-07-03": "Independence Day",           // Jul 4 is Sat -> observed Fri
		"2026-09-07": "Labor Day",                  // 1st Mon Sep
		"2026-10-12": "Columbus Day",               // 2nd Mon Oct
		"2026-11-11": "Veterans Day",               // Wed
		"2026-11-26": "Thanksgiving",               // 4th Thu Nov
		"2026-12-25": "Christmas Day",              // Fri
	}
	got := federalHolidays(2026)
	if len(got) != len(want) {
		t.Fatalf("got %d holidays, want %d: %v", len(got), len(want), got)
	}
	for date, name := range want {
		if got[date] != name {
			t.Errorf("%s: got %q, want %q", date, got[date], name)
		}
	}
}

func TestNYCClosuresEmbedded(t *testing.T) {
	c := nycClosures()
	if c["2026-09-21"] != "Yom Kippur" {
		t.Errorf("2026-09-21: got %q, want Yom Kippur", c["2026-09-21"])
	}
	if _, ok := c["2026-12-28"]; !ok {
		t.Errorf("expected 2026-12-28 winter recess in embedded list")
	}
	// Comment/blank lines must not leak in as dates.
	if _, ok := c["#"]; ok {
		t.Errorf("comment line parsed as a closure")
	}
}

func TestHeadsUp(t *testing.T) {
	d := func(s string) time.Time {
		tt, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		return tt
	}

	// Federal only.
	if m := headsUp(d("2026-12-25")); !strings.Contains(m, "Christmas Day") || !strings.Contains(m, "%fed") || strings.Contains(m, "%nyc") {
		t.Errorf("christmas eve: %q", m)
	}
	// NYC school only.
	if m := headsUp(d("2026-09-21")); !strings.Contains(m, "Yom Kippur") || !strings.Contains(m, "%nyc") || strings.Contains(m, "%fed") {
		t.Errorf("yom kippur eve: %q", m)
	}
	// Thanksgiving is federal; the school list leaves federal days out.
	if m := headsUp(d("2026-11-26")); !strings.Contains(m, "Thanksgiving") || !strings.Contains(m, "%fed") || strings.Contains(m, "%nyc") {
		t.Errorf("thanksgiving eve: %q", m)
	}
	// Ordinary day -> nothing.
	if m := headsUp(d("2026-10-23")); m != "" {
		t.Errorf("ordinary day should be empty, got %q", m)
	}
	// Every heads-up is a three-panel joke.
	for _, day := range []string{"2026-12-25", "2026-09-21", "2026-11-26", "2026-01-19", "2026-12-28"} {
		if m := headsUp(d(day)); m == "" {
			t.Errorf("%s should get a heads-up", day)
		} else if err := botkit.Check(m, form); err != nil {
			t.Errorf("%s: %v\n%s", day, err, m)
		}
	}
}
