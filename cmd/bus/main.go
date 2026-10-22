// Command bus is the @bus Krabber bot: a rush-hour heads-up on MTA bus service
// changes for a watched set of Manhattan routes. It reads the keyless MTA Bus
// GTFS-realtime alerts feed, keeps the alerts active right now on a watched
// route, and posts one molt. No watched route affected means no molt.
//
// FEED_URL and ROUTES (comma-separated) override the defaults below.
package main

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/t0ul/krabber-net/internal/botkit"
	"github.com/t0ul/krabber-net/internal/transit"
)

const (
	defaultFeed = "https://gtfsrt.prod.obanyc.com/alerts"
	// West-side + crosstown routes around the user's trips: up the west side
	// (M11/M12), down to Battery Park City (M20), and crosstown to Chelsea
	// Piers (M23), Bryant/Rockefeller (M42/M50), Columbus Circle (M57/M104),
	// and the UWS museums (M66/M72/M79). SBS "+" variants match too.
	defaultRoutes = "M11,M12,M20,M23,M42,M50,M57,M66,M72,M79,M104"
	maxRoutes     = 6
	tag           = "%bus"
)

func main() { botkit.Run("bus", compose) }

func compose(ctx context.Context) (string, error) {
	feed := botkit.Getenv("FEED_URL", defaultFeed)
	watch := transit.WatchSet(botkit.Getenv("ROUTES", defaultRoutes))
	ua := botkit.Getenv("USER_AGENT", "krabber-bus (+https://krabber.net)")

	alerts, err := transit.Fetch(ctx, feed, ua)
	if err != nil {
		return "", err
	}
	routes, headline := transit.Relevant(alerts, time.Now(), watch)
	if len(routes) == 0 {
		return "", nil // nothing on the watched routes right now
	}
	more := ""
	if len(routes) > maxRoutes {
		more = fmt.Sprintf(" +%d more", len(routes)-maxRoutes)
		routes = routes[:maxRoutes]
	}

	lead := fmt.Sprintf("🚌 MTA service changes on %s%s.", strings.Join(routes, ", "), more)
	if headline != "" {
		budget := botkit.MoltMax - utf8.RuneCountInString(lead) - utf8.RuneCountInString(tag) - 2
		if budget >= 24 {
			return lead + " " + botkit.Truncate(headline, budget) + " " + tag, nil
		}
	}
	return botkit.Fit(lead+" "+tag, lead), nil
}
