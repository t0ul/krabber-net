package web

import (
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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

type deleteForm struct {
	Password            string `form:"password"`
	validator.Validator `form:"-"`
}

type settingsForms struct {
	Profile     profileForm
	Password    passwordForm
	Delete      deleteForm
	AvatarError string
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
	f.Password.Current, f.Password.New, f.Password.Confirm, f.Delete.Password = "", "", "", ""
	data := app.newTemplateData(r)
	data.Form = f
	_, byID, _ := app.snapshot(r)
	for id, name := range blocksOf(r).Blocking {
		av := ""
		if c, ok := byID[id]; ok {
			av = c.Avatar
		}
		data.Blocked = append(data.Blocked, store.Crab{ID: id, UserName: name, Avatar: av})
	}
	sort.Slice(data.Blocked, func(i, j int) bool { return data.Blocked[i].UserName < data.Blocked[j].UserName })
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

	c := *currentCrab(r)
	err := app.store.UpdateProfile(r.Context(), &c, store.Profile{
		DisplayName: f.DisplayName,
		Bio:         f.Bio,
		Location:    f.Location,
		Website:     f.Website,
	})
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	c.DisplayName, c.Bio, c.Location, c.Website = f.DisplayName, f.Bio, f.Location, f.Website
	app.dir.putCrab(c)
	app.sessions.Put(r.Context(), sessionFlash, "Profile saved.")
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (app *App) settingsContentPost(w http.ResponseWriter, r *http.Request) {
	var f struct {
		ShowNSFW   bool   `form:"show_nsfw"`
		MutedWords string `form:"muted_words"`
	}
	if err := app.decodePostForm(w, r, &f); err != nil {
		app.clientError(w, http.StatusBadRequest)
		return
	}
	c := *currentCrab(r)
	filters := store.ContentFilters{ShowNSFW: f.ShowNSFW, MutedWords: parseMutedWords(f.MutedWords)}
	if err := app.store.SetContentFilters(r.Context(), &c, filters); err != nil {
		app.serverError(w, r, err)
		return
	}
	app.sessions.Put(r.Context(), sessionFlash, "Changes saved.")
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// parseMutedWords splits a comma-separated list into lowercase words and
// phrases, dropping blanks and repeats. Anything past the limits is left off.
func parseMutedWords(raw string) []string {
	var words []string
	seen := map[string]bool{}
	total := 0
	for part := range strings.SplitSeq(raw, ",") {
		w := strings.ToLower(strings.Join(strings.Fields(singleLine(part)), " "))
		if w == "" || seen[w] || utf8.RuneCountInString(w) > store.MaxMutedWordLen {
			continue
		}
		total += len(w) + 1
		if total > store.MaxMutedWordsLen || len(words) == store.MaxMutedWords {
			break
		}
		seen[w] = true
		words = append(words, w)
	}
	return words
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

	f.CheckField(validator.MinChars(f.New, auth.MinPasswordLength), "new_password", "Use at least 8 characters")
	f.CheckField(validator.MaxBytes(f.New, auth.MaxPasswordLength), "new_password", "Use at most 72 bytes")
	f.CheckField(f.New == f.Confirm, "confirm_password", "The passwords don't match")
	ok, status, err := app.confirmPassword(r, c, f.Current)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	if !ok {
		f.AddFieldError("current_password", passwordMessage(status))
	}
	if !f.Valid() {
		if status == 0 {
			status = http.StatusUnprocessableEntity
		}
		app.renderSettings(w, r, status, forms())
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

func (app *App) settingsDeletePost(w http.ResponseWriter, r *http.Request) {
	var f deleteForm
	if err := app.decodePostForm(w, r, &f); err != nil {
		app.clientError(w, http.StatusBadRequest)
		return
	}
	c := currentCrab(r)
	ok, status, err := app.confirmPassword(r, c, f.Password)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	if !ok {
		f.AddFieldError("password", passwordMessage(status))
		app.renderSettings(w, r, status, settingsForms{
			Profile: profileForm{DisplayName: c.DisplayName, Bio: c.Bio, Location: c.Location, Website: c.Website},
			Delete:  f,
		})
		return
	}
	if _, err := app.store.DeleteAccount(r.Context(), c); err != nil {
		app.serverError(w, r, err)
		return
	}
	app.dir.setGone(c.ID, true)
	app.endSession(r)
	app.log.Info("account deleted", "crab", c.ID)
	app.sessions.Put(r.Context(), sessionFlash, "Your account is deleted. So long, and thanks for all the molts.")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// confirmPassword checks the signed-in crab's password before a sensitive
// change. Wrong guesses are limited per crab: when ok is false, status is
// 429 once the limit is reached and 422 for a wrong password.
func (app *App) confirmPassword(r *http.Request, c *store.Crab, password string) (ok bool, status int, err error) {
	failures, err := app.store.Count(r.Context(), "password-change", c.ID, passwordChangeWindow)
	if err != nil {
		return false, 0, err
	}
	if failures >= passwordChangeLimit {
		return false, http.StatusTooManyRequests, nil
	}
	match := false
	if len(password) <= auth.MaxPasswordLength {
		if match, err = auth.CheckPassword(c.PasswordHash, password); err != nil {
			return false, 0, err
		}
	}
	if !match {
		if _, err := app.store.Hit(r.Context(), "password-change", c.ID, passwordChangeWindow); err != nil {
			return false, 0, err
		}
		return false, http.StatusUnprocessableEntity, nil
	}
	return true, 0, nil
}

func passwordMessage(status int) string {
	if status == http.StatusTooManyRequests {
		return "Too many attempts. Please wait a few minutes and try again."
	}
	return "That isn't your current password"
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
