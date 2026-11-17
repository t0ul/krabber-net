package rules

import (
	"context"
	"errors"
	"time"

	"github.com/t0ul/krabber-net/engine/auth"
	"github.com/t0ul/krabber-net/engine/store"
	"github.com/t0ul/krabber-net/engine/validator"
)

// The account flows' limits and lifetimes. Their counters live in the
// table (Store.Hit), so a front end that keeps the same names and numbers
// shares them: guesses at a password on one site count on the other.
const (
	ActivationTTL = 3 * 24 * time.Hour
	ResetTTL      = time.Hour

	LoginIPLimit      = 20 // attempts per network per window
	LoginFailureLimit = 5  // failed passwords per email and network per window
	// LoginEmailFailureLimit caps guesses at one email from everywhere, high
	// enough that a stranger can't lock the owner out with a few tries.
	LoginEmailFailureLimit = 50
	LoginWindow            = 15 * time.Minute

	SignupIPLimit    = 3 // accounts per network per hour
	ResendIPLimit    = 5 // activation resends per network per hour
	ResendEmailLimit = 3 // activation resends per email per day
	ResetIPLimit     = 5 // reset requests per network per hour
	ResetEmailLimit  = 3 // reset emails per address per day
)

// Under counts one attempt at action by key and reports whether it is
// within limit for window.
func (s *Service) Under(ctx context.Context, action, key string, limit int, window time.Duration) (bool, error) {
	if key == "" {
		key = "unknown"
	}
	n, err := s.Store.Hit(ctx, action, key, window)
	if err != nil {
		return false, err
	}
	return n <= limit, nil
}

// LoginResult is how a sign-in attempt came out. Each front end words it.
type LoginResult int

const (
	SignedIn     LoginResult = iota
	TooManyTries             // the network or the email has guessed too often lately
	Invalid                  // the email or password can't be one (the front end may say which field)
	Incorrect                // no such email, or the wrong password: never says which
	Unavailable              // the account is banned or deleted
	NotActivated             // the password is right but the account isn't activated yet
)

// Login checks a sign-in the way every front end must, in this order: the
// network's attempts, then the form, then the email's failed guesses —
// counted before bcrypt runs, so concurrent guesses can't all get in under
// the limit, and given back when the password is right — and only then the
// account's state. net is the client's network (an address prefix); email
// is normalised here. The crab comes back only with SignedIn.
func (s *Service) Login(ctx context.Context, email, password, net string) (*store.Crab, LoginResult, error) {
	email = store.NormalizeEmail(email)
	if ok, err := s.Under(ctx, "login-ip", net, LoginIPLimit, LoginWindow); err != nil {
		return nil, 0, err
	} else if !ok {
		return nil, TooManyTries, nil
	}
	if !validator.Matches(email, validator.EmailRX) || !validator.NotBlank(password) || !validator.MaxBytes(password, auth.MaxPasswordLength) {
		return nil, Invalid, nil
	}

	// Wrong passwords are counted per email and network, so someone else
	// can't lock an account out, and per email overall at a much higher
	// limit.
	perNet := email + " " + net
	tries, err := s.Store.Hit(ctx, "login-fail", perNet, LoginWindow)
	if err != nil {
		return nil, 0, err
	}
	anywhere, err := s.Store.Hit(ctx, "login-fail-email", email, LoginWindow)
	if err != nil {
		return nil, 0, err
	}
	if tries > LoginFailureLimit || anywhere > LoginEmailFailureLimit {
		return nil, TooManyTries, nil
	}

	crab, err := s.Store.CrabByEmail(ctx, email)
	var hash []byte
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return nil, 0, err
	default:
		hash = crab.PasswordHash
	}
	match, err := auth.CheckPassword(hash, password)
	if err != nil {
		return nil, 0, err
	}
	if !match {
		return nil, Incorrect, nil
	}
	for action, key := range map[string]string{"login-fail": perNet, "login-fail-email": email} {
		if err := s.Store.Unhit(ctx, action, key, LoginWindow); err != nil {
			s.Log.Warn("login limit", "err", err)
		}
	}
	switch {
	case crab.Banned || crab.Deleted:
		return nil, Unavailable, nil
	case !crab.Activated:
		return nil, NotActivated, nil
	}
	return crab, SignedIn, nil
}

// SignUp makes a new, not yet activated account. The front end has already
// checked the site's sign-up mode and cap, the network's limit, the fields
// and the human check. Errors are the table's: ErrInvalidInvite,
// ErrDuplicateEmail, ErrDuplicateUsername.
func (s *Service) SignUp(ctx context.Context, username, email, password, invite string) (*store.Crab, error) {
	hash, err := auth.HashPassword(password)
	if err != nil {
		return nil, err
	}
	return s.Store.CreateCrab(ctx, username, store.NormalizeEmail(email), hash, invite)
}

// ActivationToken is a fresh activation token for c, to mail them.
func (s *Service) ActivationToken(ctx context.Context, c *store.Crab) (string, error) {
	return s.Store.NewToken(ctx, c.ID, store.ScopeActivation, ActivationTTL)
}

// Activate uses up an activation token and activates its account, returning
// the account's ID. store.ErrInvalidToken when the token is wrong, used or
// expired.
func (s *Service) Activate(ctx context.Context, token string) (string, error) {
	tok, err := s.Store.ConsumeToken(ctx, store.ScopeActivation, token)
	if err != nil {
		return "", err
	}
	if err := s.Store.ActivateCrab(ctx, tok.CrabID); err != nil {
		return "", err
	}
	return tok.CrabID, nil
}

// ResendTo is the account a new activation email should go to for email,
// if any: one that exists, isn't active, banned or deleted, and hasn't had
// ResendEmailLimit emails today. It never says why not.
func (s *Service) ResendTo(ctx context.Context, email string) *store.Crab {
	crab, err := s.Store.CrabByEmail(ctx, email)
	if err != nil || crab.Activated || crab.Banned || crab.Deleted {
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			s.Log.Warn("resend lookup", "err", err)
		}
		return nil
	}
	if n, err := s.Store.Hit(ctx, "resend-email", email, 24*time.Hour); err != nil || n > ResendEmailLimit {
		return nil
	}
	return crab
}

// ResetTo is the account a password-reset email should go to for email,
// if any: one that can sign in and hasn't had ResetEmailLimit emails today.
// It never says why not.
func (s *Service) ResetTo(ctx context.Context, email string) *store.Crab {
	crab, err := s.Store.CrabByEmail(ctx, email)
	if err != nil || !crab.CanSignIn() {
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			s.Log.Warn("reset lookup", "err", err)
		}
		return nil
	}
	if n, err := s.Store.Hit(ctx, "reset-email", email, 24*time.Hour); err != nil || n > ResetEmailLimit {
		return nil
	}
	return crab
}

// ResetToken is a fresh password-reset token for c, to mail them.
func (s *Service) ResetToken(ctx context.Context, c *store.Crab) (string, error) {
	return s.Store.NewToken(ctx, c.ID, store.ScopePasswordReset, ResetTTL)
}

// ResetPassword uses up a reset token and sets its account's password,
// signing it out everywhere. The front end has already checked the new
// password, so a rejected one never uses up the token. A token is
// store.ErrInvalidToken when wrong, used or expired, and store.ErrNotFound
// when its account can't sign in or changed its password after the link
// was sent (which retires every link still in a mailbox).
func (s *Service) ResetPassword(ctx context.Context, token, password string) (*store.Crab, error) {
	tok, err := s.Store.ConsumeToken(ctx, store.ScopePasswordReset, token)
	if err != nil {
		return nil, err
	}
	crab, err := s.Store.CrabByID(ctx, tok.CrabID)
	if err == nil && (!crab.CanSignIn() || tok.IssuedAt < crab.SessionsValidAfter) {
		err = store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return nil, err
	}
	if _, err := s.Store.SetPassword(ctx, crab, hash); err != nil {
		return nil, err
	}
	return crab, nil
}
