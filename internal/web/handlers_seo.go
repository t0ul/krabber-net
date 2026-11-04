package web

// What crawlers and link previews see. Public pages carry a description, a
// canonical URL and Open Graph tags (partials/header.html); the sitemap lists
// them; robots.txt names the sitemap and keeps crawlers off the pages behind
// sign-in, which would only redirect them to the login form.

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/t0ul/krabber-net/internal/store"
)

const (
	descriptionChars = 160 // what a search result shows of a description
	titleChars       = 70  // what it shows of a title
)

// describe fills in what the header shows crawlers and link previews on a
// public page: its description, its canonical URL (this path on the site's
// base URL, so www and query strings don't make duplicates) and the Open
// Graph type: "article" for a molt, "profile" for a krab, "website" otherwise.
func (app *App) describe(r *http.Request, data *templateData, description, ogType string) {
	data.Description = description
	data.Canonical = app.absolute(r.URL.Path)
	data.OGType = ogType
	data.OGImage = app.absolute(asset("img/app-icon-512.png"))
}

// absolute makes a site-relative URL absolute.
func (app *App) absolute(ref string) string {
	u, err := url.Parse(ref)
	if err != nil {
		return app.cfg.BaseURL.String()
	}
	return app.cfg.BaseURL.ResolveReference(u).String()
}

// excerpt squeezes text onto one line and, past n runes, cuts it at a word
// boundary with an ellipsis.
func excerpt(text string, n int) string {
	s := strings.Join(strings.Fields(text), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	cut := n - 1 // leaves room for the ellipsis
	for i := cut - 1; i > cut/2; i-- {
		if r[i] == ' ' {
			cut = i
			break
		}
	}
	return strings.TrimRight(string(r[:cut]), " ,;:.!?-") + "…"
}

// moltTitle names a molt's page: who wrote it and how it starts. An NSFW
// molt's text stays out of titles, descriptions and link previews.
func moltTitle(m store.Molt) string {
	if m.NSFW {
		return "A molt by @" + m.Author
	}
	name := m.AuthorName
	if name == "" {
		name = m.Author
	}
	return name + ": " + excerpt(m.Content, titleChars)
}

func moltDescription(m store.Molt) string {
	if m.NSFW {
		return "A molt by @" + m.Author + " on Krabber, marked NSFW."
	}
	return excerpt(m.Content, descriptionChars)
}

// profileDescription introduces a krab: name, username and bio, if any.
func profileDescription(c *store.Crab) string {
	name := c.DisplayName
	if name == "" {
		name = c.UserName
	}
	s := fmt.Sprintf("%s (@%s) on Krabber", name, c.UserName)
	if room := descriptionChars - utf8.RuneCountInString(s) - 2; room > 20 && strings.TrimSpace(c.Bio) != "" {
		return s + ": " + excerpt(c.Bio, room)
	}
	return s + "."
}

// sitemap lists the public pages for crawlers: the welcome page, the Sea,
// the writing forms, every krab's profile and the newest molts. It comes
// from the directory snapshot, so it costs no table reads. Replies, remolts
// and NSFW molts stay out: a reply's page repeats its parent, a remolt's
// shows the original, and NSFW molts aren't for search results.
func (app *App) sitemap(w http.ResponseWriter, r *http.Request) {
	d := app.loadDirectory(r.Context())
	var b strings.Builder
	b.WriteString(xml.Header)
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	add := func(path string, lastmod time.Time) {
		b.WriteString("<url><loc>")
		_ = xml.EscapeText(&b, []byte(app.absolute(path)))
		b.WriteString("</loc>")
		if !lastmod.IsZero() {
			b.WriteString("<lastmod>" + lastmod.UTC().Format(time.RFC3339) + "</lastmod>")
		}
		b.WriteString("</url>\n")
	}
	add("/", time.Time{})
	add("/sea", time.Time{})
	add("/forms", time.Time{})
	for _, c := range d.crabs {
		add("/krabs/"+c.UserName, time.Time{})
	}
	for _, m := range d.recent {
		if m.Deleted || m.Removed || m.NSFW || m.Remolt || m.ReplyTo != "" || d.gone[m.AuthorID] {
			continue
		}
		add("/molt/view/"+m.ID, m.CreatedAt)
	}
	b.WriteString("</urlset>\n")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = io.WriteString(w, b.String())
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

func (app *App) robots(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = io.WriteString(w, robotsTxt+"\nSitemap: "+app.absolute("/sitemap.xml")+"\n")
}
