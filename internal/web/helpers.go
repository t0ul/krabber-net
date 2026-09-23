package web

import (
	"errors"
	"net/http"

	"github.com/go-playground/form/v4"
)

const maxFormBytes = 32 << 10

func (app *App) serverError(w http.ResponseWriter, r *http.Request, err error) {
	app.log.Error("server error", "err", err, "method", r.Method, "path", r.URL.Path)
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}

func (app *App) clientError(w http.ResponseWriter, status int) {
	http.Error(w, http.StatusText(status), status)
}

func (app *App) notFound(w http.ResponseWriter) {
	app.clientError(w, http.StatusNotFound)
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
