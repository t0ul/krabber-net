package main

import (
	"strings"
	"testing"

	"github.com/t0ul/krabber-net/internal/botkit"
)

// Every Shell Prompt is a haiku. (Syllables are a hint the API never
// enforces, so the test holds to the exact rules.)
func TestPromptsAreHaikus(t *testing.T) {
	for i := range prompts {
		m := prompt(i)
		if err := botkit.Check(m, form); err != nil {
			t.Errorf("prompt %d: %v\n%s", i, err, m)
		}
		if !strings.HasPrefix(m, "🐚 Shell Prompt\n") || !strings.Contains(m, "Reply in a molt") {
			t.Errorf("prompt %d: %q", i, m)
		}
	}
}
