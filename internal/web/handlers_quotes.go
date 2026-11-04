package web

import (
	"errors"
	"net/http"

	"github.com/t0ul/krabber-net/internal/store"
)

// quotePage shows the form for quoting the molt in the URL.
func (app *App) quotePage(w http.ResponseWriter, r *http.Request) {
	app.renderQuote(w, r, http.StatusOK, moltForm{})
}

func (app *App) renderQuote(w http.ResponseWriter, r *http.Request, status int, f moltForm) {
	m, ok := app.moltFromPath(w, r)
	if !ok {
		return
	}
	if status == http.StatusOK { // a fresh quote starts on the krab's default form
		f.Form = app.startForm(currentCrab(r))
	}
	f.Check = app.checkMolt(f)
	switch {
	case status == http.StatusOK: // a fresh form has nothing to complain about yet
		f.Check.Message = ""
	case f.Check.Blank:
		f.Check.Message = "Say something about it."
	}
	quoted := []store.Molt{*m}
	app.withDisplay(r, quoted)
	data := app.newTemplateData(r)
	data.Molt = quoted[0]
	data.Form = f
	app.renderCompose(w, r, status, "quote", data)
}

// renderCompose shows the quote or edit form (kind): as its page, or for
// htmx as the modal's content (opening it) or the re-filled form (after a
// rejected post).
func (app *App) renderCompose(w http.ResponseWriter, r *http.Request, status int, kind string, data templateData) {
	switch {
	case !isHTMX(r):
		app.render(w, r, status, kind+".html", data)
	case r.Method == http.MethodGet:
		app.renderTemplate(w, r, status, fragmentPage, kind+"-modal", data)
	default:
		app.renderTemplate(w, r, status, fragmentPage, kind+"-form", data)
	}
}

// quoteCreatePost stores a quote of the molt in the URL and opens its thread.
func (app *App) quoteCreatePost(w http.ResponseWriter, r *http.Request) {
	f, ok := app.decodeMoltForm(w, r)
	if !ok {
		return
	}
	// The checklist in the re-shown form says what's wrong, so there's no
	// field error to add.
	if app.checkMolt(f).Message != "" {
		app.renderQuote(w, r, http.StatusUnprocessableEntity, f)
		return
	}
	if !app.underWriteLimit(w, r, "molt") {
		return
	}
	quoted, ok := app.moltFromPath(w, r)
	if !ok {
		return
	}
	m, err := app.store.Quote(r.Context(), currentCrab(r), quoted, f.Content, store.WithNSFW(f.NSFW), store.WithForm(f.Form))
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrNotAllowed):
		app.notFound(w, r)
		return
	case err != nil:
		app.serverError(w, r, err)
		return
	}
	app.publish(r, m, quoted.AuthorID)
	app.notify(r, quoted.AuthorID, store.NotifyQuote, m.ID, f.Content)
	redirect(w, r, "/molt/view/"+m.ID)
}

// moltQuotesView lists the quotes of a molt, newest first.
func (app *App) moltQuotesView(w http.ResponseWriter, r *http.Request) {
	m, ok := app.moltFromPath(w, r)
	if !ok {
		return
	}
	p, err := app.store.QuotesPage(r.Context(), m.ID, afterParam(r), pageSize)
	data := app.newTemplateData(r)
	if !app.setPage(w, r, &data, p, err) {
		return
	}
	data.Molt = *m
	data.EmptyMessage = "No quotes yet."
	app.renderMolts(w, r, "quotes.html", data)
}
