package web

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/t0ul/krabber-net/internal/store"
	"github.com/t0ul/krabber-net/internal/trophies"
)

const tabTrophies = "trophies"

// trophyCase is one trophy on a profile's Trophies tab.
type trophyCase struct {
	trophies.Trophy
	AwardedAt time.Time
}

func trophyByID(id string) trophies.Trophy {
	if t, ok := trophies.Get(id); ok {
		return t
	}
	return trophies.Trophy{ID: id, Title: id}
}

// award gives c each trophy it doesn't have yet, tells them about it, and
// returns the ones it gave. Trophies are a bonus, so failures are logged,
// never shown.
func (app *App) award(ctx context.Context, c *store.Crab, ids ...string) []string {
	var given []string
	for _, id := range ids {
		ok, err := app.store.AwardTrophy(ctx, c, id)
		if err != nil {
			app.log.Warn("award trophy", "crab", c.ID, "trophy", id, "err", err)
			continue
		}
		if !ok {
			continue
		}
		given = append(given, id)
		c.Trophies++
		if err := app.store.AddNotification(ctx, store.Notification{
			RecipientID: c.ID,
			Type:        store.NotifyTrophy,
			Actor:       "Krabber",
			Snippet:     id,
		}); err != nil {
			app.log.Warn("trophy notification", "crab", c.ID, "trophy", id, "err", err)
		}
	}
	return given
}

// awardFollow runs after follower follows (or unfollows) followee.
// followee holds the counts from before the change.
func (app *App) awardFollow(ctx context.Context, follower, followee *store.Crab, follow bool) {
	if follow {
		ids := trophies.ForFollowers(followee.FollowerCount, followee.FollowerCount+1, followee.FollowingCount)
		if follower.Verified {
			ids = append(ids, "captivated")
		}
		app.award(ctx, followee, ids...)
		return
	}
	// Back-Krabber: followee followed follower back, and follower just left.
	back, err := app.store.IsFollowing(ctx, followee.ID, follower.ID)
	if err != nil {
		app.log.Warn("back-krabber check", "err", err)
		return
	}
	if back {
		app.award(ctx, followee, "back-krabber")
	}
}

// awardMolt runs after c publishes m. c's MoltCount is from before.
func (app *App) awardMolt(ctx context.Context, c *store.Crab, m *store.Molt) {
	app.award(ctx, c, trophies.ForMolts(c.MoltCount, c.MoltCount+1, m.Tags)...)
}

// awardLike runs after liker likes m. m's LikeCount is from before.
func (app *App) awardLike(ctx context.Context, liker *store.Crab, m *store.Molt) {
	if trophies.Rogen(m.Content, m.Tags) {
		app.award(ctx, liker, "rogen")
	}
	ids := trophies.ForLikes(m.LikeCount, m.LikeCount+1)
	switch {
	case len(ids) == 0:
		return
	case m.AuthorID == liker.ID:
		app.award(ctx, liker, ids...)
		return
	}
	author, err := app.store.CrabByID(ctx, m.AuthorID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			app.log.Warn("like trophy author", "err", err)
		}
		return
	}
	app.award(ctx, author, ids...)
}

// trophyCaseOf loads a crab's trophies with their catalog entries.
func (app *App) trophyCaseOf(ctx context.Context, crabID string) ([]trophyCase, error) {
	awards, err := app.store.Trophies(ctx, crabID)
	if err != nil {
		return nil, err
	}
	out := make([]trophyCase, 0, len(awards))
	for _, a := range awards {
		out = append(out, trophyCase{Trophy: trophyByID(a.TrophyID), AwardedAt: a.AwardedAt})
	}
	return out, nil
}

func (app *App) profileTrophies(w http.ResponseWriter, r *http.Request) {
	app.profileTab(w, r, tabTrophies)
}
