// Package web is the Krabber website: routes, handlers, templates and the
// security middleware in front of them.
package web

import (
	"cmp"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/go-playground/form/v4"

	"github.com/t0ul/krabber-net/internal/config"
	"github.com/t0ul/krabber-net/internal/mail"
	"github.com/t0ul/krabber-net/internal/store"
)

// FanoutQueue hands new molts to the background fan-out worker.
type FanoutQueue interface {
	Enqueue(m *store.Molt)
}

// Notifier hands notifications to the background writer.
type Notifier interface {
	Notify(n store.Notification)
}

// CardQueue asks the background worker for a link card.
type CardQueue interface {
	EnqueueCard(url string)
}

// App holds the website's dependencies.
type App struct {
	cfg       *config.Config
	log       *slog.Logger
	store     *store.Store
	mailer    *mail.Mailer
	sessions  *scs.SessionManager
	templates map[string]*template.Template
	forms     *form.Decoder
	fanout    FanoutQueue
	notifier  Notifier
	cards     CardQueue
	turnstile *turnstile
	dir       *directory
	fof       *suggestions
	writes    *writeLimiter
	// trophiesHeld skips award attempts for trophies a krab already has.
	trophiesHeld *heldTrophies
	strangersSea *seaForStrangers

	version string    // the build's commit, for @system's molts
	started time.Time // for @system's uptime
}

// Deps are the collaborators App needs.
type Deps struct {
	Config   *config.Config
	Log      *slog.Logger
	Store    *store.Store
	Mailer   *mail.Mailer
	Fanout   FanoutQueue
	Notifier Notifier
	Cards    CardQueue
	Version  string // the build's commit; "dev" when unset
}

// New builds the App, parsing templates and configuring sessions.
func New(d Deps) (*App, error) {
	templates, err := newTemplateCache()
	if err != nil {
		return nil, err
	}

	sm := scs.New()
	sm.Store = d.Store.Sessions()
	sm.Lifetime = 12 * time.Hour
	sm.Cookie.Name = "__Host-krabber_session"
	sm.Cookie.Path = "/"
	sm.Cookie.HttpOnly = true
	sm.Cookie.Secure = true
	sm.Cookie.SameSite = http.SameSiteLaxMode
	sm.Cookie.Persist = true
	if d.Config.IsDev() {
		// localhost is plain HTTP; __Host- cookies require Secure.
		sm.Cookie.Name = "krabber_session"
		sm.Cookie.Secure = false
	}
	sm.ErrorFunc = func(w http.ResponseWriter, r *http.Request, err error) {
		d.Log.Error("session error", "err", err, "path", r.URL.Path)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	}

	return &App{
		cfg:       d.Config,
		log:       d.Log,
		store:     d.Store,
		mailer:    d.Mailer,
		sessions:  sm,
		templates: templates,
		forms:     form.NewDecoder(),
		fanout:    d.Fanout,
		notifier:  d.Notifier,
		cards:     d.Cards,
		turnstile: newTurnstile(d.Config.TurnstileSecret),
		dir:       &directory{},
		fof:       &suggestions{},
		writes:    &writeLimiter{},

		trophiesHeld: &heldTrophies{},
		strangersSea: &seaForStrangers{},
		version:      cmp.Or(d.Version, "dev"),
		started:      time.Now(),
	}, nil
}
