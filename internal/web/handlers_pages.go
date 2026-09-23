package web

import (
	"errors"
	"io/fs"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/t0ul/krabber-net/internal/store"
	"github.com/t0ul/krabber-net/ui"
)

// fragmentPage is any page's template set; every set includes the partials.
const fragmentPage = "trench.html"

func (app *App) home(w http.ResponseWriter, r *http.Request) {
	if currentCrab(r) != nil {
		http.Redirect(w, r, "/trench", http.StatusSeeOther)
		return
	}
	app.render(w, r, http.StatusOK, "welcome.html", app.newTemplateData(r))
}

// renderFeed renders a page whose main content is a list of molts.
func (app *App) renderFeed(w http.ResponseWriter, r *http.Request, page string, molts []store.Molt, err error, empty string) {
	if err == nil {
		molts, err = app.withLikes(r, molts)
	}
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	data := app.newTemplateData(r)
	data.Molts = molts
	data.EmptyMessage = empty
	app.render(w, r, http.StatusOK, page, data)
}

func (app *App) sea(w http.ResponseWriter, r *http.Request) {
	molts, err := app.store.Sea(r.Context(), pageSize)
	app.renderFeed(w, r, "sea.html", molts, err, "The sea is calm. Nobody has molted this week.")
}

func (app *App) trench(w http.ResponseWriter, r *http.Request) {
	molts, err := app.store.Trench(r.Context(), currentCrab(r).ID, pageSize)
	app.renderFeed(w, r, "trench.html", molts, err, "Your trench is empty. Molt something, or follow some crabs.")
}

func (app *App) moltinTime(w http.ResponseWriter, r *http.Request) {
	molts, err := app.store.MoltsByOwner(r.Context(), currentCrab(r).ID, pageSize)
	app.renderFeed(w, r, "moltinTime.html", molts, err, "You haven't molted anything yet.")
}

// notifications lists the crab's notifications and clears the unread badge.
func (app *App) notifications(w http.ResponseWriter, r *http.Request) {
	c := currentCrab(r)
	notes, err := app.store.Notifications(r.Context(), c.ID, 50)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	if err := app.store.MarkNotificationsRead(r.Context(), c.ID); err != nil {
		app.log.Warn("mark notifications read", "err", err)
	}
	data := app.newTemplateData(r)
	data.Unread = 0
	data.Notifications = notes
	app.render(w, r, http.StatusOK, "notifications.html", data)
}

// notificationBadge is polled by the nav so the unread count stays current.
func (app *App) notificationBadge(w http.ResponseWriter, r *http.Request) {
	n, err := app.store.UnreadNotifications(r.Context(), currentCrab(r).ID)
	if err != nil {
		app.log.Warn("unread notifications", "err", err)
	}
	app.renderTemplate(w, r, http.StatusOK, fragmentPage, "notification-badge", n)
}

// profile shows a crab's page: counts, follow button and their molts.
func (app *App) profile(w http.ResponseWriter, r *http.Request) {
	p, ok := app.crabFromName(w, r)
	if !ok {
		return
	}
	molts, err := app.store.MoltsByOwner(r.Context(), p.ID, pageSize)
	if err == nil {
		molts, err = app.withLikes(r, molts)
	}
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	data := app.newTemplateData(r)
	data.Profile = p
	data.Molts = molts
	data.EmptyMessage = "@" + p.UserName + " hasn't molted yet."
	if c := currentCrab(r); c != nil && c.ID != p.ID {
		if data.IsFollowing, err = app.store.IsFollowing(r.Context(), c.ID, p.ID); err != nil {
			app.serverError(w, r, err)
			return
		}
	}
	app.render(w, r, http.StatusOK, "profile.html", data)
}

func (app *App) followersList(w http.ResponseWriter, r *http.Request) {
	app.followList(w, r, true)
}

func (app *App) followingList(w http.ResponseWriter, r *http.Request) {
	app.followList(w, r, false)
}

func (app *App) followList(w http.ResponseWriter, r *http.Request, followers bool) {
	p, ok := app.crabFromName(w, r)
	if !ok {
		return
	}
	var rows []store.Follow
	var err error
	if followers {
		rows, err = app.store.Followers(r.Context(), p.ID, 200)
	} else {
		rows, err = app.store.Following(r.Context(), p.ID, 200)
	}
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	_, byID, _ := app.snapshot(r)
	followed := app.following(r)
	data := app.newTemplateData(r)
	for _, f := range rows {
		id, name := f.FolloweeID, f.FolloweeName
		if followers {
			id, name = f.FollowerID, f.FollowerName
		}
		c, ok := byID[id]
		if !ok {
			c = store.Crab{ID: id, UserName: name}
		}
		data.CrabRows = append(data.CrabRows, crabRow{Crab: c, Following: followed[id]})
	}
	data.ListBack = "/crabs/" + p.UserName
	if followers {
		data.ListTitle = "Crabs following @" + p.UserName
		data.EmptyMessage = "No followers yet."
	} else {
		data.ListTitle = "Crabs @" + p.UserName + " follows"
		data.EmptyMessage = "Not following anyone yet."
	}
	app.render(w, r, http.StatusOK, "crabs.html", data)
}

// allCrabs lists every crab, most-followed first, with follow buttons.
func (app *App) allCrabs(w http.ResponseWriter, r *http.Request) {
	crabs, _, _ := app.snapshot(r)
	followed := app.following(r)
	me := currentCrab(r).ID
	data := app.newTemplateData(r)
	for _, c := range crabs {
		if c.ID != me {
			data.CrabRows = append(data.CrabRows, crabRow{Crab: c, Following: followed[c.ID]})
		}
	}
	sort.SliceStable(data.CrabRows, func(i, j int) bool {
		return data.CrabRows[i].Crab.FollowerCount > data.CrabRows[j].Crab.FollowerCount
	})
	data.ListTitle = "All crabs"
	data.EmptyMessage = "No other crabs yet. Invite a friend!"
	app.render(w, r, http.StatusOK, "crabs.html", data)
}

func (app *App) searchPage(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	data := app.newTemplateData(r)
	data.Query = q
	data.EmptyMessage = "Try a name or a few words from a molt."
	if n := utf8.RuneCountInString(q); n >= 2 && n <= 50 {
		rows, molts := app.search(r, q)
		var err error
		molts, err = app.withLikes(r, molts)
		if err != nil {
			app.serverError(w, r, err)
			return
		}
		data.CrabRows = rows
		data.Molts = molts
		data.EmptyMessage = "No molts match “" + q + "”."
	}
	app.render(w, r, http.StatusOK, "results.html", data)
}

func (app *App) followPost(w http.ResponseWriter, r *http.Request) {
	app.setFollow(w, r, true)
}

func (app *App) unfollowPost(w http.ResponseWriter, r *http.Request) {
	app.setFollow(w, r, false)
}

// setFollow follows or unfollows, then returns the updated button.
func (app *App) setFollow(w http.ResponseWriter, r *http.Request, follow bool) {
	followee, ok := app.crabFromPath(w, r)
	if !ok {
		return
	}
	var err error
	if follow {
		err = app.store.Follow(r.Context(), currentCrab(r), followee)
		if err == nil {
			app.notify(r, followee.ID, store.NotifyFollow, "", "")
		}
	} else {
		err = app.store.Unfollow(r.Context(), currentCrab(r), followee)
	}
	if err != nil && !errors.Is(err, store.ErrAlreadyExists) && !errors.Is(err, store.ErrNotAllowed) && !errors.Is(err, store.ErrNotFound) {
		app.serverError(w, r, err)
		return
	}
	app.dir.invalidate()
	if !isHTMX(r) {
		http.Redirect(w, r, "/crabs/"+followee.UserName, http.StatusSeeOther)
		return
	}
	data := app.newTemplateData(r)
	app.renderTemplate(w, r, http.StatusOK, fragmentPage, "follow-button",
		map[string]any{"C": followee, "D": data, "Following": follow})
}

func (app *App) crabFromPath(w http.ResponseWriter, r *http.Request) (*store.Crab, bool) {
	c, err := app.store.CrabByID(r.Context(), r.PathValue("id"))
	return app.checkCrab(w, r, c, err)
}

func (app *App) crabFromName(w http.ResponseWriter, r *http.Request) (*store.Crab, bool) {
	c, err := app.store.CrabByUsername(r.Context(), r.PathValue("name"))
	return app.checkCrab(w, r, c, err)
}

func (app *App) checkCrab(w http.ResponseWriter, r *http.Request, c *store.Crab, err error) (*store.Crab, bool) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		app.notFound(w)
		return nil, false
	case err != nil:
		app.serverError(w, r, err)
		return nil, false
	case !c.CanSignIn():
		app.notFound(w)
		return nil, false
	}
	return c, true
}

func (app *App) crabmin(w http.ResponseWriter, r *http.Request) {
	app.render(w, r, http.StatusOK, "crabmin.html", app.newTemplateData(r))
}

func (app *App) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

// staticFiles serves embedded assets. CloudFront caches them for a day and the
// deploy pipeline invalidates /static/* after each release; pages link them
// with ?v=<content hash> so browsers pick up new versions too.
func staticFiles() http.Handler {
	sub, err := fs.Sub(ui.Files, "static")
	if err != nil {
		panic(err)
	}
	files := http.StripPrefix("/static/", http.FileServerFS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=86400")
		files.ServeHTTP(w, r)
	})
}
