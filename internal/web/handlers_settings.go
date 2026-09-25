package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/t0ul/krabber-net/internal/auth"
	"github.com/t0ul/krabber-net/internal/store"
	"github.com/t0ul/krabber-net/internal/trophies"
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
	Age                 string `form:"fun_age"`
	Pronouns            string `form:"fun_pronouns"`
	Quote               string `form:"fun_quote"`
	Jam                 string `form:"fun_jam"`
	Obsession           string `form:"fun_obsession"`
	Remember            string `form:"fun_remember"`
	Emoji               string `form:"fun_emoji"`
	validator.Validator `form:"-"`
}

func profileFormFor(p store.Profile) profileForm {
	return profileForm{
		DisplayName: p.DisplayName, Bio: p.Bio, Location: p.Location, Website: p.Website,
		Age: p.Age, Pronouns: p.Pronouns, Quote: p.Quote, Jam: p.Jam,
		Obsession: p.Obsession, Remember: p.Remember, Emoji: p.Emoji,
	}
}

func (f *profileForm) profile() store.Profile {
	return store.Profile{
		DisplayName: f.DisplayName, Bio: f.Bio, Location: f.Location, Website: f.Website,
		FunFacts: store.FunFacts{
			Age: f.Age, Pronouns: f.Pronouns, Quote: f.Quote, Jam: f.Jam,
			Obsession: f.Obsession, Remember: f.Remember, Emoji: f.Emoji,
		},
	}
}

type funFactField struct {
	value *string
	key   string
	max   int
}

// funFactFields are the fun-fact inputs with their limits, in Crabber's order.
func (f *profileForm) funFactFields() []funFactField {
	return []funFactField{
		{&f.Age, "fun_age", store.MaxAge},
		{&f.Pronouns, "fun_pronouns", store.MaxPronouns},
		{&f.Quote, "fun_quote", store.MaxFunFact},
		{&f.Jam, "fun_jam", store.MaxFunFact},
		{&f.Obsession, "fun_obsession", store.MaxFunFact},
		{&f.Remember, "fun_remember", store.MaxFunFact},
		{&f.Emoji, "fun_emoji", store.MaxEmoji},
	}
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

type usernameForm struct {
	Name                string `form:"username"`
	validator.Validator `form:"-"`
}

type settingsForms struct {
	Profile     profileForm
	Username    usernameForm
	Password    passwordForm
	Delete      deleteForm
	AvatarError string
	// NextRename is when the crab may change their username again, as a
	// date; empty when they may now.
	NextRename string

	InviteCode     string
	InviteLink     string
	InviteDisabled bool
	Invites        int
}

func (app *App) settings(w http.ResponseWriter, r *http.Request) {
	app.renderSettings(w, r, http.StatusOK, settingsForms{Profile: profileFormFor(currentCrab(r).Profile)})
}

func (app *App) renderSettings(w http.ResponseWriter, r *http.Request, status int, f settingsForms) {
	f.Password.Current, f.Password.New, f.Password.Confirm, f.Delete.Password = "", "", "", ""
	me := currentCrab(r)
	if next := app.store.NextUsernameChange(me); !next.IsZero() {
		f.NextRename = next.UTC().Format("January 2, 2006")
	}
	if code, err := app.store.EnsureInviteCode(r.Context(), me); err != nil {
		app.log.Warn("invite code", "err", err, "crab", me.ID)
	} else {
		f.InviteCode, f.Invites = code, me.Invites
		link := *app.cfg.BaseURL.JoinPath("/krab/signup")
		link.RawQuery = url.Values{"code": {code}}.Encode()
		f.InviteLink = link.String()
		if f.InviteDisabled, err = app.store.InviteCodeDisabled(r.Context(), me); err != nil {
			app.log.Warn("invite code status", "err", err, "crab", me.ID)
		}
	}
	data := app.newTemplateData(r)
	data.Form = f
	_, byID, _ := app.snapshot(r)
	for id, name := range blocksOf(r).Blocking {
		av := ""
		if c, ok := byID[id]; ok {
			name, av = c.UserName, c.Avatar
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
	for _, ff := range f.funFactFields() {
		*ff.value = singleLine(*ff.value)
		f.CheckField(validator.MaxChars(*ff.value, ff.max), ff.key, fmt.Sprintf("Keep it under %d characters", ff.max))
	}
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
	if err := app.store.UpdateProfile(r.Context(), &c, f.profile()); err != nil {
		app.serverError(w, r, err)
		return
	}
	c.Profile = f.profile()
	p, facts := c.Profile, c.FunFacts
	if trophies.CustomizedProfile(p.DisplayName, p.Bio, p.Location, p.Website, !facts.Empty()) {
		app.award(r.Context(), &c, "i-want-it-that-way")
	}
	app.dir.putCrab(c)
	app.sessions.Put(r.Context(), sessionFlash, "Profile saved.")
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (app *App) settingsAppearancePost(w http.ResponseWriter, r *http.Request) {
	var f struct {
		Light    bool `form:"light_mode"`
		Dyslexic bool `form:"dyslexic_mode"`
	}
	if err := app.decodePostForm(w, r, &f); err != nil {
		app.clientError(w, http.StatusBadRequest)
		return
	}
	c := *currentCrab(r)
	if err := app.store.SetAppearance(r.Context(), &c, store.Appearance{LightMode: f.Light, DyslexicMode: f.Dyslexic}); err != nil {
		app.serverError(w, r, err)
		return
	}
	app.sessions.Put(r.Context(), sessionFlash, "Changes saved.")
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (app *App) settingsUsernamePost(w http.ResponseWriter, r *http.Request) {
	var f usernameForm
	if err := app.decodePostForm(w, r, &f); err != nil {
		app.clientError(w, http.StatusBadRequest)
		return
	}
	c := *currentCrab(r)
	f.Name = strings.TrimPrefix(strings.TrimSpace(f.Name), "@")
	f.CheckField(validator.Matches(f.Name, validator.UsernameRX), "username", "Use 3–20 letters, numbers or underscores")
	f.CheckField(f.Name != c.UserName, "username", "That's already your username")
	rerender := func(status int) {
		app.renderSettings(w, r, status, settingsForms{Profile: profileFormFor(c.Profile), Username: f})
	}
	if !f.Valid() {
		rerender(http.StatusUnprocessableEntity)
		return
	}
	old := c.UserName
	switch err := app.store.ChangeUsername(r.Context(), &c, f.Name); {
	case errors.Is(err, store.ErrDuplicateUsername):
		f.AddFieldError("username", "That name is taken")
		rerender(http.StatusUnprocessableEntity)
		return
	case errors.Is(err, store.ErrTooSoon):
		f.AddFieldError("username", "You can only change your username once every 30 days")
		rerender(http.StatusTooManyRequests)
		return
	case err != nil:
		app.serverError(w, r, err)
		return
	}
	app.dir.putCrab(c)
	msg := "You're now @" + c.UserName + "."
	if !strings.EqualFold(old, c.UserName) {
		msg += " Links to @" + old + " keep working for 30 days."
	}
	app.sessions.Put(r.Context(), sessionFlash, msg)
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
			Profile:  profileFormFor(c.Profile),
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
			Profile: profileFormFor(c.Profile),
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
