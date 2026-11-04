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
	// Rhyme and syllables count too: the site holds a bot to them.
	if err := Check("one line\nanother", "couplet"); err == nil || !strings.Contains(err.Error(), "rhyme") {
		t.Errorf("an unrhymed couplet: %v", err)
	}
	if err := Check("gm crabs 🦀 Monday\nfirst light at 6:30am on the reef\nlow tide and big plans", "haiku"); err == nil || !strings.Contains(err.Error(), "4-7-5") {
		t.Errorf("a four-seven-five haiku: %v", err)
	}
}

func TestFitLine(t *testing.T) {
	fillers := []string{"today", "now"}
	if got := FitLine(5, fillers, "gm crabs 🦀 Monday", "gm crabs 🦀 it's Monday"); got != "gm crabs 🦀 it's Monday" {
		t.Errorf("the first candidate that counts wins: %q", got)
	}
	if got := FitLine(5, fillers, "South Brooklyn"); got != "South Brooklyn, today" {
		t.Errorf("a short line is padded: %q", got)
	}
	if got := FitLine(7, fillers, "no school: Day after Thanksgiving", "Day after Thanksgiving"); got != "Day after Thanksgiving, now" {
		t.Errorf("the candidate nearest below is padded, not the first: %q", got)
	}
	if got := FitLine(5, fillers, "Service on the East River is suspended until further notice."); got != "Service on the East" {
		t.Errorf("an over-long line is cut from the end: %q", got)
	}
	if got := FitLine(5, fillers, ""); got != "today, today, now" {
		t.Errorf("an empty line is all filler: %q", got)
	}
	if got := FitLine(5, nil, "hi"); got != "hi" {
		t.Errorf("without fillers a short line stays short: %q", got)
	}
	if err := Check(Haiku(fillers, []string{"gm crabs"}, []string{"a new morning on the reef"}, []string{"low tide and big plans"}), "haiku"); err != nil {
		t.Errorf("a fitted haiku passes the site's check: %v", err)
	}
}
