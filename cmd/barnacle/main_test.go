package main

import (
	"testing"

	"github.com/t0ul/krabber-net/internal/botkit"
)

// Every Shell Prompt is a haiku the site counts as five-seven-five, and
// each question fits its line as written, with nothing padded on.
func TestPromptsAreHaikus(t *testing.T) {
	for i := range prompts {
		m := prompt(i)
		if err := botkit.Check(m, form); err != nil {
			t.Errorf("prompt %d: %v\n%s", i, err, m)
		}
		if want := "🐚 Shell Prompt for the reef\n" + prompts[i] + "\nreply in a molt"; m != want {
			t.Errorf("prompt %d was padded or cut:\n%s", i, m)
		}
	}
}
