// Command ferry is the @ferry Krabber bot: a heads-up on NYC Ferry service
// changes. It reads the keyless NYC Ferry GTFS-realtime alerts feed and posts
// one molt for the alerts in effect right now, as a haiku (every krab's
// molts start as one): the announcement, the routes, the headline. The ferry
// network is small, so by default it watches every route; ROUTES can narrow
// it. No active alert means no molt.
package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/t0ul/krabber-net/internal/botkit"
	"github.com/t0ul/krabber-net/internal/transit"
)

const (
	defaultFeed = "http://nycferry.connexionz.net/rtt/public/utility/gtfsrealtime.aspx/alert"
	maxRoutes   = 4
	form        = "haiku" // the writing form every @ferry molt takes, counted five-seven-five by the site
	lead        = "⛴️ %ferry service change"
)

// fillers pad a line that comes up short.
var fillers = []string{"this morning", "right now", "today", "now"}

// routeNames maps NYC Ferry route codes to readable names; unknown codes show
// as-is.
var routeNames = map[string]string{
	"ER": "East River",
	"SB": "South Brooklyn",
	"RW": "Rockaway",
	"AS": "Astoria",
	"SV": "Soundview",
	"SG": "St. George",
	"GI": "Governors Island",
}

func main() { botkit.Run("ferry", form, compose) }

func compose(ctx context.Context) (string, error) {
	feed := botkit.Getenv("FEED_URL", defaultFeed)
	// Empty ROUTES => watch every route (the small ferry network).
	watch := transit.WatchSet(botkit.Getenv("ROUTES", ""))
	ua := botkit.Getenv("USER_AGENT", "krabber-ferry (+https://krabber.net)")

	alerts, err := transit.Fetch(ctx, feed, ua)
	if err != nil {
		return "", err
	}
	routes, headline := transit.Relevant(alerts, time.Now(), watch)
	if len(routes) == 0 && headline == "" {
		return "", nil // all clear
	}
	return announce(routes, headline), nil
}

// announce lays the alert out as a haiku: the announcement (with the tag),
// the routes, the headline, each line fitted to its count. A long headline
// is cut; the point is to say something is up, and the app has the rest.
func announce(routes []string, headline string) string {
	where := "every route"
	if names := friendly(routes); names != "" {
		where = names
	}
	headline = strings.Join(strings.Fields(headline), " ")
	if headline == "" {
		headline = "check before you board"
	}
	return botkit.Haiku(fillers, []string{lead}, []string{where}, []string{headline})
}

// friendly turns route codes into a readable, capped list ("East River, Astoria").
func friendly(routes []string) string {
	if len(routes) == 0 {
		return ""
	}
	var names []string
	for _, r := range routes {
		if n, ok := routeNames[transit.Normalize(r)]; ok {
			names = append(names, n)
		} else {
			names = append(names, r)
		}
	}
	more := ""
	if len(names) > maxRoutes {
		more = fmt.Sprintf(" +%d more", len(names)-maxRoutes)
		names = names[:maxRoutes]
	}
	return strings.Join(names, ", ") + more
}
