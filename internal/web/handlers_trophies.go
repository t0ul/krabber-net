package web

import (
	"context"
	"net/http"
	"time"

	"github.com/t0ul/krabber-net/engine/store"
	"github.com/t0ul/krabber-net/engine/trophies"
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

// award gives c each trophy it doesn't have yet and returns the ones it
// gave: the engine's awards, for the pages that give one directly.
func (app *App) award(ctx context.Context, c *store.Crab, ids ...string) []string {
	return app.rules.Awards.Award(ctx, c, ids...)
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
