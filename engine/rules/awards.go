package rules

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/t0ul/krabber-net/engine/store"
	"github.com/t0ul/krabber-net/engine/trophies"
)

// Awards gives trophies when their moment comes. Every front end awards
// them the same way, so a count crossed on one site is never missed; a site
// may choose never to show them.
type Awards struct {
	Store *store.Store
	Log   *slog.Logger
	held  heldTrophies
}

// heldTrophies remembers which krabs have which trophies. Some triggers
// repeat (a hidden krabtag used again, every profile save), and an award
// that turns out to be a repeat still bills its whole transaction.
type heldTrophies struct {
	mu   sync.Mutex
	held map[string]bool
}

const heldTrophiesMax = 200_000 // entries before the cache starts over

func (h *heldTrophies) has(crabID, trophyID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.held[crabID+"#"+trophyID]
}

func (h *heldTrophies) set(crabID, trophyID string, held bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.held == nil || len(h.held) >= heldTrophiesMax {
		h.held = map[string]bool{}
	}
	if held {
		h.held[crabID+"#"+trophyID] = true
	} else {
		delete(h.held, crabID+"#"+trophyID)
	}
}

// Forget drops what this process remembers about crabID holding trophyID,
// as when a moderator takes it back.
func (a *Awards) Forget(crabID, trophyID string) { a.held.set(crabID, trophyID, false) }

// Award gives c each trophy it doesn't have yet, tells them about it, and
// returns the ones it gave. Trophies are a bonus, so failures are logged,
// never shown.
func (a *Awards) Award(ctx context.Context, c *store.Crab, ids ...string) []string {
	var given []string
	for _, id := range ids {
		if a.held.has(c.ID, id) {
			continue
		}
		ok, err := a.Store.AwardTrophy(ctx, c, id)
		if err != nil {
			a.Log.Warn("award trophy", "crab", c.ID, "trophy", id, "err", err)
			continue
		}
		a.held.set(c.ID, id, true)
		if !ok {
			continue
		}
		given = append(given, id)
		c.Trophies++
		if err := a.Store.AddNotification(ctx, store.Notification{
			RecipientID: c.ID,
			Type:        store.NotifyTrophy,
			Actor:       "Krabber",
			Snippet:     id,
		}); err != nil {
			a.Log.Warn("trophy notification", "crab", c.ID, "trophy", id, "err", err)
		}
	}
	return given
}

// Follow runs after follower follows (or unfollows) followee. followee
// holds the counts from before the change.
func (a *Awards) Follow(ctx context.Context, follower, followee *store.Crab, follow bool) {
	if follow {
		ids := trophies.ForFollowers(followee.FollowerCount, followee.FollowerCount+1, followee.FollowingCount)
		if follower.Verified {
			ids = append(ids, "captivated")
		}
		a.Award(ctx, followee, ids...)
		return
	}
	// Back-Krabber: followee followed follower back, and follower just left.
	back, err := a.Store.IsFollowing(ctx, followee.ID, follower.ID)
	if err != nil {
		a.Log.Warn("back-krabber check", "err", err)
		return
	}
	if back {
		a.Award(ctx, followee, "back-krabber")
	}
}

// Molt runs after c publishes m. c's MoltCount is from before.
func (a *Awards) Molt(ctx context.Context, c *store.Crab, m *store.Molt) {
	a.Award(ctx, c, trophies.ForMolts(c.MoltCount, c.MoltCount+1, m.Tags)...)
}

// Like runs after liker likes m. m's LikeCount is from before.
func (a *Awards) Like(ctx context.Context, liker *store.Crab, m *store.Molt) {
	if trophies.Rogen(m.Content, m.Tags) {
		a.Award(ctx, liker, "rogen")
	}
	ids := trophies.ForLikes(m.LikeCount, m.LikeCount+1)
	switch {
	case len(ids) == 0:
		return
	case m.AuthorID == liker.ID:
		a.Award(ctx, liker, ids...)
		return
	}
	author, err := a.Store.CrabByID(ctx, m.AuthorID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			a.Log.Warn("like trophy author", "err", err)
		}
		return
	}
	a.Award(ctx, author, ids...)
}
