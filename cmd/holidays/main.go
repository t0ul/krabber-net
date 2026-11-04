// Command holidays is the @holidays Krabber bot: a day-before heads-up for days
// off. It posts when TOMORROW is a U.S. federal holiday (computed, tagged %fed)
// and/or a scheduled NYC public-school closure (from an embedded, hand-kept list,
// tagged %nyc). The heads-up is a three-panel joke: the day, what it means,
// and the punchline (sleep in). If tomorrow is an ordinary day it posts
// nothing. Scheduled once a morning.
package main

import (
	"bufio"
	"context"
	_ "embed"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/t0ul/krabber-net/internal/botkit"
)

//go:embed nyc_school_closed.txt
var nycClosuresRaw string

// form is the writing form every heads-up takes.
const form = "three-panel"

func main() { botkit.Run("holidays", form, compose) }

func compose(ctx context.Context) (string, error) {
	tomorrow := time.Now().In(nyLoc()).AddDate(0, 0, 1)
	return headsUp(tomorrow), nil
}

// headsUp builds the molt announcing the given day, or "" if it's ordinary.
func headsUp(day time.Time) string {
	key := day.Format("2006-01-02")
	pretty := day.Format("Mon Jan 2")

	fedName := federalHolidays(day.Year())[key]
	nycName := nycClosures()[key]

	switch {
	case fedName != "" && nycName != "":
		return panels(
			fmt.Sprintf("Heads up 🦀 tomorrow (%s) is %s.", pretty, fedName),
			"A federal holiday, and NYC public schools are closed.",
			"Sleep in. %fed %nyc")
	case fedName != "":
		return panels(
			fmt.Sprintf("Heads up 🦀 tomorrow (%s) is %s.", pretty, fedName),
			"A federal holiday: most crabs are off work.",
			"Sleep in. %fed")
	case nycName != "":
		return panels(
			fmt.Sprintf("Heads up 🦀 no NYC public school tomorrow (%s).", pretty),
			fmt.Sprintf("The schools are closed: %s.", nycName),
			"Small crabs, sleep in. %nyc")
	default:
		return "" // ordinary day, nothing to announce
	}
}

// panels lays out a three-panel joke: setup, build, punchline, a blank line
// between each.
func panels(setup, build, punchline string) string {
	return setup + "\n\n" + build + "\n\n" + punchline
}

// nycClosures parses the embedded closure list into date -> label.
func nycClosures() map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(nycClosuresRaw))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		date, label, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		out[strings.TrimSpace(date)] = strings.TrimSpace(label)
	}
	return out
}

// federalHolidays returns the observed U.S. federal holidays for a year, keyed by
// "2006-01-02". "Observed" means the day people actually get off: a holiday on a
// Saturday is observed the preceding Friday, on a Sunday the following Monday.
func federalHolidays(year int) map[string]string {
	fixed := func(month time.Month, day int) string {
		return observed(date(year, month, day)).Format("2006-01-02")
	}
	floating := func(t time.Time) string { return t.Format("2006-01-02") }

	return map[string]string{
		fixed(time.January, 1): "New Year's Day",
		floating(nthWeekday(year, time.January, time.Monday, 3)):  "Martin Luther King Jr. Day",
		floating(nthWeekday(year, time.February, time.Monday, 3)): "Presidents' Day",
		floating(lastWeekday(year, time.May, time.Monday)):        "Memorial Day",
		fixed(time.June, 19): "Juneteenth",
		fixed(time.July, 4):  "Independence Day",
		floating(nthWeekday(year, time.September, time.Monday, 1)):  "Labor Day",
		floating(nthWeekday(year, time.October, time.Monday, 2)):    "Columbus Day",
		fixed(time.November, 11):                                    "Veterans Day",
		floating(nthWeekday(year, time.November, time.Thursday, 4)): "Thanksgiving",
		fixed(time.December, 25):                                    "Christmas Day",
	}
}

// date builds a noon UTC date (noon avoids any DST/midnight edge in weekday math).
func date(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 12, 0, 0, 0, time.UTC)
}

// observed shifts a weekend holiday to the weekday it's observed on.
func observed(d time.Time) time.Time {
	switch d.Weekday() {
	case time.Saturday:
		return d.AddDate(0, 0, -1)
	case time.Sunday:
		return d.AddDate(0, 0, 1)
	default:
		return d
	}
}

// nthWeekday returns the nth given weekday of a month (n starts at 1).
func nthWeekday(year int, month time.Month, wd time.Weekday, n int) time.Time {
	d := date(year, month, 1)
	offset := (int(wd) - int(d.Weekday()) + 7) % 7
	return d.AddDate(0, 0, offset+(n-1)*7)
}

// lastWeekday returns the last given weekday of a month.
func lastWeekday(year int, month time.Month, wd time.Weekday) time.Time {
	d := date(year, month+1, 1).AddDate(0, 0, -1) // last day of month
	back := (int(d.Weekday()) - int(wd) + 7) % 7
	return d.AddDate(0, 0, -back)
}

func nyLoc() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.UTC
	}
	return loc
}
