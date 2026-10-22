package transit

import (
	"os"
	"testing"
	"time"
)

// Parses the real captured feeds (internal/transit/testdata/*.pb) — no mocks.

func load(t *testing.T, name string) []Alert {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	alerts, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return alerts
}

func TestParseBusFeed(t *testing.T) {
	alerts := load(t, "bus_alerts.pb")
	if len(alerts) == 0 {
		t.Fatal("no alerts parsed from bus feed")
	}
	// Every alert should carry at least one route and some HTML-free text.
	routed, texted := 0, 0
	for _, a := range alerts {
		if len(a.Routes) > 0 {
			routed++
		}
		if h := a.Headline(); h != "" {
			texted++
			if containsHTMLTag(h) {
				t.Errorf("headline still has HTML: %q", h)
			}
		}
	}
	if routed == 0 || texted == 0 {
		t.Fatalf("expected routed and texted alerts, got routed=%d texted=%d", routed, texted)
	}
}

func TestParseFerryFeed(t *testing.T) {
	alerts := load(t, "ferry_alerts.pb")
	if len(alerts) == 0 {
		t.Fatal("no alerts parsed from ferry feed")
	}
}

func TestRelevantFiltersByRoute(t *testing.T) {
	alerts := load(t, "bus_alerts.pb")
	// A known Manhattan route present in the feed strings.
	watch := WatchSet("M101")
	routes, _ := Relevant(alerts, time.Now(), watch)
	for _, r := range routes {
		if Normalize(r) != "M101" {
			t.Errorf("route %q leaked past the M101 watch", r)
		}
	}
	// Empty watch matches everything the feed had active now.
	all, _ := Relevant(alerts, time.Now(), nil)
	if len(all) == 0 {
		t.Skip("no bus alerts active at test time; nothing to assert on open watch")
	}
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"M79+":          "M79",
		"m11":           "M11",
		"MTA NYCT_M23+": "M23",
		" B61 ":         "B61",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestActiveAt(t *testing.T) {
	base := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	a := Alert{periods: [][2]time.Time{{base.Add(-time.Hour), base.Add(time.Hour)}}}
	if !a.ActiveAt(base) {
		t.Error("should be active inside its window")
	}
	if a.ActiveAt(base.Add(2 * time.Hour)) {
		t.Error("should be inactive after its window")
	}
	open := Alert{} // no periods = always active
	if !open.ActiveAt(base) {
		t.Error("no-period alert should always be active")
	}
}

func containsHTMLTag(s string) bool {
	return tagRE.MatchString(s)
}
