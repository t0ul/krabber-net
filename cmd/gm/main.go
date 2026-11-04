// Command gm is the @gm Krabber bot: a sunrise "good morning" molt for the
// reef. It greets the day, names the weekday, tries for the local first-light
// time (NOAA-adjacent sunrise-sunset.org, keyless; dropped if it fails), and
// adds a rotating reef musing. The molt is a haiku, as every krab's molts
// are by default: the day, the light on the reef, the musing. Scheduled once
// a day around NYC sunrise.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	_ "time/tzdata" // embed tz database so America/New_York resolves on Lambda

	"github.com/t0ul/krabber-net/internal/botkit"
)

// The Battery / Lower Manhattan, good enough for a city-wide sunrise.
const (
	nycLat = "40.7829"
	nycLon = "-73.9654"
)

// form is the writing form every @gm molt takes: the haiku every krab's
// molts start as, counted five-seven-five by the site's own counter.
const form = "haiku"

// musings rotate by day of year so every morning reads a little different.
// Each is the haiku's last line, five syllables as the site counts them.
var musings = []string{
	"low tide and big plans",
	"claws out, let us go",
	"scuttle with purpose",
	"the reef is yours, crabs",
	"fresh current, fresh start",
	"molt something that sings",
	"pinch the day, gently",
	"salt is in the blood",
	"sideways still counts",
	"be the krab you sea",
	"shell on, shields up, go",
	"find your tidepool crew",
	"small crab, a big wake",
	"every tide turns",
}

// fillers pad a line that comes up short.
var fillers = []string{"today", "crabs", "now"}

func main() { botkit.Run("gm", form, compose) }

func compose(ctx context.Context) (string, error) {
	loc := nyLoc()
	now := time.Now().In(loc)
	return greeting(now, sunriseLine(ctx, loc)), nil
}

// greeting is the morning's haiku: the day, the first light on the reef
// (or just the reef, when the lookup failed), and the day's musing.
func greeting(now time.Time, sun string) string {
	day := now.Weekday().String()
	reef := []string{"a new morning on the reef"}
	if sun != "" {
		reef = []string{sun + " on the reef", "first light at " + strings.TrimPrefix(sun, "first light ") + " on the reef"}
	}
	return botkit.Haiku(fillers,
		[]string{"gm crabs 🦀 " + day, "gm crabs 🦀 it's " + day},
		reef,
		[]string{musings[now.YearDay()%len(musings)]})
}

// sunriseLine returns something like "first light 6:58am" for NYC, or "" if
// the (keyless) lookup fails — the molt still posts without it.
func sunriseLine(ctx context.Context, loc *time.Location) string {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	url := fmt.Sprintf("https://api.sunrise-sunset.org/json?lat=%s&lng=%s&formatted=0", nycLat, nycLon)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", botkit.Getenv("USER_AGENT", "krabber-gm (+https://krabber.net)"))

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return ""
	}

	var body struct {
		Status  string `json:"status"`
		Results struct {
			Sunrise string `json:"sunrise"`
		} `json:"results"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil || body.Status != "OK" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, body.Results.Sunrise)
	if err != nil {
		return ""
	}
	// "3:04pm" lowercased, trimmed leading zero already absent with %-less format.
	return "first light " + t.In(loc).Format("3:04pm")
}

// nyLoc returns America/New_York, or UTC if the zone can't be loaded.
func nyLoc() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.UTC
	}
	return loc
}
