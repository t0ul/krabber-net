package web

import (
	"net/http"
	"strings"
	"time"

	"github.com/t0ul/krabber-net/internal/avatar"
)

const avatarRerollLimit = 3

func (app *App) avatarSVG(w http.ResponseWriter, r *http.Request) {
	app.writeGeneratedSVG(w, r, avatar.SVG)
}

func (app *App) bannerSVG(w http.ResponseWriter, r *http.Request) {
	app.writeGeneratedSVG(w, r, avatar.Banner)
}

func (app *App) writeGeneratedSVG(w http.ResponseWriter, r *http.Request, render func(string) (string, bool)) {
	code := strings.TrimSuffix(r.PathValue("code"), ".svg")
	svg, ok := render(code)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write([]byte(svg))
}

func (app *App) settingsAvatarPost(w http.ResponseWriter, r *http.Request) {
	c := currentCrab(r)
	n, err := app.store.Hit(r.Context(), "avatar-reroll", c.ID, 24*time.Hour)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	if n > avatarRerollLimit {
		forms := settingsForms{
			Profile:     profileFormFor(c.Profile),
			AvatarError: "That's enough rerolls for today. Come back tomorrow.",
		}
		app.renderSettings(w, r, http.StatusTooManyRequests, forms)
		return
	}
	if _, err := app.store.RerollAvatar(r.Context(), c); err != nil {
		app.serverError(w, r, err)
		return
	}
	fresh, err := app.store.CrabByKey(r.Context(), c.PK, c.SK)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	app.dir.putCrab(*fresh)
	app.sessions.Put(r.Context(), sessionFlash, "Your krab has a new look.")
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}
