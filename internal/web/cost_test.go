package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/t0ul/krabber-net/internal/auth"
	"github.com/t0ul/krabber-net/internal/config"
	"github.com/t0ul/krabber-net/internal/store"
)

// requestCosts keeps the DynamoDB units of the last request the server logged.
type requestCosts struct {
	mu   sync.Mutex
	last requestCost
}

type requestCost struct {
	path        string
	read, write float64
	calls       int64
}

func (c *requestCosts) Enabled(context.Context, slog.Level) bool { return true }
func (c *requestCosts) WithAttrs([]slog.Attr) slog.Handler       { return c }
func (c *requestCosts) WithGroup(string) slog.Handler            { return c }

func (c *requestCosts) Handle(_ context.Context, r slog.Record) error {
	if r.Message != "request" {
		return nil
	}
	var rc requestCost
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "path":
			rc.path = a.Value.String()
		case "rru":
			rc.read = a.Value.Float64()
		case "wru":
			rc.write = a.Value.Float64()
		case "ddb_calls":
			rc.calls = a.Value.Int64()
		}
		return true
	})
	c.mu.Lock()
	c.last = rc
	c.mu.Unlock()
	return nil
}

func (c *requestCosts) take() requestCost {
	c.mu.Lock()
	defer c.mu.Unlock()
	rc := c.last
	c.last = requestCost{}
	return rc
}

// TestCostProfile measures what the main pages and actions cost in DynamoDB
// units on a small seeded community: 25 krabs following about ten others
// each, 100 molts fanned out to their followers, replies, likes, a remolt,
// bookmarks and a link card. Units follow AWS billing (reads of missing
// items count), except that DynamoDB Local leaves out index writes, which
// add about one unit per index entry written (a like's GSI7 entry, a molt's
// GSI3 and GSI5 entries). Notifications and fan-out run in the background
// and aren't in these numbers. It fails when a page goes over its budget, so
// a change that makes a page more expensive shows up here. Run with -v to
// see the table.
func TestCostProfile(t *testing.T) {
	costs := &requestCosts{}
	h := newHarnessWith(t, func(c *config.Config) { c.LogAllRequests = true }, costs)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	hash, err := auth.HashPassword("secret-viewer")
	must(err)
	newCrab := func(name string, hash []byte) *store.Crab {
		c, err := h.store.CreateCrab(ctx, name, name+"@krabber.test", hash)
		must(err)
		must(h.store.ActivateCrab(ctx, c.ID))
		c, err = h.store.CrabByKey(ctx, c.PK, c.SK)
		must(err)
		return c
	}
	viewer := newCrab("viewer", hash)
	var krabs []*store.Crab
	for i := range 24 {
		krabs = append(krabs, newCrab(fmt.Sprintf("krab%02d", i), []byte("x")))
	}
	for i := range 20 {
		must(h.store.Follow(ctx, viewer, krabs[i]))
	}
	for i, k := range krabs {
		for j := 1; j <= 10; j++ {
			must(h.store.Follow(ctx, k, krabs[(i+j)%len(krabs)]))
		}
		if i%3 == 0 {
			must(h.store.Follow(ctx, k, viewer))
		}
	}
	fanout := func(m *store.Molt) {
		must(h.store.AddToTrenches(ctx, m, []string{m.OwnerID}))
		must(h.store.EachFollowerPage(ctx, m.OwnerID, func(ids []string) error {
			return h.store.AddToTrenches(ctx, m, ids)
		}))
	}
	must(h.store.PutLinkCard(ctx, store.LinkCard{URL: "https://example.com/reef", Title: "The reef", Host: "example.com"}))
	var molts []*store.Molt
	for round := range 4 {
		for i, k := range krabs {
			text := fmt.Sprintf("molt %d from krab %d", round, i)
			switch (round + i) % 4 {
			case 0:
				text += " %krabs"
			case 1:
				text += " hi @viewer"
			case 2:
				text += " https://example.com/reef"
			}
			m, err := h.store.CreateMolt(ctx, k, text)
			must(err)
			fanout(m)
			molts = append(molts, m)
		}
	}
	mine, err := h.store.CreateMolt(ctx, viewer, "the viewer's own molt %krabs")
	must(err)
	fanout(mine)
	for i := range 8 {
		must(h.store.LikeMolt(ctx, viewer, molts[len(molts)-1-i*3]))
		_, err := h.store.Reply(ctx, krabs[i], mine, fmt.Sprintf("reply %d", i))
		must(err)
		must(h.store.LikeMolt(ctx, krabs[i], mine))
		must(h.store.AddNotification(ctx, store.Notification{RecipientID: viewer.ID, Type: store.NotifyLike, ActorID: krabs[i].ID, Actor: krabs[i].UserName, MoltID: mine.ID}))
	}
	must(h.store.Bookmark(ctx, viewer, molts[len(molts)-2]))
	re, err := h.store.Remolt(ctx, krabs[3], molts[10])
	must(err)
	fanout(re)

	// The viewer's newest trench entry, for the "new molts" polls.
	top, err := h.store.Trench(ctx, viewer.ID, 1)
	must(err)
	since := top[0].FeedID()
	next, err := h.store.TrenchPage(ctx, viewer.ID, "", pageSize)
	must(err)

	type row struct {
		name   string
		budget [2]float64 // read, write
		do     func()
	}
	var tok string
	signedIn := []row{
		{"GET /trench (page 1)", [2]float64{22, 0}, func() { h.get("/trench") }},
		{"GET /trench (load more)", [2]float64{20, 0}, func() { h.get("/trench?after="+url.QueryEscape(next.Next), "HX-Request", "true") }},
		{"GET /sea", [2]float64{23, 0}, func() { h.get("/sea") }},
		{"GET /krabs/{name}", [2]float64{13, 0}, func() { h.get("/krabs/krab05") }},
		{"GET /molt/view/{id} (8 replies)", [2]float64{17, 0}, func() { h.get("/molt/view/" + mine.ID) }},
		{"GET /notifications", [2]float64{6, 2}, func() { h.get("/notifications") }},
		{"GET /krabtag/{tag}", [2]float64{21, 0}, func() { h.get("/krabtag/krabs") }},
		{"GET /search?q=molt", [2]float64{10, 0}, func() { h.get("/search?q=molt") }},
		{"GET /stats", [2]float64{8, 0}, func() { h.get("/stats") }},
		{"poll /trench/new", [2]float64{4, 0}, func() { h.get("/trench/new?since="+since, "HX-Request", "true") }},
		{"poll /sea/new", [2]float64{4, 0}, func() { h.get("/sea/new?since="+since, "HX-Request", "true") }},
		{"poll /notifications/badge", [2]float64{3.5, 0}, func() { h.get("/notifications/badge", "HX-Request", "true") }},
		{"POST like", [2]float64{8, 6}, func() {
			h.post("/molt/like/"+molts[5].ID, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
		}},
		{"POST unlike", [2]float64{8, 6}, func() {
			h.post("/molt/like/"+molts[5].ID, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
		}},
		{"POST molt (+1 write per follower later)", [2]float64{4, 20}, func() {
			h.post("/molt/create", url.Values{"csrf_token": {tok}, "content": {"a new molt %krabs"}}, "HX-Request", "true")
		}},
		{"POST reply", [2]float64{8, 11}, func() {
			h.post("/molt/reply/"+molts[7].ID, url.Values{"csrf_token": {tok}, "content": {"nice"}}, "HX-Request", "true")
		}},
		{"POST follow", [2]float64{4, 13}, func() {
			h.post("/follow/"+krabs[22].ID, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
		}},
		{"POST unfollow", [2]float64{5, 8}, func() {
			h.post("/unfollow/"+krabs[22].ID, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
		}},
	}
	signedOut := []row{
		{"signed out GET /sea", [2]float64{16, 0}, func() { h.get("/sea") }},
		{"signed out GET /krabs/{name}", [2]float64{4, 0}, func() { h.get("/krabs/krab05") }},
		{"signed out poll /sea/new", [2]float64{1, 0}, func() { h.get("/sea/new?since="+since, "HX-Request", "true") }},
	}

	h.login("viewer@krabber.test", "secret-viewer")
	tok = h.csrf("/settings")
	h.get("/trench") // warm the directory snapshot, as a running server has
	var b strings.Builder
	fmt.Fprintf(&b, "\n%-42s %8s %8s %6s\n", "request", "read", "write", "calls")
	run := func(rows []row) {
		for _, r := range rows {
			costs.take()
			r.do()
			c := costs.take()
			fmt.Fprintf(&b, "%-42s %8.1f %8.1f %6d\n", r.name, c.read, c.write, c.calls)
			if c.read > r.budget[0] || c.write > r.budget[1] {
				t.Errorf("%s: %.1f read and %.1f write units, budget %.0f and %.0f", r.name, c.read, c.write, r.budget[0], r.budget[1])
			}
		}
	}
	run(signedIn)
	h.client = h.newClient()
	h.get("/sea")
	run(signedOut)
	t.Log(b.String())
	if os.Getenv("KRABBER_COST_PROFILE_OUT") != "" {
		must(os.WriteFile(os.Getenv("KRABBER_COST_PROFILE_OUT"), []byte(b.String()), 0o600))
	}
}
