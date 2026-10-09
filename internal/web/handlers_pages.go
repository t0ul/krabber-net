package web

import (
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
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
func (app *App) renderFeed(w http.ResponseWriter, r *http.Request, page string, p store.Page, err error, empty string) {
	data := app.newTemplateData(r)
	if !app.setPage(w, r, &data, p, err) {
		return
	}
	app.finishFeed(w, r, page, data, empty)
}

// finishFeed renders a feed whose molts are ready. Only signed-in krabs get
// the "new molts" poller.
func (app *App) finishFeed(w http.ResponseWriter, r *http.Request, page string, data templateData, empty string) {
	data.EmptyMessage = empty
	if path := r.URL.Path; (path == "/sea" || path == "/trench") && data.IsAuthenticated {
		data.FeedPath = path
		if len(data.Molts) > 0 {
			data.Since = data.Molts[0].FeedID()
		}
	}
	app.renderMolts(w, r, page, data)
}

// seaForStrangers is the signed-out Sea, which is the same for every
// visitor: its first page is kept for a minute, so crawlers and passers-by
// cost nothing after the first view.
type seaForStrangers struct {
	mu    sync.Mutex
	at    time.Time
	molts []store.Molt
	more  bool
}

const seaForStrangersTTL = time.Minute

func (c *seaForStrangers) get() ([]store.Molt, bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.molts == nil || time.Since(c.at) > seaForStrangersTTL {
		return nil, false, false
	}
	return c.molts, c.more, true
}

func (c *seaForStrangers) put(molts []store.Molt, more bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.molts, c.more, c.at = slices.Clone(molts), more, time.Now()
	if c.molts == nil {
		c.molts = []store.Molt{}
	}
}

const newMoltsCap = 99

func (app *App) seaNew(w http.ResponseWriter, r *http.Request) {
	app.renderNewMolts(w, r, "/sea", func(since string) (int, error) {
		return app.store.SeaNewer(r.Context(), since, newMoltsCap)
	})
}

func (app *App) trenchNew(w http.ResponseWriter, r *http.Request) {
	app.renderNewMolts(w, r, "/trench", func(since string) (int, error) {
		return app.store.TrenchNewer(r.Context(), currentCrab(r).ID, since, newMoltsCap)
	})
}

func (app *App) renderNewMolts(w http.ResponseWriter, r *http.Request, feed string, count func(string) (int, error)) {
	since := r.URL.Query().Get("since")
	n, err := count(since)
	if err != nil {
		app.log.Warn("new molts", "err", err)
	}
	data := app.newTemplateData(r)
	data.FeedPath = feed
	data.Since = since
	data.NewCount = n
	if data.IsAuthenticated {
		data.Unread = app.unread(r)
	}
	app.renderTemplate(w, r, http.StatusOK, fragmentPage, "new-molts-poll", data)
}

const seaEmpty = "The sea is calm. Nobody has molted this week."

func (app *App) sea(w http.ResponseWriter, r *http.Request) {
	if currentCrab(r) != nil {
		p, err := app.store.SeaPage(r.Context(), afterParam(r), pageSize)
		app.renderFeed(w, r, "sea.html", p, err, seaEmpty)
		return
	}
	// Signed out: always the first page (firstPageSignedOut).
	data := app.newTemplateData(r)
	if molts, more, ok := app.strangersSea.get(); ok {
		data.Molts, data.MoreForMembers = molts, more
	} else {
		p, err := app.store.SeaPage(r.Context(), "", pageSize)
		if !app.setPage(w, r, &data, p, err) {
			return
		}
		app.strangersSea.put(data.Molts, data.MoreForMembers)
	}
	app.finishFeed(w, r, "sea.html", data, seaEmpty)
}

func (app *App) trench(w http.ResponseWriter, r *http.Request) {
	c := currentCrab(r)
	p, err := app.store.TrenchPage(r.Context(), c.ID, afterParam(r), pageSize)
	data := app.newTemplateData(r)
	if !app.setPage(w, r, &data, p, err) {
		return
	}
	// Someone who follows nobody yet gets a few krabs to start with.
	if c.FollowingCount == 0 && afterParam(r) == "" {
		data.CrabRows = app.starterKrabs(r, onboardingPicks)
	}
	app.finishFeed(w, r, "trench.html", data, "Your trench is empty. Molt something, or follow some krabs.")
}

const onboardingPicks = 6

// starterKrabs are the most followed krabs, from the directory (free),
// leaving out the viewer, @system and anyone hidden from them.
func (app *App) starterKrabs(r *http.Request, n int) []crabRow {
	me := currentCrab(r).ID
	hidden := app.hidden(r)
	var rows []crabRow
	for _, c := range app.dirData(r).crabs {
		if len(rows) == n {
			break
		}
		if c.ID != me && c.UserName != systemName && !hidden(c.ID) {
			rows = append(rows, crabRow{Crab: c})
		}
	}
	return rows
}

// notifications lists the crab's notifications and clears the unread badge.
func (app *App) notifications(w http.ResponseWriter, r *http.Request) {
	c := currentCrab(r)
	notes, err := app.store.Notifications(r.Context(), c.ID, 50)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	if app.unread(r) > 0 { // a read costs a tenth of a write
		if err := app.store.MarkNotificationsRead(r.Context(), c.ID); err != nil {
			app.log.Warn("mark notifications read", "err", err)
		}
	}
	data := app.newTemplateData(r)
	data.Unread, data.unreadKnown = 0, true
	hidden := app.hidden(r)
	_, byID, _ := app.snapshot(r)
	for _, n := range notes {
		if hidden(n.ActorID) {
			continue
		}
		if a, ok := byID[n.ActorID]; ok {
			n.Actor, n.ActorAvatar, n.ActorVerified = a.UserName, a.Avatar, a.Verified
		}
		data.Notifications = append(data.Notifications, n)
	}
	app.render(w, r, http.StatusOK, "notifications.html", data)
}

// notificationBadge is polled by the nav (on pages without a feed poll) so
// the unread count stays current.
func (app *App) notificationBadge(w http.ResponseWriter, r *http.Request) {
	app.renderTemplate(w, r, http.StatusOK, fragmentPage, "notification-badge",
		map[string]any{"N": app.unread(r), "Poll": true})
}

// Profile tabs.
const (
	tabMolts   = "molts"
	tabReplies = "replies"
	tabLikes   = "likes"
)

func (app *App) profile(w http.ResponseWriter, r *http.Request) { app.profileTab(w, r, tabMolts) }
func (app *App) profileReplies(w http.ResponseWriter, r *http.Request) {
	app.profileTab(w, r, tabReplies)
}
func (app *App) profileLikes(w http.ResponseWriter, r *http.Request) { app.profileTab(w, r, tabLikes) }

// profileTab shows a crab's page: counts, follow button, and one tab of
// their molts, replies, likes or trophies.

// profileTab shows a crab's page: counts, follow button, and one tab of
// their molts, replies or likes.
func (app *App) profileTab(w http.ResponseWriter, r *http.Request, tab string) {
	p, ok := app.crabFromName(w, r)
	if !ok {
		return
	}
	data := app.newTemplateData(r)
	data.Profile = p
	data.Tab = tab
	if _, blocking := blocksOf(r).Blocking[p.ID]; blocking {
		data.IsBlocking = true
		data.EmptyMessage = "You blocked @" + p.UserName + ". Unblock them to see their molts."
		app.render(w, r, http.StatusOK, "profile.html", data)
		return
	}
	if tab == tabTrophies {
		var err error
		if data.TrophyCase, err = app.trophyCaseOf(r.Context(), p.ID); err != nil {
			app.serverError(w, r, err)
			return
		}
		if !app.setFollowsState(w, r, &data, p) {
			return
		}
		app.render(w, r, http.StatusOK, "profile.html", data)
		return
	}
	var page store.Page
	var err error
	switch tab {
	case tabReplies:
		data.EmptyMessage = "@" + p.UserName + " hasn't replied to anyone yet."
		page, err = app.store.RepliesByOwnerPage(r.Context(), p.ID, afterParam(r), pageSize)
	case tabLikes:
		data.EmptyMessage = "@" + p.UserName + " hasn't liked any molts yet."
		page, err = app.store.LikedMoltsPage(r.Context(), p.ID, afterParam(r), pageSize)
	default:
		data.EmptyMessage = "@" + p.UserName + " hasn't molted yet."
		page, err = app.store.MoltsByOwnerPage(r.Context(), p.ID, afterParam(r), pageSize)
	}
	// The pin rides along in the same display pass, then comes off the front.
	// Later pages skip it so "Load more" doesn't repeat the pin.
	var pinned *store.Molt
	if err == nil && tab == tabMolts && afterParam(r) == "" {
		if pinned, err = app.store.PinnedMolt(r.Context(), p); pinned != nil {
			page.Molts = append([]store.Molt{*pinned}, page.Molts...)
		}
	}
	if !app.setPage(w, r, &data, page, err) {
		return
	}
	if pinned != nil && len(data.Molts) > 0 && data.Molts[0].ID == pinned.ID {
		data.Pinned = &data.Molts[0]
		data.Molts = data.Molts[1:]
	}
	if !app.setFollowsState(w, r, &data, p) {
		return
	}
	app.renderMolts(w, r, "profile.html", data)
}

// setFollowsState fills in whether the viewer and p follow each other.
func (app *App) setFollowsState(w http.ResponseWriter, r *http.Request, data *templateData, p *store.Crab) bool {
	c := currentCrab(r)
	if c == nil || c.ID == p.ID {
		return true
	}
	var err error
	if data.IsFollowing, err = app.store.IsFollowing(r.Context(), c.ID, p.ID); err != nil {
		app.serverError(w, r, err)
		return false
	}
	if data.FollowsYou, err = app.store.IsFollowing(r.Context(), p.ID, c.ID); err != nil {
		app.serverError(w, r, err)
		return false
	}
	return true
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
	data.CrabRows = app.visibleCrabs(r, data.CrabRows)
	data.ListBack = "/krabs/" + p.UserName
	if followers {
		data.ListTitle = "Krabs following @" + p.UserName
		data.EmptyMessage = "No followers yet."
	} else {
		data.ListTitle = "Krabs @" + p.UserName + " follows"
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
	data.CrabRows = app.visibleCrabs(r, data.CrabRows)
	sort.SliceStable(data.CrabRows, func(i, j int) bool {
		return data.CrabRows[i].Crab.FollowerCount > data.CrabRows[j].Crab.FollowerCount
	})
	data.ListTitle = "All krabs"
	data.EmptyMessage = "No other krabs yet. Invite a friend!"
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
		data.CrabRows = app.visibleCrabs(r, rows)
		data.Molts = withoutMuted(r, molts)
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

// backfillMolts is how many of a newly followed krab's molts go straight
// into the follower's Trench (one batch write).
const backfillMolts = 10

// backfill puts followee's latest molts in the viewer's Trench, which
// otherwise only gets what they post from now on. It's a bonus, so a
// failure is only logged.
func (app *App) backfill(r *http.Request, followee *store.Crab) {
	molts, err := app.store.MoltsByOwner(r.Context(), followee.ID, backfillMolts)
	if err == nil {
		err = app.store.BackfillTrench(r.Context(), currentCrab(r).ID, molts)
	}
	if err != nil {
		app.log.Warn("trench backfill", "err", err, "followee", followee.ID)
	}
}

// setFollow follows or unfollows, then returns the updated button.
func (app *App) setFollow(w http.ResponseWriter, r *http.Request, follow bool) {
	followee, ok := app.crabFromPath(w, r)
	if !ok || !app.underWriteLimit(w, r, "follow") {
		return
	}
	var err error
	if follow {
		err = app.store.Follow(r.Context(), currentCrab(r), followee)
		if err == nil {
			app.notify(r, followee.ID, store.NotifyFollow, "", "")
			app.awardFollow(r.Context(), currentCrab(r), followee, true)
			app.backfill(r, followee)
		}
		if errors.Is(err, store.ErrBlocked) {
			follow = false
		}
	} else {
		err = app.store.Unfollow(r.Context(), currentCrab(r), followee)
		if err == nil {
			app.awardFollow(r.Context(), currentCrab(r), followee, false)
		}
	}
	if err != nil && !errors.Is(err, store.ErrAlreadyExists) && !errors.Is(err, store.ErrNotAllowed) &&
		!errors.Is(err, store.ErrNotFound) && !errors.Is(err, store.ErrBlocked) {
		app.serverError(w, r, err)
		return
	}
	app.fof.forget(currentCrab(r).ID)
	if !isHTMX(r) {
		http.Redirect(w, r, "/krabs/"+followee.UserName, http.StatusSeeOther)
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

// crabFromName loads the crab named in the URL; crabs who blocked the viewer
// don't exist as far as the viewer can tell.
func (app *App) crabFromName(w http.ResponseWriter, r *http.Request) (*store.Crab, bool) {
	name := r.PathValue("name")
	c, err := app.store.CrabByUsername(r.Context(), name)
	if err == nil && blocksOf(r).BlockedBy[c.ID] {
		err = store.ErrNotFound
	}
	c, ok := app.checkCrab(w, r, c, err)
	if ok && c.UserName != name && r.Method == http.MethodGet {
		// An old name (held after a rename) or other capitalization goes to
		// the crab's current address.
		rest := strings.TrimPrefix(r.URL.Path, "/krabs/"+name)
		target := "/krabs/" + url.PathEscape(c.UserName) + rest
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusMovedPermanently) //nolint:gosec // target always starts with /krabs/, so it stays on this site
		return nil, false
	}
	return c, ok
}

func (app *App) checkCrab(w http.ResponseWriter, r *http.Request, c *store.Crab, err error) (*store.Crab, bool) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		app.notFound(w, r)
		return nil, false
	case err != nil:
		app.serverError(w, r, err)
		return nil, false
	case !c.CanSignIn():
		app.notFound(w, r)
		return nil, false
	}
	return c, true
}

func (app *App) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

func (app *App) termsPage(w http.ResponseWriter, r *http.Request) {
	data := app.newTemplateData(r)
	data.ContactEmail = app.cfg.ContactEmail
	app.render(w, r, http.StatusOK, "terms.html", data)
}

func (app *App) privacyPage(w http.ResponseWriter, r *http.Request) {
	data := app.newTemplateData(r)
	data.ContactEmail = app.cfg.ContactEmail
	app.render(w, r, http.StatusOK, "privacy.html", data)
}

// robotsTxt keeps crawlers to public profiles and molts; pages behind sign-in
// would only redirect them to the login form.
const robotsTxt = `User-agent: *
Disallow: /krab/
Disallow: /krabmin
Disallow: /settings
Disallow: /notifications
Disallow: /trench
Disallow: /search
Disallow: /molt/report/
Disallow: /molt/likes/

# AI training and LLM scrapers aren't welcome anywhere. Advisory only: the
# well-behaved ones honor this, the rest are caught by the WAF rate and
# anonymous-IP rules (infra/prod/edge.tf).
User-agent: GPTBot
User-agent: ChatGPT-User
User-agent: OAI-SearchBot
User-agent: ClaudeBot
User-agent: anthropic-ai
User-agent: Claude-Web
User-agent: CCBot
User-agent: Google-Extended
User-agent: PerplexityBot
User-agent: Bytespider
User-agent: Amazonbot
User-agent: Applebot-Extended
User-agent: Meta-ExternalAgent
User-agent: Diffbot
User-agent: Omgilibot
User-agent: YouBot
Disallow: /
`

// serveEmbedded serves one embedded file at a root path: the service worker
// (which must live at / to control the whole site) and the offline page it
// keeps. Both are revalidated on every load so an update takes effect.
func serveEmbedded(name, contentType string) http.HandlerFunc {
	body, err := fs.ReadFile(ui.Files, name)
	if err != nil {
		panic(err)
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(body)
	}
}

func robots(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write([]byte(robotsTxt))
}

// favicon answers browsers that ask for /favicon.ico without reading the
// page's <link rel="icon">.
func favicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.Redirect(w, r, asset("img/favicon.svg"), http.StatusMovedPermanently)
}

// staticFiles serves embedded assets. Pages link them with ?v=<content hash>
// (see asset), which CloudFront keeps in its cache key, so a release's new
// files get new URLs everywhere; the deploy pipeline also invalidates
// /static/* as a backstop.
func staticFiles() http.Handler {
	// Go doesn't know the web app manifest's type.
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
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
		// A ?v= URL changes whenever any static file does, so browsers can
		// keep it for good; every request CloudFront answers counts toward
		// the plan's allowance, cached or not.
		if r.URL.Query().Get("v") == assetVersion {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=86400")
		}
		files.ServeHTTP(w, r)
	})
}
