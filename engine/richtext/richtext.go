// Package richtext finds @mentions, %crabtags and web links in molt text and
// renders it as HTML with them linked, following Crabber's patterns: each
// starts the text or follows whitespace.
package richtext

import (
	"html"
	"html/template"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// MaxTagLength is the longest crabtag that's indexed and linked, in characters.
	MaxTagLength = 64
	// MaxPerMolt caps how many distinct tags, and how many mentions, a molt records.
	MaxPerMolt = 10
	// maxLinkText is how much of a link's address is shown before "…".
	maxLinkText = 35
)

var (
	token  = regexp.MustCompile(`(?:^|\s)(?:@([A-Za-z0-9_]{1,32})\b|%([\p{L}\p{N}_]+)|((?i:https?)://[^\s<>"]+))`)
	tagRe  = regexp.MustCompile(`^[\p{L}\p{N}_]+$`)
	crabRe = regexp.MustCompile(`[cC][rR][aA][bB]`)
)

// Krabify spells every "crab" in s as "krab", keeping the case of each
// letter (Crab → Krab, CRAB → KRAB, crabs → krabs, Crabber → Krabber).
// Molts are stored as written and changed only when shown, so it can be
// undone, and mentions (usernames) and links aren't touched.
func Krabify(s string) string {
	return crabRe.ReplaceAllStringFunc(s, func(w string) string {
		if w[0] == 'C' {
			return "K" + w[1:]
		}
		return "k" + w[1:]
	})
}

// urlLike finds addresses in plain text that aren't linked (say, one in
// parentheses), which must keep their spelling.
var urlLike = regexp.MustCompile(`(?i)https?://[^\s<>"]+`)

// text escapes a plain stretch of molt text for HTML, with crab spelled krab
// outside anything that looks like an address.
func text(s string) string {
	var b strings.Builder
	last := 0
	for _, m := range urlLike.FindAllStringIndex(s, -1) {
		b.WriteString(html.EscapeString(Krabify(s[last:m[0]])))
		b.WriteString(html.EscapeString(s[m[0]:m[1]]))
		last = m[1]
	}
	b.WriteString(html.EscapeString(Krabify(s[last:])))
	return b.String()
}

// linkEnd trims punctuation that usually ends the sentence, not the link,
// from the link at s[start:end], and returns the new end.
func linkEnd(s string, start, end int) int {
	for end > start {
		switch c := s[end-1]; c {
		case '.', ',', ';', ':', '!', '?', '\'':
			end--
		case ')':
			if strings.Count(s[start:end], "(") >= strings.Count(s[start:end], ")") {
				return end
			}
			end--
		default:
			return end
		}
	}
	return end
}

// URLs returns the web links in s, in order.
func URLs(s string) []string {
	var out []string
	for _, m := range token.FindAllStringSubmatchIndex(s, -1) {
		if m[6] >= 0 {
			if end := linkEnd(s, m[6], m[7]); end > m[6] {
				out = append(out, s[m[6]:end])
			}
		}
	}
	return out
}

// FirstURL returns the first web link in s, or "".
func FirstURL(s string) string {
	if urls := URLs(s); len(urls) > 0 {
		return urls[0]
	}
	return ""
}

var youtubeIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// YouTubeID returns the video ID of a YouTube watch, share, Shorts, embed or
// live link, or "".
func YouTubeID(link string) string {
	u, err := url.Parse(link)
	if err != nil || (!strings.EqualFold(u.Scheme, "https") && !strings.EqualFold(u.Scheme, "http")) {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	host = strings.TrimPrefix(strings.TrimPrefix(host, "www."), "m.")
	var id string
	switch host {
	case "youtu.be":
		id = strings.TrimPrefix(u.Path, "/")
	case "youtube.com":
		if u.Path == "/watch" {
			id = u.Query().Get("v")
			break
		}
		for _, prefix := range []string{"/shorts/", "/embed/", "/live/"} {
			if rest, ok := strings.CutPrefix(u.Path, prefix); ok {
				id = rest
			}
		}
	}
	id = strings.TrimSuffix(id, "/")
	if !youtubeIDRe.MatchString(id) {
		return ""
	}
	return id
}

// linkText is how a link reads in a molt: without the scheme, shortened.
func linkText(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if utf8.RuneCountInString(u) <= maxLinkText {
		return u
	}
	r := []rune(u)
	return string(r[:maxLinkText-1]) + "…"
}

// Tags returns the distinct crabtags in s, lowercased, in order of appearance.
func Tags(s string) []string {
	return collect(s, 2, func(name string) bool { return utf8.RuneCountInString(name) <= MaxTagLength })
}

// Mentions returns the distinct usernames mentioned in s, lowercased.
func Mentions(s string) []string {
	return collect(s, 1, func(string) bool { return true })
}

// ValidTag reports whether name can be a crabtag.
func ValidTag(name string) bool {
	return tagRe.MatchString(name) && utf8.RuneCountInString(name) <= MaxTagLength
}

func collect(s string, group int, ok func(string) bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range token.FindAllStringSubmatchIndex(s, -1) {
		if m[2*group] < 0 {
			continue
		}
		name := strings.ToLower(s[m[2*group]:m[2*group+1]])
		if seen[name] || !ok(name) {
			continue
		}
		seen[name] = true
		out = append(out, name)
		if len(out) == MaxPerMolt {
			break
		}
	}
	return out
}

// HTML escapes s (crab spelled krab, see Krabify) and links its krabtags,
// web links, and the mentions that
// known resolves to a krab (it returns the krab's username as stored).
func HTML(s string, known func(lowerName string) (string, bool)) template.HTML {
	var b strings.Builder
	last := 0
	for _, m := range token.FindAllStringSubmatchIndex(s, -1) {
		switch {
		case m[2] >= 0:
			name, ok := known(strings.ToLower(s[m[2]:m[3]]))
			if !ok {
				continue
			}
			at := m[2] - 1
			b.WriteString(text(s[last:at]))
			b.WriteString(`<a href="/krabs/` + url.PathEscape(name) + `" class="mention zindex-front">`)
			b.WriteString(html.EscapeString(s[at:m[3]]))
			b.WriteString(`</a>`)
			last = m[3]
		case m[4] >= 0:
			name := s[m[4]:m[5]]
			if utf8.RuneCountInString(name) > MaxTagLength {
				continue
			}
			pct := m[4] - 1
			b.WriteString(text(s[last:pct]))
			b.WriteString(`<a href="/krabtag/` + url.PathEscape(strings.ToLower(name)) + `" class="crabtag zindex-front">`)
			b.WriteString(text(s[pct:m[5]]))
			b.WriteString(`</a>`)
			last = m[5]
		case m[6] >= 0:
			end := linkEnd(s, m[6], m[7])
			u, err := url.Parse(s[m[6]:end])
			if err != nil || u.Host == "" {
				continue
			}
			b.WriteString(text(s[last:m[6]]))
			b.WriteString(`<a href="` + html.EscapeString(s[m[6]:end]) + `" class="mention zindex-front" target="_blank" rel="nofollow ugc noopener noreferrer">`)
			b.WriteString(html.EscapeString(linkText(s[m[6]:end])))
			b.WriteString(`</a>`)
			last = end
		}
	}
	b.WriteString(text(s[last:]))
	return template.HTML(b.String()) //nolint:gosec // every piece of s is escaped above
}
