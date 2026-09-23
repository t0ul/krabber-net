package web

import (
	"errors"
	"net/http"

	"github.com/t0ul/krabber-net/internal/store"
)

// visibleMolts drops molts written or remolted by a crab on either side of a
// block with the viewer.
func visibleMolts(r *http.Request, molts []store.Molt) []store.Molt {
	b := blocksOf(r)
	if len(b.Blocking)+len(b.BlockedBy) == 0 {
		return molts
	}
	out := make([]store.Molt, 0, len(molts))
	for _, m := range molts {
		if !b.Hides(m.OwnerID) && !b.Hides(m.AuthorID) {
			out = append(out, m)
		}
	}
	return out
}

// visibleCrabs drops crab rows on either side of a block with the viewer.
func visibleCrabs(r *http.Request, rows []crabRow) []crabRow {
	b := blocksOf(r)
	if len(b.Blocking)+len(b.BlockedBy) == 0 {
		return rows
	}
	out := make([]crabRow, 0, len(rows))
	for _, row := range rows {
		if !b.Hides(row.Crab.ID) {
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
		app.notFound(w)
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
	http.Redirect(w, r, "/crabs/"+other.UserName, http.StatusSeeOther)
}
