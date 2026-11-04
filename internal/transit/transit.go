// Package transit reads GTFS-realtime service-alert feeds (NYC Ferry, MTA Bus),
// both keyless public feeds, and distills them to the handful of facts the
// @ferry and @bus Krabber bots post: which routes have an alert active right
// now, and a short human headline. Feed text is HTML, so it is stripped to
// plain text here.
package transit

import (
	"context"
	"fmt"
	"html"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/proto"
)

// Alert is one service alert reduced to what a molt needs.
type Alert struct {
	Routes      []string // route ids as the feed gives them, e.g. "M79+", "ER"
	Header      string   // short headline, HTML stripped
	Description string   // longer text, HTML stripped (fallback headline)
	periods     [][2]time.Time
}

// Headline returns the header, or the description if there's no header.
func (a Alert) Headline() string {
	if a.Header != "" {
		return a.Header
	}
	return a.Description
}

// ActiveAt reports whether the alert is in effect at t. An alert with no active
// period is treated as always in effect (that's how GTFS-rt defines it).
func (a Alert) ActiveAt(t time.Time) bool {
	if len(a.periods) == 0 {
		return true
	}
	for _, p := range a.periods {
		if (p[0].IsZero() || !t.Before(p[0])) && (p[1].IsZero() || !t.After(p[1])) {
			return true
		}
	}
	return false
}

// Fetch downloads and parses a GTFS-realtime alerts feed.
func Fetch(ctx context.Context, url, userAgent string) ([]Alert, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("feed %s returned %d", url, res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	return Parse(body)
}

// Parse decodes a raw GTFS-realtime FeedMessage into alerts.
func Parse(data []byte) ([]Alert, error) {
	var feed gtfs.FeedMessage
	if err := proto.Unmarshal(data, &feed); err != nil {
		return nil, fmt.Errorf("decode gtfs-rt: %w", err)
	}
	var out []Alert
	for _, e := range feed.GetEntity() {
		al := e.GetAlert()
		if al == nil {
			continue
		}
		var routes []string
		seen := map[string]bool{}
		for _, sel := range al.GetInformedEntity() {
			if r := sel.GetRouteId(); r != "" && !seen[r] {
				seen[r] = true
				routes = append(routes, r)
			}
		}
		var periods [][2]time.Time
		for _, p := range al.GetActivePeriod() {
			var start, end time.Time
			if p.GetStart() != 0 {
				start = unix(p.GetStart())
			}
			if p.GetEnd() != 0 {
				end = unix(p.GetEnd())
			}
			periods = append(periods, [2]time.Time{start, end})
		}
		out = append(out, Alert{
			Routes:      routes,
			Header:      clean(english(al.GetHeaderText())),
			Description: clean(english(al.GetDescriptionText())),
			periods:     periods,
		})
	}
	return out, nil
}

// Relevant returns the display route ids with an alert active at t that match
// watch (a nil/empty watch matches every route), plus one representative
// headline. Route ids are returned sorted and de-duplicated.
func Relevant(alerts []Alert, t time.Time, watch map[string]bool) (routes []string, headline string) {
	seen := map[string]bool{}
	for _, a := range alerts {
		if !a.ActiveAt(t) {
			continue
		}
		matched := false
		for _, r := range a.Routes {
			if len(watch) == 0 || watch[Normalize(r)] {
				if !seen[r] {
					seen[r] = true
					routes = append(routes, r)
				}
				matched = true
			}
		}
		if matched && headline == "" {
			headline = a.Headline()
		}
	}
	sort.Strings(routes)
	return routes, headline
}

// Normalize reduces a route id to a comparison key: drops any agency prefix
// ("MTA NYCT_M11" -> "M11") and the SBS "+" suffix ("M79+" -> "M79"), upper-cased.
func Normalize(routeID string) string {
	r := routeID
	if i := strings.LastIndex(r, "_"); i >= 0 {
		r = r[i+1:]
	}
	r = strings.TrimSpace(strings.ToUpper(r))
	r = strings.TrimSuffix(r, "+")
	return r
}

// WatchSet turns a comma-separated route list into a normalized lookup set.
func WatchSet(csv string) map[string]bool {
	set := map[string]bool{}
	for _, r := range strings.Split(csv, ",") {
		if n := Normalize(r); n != "" {
			set[n] = true
		}
	}
	return set
}

var tagRE = regexp.MustCompile(`<[^>]*>`)

// clean turns feed HTML into a single line of plain text.
func clean(s string) string {
	s = tagRE.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.Join(strings.Fields(s), " ")
}

// english picks the English translation, falling back to the first non-empty one.
func english(ts *gtfs.TranslatedString) string {
	if ts == nil {
		return ""
	}
	var first string
	for _, tr := range ts.GetTranslation() {
		txt := tr.GetText()
		if txt == "" {
			continue
		}
		if first == "" {
			first = txt
		}
		if strings.HasPrefix(strings.ToLower(tr.GetLanguage()), "en") {
			return txt
		}
	}
	return first
}

// unix is a feed's POSIX time in seconds. A value too large for an int64
// can only be the far future, so it's held to the largest one.
func unix(sec uint64) time.Time {
	if sec > math.MaxInt64 {
		sec = math.MaxInt64
	}
	return time.Unix(int64(sec), 0)
}
