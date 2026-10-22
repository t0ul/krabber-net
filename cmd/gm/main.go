// Command gm is the @gm Krabber bot: a sunrise "good morning" molt for the
// reef. It greets the day, names the weekday, tries for the local first-light
// time (NOAA-adjacent sunrise-sunset.org, keyless; dropped if it fails), and
// adds a rotating reef musing. Scheduled once a day around NYC sunrise.
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

// musings rotate by day of year so every morning reads a little different.
var musings = []string{
	"Low tide, big plans.",
	"Claws out, let's go.",
	"Scuttle with purpose today.",
	"The reef is yours.",
	"Fresh current, fresh start.",
	"Molt something worth reading.",
	"Pinch the day, gently.",
	"Sand between the legs, salt in the blood.",
	"Sideways is still forward.",
	"Be the crab you want to sea.",
	"Shell on, shields up.",
	"Find your tidepool people.",
	"Small crab, big wake.",
	"Every tide turns. So can your day.",
}

func main() { botkit.Run("gm", compose) }

func compose(ctx context.Context) (string, error) {
	loc := nyLoc()
	now := time.Now().In(loc)
	base := fmt.Sprintf("gm crabs 🦀 %s on the reef.", now.Weekday())
	musing := musings[now.YearDay()%len(musings)]

	sun := sunriseLine(ctx, loc) // "" if the lookup fails; we just drop it

	withSun := base
	if sun != "" {
		withSun = base + " " + sun
	}
	// Widest first: base + sunrise + musing, then base + sunrise, then base.
	return botkit.Fit(
		withSun+" "+musing,
		withSun,
		base,
	), nil
}

// sunriseLine returns something like "First light 6:58am." for NYC, or "" if
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
	return "First light " + t.In(loc).Format("3:04pm") + "."
}

// nyLoc returns America/New_York, or UTC if the zone can't be loaded.
func nyLoc() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.UTC
	}
	return loc
}
