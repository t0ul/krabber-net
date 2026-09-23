package web

import "net/http"

// Routes returns the full handler. Order, outermost first:
//
//	recoverPanic → originVerify → withClientIP → logRequests → securityHeaders
//	  /static/*, /healthz: served directly (no session)
//	  everything else: sessions → crossOriginProtection → csrf → authenticate → mux
func (app *App) Routes() http.Handler {
	mux := http.NewServeMux()

	// Public pages.
	mux.HandleFunc("GET /{$}", app.home)
	mux.HandleFunc("GET /sea", app.sea)
	mux.HandleFunc("GET /molt/view/{id}", app.moltView)
	mux.HandleFunc("GET /molt/likes/view/{id}", app.moltLikesView)

	// Accounts.
	mux.HandleFunc("GET /crab/signup", app.signupPage)
	mux.HandleFunc("POST /crab/signup", app.signupPost)
	mux.HandleFunc("GET /crab/activate", app.activatePage)
	mux.HandleFunc("POST /crab/activate", app.activatePost)
	mux.HandleFunc("POST /crab/activate/resend", app.resendPost)
	mux.HandleFunc("GET /crab/forgot", app.forgotPage)
	mux.HandleFunc("POST /crab/forgot", app.forgotPost)
	mux.HandleFunc("GET /crab/reset", app.resetPage)
	mux.HandleFunc("POST /crab/reset", app.resetPost)
	mux.HandleFunc("GET /crab/login", app.loginPage)
	mux.HandleFunc("POST /crab/login", app.loginPost)
	mux.HandleFunc("POST /crab/logout", app.requireAuthentication(app.logoutPost))

	// Signed-in pages and actions.
	mux.HandleFunc("GET /trench", app.requireAuthentication(app.trench))
	mux.HandleFunc("GET /moltinTime", app.requireAuthentication(app.moltinTime))
	mux.HandleFunc("GET /notifications", app.requireAuthentication(app.notifications))
	mux.HandleFunc("GET /notifications/badge", app.requireAuthentication(app.notificationBadge))
	mux.HandleFunc("GET /settings", app.requireAuthentication(app.settings))
	mux.HandleFunc("POST /block/{id}", app.requireAuthentication(app.blockPost))
	mux.HandleFunc("POST /unblock/{id}", app.requireAuthentication(app.unblockPost))
	mux.HandleFunc("POST /settings/profile", app.requireAuthentication(app.settingsProfilePost))
	mux.HandleFunc("POST /settings/password", app.requireAuthentication(app.settingsPasswordPost))
	mux.HandleFunc("POST /settings/delete", app.requireAuthentication(app.settingsDeletePost))
	mux.HandleFunc("GET /crabs", app.requireAuthentication(app.allCrabs))
	mux.HandleFunc("GET /crabs/{name}", app.profile)
	mux.HandleFunc("GET /crabs/{name}/followers", app.followersList)
	mux.HandleFunc("GET /crabs/{name}/following", app.followingList)
	mux.HandleFunc("GET /search", app.searchPage)
	mux.HandleFunc("POST /molt/create", app.requireAuthentication(app.moltCreatePost))
	mux.HandleFunc("POST /molt/like/{id}", app.requireAuthentication(app.moltLikePost))
	mux.HandleFunc("POST /molt/delete/{id}", app.requireAuthentication(app.moltDeletePost))
	mux.HandleFunc("GET /molt/report/{id}", app.requireAuthentication(app.reportPage))
	mux.HandleFunc("POST /molt/report/{id}", app.requireAuthentication(app.reportPost))
	mux.HandleFunc("POST /remolt/{id}", app.requireAuthentication(app.remoltPost))
	mux.HandleFunc("POST /comment/{id}", app.requireAuthentication(app.commentCreatePost))
	mux.HandleFunc("POST /follow/{id}", app.requireAuthentication(app.followPost))
	mux.HandleFunc("POST /unfollow/{id}", app.requireAuthentication(app.unfollowPost))

	// Admin.
	mux.HandleFunc("GET /crabmin", app.requireModerator(app.crabmin))
	mux.HandleFunc("GET /crabmin/log", app.requireModerator(app.crabminLog))
	mux.HandleFunc("GET /crabmin/reports", app.requireModerator(app.crabminReports))
	mux.HandleFunc("GET /crabmin/crabs/{name}", app.requireModerator(app.crabminCrab))
	mux.HandleFunc("POST /crabmin/crabs/{id}", app.requireModerator(app.crabminCrabPost))
	mux.HandleFunc("GET /crabmin/molts/{id}", app.requireModerator(app.crabminMolt))
	mux.HandleFunc("POST /crabmin/molts/{id}", app.requireModerator(app.crabminMoltPost))

	dynamic := app.sessions.LoadAndSave(app.crossOriginProtection(app.csrf(app.authenticate(mux))))

	root := http.NewServeMux()
	root.Handle("GET /static/", staticFiles())
	root.HandleFunc("GET /healthz", app.healthz)
	root.Handle("/", dynamic)

	return app.recoverPanic(app.originVerify(app.withClientIP(app.logRequests(app.securityHeaders(root)))))
}
