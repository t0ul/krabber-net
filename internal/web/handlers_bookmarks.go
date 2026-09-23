package web

import (
	"errors"
	"net/http"

	"github.com/t0ul/krabber-net/internal/store"
)

// bookmarksPage lists the viewer's bookmarks, most recently bookmarked first.
func (app *App) bookmarksPage(w http.ResponseWriter, r *http.Request) {
	molts, err := app.store.Bookmarks(r.Context(), currentCrab(r).ID, pageSize)
	if err == nil {
		molts, err = app.withLikes(r, molts)
	}
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	data := app.newTemplateData(r)
	data.Molts = molts
	data.EmptyMessage = "You have no bookmarks."
	app.render(w, r, http.StatusOK, "bookmarks.html", data)
}

// bookmarkPost adds the molt to the viewer's bookmarks, or removes it, and
// returns the toggled "…" menu item.
func (app *App) bookmarkPost(w http.ResponseWriter, r *http.Request) {
	m, ok := app.moltFromPath(w, r)
	if !ok {
		return
	}
	bookmarked, err := app.store.ToggleBookmark(r.Context(), currentCrab(r), m)
	switch {
	case errors.Is(err, store.ErrNotAllowed):
		app.notFound(w, r)
		return
	case err != nil:
		app.serverError(w, r, err)
		return
	}
	if !isHTMX(r) {
		redirect(w, r, "/molt/view/"+m.ID)
		return
	}
	m.Bookmarked = bookmarked
	data := templateData{IsAuthenticated: true, CSRFToken: csrfToken(r), CrabID: currentCrab(r).ID}
	app.renderTemplate(w, r, http.StatusOK, fragmentPage, "bookmark-button", map[string]any{"M": m, "D": data})
}
