package web

import (
	"errors"
	"net/http"

	"github.com/t0ul/krabber-net/internal/store"
)

// pinPost pins the viewer's own molt to the top of their profile. htmx
// reloads the profile so the pin moves to the top.
func (app *App) pinPost(w http.ResponseWriter, r *http.Request) {
	m, ok := app.moltFromPath(w, r)
	if !ok || !app.underWriteLimit(w, r, "edit") {
		return
	}
	c := currentCrab(r)
	switch err := app.store.Pin(r.Context(), c, m); {
	case errors.Is(err, store.ErrNotAllowed), errors.Is(err, store.ErrNotFound):
		app.notFound(w, r)
		return
	case err != nil:
		app.serverError(w, r, err)
		return
	}
	app.backToProfile(w, r, c)
}

// unpinPost clears the viewer's pin, if it's still the molt in the URL.
func (app *App) unpinPost(w http.ResponseWriter, r *http.Request) {
	if !app.underWriteLimit(w, r, "edit") {
		return
	}
	c := currentCrab(r)
	if err := app.store.Unpin(r.Context(), c, r.PathValue("id")); err != nil && !errors.Is(err, store.ErrNotFound) {
		app.serverError(w, r, err)
		return
	}
	app.backToProfile(w, r, c)
}

func (app *App) backToProfile(w http.ResponseWriter, r *http.Request, c *store.Crab) {
	if isHTMX(r) {
		w.Header().Set("HX-Refresh", "true")
		noContent(w)
		return
	}
	http.Redirect(w, r, "/krabs/"+c.UserName, http.StatusSeeOther)
}
