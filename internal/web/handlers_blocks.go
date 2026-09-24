package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/t0ul/krabber-net/internal/store"
)

// hidden reports whether the viewer must not see a crab or anything by them:
// the crab is banned or deleted, or on either side of a block with the viewer.
func (app *App) hidden(r *http.Request) func(crabID string) bool {
	b := blocksOf(r)
	gone := app.goneCrabs(r)
	return func(id string) bool { return gone[id] || b.Hides(id) }
}

// visibleMolts drops molts written or remolted by a hidden crab.
func (app *App) visibleMolts(r *http.Request, molts []store.Molt) []store.Molt {
	hidden := app.hidden(r)
	out := make([]store.Molt, 0, len(molts))
	for _, m := range molts {
		if !hidden(m.OwnerID) && !hidden(m.AuthorID) {
			out = append(out, m)
		}
	}
	return out
}

// withoutMuted drops molts containing one of the viewer's muted words (not
// their own). Remolts are resolved first, so they match on the original's text.
func withoutMuted(r *http.Request, molts []store.Molt) []store.Molt {
	c := currentCrab(r)
	if c == nil || len(c.MutedWords) == 0 {
		return molts
	}
	out := make([]store.Molt, 0, len(molts))
	for _, m := range molts {
		if m.AuthorID == c.ID || !mutes(c.MutedWords, m.Content) {
			out = append(out, m)
		}
	}
	return out
}

func mutes(words []string, text string) bool {
	text = strings.ToLower(text)
	for _, w := range words {
		if strings.Contains(text, w) {
			return true
		}
	}
	return false
}

// visibleCrabs drops rows for hidden crabs.
func (app *App) visibleCrabs(r *http.Request, rows []crabRow) []crabRow {
	hidden := app.hidden(r)
	out := make([]crabRow, 0, len(rows))
	for _, row := range rows {
		if !hidden(row.Crab.ID) {
			out = append(out, row)
		}
	}
	return out
}

func (app *App) blockPost(w http.ResponseWriter, r *http.Request) {
	app.setBlock(w, r, true)
}

func (app *App) unblockPost(w http.ResponseWriter, r *http.Request) {
	app.setBlock(w, r, false)
}

// setBlock blocks or unblocks, then reloads the page so every list reflects it.
func (app *App) setBlock(w http.ResponseWriter, r *http.Request, block bool) {
	other, err := app.store.CrabByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		app.notFound(w, r)
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	if block {
		err = app.store.Block(r.Context(), currentCrab(r), other)
	} else {
		err = app.store.Unblock(r.Context(), currentCrab(r), other)
	}
	switch {
	case errors.Is(err, store.ErrNotAllowed):
		app.clientError(w, http.StatusBadRequest)
		return
	case err != nil && !errors.Is(err, store.ErrAlreadyExists) && !errors.Is(err, store.ErrNotFound):
		app.serverError(w, r, err)
		return
	}
	app.dir.invalidate()
	if block {
		app.sessions.Put(r.Context(), sessionFlash, "@"+other.UserName+" is blocked. You won't see each other's molts.")
	} else {
		app.sessions.Put(r.Context(), sessionFlash, "@"+other.UserName+" is unblocked.")
	}
	if isHTMX(r) {
		w.Header().Set("HX-Refresh", "true")
		noContent(w)
		return
	}
	http.Redirect(w, r, "/krabs/"+other.UserName, http.StatusSeeOther)
}
