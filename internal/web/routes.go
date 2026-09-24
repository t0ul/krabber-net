package web

import (
	"net/http"
	"strings"
)

// Routes returns the full handler. Order, outermost first:
//
//	recoverPanic → originVerify → withClientIP → logRequests → securityHeaders
//	  /static/*, /healthz, /robots.txt, /favicon.ico, old /crab… URLs: served directly (no session)
//	  everything else: sessions → crossOriginProtection → csrf → authenticate → mux
func (app *App) Routes() http.Handler {
	mux := http.NewServeMux()

	// Public pages.
	mux.HandleFunc("GET /{$}", app.home)
	mux.HandleFunc("GET /sea", app.sea)
	mux.HandleFunc("GET /sea/new", app.seaNew)
	mux.HandleFunc("GET /molt/view/{id}", app.moltView)
	mux.HandleFunc("GET /molt/likes/view/{id}", app.moltLikesView)
	mux.HandleFunc("GET /molt/view/{id}/quotes", app.moltQuotesView)
	mux.HandleFunc("GET /krabtag/{tag}", app.crabtagPage)
	mux.HandleFunc("GET /stats", app.statsPage)
	mux.HandleFunc("GET /terms", app.termsPage)
	mux.HandleFunc("GET /privacy", app.privacyPage)
	mux.HandleFunc("/", app.notFound)

	// Accounts.
	mux.HandleFunc("GET /krab/signup", app.signupPage)
	mux.HandleFunc("POST /krab/signup", app.signupPost)
	mux.HandleFunc("GET /krab/activate", app.activatePage)
	mux.HandleFunc("POST /krab/activate", app.activatePost)
	mux.HandleFunc("POST /krab/activate/resend", app.resendPost)
	mux.HandleFunc("GET /krab/forgot", app.forgotPage)
	mux.HandleFunc("POST /krab/forgot", app.forgotPost)
	mux.HandleFunc("GET /krab/reset", app.resetPage)
	mux.HandleFunc("POST /krab/reset", app.resetPost)
	mux.HandleFunc("GET /krab/login", app.loginPage)
	mux.HandleFunc("POST /krab/login", app.loginPost)
	mux.HandleFunc("POST /krab/logout", app.requireAuthentication(app.logoutPost))

	// Signed-in pages and actions.
	mux.HandleFunc("GET /trench", app.requireAuthentication(app.trench))
	mux.HandleFunc("GET /trench/new", app.requireAuthentication(app.trenchNew))
	mux.HandleFunc("GET /notifications", app.requireAuthentication(app.notifications))
	mux.HandleFunc("GET /notifications/badge", app.requireAuthentication(app.notificationBadge))
	mux.HandleFunc("GET /bookmarks", app.requireAuthentication(app.bookmarksPage))
	mux.HandleFunc("POST /molt/bookmark/{id}", app.requireAuthentication(app.bookmarkPost))
	mux.HandleFunc("GET /molt/edit/{id}", app.requireAuthentication(app.editPage))
	mux.HandleFunc("POST /molt/edit/{id}", app.requireAuthentication(app.editPost))
	mux.HandleFunc("POST /molt/pin/{id}", app.requireAuthentication(app.pinPost))
	mux.HandleFunc("POST /molt/unpin/{id}", app.requireAuthentication(app.unpinPost))
	mux.HandleFunc("GET /settings", app.requireAuthentication(app.settings))
	mux.HandleFunc("POST /block/{id}", app.requireAuthentication(app.blockPost))
	mux.HandleFunc("POST /unblock/{id}", app.requireAuthentication(app.unblockPost))
	mux.HandleFunc("POST /settings/profile", app.requireAuthentication(app.settingsProfilePost))
	mux.HandleFunc("POST /settings/avatar", app.requireAuthentication(app.settingsAvatarPost))
	mux.HandleFunc("POST /settings/content", app.requireAuthentication(app.settingsContentPost))
	mux.HandleFunc("POST /settings/username", app.requireAuthentication(app.settingsUsernamePost))
	mux.HandleFunc("POST /settings/password", app.requireAuthentication(app.settingsPasswordPost))
	mux.HandleFunc("POST /settings/delete", app.requireAuthentication(app.settingsDeletePost))
	mux.HandleFunc("GET /krabs", app.requireAuthentication(app.allCrabs))
	mux.HandleFunc("GET /krabs/{name}", app.profile)
	mux.HandleFunc("GET /krabs/{name}/replies", app.profileReplies)
	mux.HandleFunc("GET /krabs/{name}/likes", app.profileLikes)
	mux.HandleFunc("GET /krabs/{name}/followers", app.followersList)
	mux.HandleFunc("GET /krabs/{name}/following", app.followingList)
	mux.HandleFunc("GET /search", app.searchPage)
	mux.HandleFunc("POST /molt/create", app.requireAuthentication(app.moltCreatePost))
	mux.HandleFunc("POST /molt/like/{id}", app.requireAuthentication(app.moltLikePost))
	mux.HandleFunc("POST /molt/delete/{id}", app.requireAuthentication(app.moltDeletePost))
	mux.HandleFunc("POST /molt/nsfw/{id}", app.requireAuthentication(app.moltNSFWPost))
	mux.HandleFunc("GET /molt/report/{id}", app.requireAuthentication(app.reportPage))
	mux.HandleFunc("POST /molt/report/{id}", app.requireAuthentication(app.reportPost))
	mux.HandleFunc("POST /remolt/{id}", app.requireAuthentication(app.remoltPost))
	mux.HandleFunc("POST /unremolt/{id}", app.requireAuthentication(app.unremoltPost))
	mux.HandleFunc("GET /molt/quote/{id}", app.requireAuthentication(app.quotePage))
	mux.HandleFunc("POST /molt/quote/{id}", app.requireAuthentication(app.quoteCreatePost))
	mux.HandleFunc("POST /molt/reply/{id}", app.requireAuthentication(app.replyCreatePost))
	mux.HandleFunc("POST /follow/{id}", app.requireAuthentication(app.followPost))
	mux.HandleFunc("POST /unfollow/{id}", app.requireAuthentication(app.unfollowPost))

	// Admin.
	mux.HandleFunc("GET /krabmin", app.requireModerator(app.crabmin))
	mux.HandleFunc("GET /krabmin/log", app.requireModerator(app.crabminLog))
	mux.HandleFunc("GET /krabmin/reports", app.requireModerator(app.crabminReports))
	mux.HandleFunc("GET /krabmin/krabs/{name}", app.requireModerator(app.crabminCrab))
	mux.HandleFunc("POST /krabmin/krabs/{id}", app.requireModerator(app.crabminCrabPost))
	mux.HandleFunc("GET /krabmin/molts/{id}", app.requireModerator(app.crabminMolt))
	mux.HandleFunc("POST /krabmin/molts/{id}", app.requireModerator(app.crabminMoltPost))

	dynamic := app.sessions.LoadAndSave(app.crossOriginProtection(app.csrf(app.authenticate(mux))))

	root := http.NewServeMux()
	root.Handle("GET /static/", staticFiles())
	root.HandleFunc("GET /avatar/{code}", app.avatarSVG)
	root.HandleFunc("GET /banner/{code}", app.bannerSVG)
	root.HandleFunc("GET /healthz", app.healthz)
	root.HandleFunc("GET /robots.txt", robots)
	root.HandleFunc("GET /favicon.ico", favicon)
	for old := range legacyRedirects {
		root.HandleFunc(old, redirectLegacy)
		root.HandleFunc(old+"/", redirectLegacy)
	}
	root.Handle("/", dynamic)

	return app.recoverPanic(app.originVerify(app.withClientIP(app.logRequests(app.securityHeaders(root)))))
}

// legacyRedirects maps the URL prefixes used before the krab rename to
// today's, so old links, bookmarks and emails keep working.
var legacyRedirects = map[string]string{
	"/crabs":   "/krabs",
	"/crab":    "/krab",
	"/crabtag": "/krabtag",
	"/crabmin": "/krabmin",
}

// redirectLegacy sends an old URL to its new prefix, keeping the rest of the
// path and the query. 308 keeps the method, so a form posted from a page
// loaded before the rename still goes through.
func redirectLegacy(w http.ResponseWriter, r *http.Request) {
	first, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	to, ok := legacyRedirects["/"+first]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if after, ok := strings.CutPrefix(rest, "crabs/"); ok && first == "crabmin" {
		rest = "krabs/" + after
	}
	target := to
	if rest != "" || strings.HasSuffix(r.URL.Path, "/") {
		target += "/" + rest
	}
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusPermanentRedirect) //nolint:gosec // target always starts with one of our own /krab… prefixes, so it stays on this site
}
