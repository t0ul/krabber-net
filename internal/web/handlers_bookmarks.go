package web

import (
	"errors"
	"net/http"

	"github.com/t0ul/krabber-net/internal/store"
)

// bookmarksPage lists the viewer's bookmarks, most recently bookmarked first.
func (app *App) bookmarksPage(w http.ResponseWriter, r *http.Request) {
	p, err := app.store.BookmarksPage(r.Context(), currentCrab(r).ID, afterParam(r), pageSize)
	data := app.newTemplateData(r)
	if !app.setPage(w, r, &data, p, err) {
		return
	}
	data.EmptyMessage = "You have no bookmarks."
	app.renderMolts(w, r, "bookmarks.html", data)
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
