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
	"strings"
	"time"

	"github.com/t0ul/krabber-net/internal/store"
	"github.com/t0ul/krabber-net/ui"
)

// templateData is everything a page template can use.
type templateData struct {
	CurrentYear      int
	Flash            string
	IsAuthenticated  bool
	IsAdmin          bool
	IsModerator      bool
	CrabID           string
	CrabName         string
	CSRFToken        string
	TurnstileSiteKey string
	Form             any
	Query            string
	CurrentPath      string
	Unread           int
	Sidebar          sidebar

	Notifications []store.Notification

	Molt         store.Molt
	Molts        []store.Molt
	Likes        []store.Like
	EmptyMessage string

	Profile     *store.Crab
	IsFollowing bool
	IsBlocking  bool
	Blocked     []store.Crab
	ModLog      []store.ModAction
	CanModerate bool
	Reports     []store.Report
	Report      *store.Report
	ReportRows  []store.ReportRow
	Gone        map[string]bool // banned and deleted crabs, for Crabmin
	CrabRows    []crabRow
	ListTitle   string
	ListBack    string

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

var modActions = map[string][2]string{ // action: {done, tried to}
	"ban":                {"banned", "ban"},
	"unban":              {"unbanned", "unban"},
	"warn":               {"warned", "warn"},
	"clear_display_name": {"cleared the display name of", "clear the display name of"},
	"clear_bio":          {"cleared the bio of", "clear the bio of"},
	"clear_location":     {"cleared the location of", "clear the location of"},
	"clear_website":      {"cleared the website of", "clear the website of"},
	"make_moderator":     {"made a moderator:", "make a moderator:"},
	"remove_moderator":   {"removed the moderator role from", "remove the moderator role from"},
	"remove_molt":        {"removed a molt by", "remove a molt by"},
	"restore_molt":       {"restored a molt by", "restore a molt by"},
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
// ?v=..., so browsers fetch new CSS/JS after every release even though the
// files are cached for a day. (CloudFront ignores the query string and is
// invalidated on deploy.)
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
	"modActionLabel": modActionLabel,
	"reportLabel":    store.ReportLabel,
	"reportReasons":  func() []store.ReportReason { return store.ReportReasons },
	"dict":           dict,
	"asset":          asset,
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
// clean 500 instead of a half-written page.
func (app *App) render(w http.ResponseWriter, r *http.Request, status int, page string, data templateData) {
	app.renderTemplate(w, r, status, page, "base", data)
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
		CurrentPath:      r.URL.Path,
	}
	if c := currentCrab(r); c != nil {
		d.IsAuthenticated = true
		d.CrabID = c.ID
		d.CrabName = c.UserName
		d.IsAdmin = c.IsAdmin()
		d.IsModerator = c.IsModerator()
		n, err := app.store.UnreadNotifications(r.Context(), c.ID)
		if err != nil {
			app.log.Warn("unread notifications", "err", err)
		}
		d.Unread = n
	}
	d.Sidebar = app.sidebarFor(r)
	return d
}

// withLikes resolves remolts to their originals and marks which molts the
// viewer has liked.
func (app *App) withLikes(r *http.Request, molts []store.Molt) ([]store.Molt, error) {
	molts, err := app.store.ResolveRemolts(r.Context(), app.visibleMolts(r, molts))
	if err != nil {
		return nil, err
	}
	c := currentCrab(r)
	if c == nil || len(molts) == 0 {
		return molts, nil
	}
	ids := make([]string, 0, len(molts))
	for _, m := range molts {
		ids = append(ids, m.ID)
	}
	liked, err := app.store.LikedIDs(r.Context(), c.ID, ids)
	if err != nil {
		return nil, err
	}
	for i := range molts {
		molts[i].Liked = liked[molts[i].ID]
	}
	return molts, nil
}
