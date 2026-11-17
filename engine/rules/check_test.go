package rules

import (
	"testing"

	"github.com/t0ul/krabber-net/engine/store"
	"github.com/t0ul/krabber-net/engine/writing"
)

const haiku = "an old silent pond\na frog jumps into the pond\nsplash! silence again"

func TestCheckDraftKrabber(t *testing.T) {
	// Krabber, forms optional: a plain note posts.
	if c := CheckDraft("", "just a note", false, Policy{}); c.Reason != Ready {
		t.Fatalf("plain note: %+v", c)
	}
	if c := CheckDraft("sestina", "x", false, Policy{}); c.Reason != UnknownForm {
		t.Fatalf("unknown form: %v", c.Reason)
	}
	if c := CheckDraft("haiku", "   ", false, Policy{}); c.Reason != Blank || !c.Blank {
		t.Fatalf("blank: %+v", c)
	}
	if c := CheckDraft("haiku", haiku, false, Policy{}); c.Reason != Ready || len(c.Lines) != 3 {
		t.Fatalf("a haiku: %+v", c)
	}
	// Forms required: a plain note can't post.
	if c := CheckDraft("", "just a note", false, Policy{Required: true}); c.Reason != NeedsForm {
		t.Fatalf("note where forms are required: %v", c.Reason)
	}
}

func TestCheckDraftHaikuOnly(t *testing.T) {
	if c := CheckDraft("haiku", haiku, false, HaikuOnly); c.Reason != Ready {
		t.Fatalf("a haiku on a haiku-only site: %+v", c)
	}
	if c := CheckDraft("", "a note", false, HaikuOnly); c.Reason != NeedsForm {
		t.Fatalf("a note on a haiku-only site: %v", c.Reason)
	}
	if c := CheckDraft("sonnet", "a sonnet", false, HaikuOnly); c.Reason != FormNotHere || c.Form.ID != "sonnet" {
		t.Fatalf("a sonnet on a haiku-only site: %+v", c)
	}
	// Two lines break an exact rule: no vouching gets that past.
	if c := CheckDraft("haiku", "two lines\nonly", true, HaikuOnly); c.Reason != RuleBroken || c.Overridable {
		t.Fatalf("two lines: %+v", c)
	}
	// A line off by the counter's ear holds the haiku back, until the
	// writer vouches for their own count.
	off := "morning on the wall\nthe meadow fills with quiet footsteps\na seal of red ink"
	c := CheckDraft("haiku", off, false, HaikuOnly)
	if c.Reason != RuleBroken || !c.Overridable || c.Rule == "" {
		t.Fatalf("off count: %+v", c)
	}
	if c := CheckDraft("haiku", off, true, HaikuOnly); c.Reason != Ready {
		t.Fatalf("vouched: %+v", c)
	}
}

func TestLayoutFor(t *testing.T) {
	f, _ := writing.ByID("haiku")
	if len(f.Layouts) < 2 {
		t.Skip("haiku has no layouts to choose between")
	}
	first, second := f.Layouts[0].ID, f.Layouts[1].ID
	if got := LayoutFor(nil, "haiku", second); got != second {
		t.Fatalf("asked %q, got %q", second, got)
	}
	if got := LayoutFor(nil, "haiku", first); got != "" {
		t.Fatalf("the form's own layout is stored as \"\", got %q", got)
	}
	c := &store.Crab{FormLayouts: map[string]string{"haiku": second}}
	if got := LayoutFor(c, "haiku", "no-such-layout"); got != second {
		t.Fatalf("the writer's default, got %q", got)
	}
	if got := LayoutFor(c, "", "stepped"); got != "" {
		t.Fatalf("a plain note has no layout, got %q", got)
	}
}

func TestLimiter(t *testing.T) {
	old := Limits["like"]
	Limits["like"] = 2
	t.Cleanup(func() { Limits["like"] = old })
	var l Limiter
	if !l.Allow("a", "like") || !l.Allow("a", "like") {
		t.Fatal("refused within the limit")
	}
	if l.Allow("a", "like") {
		t.Fatal("allowed past the limit")
	}
	if !l.Allow("b", "like") {
		t.Fatal("one krab's count stopped another")
	}
}

func TestCardURL(t *testing.T) {
	if got := CardURL("see https://example.com/a and https://example.org/b"); got == "" {
		t.Fatal("no card for a link")
	}
	if got := CardURL("https://www.youtube.com/watch?v=dQw4w9WgXcQ"); got != "" {
		t.Fatalf("a video gets the player, not a card: %q", got)
	}
	if got := CardURL("no links here"); got != "" {
		t.Fatalf("a card for no link: %q", got)
	}
}
