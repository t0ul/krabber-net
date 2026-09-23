package richtext

import (
	"slices"
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
	want := `&lt;b&gt;hi&lt;/b&gt; <a href="/crabs/SpongeBob" class="mention zindex-front">@spongebob</a> &amp; @nobody <a href="/crabtag/tag" class="crabtag zindex-front">%Tag</a>!`
	if got != want {
		t.Errorf("HTML =\n%s\nwant\n%s", got, want)
	}
	if got := string(HTML("plain 'text'", known)); got != "plain &#39;text&#39;" {
		t.Errorf("plain = %s", got)
	}
}
