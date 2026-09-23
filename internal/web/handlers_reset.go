package web

import (
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/t0ul/krabber-net/internal/auth"
	"github.com/t0ul/krabber-net/internal/mail"
	"github.com/t0ul/krabber-net/internal/store"
	"github.com/t0ul/krabber-net/internal/validator"
)

const (
	resetTTL        = time.Hour
	resetIPLimit    = 5 // reset requests per IP per hour
	resetEmailLimit = 3 // reset emails per address per day
)

type forgotForm struct {
	Email               string `form:"email"`
	Turnstile           string `form:"cf-turnstile-response"`
	validator.Validator `form:"-"`
}

type resetForm struct {
	Token               string `form:"token"`
	Password            string `form:"password"`
	Confirm             string `form:"confirm_password"`
	validator.Validator `form:"-"`
}

func (app *App) forgotPage(w http.ResponseWriter, r *http.Request) {
	data := app.newTemplateData(r)
	data.Form = forgotForm{}
	app.render(w, r, http.StatusOK, "forgot.html", data)
}

// forgotPost always answers the same way so it can't be used to discover
// which emails have accounts.
func (app *App) forgotPost(w http.ResponseWriter, r *http.Request) {
	var f forgotForm
	if err := app.decodePostForm(w, r, &f); err != nil {
		app.clientError(w, http.StatusBadRequest)
		return
	}
	email := store.NormalizeEmail(f.Email)
	const done = "If that email belongs to an account, we've sent a link to reset the password. It expires in an hour."
	finish := func() {
		app.sessions.Put(r.Context(), sessionFlash, done)
		http.Redirect(w, r, "/crab/reset", http.StatusSeeOther)
	}

	ipOK, err := app.underLimit(r, "reset-ip", clientIP(r), resetIPLimit, time.Hour)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	human, err := app.turnstile.verify(r.Context(), f.Turnstile, clientIP(r))
	if err != nil {
		app.log.Warn("turnstile unavailable", "err", err)
	}
	if !ipOK || !human || !validator.Matches(email, validator.EmailRX) {
		finish()
		return
	}

	crab, err := app.store.CrabByEmail(r.Context(), email)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		app.serverError(w, r, err)
		return
	case crab.CanSignIn():
		emailOK, err := app.underLimit(r, "reset-email", email, resetEmailLimit, 24*time.Hour)
		if err != nil {
			app.serverError(w, r, err)
			return
		}
		if emailOK {
			if err := app.sendReset(r, crab); err != nil && !errors.Is(err, mail.ErrDailyCapReached) {
				app.log.Warn("reset email not sent", "err", err, "crab", crab.ID)
			}
		}
	}
	finish()
}

func (app *App) sendReset(r *http.Request, c *store.Crab) error {
	token, err := app.store.NewToken(r.Context(), c.ID, store.ScopePasswordReset, resetTTL)
	if err != nil {
		return err
	}
	page := app.cfg.BaseURL.JoinPath("/crab/reset")
	link := *page
	link.RawQuery = url.Values{"token": {token}}.Encode()
	return app.mailer.Send(r.Context(), c.Email, "password-reset", map[string]string{
		"UserName":  c.UserName,
		"Token":     token,
		"ResetURL":  link.String(),
		"ResetPage": page.String(),
	})
}

// resetPage shows the new-password form. Like activation, the token in the
// link only pre-fills it, so link-following mail scanners can't use it up.
func (app *App) resetPage(w http.ResponseWriter, r *http.Request) {
	data := app.newTemplateData(r)
	data.Form = resetForm{Token: r.URL.Query().Get("token")}
	app.render(w, r, http.StatusOK, "reset.html", data)
}

func (app *App) resetPost(w http.ResponseWriter, r *http.Request) {
	var f resetForm
	if err := app.decodePostForm(w, r, &f); err != nil {
		app.clientError(w, http.StatusBadRequest)
		return
	}
	rerender := func(status int) {
		f.Password, f.Confirm = "", ""
		data := app.newTemplateData(r)
		data.Form = f
		app.render(w, r, status, "reset.html", data)
	}
	// Check the password first: a rejected password mustn't use up the token.
	f.CheckField(validator.NotBlank(f.Token), "token", "Paste the token from the email")
	f.CheckField(validator.MinChars(f.Password, auth.MinPasswordLength), "password", "Use at least 8 characters")
	f.CheckField(validator.MaxBytes(f.Password, auth.MaxPasswordLength), "password", "Use at most 72 bytes")
	f.CheckField(f.Password == f.Confirm, "confirm_password", "The passwords don't match")
	if !f.Valid() {
		rerender(http.StatusUnprocessableEntity)
		return
	}

	tok, err := app.store.ConsumeToken(r.Context(), store.ScopePasswordReset, f.Token)
	switch {
	case errors.Is(err, store.ErrInvalidToken):
		f.AddFieldError("token", "That token is invalid or has expired. Request a new link below.")
		rerender(http.StatusUnprocessableEntity)
		return
	case err != nil:
		app.serverError(w, r, err)
		return
	}
	crab, err := app.store.CrabByID(r.Context(), tok.CrabID)
	if err == nil && !crab.CanSignIn() {
		err = store.ErrNotFound
	}
	if errors.Is(err, store.ErrNotFound) {
		f.AddFieldError("token", "That token is invalid or has expired. Request a new link below.")
		rerender(http.StatusUnprocessableEntity)
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	hash, err := auth.HashPassword(f.Password)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	if _, err := app.store.SetPassword(r.Context(), crab, hash); err != nil {
		app.serverError(w, r, err)
		return
	}
	app.endSession(r)
	app.sessions.Put(r.Context(), sessionFlash, "Your password is reset and every device is signed out. Log in with the new one.")
	http.Redirect(w, r, "/crab/login", http.StatusSeeOther)
}
