// Command gm is the @gm Krabber bot: a sunrise "good morning" molt for the
// reef. It greets the day, names the weekday, tries for the local first-light
// time (NOAA-adjacent sunrise-sunset.org, keyless; dropped if it fails), and
// adds a rotating reef musing. The molt is a couplet: the greeting, then the
// musing, rhymed with the reef. Scheduled once a day around NYC sunrise.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
	_ "time/tzdata" // embed tz database so America/New_York resolves on Lambda

	"github.com/t0ul/krabber-net/internal/botkit"
)

// The Battery / Lower Manhattan, good enough for a city-wide sunrise.
const (
	nycLat = "40.7829"
	nycLon = "-73.9654"
)

// form is the writing form every @gm molt takes.
const form = "couplet"

// musings rotate by day of year so every morning reads a little different.
// Each is the couplet's second line, so it rhymes with "reef".
var musings = []string{
	"Low tide, big plans, and the to-do list brief.",
	"Claws out, let's go: no time for grief.",
	"Scuttle with purpose; keep worry brief.",
	"The reef is yours, so act like the chief.",
	"Fresh current, fresh start, a new leaf.",
	"Molt something worth reading, leaf by leaf.",
	"Pinch the day gently; be a gentle thief.",
	"Sand in the legs, salt in the blood, in brief.",
	"Sideways is forward; that's my belief.",
	"Be the crab you want to sea, chief.",
	"Shell on, shields up, and shrug off the grief.",
	"Find your tidepool people; that's relief.",
	"Small crab, big wake, and that's the whole brief.",
	"Every tide turns, and so does grief.",
}

func main() { botkit.Run("gm", form, compose) }

func compose(ctx context.Context) (string, error) {
	loc := nyLoc()
	now := time.Now().In(loc)
	return greeting(now, sunriseLine(ctx, loc)), nil
}

// greeting is the morning's couplet: the day (with the sunrise, when the
// lookup gave one) on the reef, then the day's musing to rhyme with it.
// Widest first: with the sunrise, then without.
func greeting(now time.Time, sun string) string {
	base := fmt.Sprintf("gm crabs 🦀 %s on the reef.", now.Weekday())
	musing := musings[now.YearDay()%len(musings)]
	withSun := base
	if sun != "" {
		withSun = fmt.Sprintf("gm crabs 🦀 %s, %s on the reef.", sun, now.Weekday())
	}
	return botkit.Fit(withSun+"\n"+musing, base+"\n"+musing)
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
