package web

import (
	"errors"
	"net/http"

	"github.com/t0ul/krabber-net/internal/store"
	"github.com/t0ul/krabber-net/internal/validator"
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
	quoted := []store.Molt{*m}
	app.withDisplay(r, quoted)
	data := app.newTemplateData(r)
	data.Molt = quoted[0]
	data.Form = f
	app.render(w, r, status, "quote.html", data)
}

// quoteCreatePost stores a quote of the molt in the URL and opens its thread.
func (app *App) quoteCreatePost(w http.ResponseWriter, r *http.Request) {
	var f moltForm
	if err := app.decodePostForm(w, r, &f); err != nil {
		app.clientError(w, http.StatusBadRequest)
		return
	}
	f.CheckField(validator.NotBlank(f.Content), "content", "Say something about it.")
	f.CheckField(validator.MaxChars(f.Content, store.MaxMoltLength), "content", "Molts can be up to 280 characters.")
	if !f.Valid() {
		app.renderQuote(w, r, http.StatusUnprocessableEntity, f)
		return
	}
	quoted, ok := app.moltFromPath(w, r)
	if !ok {
		return
	}
	m, err := app.store.Quote(r.Context(), currentCrab(r), quoted, f.Content)
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
	quotes, err := app.store.Quotes(r.Context(), m.ID, 100)
	if err == nil {
		quotes, err = app.withLikes(r, quotes)
	}
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	data := app.newTemplateData(r)
	data.Molt = *m
	data.Molts = quotes
	data.EmptyMessage = "No quotes yet."
	app.render(w, r, http.StatusOK, "quotes.html", data)
}
