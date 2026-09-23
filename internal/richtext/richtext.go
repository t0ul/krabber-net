// Package richtext finds @mentions and %crabtags in molt text and renders it
// as HTML with them linked, following Crabber's patterns: a mention or tag
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
)

var (
	token = regexp.MustCompile(`(?:^|\s)(?:@([A-Za-z0-9_]{1,32})\b|%([\p{L}\p{N}_]+))`)
	tagRe = regexp.MustCompile(`^[\p{L}\p{N}_]+$`)
)

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

// HTML escapes s and links its crabtags, and the mentions that known
// resolves to a crab (it returns the crab's username as stored).
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
			b.WriteString(html.EscapeString(s[last:at]))
			b.WriteString(`<a href="/crabs/` + url.PathEscape(name) + `" class="mention zindex-front">`)
			b.WriteString(html.EscapeString(s[at:m[3]]))
			b.WriteString(`</a>`)
			last = m[3]
		case m[4] >= 0:
			name := s[m[4]:m[5]]
			if utf8.RuneCountInString(name) > MaxTagLength {
				continue
			}
			pct := m[4] - 1
			b.WriteString(html.EscapeString(s[last:pct]))
			b.WriteString(`<a href="/crabtag/` + url.PathEscape(strings.ToLower(name)) + `" class="crabtag zindex-front">`)
			b.WriteString(html.EscapeString(s[pct:m[5]]))
			b.WriteString(`</a>`)
			last = m[5]
		}
	}
	b.WriteString(html.EscapeString(s[last:]))
	return template.HTML(b.String()) //nolint:gosec // every piece of s is escaped above
}
