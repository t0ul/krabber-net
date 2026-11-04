package web

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/t0ul/krabber-net/internal/store"
)

func TestExcerpt(t *testing.T) {
	for _, tc := range []struct {
		text string
		n    int
		want string
	}{
		{"the reef at sunrise\nall of you, still here", 100, "the reef at sunrise all of you, still here"},
		{"  spaced   out  ", 20, "spaced out"},
		{"one two three four five", 12, "one two…"},
		{"abcdefghijklmnop", 8, "abcdefg…"},
		{"a comma, is here", 9, "a comma…"},
		{"héllo wörld again", 12, "héllo wörld…"},
		{"", 10, ""},
	} {
		if got := excerpt(tc.text, tc.n); got != tc.want {
			t.Errorf("excerpt(%q, %d) = %q, want %q", tc.text, tc.n, got, tc.want)
		}
	}
}

func TestMoltTitleAndDescription(t *testing.T) {
	m := store.Molt{Author: "plankton", AuthorName: "Sheldon", Content: "Chum is\nmetabolic fuel"}
	if got := moltTitle(m); got != "Sheldon: Chum is metabolic fuel" {
		t.Errorf("title %q", got)
	}
	if got := moltDescription(m); got != "Chum is metabolic fuel" {
		t.Errorf("description %q", got)
	}

	// An NSFW molt's text stays off search results and link previews.
	m.NSFW = true
	if got := moltTitle(m); got != "A molt by @plankton" {
		t.Errorf("NSFW title %q", got)
	}
	if got := moltDescription(m); strings.Contains(got, "Chum") || !strings.Contains(got, "NSFW") {
		t.Errorf("NSFW description %q", got)
	}

	// Long molts are cut short, with the username when there's no display name.
	m = store.Molt{Author: "gary", Content: strings.Repeat("meow ", 60)}
	if got := moltTitle(m); !strings.HasPrefix(got, "gary: meow meow") || !strings.HasSuffix(got, "…") || utf8.RuneCountInString(got) > 80 {
		t.Errorf("long title %q", got)
	}
	if got := moltDescription(m); !strings.HasSuffix(got, "…") || utf8.RuneCountInString(got) > 160 {
		t.Errorf("long description %q (%d runes)", got, utf8.RuneCountInString(got))
	}
}
