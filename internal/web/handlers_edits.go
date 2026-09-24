package web

import (
	"errors"
	"net/http"
	"time"

	"github.com/t0ul/krabber-net/internal/store"
	"github.com/t0ul/krabber-net/internal/validator"
)

const notEditable = "Molt is no longer editable (must be less than 5 minutes old)"

// editPage shows the form for editing the viewer's own molt.
func (app *App) editPage(w http.ResponseWriter, r *http.Request) {
	m, ok := app.ownMolt(w, r)
	if !ok {
		return
	}
	app.renderEdit(w, r, http.StatusOK, m, moltForm{Content: m.Content})
}

func (app *App) renderEdit(w http.ResponseWriter, r *http.Request, status int, m *store.Molt, f moltForm) {
	data := app.newTemplateData(r)
	data.Molt = *m
	data.Form = f
	app.renderCompose(w, r, status, "edit", data)
}

// editPost saves the new text and opens the molt's thread.
func (app *App) editPost(w http.ResponseWriter, r *http.Request) {
	f, ok := app.decodeMoltForm(w, r)
	if !ok {
		return
	}
	m, ok := app.ownMolt(w, r)
	if !ok {
		return
	}
	f.CheckField(validator.NotBlank(f.Content), "content", "Molt text cannot be blank")
	f.CheckField(validator.MaxChars(f.Content, store.MaxMoltLength), "content", "Molts can be up to 280 characters.")
	f.CheckField(f.Content != m.Content, "content", "No changes were made")
	if !f.Valid() {
		app.renderEdit(w, r, http.StatusUnprocessableEntity, m, f)
		return
	}
	edited, err := app.store.EditMolt(r.Context(), currentCrab(r), m, f.Content)
	switch {
	case errors.Is(err, store.ErrNotAllowed):
		app.sessions.Put(r.Context(), sessionFlash, notEditable)
		redirect(w, r, "/molt/view/"+m.ID)
		return
	case errors.Is(err, store.ErrNotFound):
		app.notFound(w, r)
		return
	case err != nil:
		app.serverError(w, r, err)
		return
	}
	app.dir.replaceMolt(*edited)
	app.notifyMentions(r, edited, "", m.Mentions...)
	app.enqueueCard(cardURL(edited.Content))
	redirect(w, r, "/molt/view/"+m.ID)
}

// ownMolt loads the viewer's own molt named in the URL; anyone else's is a
// 404. A molt past the edit window sends the viewer back to it with a note.
func (app *App) ownMolt(w http.ResponseWriter, r *http.Request) (*store.Molt, bool) {
	m, err := app.store.MoltByID(r.Context(), r.PathValue("id"))
	if err == nil && (m.AuthorID != currentCrab(r).ID || m.Remolt || m.Deleted || m.Removed) {
		err = store.ErrNotFound
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		app.notFound(w, r)
		return nil, false
	case err != nil:
		app.serverError(w, r, err)
		return nil, false
	}
	if !m.Editable(time.Now()) {
		app.sessions.Put(r.Context(), sessionFlash, notEditable)
		redirect(w, r, "/molt/view/"+m.ID)
		return nil, false
	}
	return m, true
}
