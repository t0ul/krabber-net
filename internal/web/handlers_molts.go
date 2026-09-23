package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/t0ul/krabber-net/internal/store"
	"github.com/t0ul/krabber-net/internal/validator"
)

const pageSize = 25

type moltForm struct {
	Content             string `form:"content"`
	validator.Validator `form:"-"`
}

type commentForm struct {
	Comment             string `form:"comment"`
	validator.Validator `form:"-"`
}

// moltCreatePost stores a molt and queues its fan-out. htmx callers get the
// rendered molt to put at the top of the list; others are redirected.
func (app *App) moltCreatePost(w http.ResponseWriter, r *http.Request) {
	var f moltForm
	if err := app.decodePostForm(w, r, &f); err != nil {
		app.clientError(w, http.StatusBadRequest)
		return
	}
	if !validator.NotBlank(f.Content) || !validator.MaxChars(f.Content, store.MaxMoltLength) {
		http.Error(w, "Molts must be 1–280 characters.", http.StatusUnprocessableEntity)
		return
	}
	m, err := app.store.CreateMolt(r.Context(), currentCrab(r), f.Content)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	// Own trench is written here so a refresh shows the molt immediately;
	// follower fan-out stays in the background (and writes the owner again).
	if err := app.store.AddToTrenches(r.Context(), m, []string{m.OwnerID}); err != nil {
		app.log.Warn("write own trench", "err", err, "molt", m.ID)
	}
	app.fanout.Enqueue(m)
	app.dir.addMolt(*m)

	if !isHTMX(r) {
		http.Redirect(w, r, moltReturnPath(r), http.StatusSeeOther)
		return
	}
	app.renderTemplate(w, r, http.StatusOK, fragmentPage, "molt",
		map[string]any{"M": m, "D": app.newTemplateData(r)})
}

// moltReturnPath sends a non-htmx compose back to the feed it was posted from.
func moltReturnPath(r *http.Request) string {
	ref, err := url.Parse(r.Header.Get("Referer"))
	if err != nil {
		return "/trench"
	}
	switch ref.Path {
	case "/trench", "/sea", "/moltinTime":
		return ref.Path
	}
	if strings.HasPrefix(ref.Path, "/crabs/") {
		return ref.Path
	}
	return "/trench"
}

// moltLikePost toggles the viewer's like and returns the refreshed action
// bar, so the heart and count update in place.
func (app *App) moltLikePost(w http.ResponseWriter, r *http.Request) {
	m, ok := app.moltFromPath(w, r)
	if !ok {
		return
	}
	liked, err := app.store.ToggleLike(r.Context(), currentCrab(r), m)
	if errors.Is(err, store.ErrNotFound) {
		app.notFound(w)
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	app.renderActions(w, r, m, &liked)
}

// remoltPost shares a molt (remolting a remolt shares the original) and
// returns the original's refreshed action bar.
func (app *App) remoltPost(w http.ResponseWriter, r *http.Request) {
	m, ok := app.moltFromPath(w, r)
	if !ok {
		return
	}
	re, err := app.store.Remolt(r.Context(), currentCrab(r), m)
	switch {
	case errors.Is(err, store.ErrAlreadyExists), errors.Is(err, store.ErrNotAllowed):
	case err != nil:
		app.serverError(w, r, err)
		return
	default:
		app.fanout.Enqueue(re)
	}
	app.renderActions(w, r, m, nil)
}

// moltDeletePost deletes the viewer's own molt, or undoes their remolt when
// the ID is a remolt. htmx removes the molt from the list; the thread page
// moves on to the trench.
func (app *App) moltDeletePost(w http.ResponseWriter, r *http.Request) {
	m, err := app.store.MoltByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		app.notFound(w)
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	err = app.store.DeleteMolt(r.Context(), currentCrab(r), m)
	switch {
	case errors.Is(err, store.ErrNotAllowed), errors.Is(err, store.ErrNotFound):
		app.notFound(w)
		return
	case err != nil:
		app.serverError(w, r, err)
		return
	}
	app.dir.removeMolt(m.ID)
	if isHTMX(r) && r.URL.Query().Get("from") != "thread" {
		// app.js removes the entry (a remolt) or every entry of the molt.
		trigger, _ := json.Marshal(map[string]any{"moltDeleted": map[string]any{"id": m.ID, "remolt": m.Remolt}})
		w.Header().Set("HX-Trigger", string(trigger))
		w.WriteHeader(http.StatusOK)
		return
	}
	redirect(w, r, "/trench")
}

// moltFromPath loads the molt named in the URL, following a remolt to its
// original.
func (app *App) moltFromPath(w http.ResponseWriter, r *http.Request) (*store.Molt, bool) {
	m, err := app.store.MoltByID(r.Context(), r.PathValue("id"))
	if err == nil && m.Remolt {
		m, err = app.store.MoltByID(r.Context(), m.RemoltOf)
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		app.notFound(w)
		return nil, false
	case err != nil:
		app.serverError(w, r, err)
		return nil, false
	}
	return m, true
}

// renderActions re-reads the molt (consistently, so counts include the write
// that just happened) and renders its action bar.
func (app *App) renderActions(w http.ResponseWriter, r *http.Request, m *store.Molt, liked *bool) {
	if !isHTMX(r) {
		http.Redirect(w, r, "/molt/view/"+m.ID, http.StatusSeeOther)
		return
	}
	fresh, err := app.store.MoltByKey(r.Context(), m.PK, m.SK)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	if liked != nil {
		fresh.Liked = *liked
	} else if ids, err := app.store.LikedIDs(r.Context(), currentCrab(r).ID, []string{fresh.ID}); err == nil {
		fresh.Liked = ids[fresh.ID]
	}
	data := templateData{IsAuthenticated: true, CSRFToken: csrfToken(r), CrabID: currentCrab(r).ID}
	app.renderTemplate(w, r, http.StatusOK, fragmentPage, "molt-actions", map[string]any{"M": fresh, "D": data})
}

func (app *App) moltView(w http.ResponseWriter, r *http.Request) {
	m, err := app.store.MoltByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		app.notFound(w)
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	molts, err := app.withLikes(r, []store.Molt{*m})
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	if len(molts) == 0 {
		app.notFound(w)
		return
	}
	shown := molts[0]
	shown.Comments, err = app.store.CommentsOn(r.Context(), shown.ID, 100)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	data := app.newTemplateData(r)
	data.Molt = shown
	app.render(w, r, http.StatusOK, "view.html", data)
}

func (app *App) moltLikesView(w http.ResponseWriter, r *http.Request) {
	likes, err := app.store.LikesOn(r.Context(), r.PathValue("id"), 100)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	data := app.newTemplateData(r)
	data.Likes = likes
	app.render(w, r, http.StatusOK, "likes.html", data)
}

// commentCreatePost stores a comment, then asks htmx to reload the thread so
// the new comment shows.
func (app *App) commentCreatePost(w http.ResponseWriter, r *http.Request) {
	var f commentForm
	if err := app.decodePostForm(w, r, &f); err != nil {
		app.clientError(w, http.StatusBadRequest)
		return
	}
	if !validator.NotBlank(f.Comment) || !validator.MaxChars(f.Comment, store.MaxCommentLength) {
		http.Error(w, "Comments must be 1–280 characters.", http.StatusUnprocessableEntity)
		return
	}
	m, ok := app.moltFromPath(w, r)
	if !ok {
		return
	}
	if _, err := app.store.AddComment(r.Context(), currentCrab(r), m, f.Comment); err != nil && !errors.Is(err, store.ErrNotFound) {
		app.serverError(w, r, err)
		return
	}
	if isHTMX(r) {
		w.Header().Set("HX-Refresh", "true")
		noContent(w)
		return
	}
	http.Redirect(w, r, "/molt/view/"+m.ID, http.StatusSeeOther)
}
