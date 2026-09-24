package richtext

import (
	"slices"
	"strings"
	"testing"
)

func TestTagsAndMentions(t *testing.T) {
	s := "%Krabby patties with @SpongeBob and @patrick_star.\n%krabby again, %Über_2 but not a%tag, 50% off, email@example.com, or @" +
		"thisusernameiswaytoolongtobevalid_x"
	if got, want := Tags(s), []string{"krabby", "über_2"}; !slices.Equal(got, want) {
		t.Errorf("tags = %q, want %q", got, want)
	}
	if got, want := Mentions(s), []string{"spongebob", "patrick_star"}; !slices.Equal(got, want) {
		t.Errorf("mentions = %q, want %q", got, want)
	}
}

func TestHTML(t *testing.T) {
	known := func(name string) (string, bool) {
		if name == "spongebob" {
			return "SpongeBob", true
		}
		return "", false
	}
	got := string(HTML("<b>hi</b> @spongebob & @nobody %Tag!", known))
	want := `&lt;b&gt;hi&lt;/b&gt; <a href="/krabs/SpongeBob" class="mention zindex-front">@spongebob</a> &amp; @nobody <a href="/krabtag/tag" class="crabtag zindex-front">%Tag</a>!`
	if got != want {
		t.Errorf("HTML =\n%s\nwant\n%s", got, want)
	}
	if got := string(HTML("plain 'text'", known)); got != "plain &#39;text&#39;" {
		t.Errorf("plain = %s", got)
	}
}

func TestLinks(t *testing.T) {
	none := func(string) (string, bool) { return "", false }
	got := string(HTML(`see https://krabber.net/krabs/bob?a=1&b="2". and (https://en.wikipedia.org/wiki/Crab_(disambiguation)) ok`, none))
	want := `see <a href="https://krabber.net/krabs/bob?a=1&amp;b=" class="mention zindex-front" target="_blank" rel="nofollow ugc noopener noreferrer">krabber.net/krabs/bob?a=1&amp;b=</a>&#34;2&#34;. and (https://en.wikipedia.org/wiki/Crab_(disambiguation)) ok`
	if got != want {
		t.Errorf("HTML =\n%s\nwant\n%s", got, want)
	}
	long := string(HTML("https://example.com/a/very/long/path/that/keeps/going/on", none))
	if want := `>example.com/a/very/long/path/that/…</a>`; !strings.HasSuffix(long, want) {
		t.Errorf("long link = %s", long)
	}
	for _, s := range []string{"javascript:alert(1)", "not a link: example.com", "xhttps://evil.test", "ftp://files.test"} {
		if got := string(HTML(s, none)); strings.Contains(got, "<a ") {
			t.Errorf("%q linked: %s", s, got)
		}
	}

	cases := map[string]string{
		"first https://a.test/x, then https://b.test": "https://a.test/x",
		"HTTPS://Loud.test!":                          "HTTPS://Loud.test",
		"(see https://wrapped.test/)":                 "https://wrapped.test/",
		"docs at https://w.test/Crab_(food) today":    "https://w.test/Crab_(food)",
		"no links, @crab %tag":                        "",
		"https://x.test/@bob %tag":                    "https://x.test/@bob",
	}
	for in, want := range cases {
		if got := FirstURL(in); got != want {
			t.Errorf("FirstURL(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Mentions("https://x.test/@bob"); len(got) != 0 {
		t.Errorf("mention inside a link: %q", got)
	}
	if got := URLs("a https://one.test b https://two.test."); !slices.Equal(got, []string{"https://one.test", "https://two.test"}) {
		t.Errorf("URLs = %q", got)
	}
}

func TestYouTubeID(t *testing.T) {
	good := map[string]string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ":           "dQw4w9WgXcQ",
		"https://youtube.com/watch?feature=share&v=dQw4w9WgXcQ": "dQw4w9WgXcQ",
		"https://m.youtube.com/watch?v=dQw4w9WgXcQ&t=42":        "dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ?si=abc":                   "dQw4w9WgXcQ",
		"https://www.youtube.com/shorts/dQw4w9WgXcQ":            "dQw4w9WgXcQ",
		"https://www.youtube.com/embed/dQw4w9WgXcQ/":            "dQw4w9WgXcQ",
		"http://youtube.com/live/dQw4w9WgXcQ":                   "dQw4w9WgXcQ",
	}
	for in, want := range good {
		if got := YouTubeID(in); got != want {
			t.Errorf("YouTubeID(%q) = %q, want %q", in, got, want)
		}
	}
	for _, in := range []string{
		"https://www.youtube.com/watch?v=short", "https://youtube.com/channel/UCabcdefghijk",
		"https://evilyoutube.com/watch?v=dQw4w9WgXcQ", "https://youtube.com.evil.test/watch?v=dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ\"><script>", "javascript://youtube.com/watch?v=dQw4w9WgXcQ",
	} {
		if got := YouTubeID(in); got != "" {
			t.Errorf("YouTubeID(%q) = %q, want none", in, got)
		}
	}
}
