package web

import (
	"net/http"
	"strings"

	"github.com/t0ul/krabber-net/internal/richtext"
)

// crabtagPage lists the molts using a crabtag, newest first.
func (app *App) crabtagPage(w http.ResponseWriter, r *http.Request) {
	tag := strings.ToLower(r.PathValue("tag"))
	if !richtext.ValidTag(tag) {
		app.notFound(w, r)
		return
	}
	molts, err := app.store.MoltsWithTag(r.Context(), tag, pageSize)
	if err == nil {
		molts, err = app.withLikes(r, molts)
	}
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	data := app.newTemplateData(r)
	data.Query = tag
	data.Molts = molts
	data.EmptyMessage = "No molts use %" + tag + " yet."
	app.render(w, r, http.StatusOK, "crabtag.html", data)
}
