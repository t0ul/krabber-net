// Command ferry is the @ferry Krabber bot: a heads-up on NYC Ferry service
// changes. It reads the keyless NYC Ferry GTFS-realtime alerts feed and posts
// one molt for the alerts in effect right now. The ferry network is small, so
// by default it watches every route; ROUTES can narrow it. No active alert
// means no molt.
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
	defaultFeed = "http://nycferry.connexionz.net/rtt/public/utility/gtfsrealtime.aspx/alert"
	maxRoutes   = 4
	tag         = "%ferry"
)

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

func main() { botkit.Run("ferry", compose) }

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

	lead := "⛴️ NYC Ferry service change"
	if names := friendly(routes); names != "" {
		lead += " — " + names
	}
	lead += "."

	if headline != "" {
		budget := botkit.MoltMax - utf8.RuneCountInString(lead) - utf8.RuneCountInString(tag) - 2
		if budget >= 24 {
			return lead + " " + botkit.Truncate(headline, budget) + " " + tag, nil
		}
	}
	return botkit.Fit(lead+" "+tag, lead), nil
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
