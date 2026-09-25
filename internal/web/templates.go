package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/t0ul/krabber-net/internal/avatar"
	"github.com/t0ul/krabber-net/internal/linkcard"
	"github.com/t0ul/krabber-net/internal/richtext"
	"github.com/t0ul/krabber-net/internal/store"
	"github.com/t0ul/krabber-net/internal/trophies"
	"github.com/t0ul/krabber-net/ui"
)

// templateData is everything a page template can use.
type templateData struct {
	CurrentYear     int
	Flash           string
	IsAuthenticated bool
	IsAdmin         bool
	IsModerator     bool
	CrabID          string
	CrabName        string
	Avatar          string // current crab's generated-crab code
	ShowNSFW        bool
	MutedWords      []string
	store.Appearance
	CSRFToken        string
	TurnstileSiteKey string
	SignupMode       string // config.SignupOpen, SignupInvite or SignupClosed
	SignupFull       bool   // MAX_KRABS reached
	Form             any
	Query            string
	CurrentPath      string
	Unread           int
	unreadKnown      bool // the handler set Unread; render doesn't read it again
	Sidebar          sidebar

	Notifications []store.Notification

	Molt         store.Molt
	Parents      []store.Molt // on a reply's thread page, oldest first
	ParentGone   bool         // the molt it replies to was deleted or is hidden
	Replies      []store.Molt
	Tab          string // the profile tab shown: molts, replies, likes or trophies
	TrophyCase   []trophyCase
	Molts        []store.Molt
	Likes        []store.Like
	EmptyMessage string
	LoadMore     string // ?after= cursor URL; empty when this is the last page
	FeedPath     string // /sea or /trench, for the new-molts poller
	Since        string // newest molt on the page; the poller asks for newer than this
	NewCount     int    // new molts since Since; 0 hides the banner

	Profile     *store.Crab
	Pinned      *store.Molt // shown above the profile's Molts tab
	IsFollowing bool
	FollowsYou  bool
	IsBlocking  bool
	Blocked     []store.Crab
	ModLog      []store.ModAction
	CanModerate bool
	// Krabmin's crab page: who invited them, and whether their code is off.
	InvitedBy      string
	InviteDisabled bool
	Reports        []store.Report
	Report         *store.Report
	ReportRows     []store.ReportRow
	Gone           map[string]bool // banned and deleted crabs, for Crabmin
	CrabRows       []crabRow
	ListTitle      string
	ListBack       string

	Stats *statsPage

	Error        errorPage // error.html
	ContactEmail string    // terms and privacy pages
}

type crabRow struct {
	Crab      store.Crab
	Following bool
}

func humanDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("Jan 2, 2006 at 3:04 PM")
}

// ago renders a short relative time the way social feeds do: "now", "5m",
// "3h", "4d", then a date. The exact time is in the element's tooltip, which
// app.js converts to the reader's local time.
func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case t.IsZero():
		return ""
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case t.Year() == time.Now().Year():
		return t.UTC().Format("Jan 2")
	default:
		return t.UTC().Format("Jan 2, 2006")
	}
}

func monthYear(t time.Time) string { return t.UTC().Format("January 2006") }

// commas writes n with thousands separators, like Crabber's commafy.
func commas(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

var modActions = map[string][2]string{ // action: {done, tried to}
	"ban":                {"banned", "ban"},
	"unban":              {"unbanned", "unban"},
	"warn":               {"warned", "warn"},
	"clear_display_name": {"cleared the display name of", "clear the display name of"},
	"clear_bio":          {"cleared the bio of", "clear the bio of"},
	"clear_location":     {"cleared the location of", "clear the location of"},
	"clear_website":      {"cleared the website of", "clear the website of"},
	"clear_fun_facts":    {"cleared the fun facts of", "clear the fun facts of"},
	"disable_invites":    {"disabled the invite code of", "disable the invite code of"},
	"enable_invites":     {"re-enabled the invite code of", "re-enable the invite code of"},
	"verify":             {"verified", "verify"},
	"unverify":           {"removed the verified badge from", "remove the verified badge from"},
	"make_moderator":     {"made a moderator:", "make a moderator:"},
	"award_trophy":       {"awarded a trophy to", "award a trophy to"},
	"revoke_trophy":      {"took a trophy back from", "take a trophy back from"},
	"remove_moderator":   {"removed the moderator role from", "remove the moderator role from"},
	"remove_molt":        {"removed a molt by", "remove a molt by"},
	"restore_molt":       {"restored a molt by", "restore a molt by"},
	"nsfw_molt":          {"marked NSFW a molt by", "mark NSFW a molt by"},
	"sfw_molt":           {"removed the NSFW label from a molt by", "remove the NSFW label from a molt by"},
	"set_role":           {"set the role of", "set the role of"},
	"dismiss_reports":    {"dismissed reports on a molt by", "dismiss reports on a molt by"},
}

// modActionLabel turns a moderation log action into words.
func modActionLabel(action string) string {
	if a, ok := strings.CutPrefix(action, "attempted_"); ok {
		if l, ok := modActions[a]; ok {
			return "tried to " + l[1]
		}
		return "tried " + a
	}
	if l, ok := modActions[action]; ok {
		return l[0]
	}
	return action
}

// websiteLabel shows a profile website without the scheme or trailing slash.
func websiteLabel(u string) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	return strings.TrimSuffix(u, "/")
}

// dict builds a map from key/value pairs so a partial can receive several
// values: {{ template "molt" (dict "M" . "D" $d) }}.
func dict(kv ...any) (map[string]any, error) {
	if len(kv)%2 != 0 {
		return nil, fmt.Errorf("dict needs key/value pairs, got %d values", len(kv))
	}
	m := make(map[string]any, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict key %v is not a string", kv[i])
		}
		m[k] = kv[i+1]
	}
	return m, nil
}

// assetVersion is a hash of the embedded static files. Asset URLs carry it as
// ?v=..., so they can be cached for good (see staticFiles) and still change
// with every release that touches a static file.
var assetVersion = func() string {
	h := sha256.New()
	_ = fs.WalkDir(ui.Files, "static", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(ui.Files, path)
		if err != nil {
			return err
		}
		h.Write([]byte(path))
		h.Write(b)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:12]
}()

func asset(path string) string { return "/static/" + path + "?v=" + assetVersion }

var templateFuncs = template.FuncMap{
	"humanDate":      humanDate,
	"ago":            ago,
	"monthYear":      monthYear,
	"websiteLabel":   websiteLabel,
	"hasPrefix":      strings.HasPrefix,
	"join":           strings.Join,
	"commas":         commas,
	"trophy":         trophyByID,
	"manualTrophies": trophies.Manual,
	"modActionLabel": modActionLabel,
	"reportLabel":    store.ReportLabel,
	"reportReasons":  func() []store.ReportReason { return store.ReportReasons },
	"dict":           dict,
	"asset":          asset,
	"editable":       editable,
	"avatarSrc":      avatarSrc,
	"bannerSrc":      bannerSrc,
}

func avatarSrc(code string) string {
	if p := avatar.Path(code); p != "" {
		return p
	}
	return asset("img/crab_illustration.jpg")
}

func bannerSrc(code string) string {
	if p := avatar.BannerPath(code); p != "" {
		return p
	}
	return asset("img/banner.png")
}

// editable reports whether a molt is still inside its edit window. Templates
// hold molts both by value and by pointer.
func editable(m any) bool {
	switch m := m.(type) {
	case store.Molt:
		return m.Editable(time.Now())
	case *store.Molt:
		return m != nil && m.Editable(time.Now())
	}
	return false
}

func newTemplateCache() (map[string]*template.Template, error) {
	pages, err := fs.Glob(ui.Files, "html/pages/*.html")
	if err != nil {
		return nil, err
	}
	cache := map[string]*template.Template{}
	for _, page := range pages {
		name := filepath.Base(page)
		ts, err := template.New(name).Funcs(templateFuncs).ParseFS(ui.Files, "html/base.html", "html/partials/*.html", page)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", page, err)
		}
		cache[name] = ts
	}
	return cache, nil
}

// render executes a page into a buffer first, so a template error becomes a
// clean 500 instead of a half-written page. Only whole pages show the nav
// badge and the sidebar, so only they read the unread count and build the
// sidebar; htmx fragments skip both.
func (app *App) render(w http.ResponseWriter, r *http.Request, status int, page string, data templateData) {
	if data.IsAuthenticated {
		if !data.unreadKnown {
			data.Unread = app.unread(r)
		}
		data.Sidebar = app.sidebarFor(r)
	}
	app.renderTemplate(w, r, status, page, "base", data)
}

func (app *App) unread(r *http.Request) int {
	n, err := app.store.UnreadNotifications(r.Context(), currentCrab(r).ID)
	if err != nil {
		app.log.Warn("unread notifications", "err", err)
	}
	return n
}

// renderTemplate executes one named template from a page's set (used for htmx
// fragments such as the molt list item).
func (app *App) renderTemplate(w http.ResponseWriter, r *http.Request, status int, page, name string, data any) {
	ts, ok := app.templates[page]
	if !ok {
		app.serverError(w, r, fmt.Errorf("template %s does not exist", page))
		return
	}
	var buf bytes.Buffer
	if err := ts.ExecuteTemplate(&buf, name, data); err != nil {
		app.serverError(w, r, fmt.Errorf("execute %s/%s: %w", page, name, err))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func (app *App) newTemplateData(r *http.Request) templateData {
	d := templateData{
		CurrentYear:      time.Now().UTC().Year(),
		Flash:            app.sessions.PopString(r.Context(), sessionFlash),
		CSRFToken:        csrfToken(r),
		TurnstileSiteKey: app.cfg.TurnstileSiteKey,
		SignupMode:       app.cfg.SignupMode,
		CurrentPath:      r.URL.Path,
	}
	if c := currentCrab(r); c != nil {
		d.IsAuthenticated = true
		d.CrabID = c.ID
		d.CrabName = c.UserName
		d.Avatar = c.Avatar
		d.ShowNSFW, d.MutedWords, d.Appearance = c.ShowNSFW, c.MutedWords, c.Appearance
		d.IsAdmin = c.IsAdmin()
		d.IsModerator = c.IsModerator()
	}
	return d
}

// withLikes resolves remolts to their originals and marks which molts the
// viewer has liked.
// withDisplay fills in what molts need to render: display names from the
// directory, by crab ID, so a crab's current name shows on everything they
// wrote (crabs the snapshot doesn't have yet keep the username stored on the
// molt), and the text with crabtags and known crabs' mentions linked.
func (app *App) withDisplay(r *http.Request, molts []store.Molt) {
	dir := app.dirData(r)
	byID := dir.byID
	known := func(name string) (string, bool) {
		n, ok := dir.names[name]
		return n, ok
	}
	viewer, showNSFW := "", false
	if c := currentCrab(r); c != nil {
		viewer, showNSFW = c.ID, c.ShowNSFW
	}
	for i := range molts {
		molts[i].ContentHTML = richtext.HTML(molts[i].Content, known)
		molts[i].Veiled = molts[i].NSFW && !showNSFW && molts[i].AuthorID != viewer
		molts[i].YouTube = firstYouTube(molts[i].Content)
	}
	for i := range molts {
		m := &molts[i]
		if c, ok := byID[m.AuthorID]; ok {
			m.Author, m.AuthorName, m.AuthorAvatar, m.AuthorVerified = c.UserName, c.Name(), c.Avatar, c.Verified
		}
		if c, ok := byID[m.RemoltedByID]; ok {
			m.RemoltedBy, m.RemoltedByName = c.UserName, c.Name()
		}
		if c, ok := byID[m.ReplyToAuthorID]; ok {
			m.ReplyToAuthor, m.ReplyToName = c.UserName, c.Name()
		}
	}
}

// displayOne is withDisplay for a single molt the handler already holds.
func (app *App) displayOne(r *http.Request, m *store.Molt) {
	ms := []store.Molt{*m}
	app.withDisplay(r, ms)
	*m = ms[0]
}

func (app *App) withLikes(r *http.Request, molts []store.Molt) ([]store.Molt, error) {
	molts, err := app.store.ResolveRemolts(r.Context(), app.visibleMolts(r, molts))
	if err != nil {
		return nil, err
	}
	app.withDisplay(r, molts)
	if err := app.withQuoted(r, molts); err != nil {
		return nil, err
	}
	app.withCards(r, molts)
	c := currentCrab(r)
	if c == nil || len(molts) == 0 {
		return molts, nil
	}
	ids := make([]string, 0, len(molts))
	for _, m := range molts {
		ids = append(ids, m.ID)
	}
	marks, err := app.store.MarksOn(r.Context(), c.ID, ids)
	if err != nil {
		return nil, err
	}
	for i := range molts {
		mk := marks[molts[i].ID]
		molts[i].Liked, molts[i].RemoltedAs, molts[i].Bookmarked = mk.Liked, mk.RemoltedAs, mk.Bookmarked
	}
	return molts, nil
}

// firstYouTube is the video ID of the first YouTube link in content, or "".
func firstYouTube(content string) string {
	for _, u := range richtext.URLs(content) {
		if id := richtext.YouTubeID(u); id != "" {
			return id
		}
	}
	return ""
}

// cardURL is the address a molt's card is cached under: its first link that
// isn't a YouTube video (those get the player), when that's a plain HTTPS
// address.
func cardURL(content string) string {
	for _, link := range richtext.URLs(content) {
		if richtext.YouTubeID(link) == "" {
			u, _ := linkcard.Normalize(link)
			return u
		}
	}
	return ""
}

// withCards attaches the cached card for each molt's first link, and asks for
// the ones not fetched yet. Cards are extras: a failed lookup only logs.
func (app *App) withCards(r *http.Request, molts []store.Molt) {
	urls := make([]string, len(molts))
	for i := range molts {
		urls[i] = cardURL(molts[i].Content)
	}
	cards, err := app.store.LinkCards(r.Context(), urls)
	if err != nil {
		app.log.Warn("link cards", "err", err)
		return
	}
	for i, u := range urls {
		if u == "" {
			continue
		}
		c, ok := cards[u]
		switch {
		case !ok:
			app.enqueueCard(u)
		case !c.Failed:
			molts[i].Card = &c
		}
	}
}

// enqueueCard asks for the card of a molt's first link, if it has one.
func (app *App) enqueueCard(u string) {
	if u != "" && app.cards != nil {
		app.cards.EnqueueCard(u)
	}
}

// withQuoted attaches the quoted molt to each quote. A quoted molt that was
// deleted, removed or written by a hidden crab stays nil.
func (app *App) withQuoted(r *http.Request, molts []store.Molt) error {
	var keys [][2]string
	for _, m := range molts {
		if m.QuoteOf != "" {
			keys = append(keys, [2]string{m.QuoteOfPK, m.QuoteOfSK})
		}
	}
	if len(keys) == 0 {
		return nil
	}
	quoted, err := app.store.MoltsByKeys(r.Context(), keys)
	if err != nil {
		return err
	}
	quoted = app.visibleMolts(r, quoted)
	app.withDisplay(r, quoted)
	byID := make(map[string]*store.Molt, len(quoted))
	for i := range quoted {
		byID[quoted[i].ID] = &quoted[i]
	}
	for i := range molts {
		if q, ok := byID[molts[i].QuoteOf]; ok {
			molts[i].Quoted = q
		}
	}
	return nil
}
