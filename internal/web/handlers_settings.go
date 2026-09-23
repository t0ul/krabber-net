package web

import (
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/t0ul/krabber-net/internal/auth"
	"github.com/t0ul/krabber-net/internal/store"
	"github.com/t0ul/krabber-net/internal/validator"
)

const (
	passwordChangeLimit  = 5 // wrong current passwords per crab per window
	passwordChangeWindow = 15 * time.Minute
)

type profileForm struct {
	DisplayName         string `form:"display_name"`
	Bio                 string `form:"bio"`
	Location            string `form:"location"`
	Website             string `form:"website"`
	validator.Validator `form:"-"`
}

type passwordForm struct {
	Current             string `form:"current_password"`
	New                 string `form:"new_password"`
	Confirm             string `form:"confirm_password"`
	validator.Validator `form:"-"`
}

type settingsForms struct {
	Profile  profileForm
	Password passwordForm
}

func (app *App) settings(w http.ResponseWriter, r *http.Request) {
	c := currentCrab(r)
	app.renderSettings(w, r, http.StatusOK, settingsForms{Profile: profileForm{
		DisplayName: c.DisplayName,
		Bio:         c.Bio,
		Location:    c.Location,
		Website:     c.Website,
	}})
}

func (app *App) renderSettings(w http.ResponseWriter, r *http.Request, status int, f settingsForms) {
	f.Password.Current, f.Password.New, f.Password.Confirm = "", "", ""
	data := app.newTemplateData(r)
	data.Form = f
	app.render(w, r, status, "settings.html", data)
}

func (app *App) settingsProfilePost(w http.ResponseWriter, r *http.Request) {
	var f profileForm
	if err := app.decodePostForm(w, r, &f); err != nil {
		app.clientError(w, http.StatusBadRequest)
		return
	}
	f.DisplayName = singleLine(f.DisplayName)
	f.Location = singleLine(f.Location)
	f.Website = strings.TrimSpace(f.Website)
	f.Bio = strings.TrimSpace(strings.ReplaceAll(f.Bio, "\r\n", "\n"))

	f.CheckField(validator.MaxChars(f.DisplayName, store.MaxDisplayName), "display_name", "Keep it under 64 characters")
	f.CheckField(validator.MaxChars(f.Bio, store.MaxBio), "bio", "Keep it under 512 characters")
	f.CheckField(validator.MaxChars(f.Location, store.MaxLocation), "location", "Keep it under 128 characters")
	f.CheckField(validator.MaxChars(f.Website, store.MaxWebsite), "website", "Keep it under 512 characters")
	if f.Website != "" {
		if site, ok := normalizeWebsite(f.Website); ok {
			f.Website = site
		} else {
			f.AddFieldError("website", "Use a web address like https://example.com")
		}
	}
	if !f.Valid() {
		app.renderSettings(w, r, http.StatusUnprocessableEntity, settingsForms{Profile: f})
		return
	}

	c := currentCrab(r)
	err := app.store.UpdateProfile(r.Context(), c, store.Profile{
		DisplayName: f.DisplayName,
		Bio:         f.Bio,
		Location:    f.Location,
		Website:     f.Website,
	})
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	app.dir.invalidate()
	app.sessions.Put(r.Context(), sessionFlash, "Profile saved.")
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (app *App) settingsPasswordPost(w http.ResponseWriter, r *http.Request) {
	var f passwordForm
	if err := app.decodePostForm(w, r, &f); err != nil {
		app.clientError(w, http.StatusBadRequest)
		return
	}
	c := currentCrab(r)
	forms := func() settingsForms {
		return settingsForms{
			Profile:  profileForm{DisplayName: c.DisplayName, Bio: c.Bio, Location: c.Location, Website: c.Website},
			Password: f,
		}
	}

	failures, err := app.store.Count(r.Context(), "password-change", c.ID, passwordChangeWindow)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	if failures >= passwordChangeLimit {
		f.AddFieldError("current_password", "Too many attempts. Please wait a few minutes and try again.")
		app.renderSettings(w, r, http.StatusTooManyRequests, forms())
		return
	}

	f.CheckField(validator.MaxBytes(f.Current, auth.MaxPasswordLength), "current_password", "That isn't your current password")
	f.CheckField(validator.MinChars(f.New, auth.MinPasswordLength), "new_password", "Use at least 8 characters")
	f.CheckField(validator.MaxBytes(f.New, auth.MaxPasswordLength), "new_password", "Use at most 72 bytes")
	f.CheckField(f.New == f.Confirm, "confirm_password", "The passwords don't match")
	if !f.Valid() {
		app.renderSettings(w, r, http.StatusUnprocessableEntity, forms())
		return
	}
	match, err := auth.CheckPassword(c.PasswordHash, f.Current)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	if !match {
		if _, err := app.store.Hit(r.Context(), "password-change", c.ID, passwordChangeWindow); err != nil {
			app.serverError(w, r, err)
			return
		}
		f.AddFieldError("current_password", "That isn't your current password")
		app.renderSettings(w, r, http.StatusUnprocessableEntity, forms())
		return
	}

	hash, err := auth.HashPassword(f.New)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	validAfter, err := app.store.SetPassword(r.Context(), c, hash)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	// Every other session is now signed out; this one continues on a new token.
	if err := app.sessions.RenewToken(r.Context()); err != nil {
		app.serverError(w, r, err)
		return
	}
	app.sessions.Put(r.Context(), sessionAuthAt, validAfter)
	app.sessions.Put(r.Context(), sessionFlash, "Password changed. You've been signed out everywhere else.")
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// singleLine trims a one-line field and drops control characters.
func singleLine(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s))
}

// normalizeWebsite accepts an http(s) address, adding https:// when the
// scheme is missing.
func normalizeWebsite(raw string) (string, bool) {
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || strings.ContainsAny(u.Host, " <>\"'") {
		return "", false
	}
	return u.String(), true
}
