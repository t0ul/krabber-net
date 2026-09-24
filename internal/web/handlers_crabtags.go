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
	p, err := app.store.MoltsWithTagPage(r.Context(), tag, afterParam(r), pageSize)
	data := app.newTemplateData(r)
	if !app.setPage(w, r, &data, p, err) {
		return
	}
	data.Query = tag
	data.EmptyMessage = "No molts use %" + tag + " yet."
	app.renderMolts(w, r, "crabtag.html", data)
}
