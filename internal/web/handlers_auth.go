package web

import (
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/t0ul/krabber-net/internal/auth"
	"github.com/t0ul/krabber-net/internal/config"
	"github.com/t0ul/krabber-net/internal/mail"
	"github.com/t0ul/krabber-net/internal/store"
	"github.com/t0ul/krabber-net/internal/validator"
)

const (
	activationTTL = 3 * 24 * time.Hour

	loginIPLimit      = 20 // attempts per IP per window
	loginFailureLimit = 5  // failed passwords per email and network per window
	// loginEmailFailureLimit caps guesses at one email from everywhere, high
	// enough that a stranger can't lock the owner out with a few tries.
	loginEmailFailureLimit = 50
	loginWindow            = 15 * time.Minute
	signupIPLimit          = 3 // accounts per IP per hour
	resendIPLimit          = 5 // resend requests per IP per hour
	resendEmailLimit       = 3 // resends per email per day
)

type signupForm struct {
	Name                string `form:"name"`
	Email               string `form:"email"`
	Password            string `form:"password"`
	Code                string `form:"code"` // invite code
	Turnstile           string `form:"cf-turnstile-response"`
	validator.Validator `form:"-"`
}

type loginForm struct {
	Email               string `form:"email"`
	Password            string `form:"password"`
	validator.Validator `form:"-"`
}

type activateForm struct {
	Token               string `form:"token"`
	validator.Validator `form:"-"`
}

type resendForm struct {
	Email               string `form:"email"`
	Turnstile           string `form:"cf-turnstile-response"`
	validator.Validator `form:"-"`
}

func (app *App) signupPage(w http.ResponseWriter, r *http.Request) {
	data := app.newTemplateData(r)
	data.Form = signupForm{Code: store.NormalizeInviteCode(r.URL.Query().Get("code"))}
	data.SignupFull = app.signupFull(r)
	app.render(w, r, http.StatusOK, "signup.html", data)
}

// signupFull reports whether MAX_KRABS krabs can already sign in. It counts
// the directory, so it's free, and a few activations past the cap (people
// who signed up just before it) are fine.
func (app *App) signupFull(r *http.Request) bool {
	return app.cfg.MaxKrabs > 0 && app.activeKrabs(r) >= app.cfg.MaxKrabs
}

func (app *App) signupPost(w http.ResponseWriter, r *http.Request) {
	var f signupForm
	if err := app.decodePostForm(w, r, &f); err != nil {
		app.clientError(w, http.StatusBadRequest)
		return
	}
	f.Email = store.NormalizeEmail(f.Email)
	f.Code = store.NormalizeInviteCode(f.Code)

	rerender := func(status int) {
		f.Password = ""
		data := app.newTemplateData(r)
		data.Form = f
		app.render(w, r, status, "signup.html", data)
	}
	if app.cfg.SignupMode == config.SignupClosed {
		f.AddNonFieldError("Registration is temporarily closed.")
		rerender(http.StatusUnprocessableEntity)
		return
	}
	if app.signupFull(r) {
		f.AddNonFieldError("Krabber is full for now. Please check back soon.")
		rerender(http.StatusUnprocessableEntity)
		return
	}

	if ok, err := app.underLimit(r, "signup-ip", clientNet(r), signupIPLimit, time.Hour); err != nil {
		app.serverError(w, r, err)
		return
	} else if !ok {
		f.AddNonFieldError("Too many sign-ups from your network. Please try again later.")
		rerender(http.StatusTooManyRequests)
		return
	}

	f.CheckField(validator.Matches(f.Name, validator.UsernameRX), "name", "Use 3–20 letters, numbers or underscores")
	f.CheckField(validator.NotBlank(f.Email), "email", "This field cannot be blank")
	f.CheckField(validator.Matches(f.Email, validator.EmailRX), "email", "This field must be a valid email address")
	f.CheckField(validator.MinChars(f.Password, auth.MinPasswordLength), "password", "This field must be at least 8 characters long")
	f.CheckField(validator.MaxBytes(f.Password, auth.MaxPasswordLength), "password", "This field must be at most 72 bytes long")
	if app.cfg.SignupMode == config.SignupInvite {
		f.CheckField(f.Code != "", "code", "You need an invite code from a krab to join right now")
	}
	if !f.Valid() {
		rerender(http.StatusUnprocessableEntity)
		return
	}
	if ok, err := app.turnstile.verify(r.Context(), f.Turnstile, clientIP(r)); err != nil || !ok {
		if err != nil {
			app.log.Warn("turnstile unavailable", "err", err)
		}
		f.AddNonFieldError("Please complete the bot check and try again.")
		rerender(http.StatusUnprocessableEntity)
		return
	}

	hash, err := auth.HashPassword(f.Password)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	crab, err := app.store.CreateCrab(r.Context(), f.Name, f.Email, hash, f.Code)
	switch {
	case errors.Is(err, store.ErrInvalidInvite):
		f.AddFieldError("code", "That invite code doesn't work")
		rerender(http.StatusUnprocessableEntity)
		return
	case errors.Is(err, store.ErrDuplicateEmail):
		f.AddFieldError("email", "Email address is already in use")
		rerender(http.StatusUnprocessableEntity)
		return
	case errors.Is(err, store.ErrDuplicateUsername):
		f.AddFieldError("name", "That name is taken")
		rerender(http.StatusUnprocessableEntity)
		return
	case err != nil:
		app.serverError(w, r, err)
		return
	}

	if crab.InvitedBy != "" {
		// Stats and the directory show the inviter's new count straight away.
		if inviter, err := app.store.CrabByID(r.Context(), crab.InvitedBy); err == nil {
			app.dir.putCrab(*inviter)
		}
	}
	flash := "Your account is ready. Check your email for the activation link."
	if err := app.sendActivation(r, crab); err != nil {
		app.log.Warn("activation email not sent", "err", err, "crab", crab.ID)
		flash = "Your account is ready, but we couldn't send the activation email just now. Use \"resend\" below in a few minutes."
	}
	app.sessions.Put(r.Context(), sessionFlash, flash)
	http.Redirect(w, r, "/krab/activate", http.StatusSeeOther)
}

func (app *App) sendActivation(r *http.Request, c *store.Crab) error {
	token, err := app.store.NewToken(r.Context(), c.ID, store.ScopeActivation, activationTTL)
	if err != nil {
		return err
	}
	page := app.cfg.BaseURL.JoinPath("/krab/activate")
	link := *page
	link.RawQuery = url.Values{"token": {token}}.Encode()
	return app.mailer.Send(r.Context(), c.Email, "activation", map[string]string{
		"UserName":     c.UserName,
		"Token":        token,
		"ActivateURL":  link.String(),
		"ActivatePage": page.String(),
	})
}

// activatePage shows the token form. A token in the link only pre-fills the
// form: activating on GET would let mail scanners that follow links activate
// accounts.
func (app *App) activatePage(w http.ResponseWriter, r *http.Request) {
	data := app.newTemplateData(r)
	data.Form = activateForm{Token: r.URL.Query().Get("token")}
	app.render(w, r, http.StatusOK, "activate.html", data)
}

func (app *App) activatePost(w http.ResponseWriter, r *http.Request) {
	var f activateForm
	if err := app.decodePostForm(w, r, &f); err != nil {
		app.clientError(w, http.StatusBadRequest)
		return
	}
	tok, err := app.store.ConsumeToken(r.Context(), store.ScopeActivation, f.Token)
	switch {
	case errors.Is(err, store.ErrInvalidToken):
		f.AddFieldError("token", "That token is invalid or has expired. Request a new one below.")
		data := app.newTemplateData(r)
		data.Form = f
		app.render(w, r, http.StatusUnprocessableEntity, "activate.html", data)
		return
	case err != nil:
		app.serverError(w, r, err)
		return
	}
	if err := app.store.ActivateCrab(r.Context(), tok.CrabID); err != nil {
		app.serverError(w, r, err)
		return
	}
	if c, err := app.store.CrabByID(r.Context(), tok.CrabID); err == nil {
		c.Activated = true
		app.dir.putCrab(*c)
	}
	app.sessions.Put(r.Context(), sessionFlash, "You're activated! Log in to start molting.")
	http.Redirect(w, r, "/krab/login", http.StatusSeeOther)
}

// resendPost always answers the same way so it can't be used to discover
// which emails have accounts.
func (app *App) resendPost(w http.ResponseWriter, r *http.Request) {
	var f resendForm
	if err := app.decodePostForm(w, r, &f); err != nil {
		app.clientError(w, http.StatusBadRequest)
		return
	}
	email := store.NormalizeEmail(f.Email)
	const done = "If that account exists and isn't active yet, we've sent a new activation email."

	ipOK, err := app.underLimit(r, "resend-ip", clientNet(r), resendIPLimit, time.Hour)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	human, err := app.turnstile.verify(r.Context(), f.Turnstile, clientIP(r))
	if err != nil {
		app.log.Warn("turnstile unavailable", "err", err)
	}
	if !ipOK || !human || !validator.Matches(email, validator.EmailRX) {
		app.sessions.Put(r.Context(), sessionFlash, done)
		http.Redirect(w, r, "/krab/activate", http.StatusSeeOther)
		return
	}

	crab, err := app.store.CrabByEmail(r.Context(), email)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		app.serverError(w, r, err)
		return
	case !crab.Activated && !crab.Banned && !crab.Deleted:
		emailOK, err := app.underLimit(r, "resend-email", email, resendEmailLimit, 24*time.Hour)
		if err != nil {
			app.serverError(w, r, err)
			return
		}
		if emailOK {
			if err := app.sendActivation(r, crab); err != nil && !errors.Is(err, mail.ErrDailyCapReached) {
				app.log.Warn("activation email not sent", "err", err, "crab", crab.ID)
			}
		}
	}
	app.sessions.Put(r.Context(), sessionFlash, done)
	http.Redirect(w, r, "/krab/activate", http.StatusSeeOther)
}

func (app *App) loginPage(w http.ResponseWriter, r *http.Request) {
	data := app.newTemplateData(r)
	data.Form = loginForm{}
	app.render(w, r, http.StatusOK, "login.html", data)
}

func (app *App) loginPost(w http.ResponseWriter, r *http.Request) {
	var f loginForm
	if err := app.decodePostForm(w, r, &f); err != nil {
		app.clientError(w, http.StatusBadRequest)
		return
	}
	f.Email = store.NormalizeEmail(f.Email)
	fail := func(status int, msg string) {
		f.Password = ""
		f.AddNonFieldError(msg)
		data := app.newTemplateData(r)
		data.Form = f
		app.render(w, r, status, "login.html", data)
	}
	const tooMany = "Too many attempts. Please wait a few minutes and try again."
	const incorrect = "Email or password is incorrect"

	if ok, err := app.underLimit(r, "login-ip", clientNet(r), loginIPLimit, loginWindow); err != nil {
		app.serverError(w, r, err)
		return
	} else if !ok {
		fail(http.StatusTooManyRequests, tooMany)
		return
	}

	f.CheckField(validator.Matches(f.Email, validator.EmailRX), "email", "This field must be a valid email address")
	f.CheckField(validator.NotBlank(f.Password), "password", "This field cannot be blank")
	f.CheckField(validator.MaxBytes(f.Password, auth.MaxPasswordLength), "password", "This field must be at most 72 bytes long")
	if !f.Valid() {
		fail(http.StatusUnprocessableEntity, incorrect)
		return
	}

	// Wrong passwords are counted per email and network, so someone else
	// can't lock an account out, and per email overall at a much higher
	// limit. Each attempt is counted before bcrypt runs, so concurrent
	// guesses can't all get in under the limit, and given back if right.
	perNet := f.Email + " " + clientNet(r)
	tries, err := app.store.Hit(r.Context(), "login-fail", perNet, loginWindow)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	anywhere, err := app.store.Hit(r.Context(), "login-fail-email", f.Email, loginWindow)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	if tries > loginFailureLimit || anywhere > loginEmailFailureLimit {
		fail(http.StatusTooManyRequests, tooMany)
		return
	}
	forgive := func() {
		for action, key := range map[string]string{"login-fail": perNet, "login-fail-email": f.Email} {
			if err := app.store.Unhit(r.Context(), action, key, loginWindow); err != nil {
				app.log.Warn("login limit", "err", err)
			}
		}
	}

	crab, err := app.store.CrabByEmail(r.Context(), f.Email)
	var hash []byte
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		app.serverError(w, r, err)
		return
	default:
		hash = crab.PasswordHash
	}
	match, err := auth.CheckPassword(hash, f.Password)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	if !match {
		fail(http.StatusUnprocessableEntity, incorrect)
		return
	}
	forgive()
	switch {
	case crab.Banned || crab.Deleted:
		fail(http.StatusUnprocessableEntity, "This account is unavailable.")
		return
	case !crab.Activated:
		fail(http.StatusUnprocessableEntity, "Please activate your account first. Check your email, or request a new link on the activation page.")
		return
	}

	ctx := r.Context()
	if err := app.sessions.RenewToken(ctx); err != nil {
		app.serverError(w, r, err)
		return
	}
	app.sessions.Put(ctx, sessionCrabID, crab.ID)
	app.sessions.Put(ctx, sessionCrabPK, crab.PK)
	app.sessions.Put(ctx, sessionCrabSK, crab.SK)
	app.sessions.Put(ctx, sessionAuthAt, time.Now().Unix())
	http.Redirect(w, r, "/trench", http.StatusSeeOther)
}

func (app *App) logoutPost(w http.ResponseWriter, r *http.Request) {
	app.endSession(r)
	app.sessions.Put(r.Context(), sessionFlash, "You've been logged out successfully!")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// underLimit counts one attempt and reports whether it's within limit.
func (app *App) underLimit(r *http.Request, action, key string, limit int, window time.Duration) (bool, error) {
	if key == "" {
		key = "unknown"
	}
	n, err := app.store.Hit(r.Context(), action, key, window)
	if err != nil {
		return false, err
	}
	return n <= limit, nil
}
