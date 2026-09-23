package web

import (
	"bytes"
	"errors"
	"net/http"
	"time"

	"github.com/go-playground/form/v4"
)

const maxFormBytes = 32 << 10

func (app *App) serverError(w http.ResponseWriter, r *http.Request, err error) {
	app.log.Error("server error", "err", err, "method", r.Method, "path", r.URL.Path)
	app.renderError(w, r, http.StatusInternalServerError)
}

func (app *App) clientError(w http.ResponseWriter, status int) {
	http.Error(w, http.StatusText(status), status)
}

func (app *App) notFound(w http.ResponseWriter, r *http.Request) {
	app.renderError(w, r, http.StatusNotFound)
}

// errorPage is what the error page says.
type errorPage struct {
	Status         int
	Title, Message string
}

var errorPages = map[int]errorPage{
	http.StatusNotFound: {http.StatusNotFound, "This page sank to the bottom of the sea",
		"The crab, molt or page you're looking for doesn't exist, was deleted, or is hidden from you."},
	http.StatusInternalServerError: {http.StatusInternalServerError, "Something went wrong on our side of the reef",
		"We've been told about it. Please try again in a moment."},
}

// renderError shows the standalone error page. It reads nothing from the
// table, so it stays cheap when scanners probe for URLs and still works when
// the table is what failed; if even the template fails, it falls back to
// plain text.
func (app *App) renderError(w http.ResponseWriter, r *http.Request, status int) {
	page, ok := errorPages[status]
	ts := app.templates["error.html"]
	var buf bytes.Buffer
	if !ok || ts == nil || ts.ExecuteTemplate(&buf, "base", templateData{
		CurrentYear:     time.Now().UTC().Year(),
		IsAuthenticated: currentCrab(r) != nil,
		Error:           page,
	}) != nil {
		http.Error(w, http.StatusText(status), status)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// decodePostForm parses a size-limited form body into dst.
func (app *App) decodePostForm(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		return err
	}
	if err := app.forms.Decode(dst, r.PostForm); err != nil {
		var invalid *form.InvalidDecoderError
		if errors.As(err, &invalid) {
			panic(err)
		}
		return err
	}
	return nil
}

func isHTMX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

// redirect sends a normal redirect, or tells htmx to navigate.
func redirect(w http.ResponseWriter, r *http.Request, url string) {
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", url)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}

// noContent answers an htmx action whose page needs no swap.
func noContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }
