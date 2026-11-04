package web

import (
	"testing"

	"github.com/t0ul/krabber-net/internal/writing"
)

func TestCheckMoltFormRequired(t *testing.T) {
	plain := moltForm{Content: "just a thought"}
	if c := checkMoltForm(plain, false); c.Message != "" {
		t.Errorf("optional mode refused a plain molt: %q", c.Message)
	}
	if c := checkMoltForm(plain, true); c.Message != "Pick a form." {
		t.Errorf("required mode: %q", c.Message)
	}
	if c := checkMoltForm(moltForm{Content: "", Form: ""}, true); c.Message != "Pick a form." {
		t.Errorf("required mode, blank and no form: %q", c.Message)
	}
	haiku := moltForm{Content: "low tide at sunrise\na krab raises one wet claw\nhello to the gulls", Form: "haiku"}
	if c := checkMoltForm(haiku, true); c.Message != "" {
		t.Errorf("required mode refused a haiku: %q", c.Message)
	}
	if c := checkMoltForm(moltForm{Content: "two lines\nonly", Form: "haiku"}, true); c.Message == "" {
		t.Error("required mode should still apply the haiku's rules")
	}
	if c := checkMoltForm(moltForm{Content: "x", Form: "sestina"}, true); c.Message != "That is not a form Krabber knows." {
		t.Errorf("unknown form: %q", c.Message)
	}
}

// formAllowed is the one line to change for a poetry-only site; by default
// every form in the catalogue counts, so the picker keeps every group.
func TestPickableWhenRequired(t *testing.T) {
	for _, g := range writing.Groups() {
		if len(pickable(g, true)) != len(writing.InGroup(g)) {
			t.Errorf("required mode should offer every %s form", g)
		}
	}
	if f, _ := writing.ByID(""); formAllowed(f) {
		t.Error("the plain note is never allowed when forms are required")
	}
	if f, _ := writing.ByID(defaultForm); !formAllowed(f) || f.Group != writing.Poetry {
		t.Error("the default form must be an allowed poem")
	}
}
