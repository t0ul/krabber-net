package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/t0ul/krabber-net/internal/store"
	"github.com/t0ul/krabber-net/internal/validator"
)

// maxAPIBodyBytes caps a JSON request body. A molt is at most 280 characters;
// this leaves room for the JSON wrapper and multi-byte runes.
const maxAPIBodyBytes = 4 << 10

// apiError writes a JSON error with the given status.
func apiError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// apiJSON writes v as a JSON response with the given status.
func apiJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (app *App) apiServerError(w http.ResponseWriter, r *http.Request, err error) {
	app.log.Error("api server error", "err", err, "method", r.Method, "path", r.URL.Path)
	apiError(w, http.StatusInternalServerError, "internal error")
}

// apiAuthenticate authenticates a bearer API key, checks it carries scope, and
// puts the key's crab in the request context the same way authenticate does for
// a session. The wrapped handler then uses currentCrab and the normal publish
// path, so an API molt behaves exactly like one posted from the site.
func (app *App) apiAuthenticate(scope string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Cap requests per client network before any table read, so spraying
		// tokens at the endpoint can't run up DynamoDB reads.
		if !app.apiRate.allow(clientNet(r), apiRatePerMin) {
			w.Header().Set("Retry-After", "60")
			apiError(w, http.StatusTooManyRequests, "too many requests, slow down")
			return
		}

		bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		bearer = strings.TrimSpace(bearer)
		if !ok || bearer == "" {
			w.Header().Set("WWW-Authenticate", "Bearer")
			apiError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		key, err := app.store.APIKeyByPlaintext(r.Context(), bearer)
		switch {
		case errors.Is(err, store.ErrNotFound):
			apiError(w, http.StatusUnauthorized, "invalid API key")
			return
		case err != nil:
			app.apiServerError(w, r, err)
			return
		}
		if !key.HasScope(scope) {
			apiError(w, http.StatusForbidden, "key is missing the "+scope+" scope")
			return
		}
		c, err := app.store.CrabByKey(r.Context(), key.CrabPK, key.CrabSK)
		switch {
		// Reject when the account is gone or can't sign in; when the email's key
		// now belongs to a different account (the ID check, after a deletion
		// freed it); or when the key predates a password change, reset or ban
		// (SessionsValidAfter, the same stamp that ends sessions).
		case errors.Is(err, store.ErrNotFound) ||
			(err == nil && (c.ID != key.CrabID || !c.CanSignIn() || key.CreatedAt < c.SessionsValidAfter)):
			apiError(w, http.StatusUnauthorized, "the key's account is unavailable")
			return
		case err != nil:
			app.apiServerError(w, r, err)
			return
		}
		if err := app.store.TouchAPIKey(r.Context(), key); err != nil {
			app.log.Warn("touch api key", "err", err, "crab", c.ID)
		}

		// Load the crab's blocks so an API molt's mentions respect them, the
		// same as authenticate does for the website.
		ctx := context.WithValue(r.Context(), ctxCrab, c)
		if c.BlockLinks > 0 {
			b, err := app.store.BlocksOf(ctx, c.ID)
			if err != nil {
				app.apiServerError(w, r, err)
				return
			}
			ctx = context.WithValue(ctx, ctxBlocks, b)
		}
		next(w, r.WithContext(ctx))
	}
}

// apiCreateMolt posts a molt as the authenticated key's crab. Body:
// {"text": "...", "nsfw": false}. It validates and rate-limits exactly like the
// web compose box, then publishes through the same path.
func (app *App) apiCreateMolt(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAPIBodyBytes)
	var in struct {
		Text string `json:"text"`
		NSFW bool   `json:"nsfw"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		apiError(w, http.StatusBadRequest, `body must be JSON: {"text": "..."}`)
		return
	}
	in.Text = strings.ReplaceAll(in.Text, "\r\n", "\n")
	if !validator.NotBlank(in.Text) || !validator.MaxChars(in.Text, store.MaxMoltLength) {
		apiError(w, http.StatusUnprocessableEntity, "text must be 1-280 characters")
		return
	}
	c := currentCrab(r)
	if !app.writes.allow(c.ID, "molt") {
		w.Header().Set("Retry-After", "3600")
		apiError(w, http.StatusTooManyRequests, "rate limit reached, try again later")
		return
	}
	m, err := app.store.CreateMolt(r.Context(), c, in.Text, store.WithNSFW(in.NSFW))
	if err != nil {
		app.apiServerError(w, r, err)
		return
	}
	app.publish(r, m, "")
	apiJSON(w, http.StatusCreated, map[string]string{
		"id":  m.ID,
		"url": app.cfg.BaseURL.JoinPath("molt", "view", m.ID).String(),
	})
}
