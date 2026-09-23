package web

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/t0ul/krabber-net/internal/store"
)

const (
	directoryTTL      = 2 * time.Minute
	directoryCrabs    = 500
	directoryMolts    = 200
	whoToFollowCount  = 3
	trendingCount     = 5
	searchResultLimit = 25
)

type sidebar struct {
	WhoToFollow []crabRow
	Trending    []store.Molt
}

// directory is a short-lived in-memory copy of all crabs and the last week's
// molts. It powers the sidebar, the crab list and search without a table scan
// per page view. At Krabber's size it holds everything; past a few hundred
// crabs, search should move to a real index.
type directory struct {
	mu      sync.Mutex
	loaded  time.Time
	dirty   bool
	crabs   []store.Crab
	byID    map[string]store.Crab
	gone    map[string]bool // banned and deleted crabs
	recent  []store.Molt
	loading bool
}

// directoryMinReload limits how often invalidate can force a reload.
const directoryMinReload = 10 * time.Second

// invalidate marks the snapshot stale after a write this instance made (a new
// molt, crab or follow), so it shows up without waiting for the TTL.
func (d *directory) invalidate() {
	d.mu.Lock()
	d.dirty = true
	d.mu.Unlock()
}

// addMolt puts a molt this instance just created at the front of the recent
// list, so search and trending see it immediately without a reload.
func (d *directory) addMolt(m store.Molt) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.byID == nil {
		return // nothing loaded yet; the first load will include it
	}
	recent := make([]store.Molt, 0, len(d.recent)+1)
	recent = append(recent, m)
	d.recent = append(recent, d.recent...)
}

// removeMolt drops a molt this instance just deleted from search and trending.
func (d *directory) removeMolt(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	recent := make([]store.Molt, 0, len(d.recent))
	for _, m := range d.recent {
		if m.ID != id {
			recent = append(recent, m)
		}
	}
	d.recent = recent
}

// setGone hides (or brings back) a crab this instance just banned, unbanned or
// deleted, without waiting for the next reload.
func (d *directory) setGone(id string, gone bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	next := make(map[string]bool, len(d.gone)+1)
	for k := range d.gone {
		next[k] = true
	}
	if gone && d.byID != nil {
		crabs := make([]store.Crab, 0, len(d.crabs))
		byID := make(map[string]store.Crab, len(d.byID))
		for _, c := range d.crabs {
			if c.ID != id {
				crabs = append(crabs, c)
				byID[c.ID] = c
			}
		}
		d.crabs, d.byID = crabs, byID
	}
	if gone {
		next[id] = true
	} else {
		delete(next, id)
	}
	d.gone = next
	d.dirty = true
}

// goneCrabs returns the IDs of banned and deleted crabs.
func (app *App) goneCrabs(r *http.Request) map[string]bool {
	app.snapshot(r)
	app.dir.mu.Lock()
	defer app.dir.mu.Unlock()
	return app.dir.gone
}

// addCrab adds a crab that just activated on this instance.
func (d *directory) addCrab(c store.Crab) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.byID == nil {
		return
	}
	if _, exists := d.byID[c.ID]; exists {
		return
	}
	crabs := make([]store.Crab, 0, len(d.crabs)+1)
	d.crabs = append(append(crabs, d.crabs...), c)
	byID := make(map[string]store.Crab, len(d.byID)+1)
	for k, v := range d.byID {
		byID[k] = v
	}
	byID[c.ID] = c
	d.byID = byID
}

func (d *directory) snapshot(ctx context.Context, s *store.Store, log func(error)) ([]store.Crab, map[string]store.Crab, []store.Molt) {
	d.mu.Lock()
	age := time.Since(d.loaded)
	fresh := age < directoryTTL && (!d.dirty || age < directoryMinReload)
	if fresh || (d.loading && d.byID != nil) {
		crabs, byID, recent := d.crabs, d.byID, d.recent
		d.mu.Unlock()
		return crabs, byID, recent
	}
	d.loading = true
	d.mu.Unlock()

	crabs, gone, err := s.ListCrabs(ctx, directoryCrabs)
	var recent []store.Molt
	if err == nil {
		recent, err = s.LatestMolts(ctx, directoryMolts)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	d.loading = false
	if err != nil {
		log(err)
		return d.crabs, d.byID, d.recent
	}
	d.crabs, d.gone, d.recent, d.loaded, d.dirty = crabs, gone, recent, time.Now(), false
	d.byID = make(map[string]store.Crab, len(crabs))
	for _, c := range crabs {
		d.byID[c.ID] = c
	}
	return d.crabs, d.byID, d.recent
}

func (app *App) snapshot(r *http.Request) ([]store.Crab, map[string]store.Crab, []store.Molt) {
	return app.dir.snapshot(r.Context(), app.store, func(err error) {
		app.log.Warn("directory refresh failed", "err", err)
	})
}

// following returns the IDs the viewer follows (empty when signed out).
func (app *App) following(r *http.Request) map[string]bool {
	c := currentCrab(r)
	if c == nil {
		return map[string]bool{}
	}
	ids, err := app.store.FollowingIDs(r.Context(), c.ID)
	if err != nil {
		app.log.Warn("following ids", "err", err)
		return map[string]bool{}
	}
	return ids
}

func (app *App) sidebarFor(r *http.Request) sidebar {
	crabs, _, recent := app.snapshot(r)
	var sb sidebar

	me := ""
	if c := currentCrab(r); c != nil {
		me = c.ID
	}
	followed := app.following(r)
	blocks := blocksOf(r)
	var fresh, rest []store.Crab
	for _, c := range crabs {
		if c.ID == me || blocks.Hides(c.ID) {
			continue
		}
		if followed[c.ID] {
			rest = append(rest, c)
		} else {
			fresh = append(fresh, c)
		}
	}
	byFollowers := func(a, b store.Crab) bool { return a.FollowerCount > b.FollowerCount }
	sort.SliceStable(fresh, func(i, j int) bool { return byFollowers(fresh[i], fresh[j]) })
	sort.SliceStable(rest, func(i, j int) bool { return byFollowers(rest[i], rest[j]) })
	candidates := append(fresh, rest...)
	for _, c := range candidates[:min(whoToFollowCount, len(candidates))] {
		sb.WhoToFollow = append(sb.WhoToFollow, crabRow{Crab: c, Following: followed[c.ID]})
	}

	var scored, recentOriginals []store.Molt
	for _, m := range app.visibleMolts(r, recent) {
		if m.Remolt {
			continue
		}
		recentOriginals = append(recentOriginals, m)
		if score(m) > 0 {
			scored = append(scored, m)
		}
	}
	sort.SliceStable(scored, func(i, j int) bool { return score(scored[i]) > score(scored[j]) })
	trending := scored
	if len(trending) == 0 {
		trending = recentOriginals
	}
	sb.Trending = trending[:min(trendingCount, len(trending))]
	return sb
}

func score(m store.Molt) int { return 2*m.LikeCount + 3*m.RemoltCount + m.ReplyCount }

// search matches crabs by name and the last week's molts by content.
func (app *App) search(r *http.Request, q string) ([]crabRow, []store.Molt) {
	crabs, _, recent := app.snapshot(r)
	needle := strings.ToLower(q)
	followed := app.following(r)

	var rows []crabRow
	for _, c := range crabs {
		if strings.Contains(strings.ToLower(c.UserName), needle) {
			rows = append(rows, crabRow{Crab: c, Following: followed[c.ID]})
			if len(rows) == searchResultLimit {
				break
			}
		}
	}
	var molts []store.Molt
	for _, m := range recent {
		if !m.Remolt && strings.Contains(strings.ToLower(m.Content), needle) {
			molts = append(molts, m)
			if len(molts) == searchResultLimit {
				break
			}
		}
	}
	return rows, molts
}
