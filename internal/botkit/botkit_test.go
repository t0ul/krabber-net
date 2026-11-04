package botkit

import (
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	if err := Check("The tide goes out, the tide comes in,\nthe krab just sits and wears a grin.", "couplet"); err != nil {
		t.Errorf("a couplet: %v", err)
	}
	if err := Check("one line only", "couplet"); err == nil || !strings.Contains(err.Error(), "Two lines") {
		t.Errorf("a one-line couplet should break the line rule: %v", err)
	}
	if err := Check("a\nb\nc", "sestina"); err == nil || !strings.Contains(err.Error(), "sestina") {
		t.Errorf("an unknown form: %v", err)
	}
	if err := Check(strings.Repeat("x", 281)+"\ny", "couplet"); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Errorf("an over-long couplet: %v", err)
	}
	// Rhyme is a hint: a couplet that doesn't rhyme still posts.
	if err := Check("one line\nanother", "couplet"); err != nil {
		t.Errorf("an unrhymed couplet: %v", err)
	}
}
