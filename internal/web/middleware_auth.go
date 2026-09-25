package web

import (
	"context"
	"errors"
	"net/http"

	"github.com/t0ul/krabber-net/internal/avatar"
	"github.com/t0ul/krabber-net/internal/store"
)

// Session keys.
const (
	sessionFlash  = "flash"
	sessionCrabID = "crabID"
	sessionCrabPK = "crabPK"
	sessionCrabSK = "crabSK"
	sessionAuthAt = "authAt" // Unix seconds of sign-in
)

type ctxKey int

const (
	ctxCrab ctxKey = iota
	ctxClientIP
	ctxBlocks
)

// blocksOf returns the signed-in crab's blocks (empty when there are none).
func blocksOf(r *http.Request) store.Blocks {
	b, _ := r.Context().Value(ctxBlocks).(store.Blocks)
	return b
}

// currentCrab returns the signed-in crab, or nil.
func currentCrab(r *http.Request) *store.Crab {
	c, _ := r.Context().Value(ctxCrab).(*store.Crab)
	return c
}

// authenticate loads the signed-in crab on every request (one consistent
// GetItem) and ends the session if the account was banned, deleted, or had
// its sessions revoked after this one began.
func (app *App) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		pk, sk := app.sessions.GetString(ctx, sessionCrabPK), app.sessions.GetString(ctx, sessionCrabSK)
		if pk == "" || sk == "" {
			next.ServeHTTP(w, r)
			return
		}
		c, err := app.store.CrabByKey(ctx, pk, sk)
		switch {
		case errors.Is(err, store.ErrNotFound):
			app.endSession(r)
		case err != nil:
			app.serverError(w, r, err)
			return
		case !c.CanSignIn() || app.sessions.GetInt64(ctx, sessionAuthAt) < c.SessionsValidAfter:
			app.endSession(r)
		default:
			if !avatar.Valid(c.Avatar) {
				if code, err := app.store.EnsureAvatar(ctx, c); err != nil {
					app.log.Warn("ensure avatar", "err", err, "crab", c.ID)
				} else {
					c.Avatar = code
					app.dir.putCrab(*c)
				}
			}
			ctx = context.WithValue(ctx, ctxCrab, c)
			if c.BlockLinks > 0 {
				b, err := app.store.BlocksOf(ctx, c.ID)
				if err != nil {
					app.serverError(w, r, err)
					return
				}
				ctx = context.WithValue(ctx, ctxBlocks, b)
			}
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

func (app *App) endSession(r *http.Request) {
	ctx := r.Context()
	_ = app.sessions.RenewToken(ctx)
	for _, k := range []string{sessionCrabID, sessionCrabPK, sessionCrabSK, sessionAuthAt} {
		app.sessions.Remove(ctx, k)
	}
}

// requireAuthentication sends anonymous visitors to the login page and keeps
// signed-in pages out of browser caches.
func (app *App) requireAuthentication(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if currentCrab(r) == nil {
			redirect(w, r, "/krab/login")
			return
		}
		next(w, r)
	}
}

// firstPageSignedOut lets signed-out visitors see a list's first page only,
// so strangers and scrapers can't page through every molt. Later pages send
// them to log in before anything is read.
func (app *App) firstPageSignedOut(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if currentCrab(r) == nil && afterParam(r) != "" {
			redirect(w, r, "/krab/login")
			return
		}
		next(w, r)
	}
}

// requireModerator hides Crabmin (404) from everyone who isn't a moderator or
// admin.
func (app *App) requireModerator(next http.HandlerFunc) http.HandlerFunc {
	return app.requireAuthentication(func(w http.ResponseWriter, r *http.Request) {
		if !currentCrab(r).IsModerator() {
			app.notFound(w, r)
			return
		}
		next(w, r)
	})
}
