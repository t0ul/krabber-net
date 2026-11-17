// Package rules holds the write rules every front end on Krabber's table
// keeps — Krabber's own website and Kusaza's haiku network alike — so there
// is exactly one copy of each: what a draft must be before it posts, the
// hourly write limits, what posting, replying, liking and following set in
// motion (feeds, notifications, mentions, trophies, link cards), and the
// account flows' limits and lifetimes.
//
// Rules decide; front ends speak. A refusal comes back as a reason, never
// as a sentence, so each site words it in its own voice.
package rules

import (
	"strings"

	"github.com/t0ul/krabber-net/engine/linkcard"
	"github.com/t0ul/krabber-net/engine/richtext"
	"github.com/t0ul/krabber-net/engine/store"
	"github.com/t0ul/krabber-net/engine/validator"
	"github.com/t0ul/krabber-net/engine/writing"
)

// Reason says why a draft can't be posted yet. Each front end words it.
type Reason int

const (
	Ready       Reason = iota // nothing stands in the way
	UnknownForm               // the form isn't in the catalogue
	NeedsForm                 // forms are required here and the draft is a plain note
	FormNotHere               // forms are required here and this site doesn't take the form
	Blank                     // nothing written
	TooLong                   // longer than the form allows (Form.MaxChars)
	RuleBroken                // one of the form's rules is broken: Check.Rule says which
)

// Policy is a site's mode: whether every post must take a writing form,
// and, then, which forms it takes.
type Policy struct {
	Required bool
	// Allowed says which forms the site takes when forms are required; nil
	// takes every form in the catalogue.
	Allowed func(writing.Form) bool
}

func (p Policy) allows(f writing.Form) bool {
	if p.Allowed == nil {
		return f.ID != ""
	}
	return p.Allowed(f)
}

// HaikuOnly is the policy of a site where every post is a haiku.
var HaikuOnly = Policy{Required: true, Allowed: func(f writing.Form) bool { return f.ID == "haiku" }}

// Check is a draft measured against the form it names.
type Check struct {
	Form    writing.Form
	Results []writing.Result      // each rule of the form, kept or broken
	Lines   []writing.LineMeasure // each line's syllables and rhyme, for a composer's meters
	Blank   bool                  // nothing written yet, so the rules show without marks
	Reason  Reason
	// Rule and Note are the first broken rule (its final period trimmed) and
	// what was measured, when Reason is RuleBroken.
	Rule, Note string
	// Overridable: only sound rules (syllables, rhyme) hold the draft back,
	// and the writer may post it anyway by vouching for their own count.
	Overridable bool
}

// CheckDraft checks a draft against the writing form it names: the form
// must exist (and be one the site takes, when forms are required), the text
// can't be blank or longer than the form allows, no exact rule may be
// broken, and the sound rules must hold unless the writer vouched for their
// own count (override).
func CheckDraft(formID, content string, override bool, p Policy) Check {
	form, ok := writing.ByID(formID)
	if !ok {
		return Check{Reason: UnknownForm}
	}
	if p.Required && !p.allows(form) {
		if form.ID == "" {
			return Check{Reason: NeedsForm}
		}
		return Check{Form: form, Reason: FormNotHere}
	}
	c := Check{Form: form, Results: form.Check(content), Lines: form.Measure(content), Blank: !validator.NotBlank(content)}
	switch {
	case c.Blank:
		c.Reason = Blank
	case !validator.MaxChars(content, form.MaxChars):
		c.Reason = TooLong
	default:
		if broken := writing.Broken(c.Results, override); len(broken) > 0 {
			c.Reason = RuleBroken
			c.Rule = strings.TrimSuffix(broken[0].Rule, ".")
			c.Note = broken[0].Note
			c.Overridable = len(writing.Broken(c.Results, true)) == 0
		}
	}
	return c
}

// LayoutFor picks the layout a post in a form is set in: the one asked for
// if the form has it, else the writer's default for the form, else the
// form's own. It gives "" for the form's own default, which is how a post
// stores it. The layout is the writer's choice; readers may ignore it.
func LayoutFor(c *store.Crab, formID, asked string) string {
	f, _ := writing.ByID(formID)
	if len(f.Layouts) == 0 {
		return ""
	}
	l, ok := f.Layout(asked)
	if !ok && c != nil {
		l, _ = f.Layout(c.FormLayouts[formID])
	}
	if l.ID == f.Layouts[0].ID {
		return ""
	}
	return l.ID
}

// CardURL is the address a post's link card is cached under: its first
// link that isn't a YouTube video (those get the player), when that's a
// plain HTTPS address.
func CardURL(content string) string {
	for _, link := range richtext.URLs(content) {
		if richtext.YouTubeID(link) == "" {
			u, _ := linkcard.Normalize(link)
			return u
		}
	}
	return ""
}
