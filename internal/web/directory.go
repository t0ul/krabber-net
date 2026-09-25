package web

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/t0ul/krabber-net/internal/platform"
	"github.com/t0ul/krabber-net/internal/store"
)

const (
	// directoryTTL is how often the snapshot is read again from the table.
	// Changes made through this server are applied to it as they happen;
	// the reload catches up counts (followers, molts) and anything done
	// elsewhere (crabctl, another instance during a deploy).
	directoryTTL = time.Hour
	// directoryRetry is how soon a failed reload is tried again.
	directoryRetry = time.Minute
	// directoryCrabs bounds the snapshot's memory (about 1 KB a krab). Past
	// it, names, search and suggestions need a different design; MAX_KRABS
	// keeps signups well below it.
	directoryCrabs = 50_000
	// directoryMolts is how many of the newest molts search, trending and
	// Stats look at.
	directoryMolts = 1_000
	// directoryReloadTimeout bounds a background reload.
	directoryReloadTimeout = 2 * time.Minute
)

// directory is an in-memory copy of every krab and the newest molts. It
// powers display names and avatars, mentions, the sidebar, the krab list,
// search and Stats without reading the table per page view. Reading it all
// costs about one read unit per 8 KB of krabs (a scan of the krab index),
// so it's reloaded hourly in the background, not per request.
type directory struct {
	mu      sync.Mutex
	data    dirData
	loaded  time.Time
	loading bool
	pending []func(dirData) dirData // changes made during a reload, replayed after it
	first   sync.Mutex              // held while the first load runs
}

// dirData is one version of the snapshot. It's never changed in place:
// changes build a new version, so readers can keep using the one they got.
type dirData struct {
	crabs  []store.Crab          // krabs who can sign in, most followed first
	byID   map[string]store.Crab //
	names  map[string]string     // lowercase username → username
	gone   map[string]bool       // banned and deleted krabs
	recent []store.Molt          // the newest molts, newest first
}

func newDirData(crabs []store.Crab, gone map[string]bool, recent []store.Molt) dirData {
	slices.SortStableFunc(crabs, func(a, b store.Crab) int {
		if c := cmp.Compare(b.FollowerCount, a.FollowerCount); c != 0 {
			return c
		}
		return a.CreatedAt.Compare(b.CreatedAt)
	})
	d := dirData{
		crabs:  crabs,
		byID:   make(map[string]store.Crab, len(crabs)),
		names:  make(map[string]string, len(crabs)),
		gone:   gone,
		recent: recent,
	}
	for _, c := range crabs {
		d.byID[c.ID] = c
		d.names[strings.ToLower(c.UserName)] = c.UserName
	}
	return d
}

// snapshot returns the current version, loading it first if there's none
// yet. A stale version is returned as is while a reload runs in the
// background.
func (d *directory) snapshot(ctx context.Context, s *store.Store, log func(error)) dirData {
	d.mu.Lock()
	if d.data.byID == nil {
		d.mu.Unlock()
		d.first.Lock()
		d.mu.Lock()
		if d.data.byID == nil {
			d.loading = true
			d.mu.Unlock()
			d.reload(ctx, s, log)
			d.mu.Lock()
		}
		d.first.Unlock()
	}
	if time.Since(d.loaded) >= directoryTTL && !d.loading {
		d.loading = true
		go func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), directoryReloadTimeout)
			defer cancel()
			d.reload(ctx, s, log)
		}()
	}
	data := d.data
	d.mu.Unlock()
	return data
}

// reload reads every krab and the newest molts, then swaps them in with
// the changes made meanwhile replayed on top. The caller sets loading.
func (d *directory) reload(ctx context.Context, s *store.Store, log func(error)) {
	crabs, gone, err := s.ListCrabs(ctx, directoryCrabs)
	if err == nil {
		for i := range crabs {
			if crabs[i].Avatar != "" {
				continue
			}
			if code, e := s.EnsureAvatar(ctx, &crabs[i]); e == nil {
				crabs[i].Avatar = code
			}
		}
	}
	var recent []store.Molt
	if err == nil {
		recent, err = s.LatestMolts(ctx, directoryMolts)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	d.loading = false
	if err != nil {
		log(err)
		d.loaded = time.Now().Add(directoryRetry - directoryTTL)
		d.pending = nil
		return
	}
	data := newDirData(crabs, gone, recent)
	for _, change := range d.pending {
		data = change(data)
	}
	d.data, d.loaded, d.pending = data, time.Now(), nil
}

// update applies a change this server just made. Changes are idempotent, so
// replaying one on a reload that already saw it does no harm.
func (d *directory) update(change func(dirData) dirData) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.data.byID == nil {
		return // nothing loaded yet; the first load will include it
	}
	d.data = change(d.data)
	if d.loading {
		d.pending = append(d.pending, change)
	}
}

// directoryEntry keeps what the directory's scan reads (ListCrabs), so a
// krab loaded whole for a request doesn't leave their email, password hash
// or settings in the snapshot.
func directoryEntry(c store.Crab) store.Crab {
	e := store.Crab{
		ID: c.ID, UserName: c.UserName, CreatedAt: c.CreatedAt,
		Activated: c.Activated, Banned: c.Banned, Deleted: c.Deleted, Verified: c.Verified,
		Avatar: c.Avatar, Invites: c.Invites, Trophies: c.Trophies,
		FollowerCount: c.FollowerCount, FollowingCount: c.FollowingCount, MoltCount: c.MoltCount,
	}
	e.DisplayName, e.Bio = c.DisplayName, c.Bio
	return e
}

// putCrab adds a krab who just activated, or replaces one whose profile,
// name or avatar just changed.
func (d *directory) putCrab(c store.Crab) {
	c = directoryEntry(c)
	d.update(func(old dirData) dirData {
		next := old
		next.crabs = slices.Clone(old.crabs)
		if i := slices.IndexFunc(next.crabs, func(o store.Crab) bool { return o.ID == c.ID }); i >= 0 {
			next.crabs[i] = c
		} else {
			next.crabs = append(next.crabs, c)
		}
		next.byID = cloneWith(old.byID, c.ID, c)
		next.names = old.names
		if prev, ok := old.byID[c.ID]; !ok || prev.UserName != c.UserName {
			next.names = make(map[string]string, len(old.names)+1)
			for k, v := range old.names {
				if !ok || k != strings.ToLower(prev.UserName) {
					next.names[k] = v
				}
			}
			next.names[strings.ToLower(c.UserName)] = c.UserName
		}
		return next
	})
}

// setGone hides (or brings back) a krab this server just banned, unbanned or
// deleted.
func (d *directory) setGone(id string, gone bool) {
	d.update(func(old dirData) dirData {
		next := old
		next.gone = make(map[string]bool, len(old.gone)+1)
		for k := range old.gone {
			next.gone[k] = true
		}
		if gone {
			next.gone[id] = true
			next.crabs = slices.DeleteFunc(slices.Clone(old.crabs), func(c store.Crab) bool { return c.ID == id })
			next.byID = make(map[string]store.Crab, len(old.byID))
			for k, v := range old.byID {
				if k != id {
					next.byID[k] = v
				}
			}
		} else {
			delete(next.gone, id)
		}
		return next
	})
}

// addMolt puts a molt this server just created (or restored) in the recent
// list at its place by ID, so search and trending see it without a reload.
func (d *directory) addMolt(m store.Molt) {
	d.update(func(old dirData) dirData {
		i, found := slices.BinarySearchFunc(old.recent, m.ID, func(o store.Molt, id string) int {
			return strings.Compare(id, o.ID) // newest (largest ID) first
		})
		if found {
			return old
		}
		next := old
		next.recent = slices.Insert(slices.Clone(old.recent), i, m)
		if len(next.recent) > directoryMolts {
			next.recent = next.recent[:directoryMolts]
		}
		return next
	})
}

// replaceMolt swaps in a molt this server just edited or relabeled, so
// search and trending see the new text and krabtags.
func (d *directory) replaceMolt(m store.Molt) {
	d.update(func(old dirData) dirData {
		i := slices.IndexFunc(old.recent, func(o store.Molt) bool { return o.ID == m.ID })
		if i < 0 {
			return old
		}
		next := old
		next.recent = slices.Clone(old.recent)
		next.recent[i] = m
		return next
	})
}

// removeMolt drops a molt this server just deleted or removed.
func (d *directory) removeMolt(id string) {
	d.update(func(old dirData) dirData {
		next := old
		next.recent = slices.DeleteFunc(slices.Clone(old.recent), func(o store.Molt) bool { return o.ID == id })
		return next
	})
}

func cloneWith[V any](m map[string]V, k string, v V) map[string]V {
	out := make(map[string]V, len(m)+1)
	for key, val := range m {
		out[key] = val
	}
	out[k] = v
	return out
}

func (app *App) dirData(r *http.Request) dirData {
	return app.loadDirectory(r.Context())
}

func (app *App) loadDirectory(ctx context.Context) dirData {
	return app.dir.snapshot(ctx, app.store, func(err error) {
		app.log.Warn("directory refresh failed", "err", err)
	})
}

// WarmUp loads the directory at startup, so the first visitor after a deploy
// doesn't wait for the scan.
func (app *App) WarmUp(ctx context.Context) {
	start := time.Now()
	ctx, meter := platform.WithMeter(ctx)
	d := app.loadDirectory(ctx)
	read, _, _ := meter.Units()
	app.log.Info("directory loaded", "krabs", len(d.crabs), "molts", len(d.recent), "ms", time.Since(start).Milliseconds(), "rru", read)
}

// snapshot returns the directory's krabs (most followed first), krabs by ID,
// and the newest molts. None of them may be changed.
func (app *App) snapshot(r *http.Request) ([]store.Crab, map[string]store.Crab, []store.Molt) {
	d := app.dirData(r)
	return d.crabs, d.byID, d.recent
}

// goneCrabs returns the IDs of banned and deleted krabs.
func (app *App) goneCrabs(r *http.Request) map[string]bool {
	return app.dirData(r).gone
}

// activeKrabs is how many krabs can sign in, for the MAX_KRABS signup cap.
func (app *App) activeKrabs(r *http.Request) int {
	return len(app.dirData(r).crabs)
}
