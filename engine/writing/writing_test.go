package writing

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCatalog(t *testing.T) {
	forms := Forms()
	if len(forms) < 10 {
		t.Fatalf("only %d forms", len(forms))
	}
	if forms[0].ID != "" || forms[0].Name != "Note" || forms[0].MaxChars != 280 || len(forms[0].Rules) != 0 {
		t.Errorf("the first form must be the plain note (no rules, 280 chars): %+v", forms[0])
	}
	idRX := regexp.MustCompile(`^[a-z-]*$`)
	seen := map[string]bool{}
	for _, f := range forms[1:] {
		if !idRX.MatchString(f.ID) || f.ID == "" || seen[f.ID] {
			t.Errorf("form %q: bad or repeated ID", f.ID)
		}
		seen[f.ID] = true
		if f.Name == "" || f.Tagline == "" || f.Example == "" || len(f.Rules) == 0 {
			t.Errorf("form %q needs a name, a tagline, an example and rules", f.ID)
		}
		if f.MaxChars < 280 || f.MaxChars > MaxLength {
			t.Errorf("form %q: max %d characters is outside 280–%d", f.ID, f.MaxChars, MaxLength)
		}
		if !slices.Contains(Groups(), f.Group) {
			t.Errorf("form %q: group %q isn't listed", f.ID, f.Group)
		}
		if utf8.RuneCountInString(f.Example) > f.MaxChars {
			t.Errorf("form %q: its own example is too long", f.ID)
		}
		if broken := Broken(f.Check(f.Example), false); len(broken) > 0 {
			t.Errorf("form %q: its own example breaks %q (%s)", f.ID, broken[0].Rule, broken[0].Note)
		}
		if n := len(f.Shape.Lines); n > 0 && !f.Shape.Grow && n != len(Lines(f.Example)) {
			t.Errorf("form %q: the example has %d lines, the shape %d", f.ID, len(Lines(f.Example)), n)
		}
		for _, m := range f.Measure(f.Example) {
			if !m.SyllablesOK || !m.RhymeOK {
				t.Errorf("form %q: the example's own measure is off: %+v", f.ID, m)
			}
		}
		if len(f.Check(f.Example)) != len(f.Rules) {
			t.Errorf("form %q: Check must report every rule", f.ID)
		}
	}
	for _, id := range []string{"haiku", "couplet", "sonnet", "limerick", "envelope", "rhyme", "simile", "metaphor", "analogy", "letter", "blog", "essay", "three-panel"} {
		if _, ok := ByID(id); !ok {
			t.Errorf("no %s form", id)
		}
	}
	if f, ok := ByID(""); !ok || f.Name != "Note" {
		t.Error(`ByID("") must be the plain note`)
	}
	if _, ok := ByID("sestina"); ok {
		t.Error("an unknown form must not be found")
	}
	for _, g := range Groups() {
		if len(InGroup(g)) == 0 {
			t.Errorf("group %q is empty", g)
		}
		for _, f := range InGroup(g) {
			if f.ID == "" {
				t.Error("the plain note belongs to no group")
			}
		}
	}
}

func TestLinesAndStanzas(t *testing.T) {
	text := "  one \r\ntwo\n\n\n three\nfour  \n\n"
	if got := Lines(text); !slices.Equal(got, []string{"one", "two", "three", "four"}) {
		t.Errorf("Lines = %q", got)
	}
	got := Stanzas(text)
	if len(got) != 2 || !slices.Equal(got[0], []string{"one", "two"}) || !slices.Equal(got[1], []string{"three", "four"}) {
		t.Errorf("Stanzas = %q", got)
	}
	if len(Lines("   \n\n")) != 0 || len(Stanzas("")) != 0 {
		t.Error("blank text has no lines or stanzas")
	}
}

func TestSyllables(t *testing.T) {
	for word, want := range map[string]int{
		"crab": 1, "the": 1, "fire": 1, "loved": 1, "waves": 1, "goes": 1, "tide": 1, "night": 1, "sky": 1,
		"haiku": 2, "ocean": 2, "table": 2, "wanted": 2, "boxes": 2, "sunrise": 2, "raises": 2, "hello": 2, "people": 2, "little": 2,
		"beautiful": 3, "syllable": 3, "poetry": 3, "radio": 3, "Krabber's": 2, "molt!": 1, "": 0, "123": 0,
	} {
		if got := Syllables(word); got != want {
			t.Errorf("Syllables(%q) = %d, want %d", word, got, want)
		}
	}
	if got := LineSyllables("a krab raises one wet claw"); got != 7 {
		t.Errorf("line = %d syllables, want 7", got)
	}
}

func TestRhymes(t *testing.T) {
	yes := [][2]string{{"moon", "soon"}, {"night", "light"}, {"night", "white"}, {"love", "dove"}, {"day", "away"},
		{"sea", "free"}, {"time", "rhyme"}, {"sky", "fly"}, {"star", "are"}, {"in", "grin"}, {"blue", "blue"}, {"Gulls.", "hulls"}}
	for _, p := range yes {
		if !Rhymes(p[0], p[1]) {
			t.Errorf("%q and %q should rhyme", p[0], p[1])
		}
	}
	no := [][2]string{{"cat", "dog"}, {"moon", "mine"}, {"crab", "krabber"}, {"", "sea"}}
	for _, p := range no {
		if Rhymes(p[0], p[1]) {
			t.Errorf("%q and %q should not rhyme", p[0], p[1])
		}
	}
	// Rhymes takes whole lines and looks at their last words.
	if !Rhymes("The tide goes out, the tide comes in,", "the krab just sits and wears a grin.") {
		t.Error("lines should rhyme on their last words")
	}
}

func TestScheme(t *testing.T) {
	lines := []string{"Roses by the sea", "a krab in the sand", "waves running free", "a claw in my hand"}
	if got := Scheme(lines); got != "ABAB" {
		t.Errorf("Scheme = %q, want ABAB", got)
	}
	if got := Scheme([]string{"one night", "two moons", "three light", "four tunes", "five lights"}); got != "ABABA" {
		t.Errorf("Scheme = %q, want ABABA", got)
	}
	if got := Scheme(nil); got != "" {
		t.Errorf("Scheme(nil) = %q", got)
	}
}

// check runs a form on text and returns the broken rules and the hints missed.
func check(t *testing.T, id, text string) (broken, hints []string) {
	t.Helper()
	f, ok := ByID(id)
	if !ok {
		t.Fatalf("no form %s", id)
	}
	for _, r := range f.Check(text) {
		switch {
		case !r.Checked() || r.OK:
		case r.Soft():
			hints = append(hints, r.Rule)
		default:
			broken = append(broken, r.Rule)
		}
	}
	return broken, hints
}

func TestHaiku(t *testing.T) {
	broken, hints := check(t, "haiku", "low tide at sunrise\na krab raises one wet claw\nhello to the gulls")
	if len(broken) != 0 || len(hints) != 0 {
		t.Errorf("a good haiku: broken %q, hints %q", broken, hints)
	}
	broken, _ = check(t, "haiku", "two lines\nonly")
	if len(broken) == 0 || !strings.Contains(broken[0], "Three lines") {
		t.Errorf("a two-line haiku: broken %q", broken)
	}
	off := "this line has too many syllables in it\nseven here, if you count them out\nfive to finish up"
	broken, hints = check(t, "haiku", off)
	if len(broken) != 1 || len(hints) != 0 || !strings.Contains(broken[0], "syllables") {
		t.Errorf("wrong syllables hold a haiku back: broken %q, hints %q", broken, hints)
	}
	f, _ := ByID("haiku")
	if b := Broken(f.Check(off), true); len(b) != 0 {
		t.Errorf("a vouched-for count lets the haiku through: %q", b[0].Rule)
	}
	if b := Broken(f.Check("two lines\nonly"), true); len(b) != 1 {
		t.Error("vouching never excuses a line count")
	}
	if r := f.Check("one\ntwo\nthree")[1]; r.Note != "1-1-1 syllables" || r.Level != Sound {
		t.Errorf("the syllable note should say what was counted, at the sound level: %+v", r)
	}
	// The composer's meters: each line against its count.
	m := f.Measure("low tide at sunrise\na krab raises one wet\nhello to the gulls, friends")
	if len(m) != 3 || m[0].Syllables != 5 || !m[0].SyllablesOK || m[1].Syllables != 6 || m[1].SyllablesOK || m[2].Syllables != 6 || m[2].SyllablesOK {
		t.Errorf("measure: %+v", m)
	}
	if len(f.Measure("")) != 0 {
		t.Error("nothing to measure in an empty text")
	}
	if sf, _ := ByID("simile"); sf.Measure("as free as the sea") != nil {
		t.Error("a sentence has no lines to measure")
	}
}

func TestMeasureRhymes(t *testing.T) {
	c, _ := ByID("couplet")
	m := c.Measure("The tide goes out, the tide comes in,\nthe krab just sits and wears a grin.")
	if len(m) != 2 || m[0].Rhyme != "A" || !m[0].RhymeOK || m[1].Rhyme != "A" || !m[1].RhymeOK {
		t.Errorf("a rhyming couplet: %+v", m)
	}
	m = c.Measure("The tide goes out, the tide comes in,\nthe krab just sits there on the sand.")
	if len(m) != 2 || !m[0].RhymeOK || m[1].RhymeOK || m[1].Rhyme != "B" {
		t.Errorf("an unrhymed couplet marks the second line: %+v", m)
	}
	s, _ := ByID("sonnet")
	lines := strings.Split(strings.TrimSpace(s.Example), "\n")
	lines[1], lines[2] = lines[2], lines[1] // AABB: lines 2 and 3 land in the wrong place
	m = s.Measure(strings.Join(lines, "\n"))
	// The first line of a letter sets the sound; the lines after it are held to it.
	if len(m) != 14 || !m[0].RhymeOK || !m[1].RhymeOK || m[2].RhymeOK || m[3].RhymeOK || !m[13].RhymeOK {
		t.Errorf("a sonnet with two lines swapped: %+v", m[:4])
	}
	r, _ := ByID("rhyme")
	m = r.Measure("Roses by the sea\na krab in the sand\nwaves running free\nand nothing rhymes with orange")
	if len(m) != 4 || !m[0].RhymeOK || m[1].RhymeOK || !m[2].RhymeOK || m[3].RhymeOK {
		t.Errorf("verse: a line without a partner is marked: %+v", m)
	}
	m = r.Measure("Roses by the sea\na krab in the sand\nwaves running free\na claw in my hand\nthe gulls all agree")
	if len(m) != 5 || !m[4].RhymeOK {
		t.Errorf("a fifth line of verse takes the last spec: %+v", m)
	}
	if e, _ := ByID("envelope"); !e.Shape.Seal || !e.Shape.Grow || e.Shape.Lines[0].Rhyme != "" {
		t.Error("an envelope grows, seals, and asks no rhyme")
	}
}

func TestCouplet(t *testing.T) {
	if broken, hints := check(t, "couplet", "The tide goes out, the tide comes in,\nthe krab just sits and wears a grin."); len(broken)+len(hints) != 0 {
		t.Errorf("a good couplet: %q %q", broken, hints)
	}
	if broken, _ := check(t, "couplet", "one line"); len(broken) == 0 || broken[0] != "Two lines." {
		t.Errorf("one line: %q", broken)
	}
	if broken, hints := check(t, "couplet", "The tide goes out and in,\nthe krab just sits there on the sand all day."); len(broken) != 1 || len(hints) != 1 {
		t.Errorf("no rhyme holds a couplet back, uneven lines only nudge: %q %q", broken, hints)
	}
}

func TestSonnet(t *testing.T) {
	f, _ := ByID("sonnet")
	if broken, _ := check(t, "sonnet", f.Example); len(broken) != 0 {
		t.Errorf("the example sonnet: %q", broken)
	}
	if broken, _ := check(t, "sonnet", strings.Repeat("a line\n", 13)); len(broken) == 0 || broken[0] != "Fourteen lines." {
		t.Errorf("thirteen lines: %q", broken)
	}
	lines := strings.Split(strings.TrimSpace(f.Example), "\n")
	lines[1], lines[2] = lines[2], lines[1] // the rhymes stay but the scheme becomes AABB
	broken, _ := check(t, "sonnet", strings.Join(lines, "\n"))
	if !slices.ContainsFunc(broken, func(h string) bool { return strings.Contains(h, "ABAB") }) {
		t.Errorf("a broken scheme holds the sonnet back: %q", broken)
	}
}

func TestLimerickEnvelopeAndRhyme(t *testing.T) {
	f, _ := ByID("limerick")
	if broken, hints := check(t, "limerick", f.Example); len(broken)+len(hints) != 0 {
		t.Errorf("the example limerick: %q %q", broken, hints)
	}
	if broken, _ := check(t, "limerick", "one\ntwo\nthree\nfour"); len(broken) == 0 || broken[0] != "Five lines." {
		t.Errorf("four lines: %q", broken)
	}

	if broken, _ := check(t, "envelope", "The sea comes back.\nIt always does, it always did,\nwhatever the moon had hid.\nThe sea comes back!"); len(broken) != 0 {
		t.Errorf("a good envelope (same first and last line, ignoring punctuation and case): %q", broken)
	}
	if broken, _ := check(t, "envelope", "The sea comes back.\nIt always does.\nIt always did.\nThe moon hid."); len(broken) != 1 {
		t.Errorf("an open envelope: %q", broken)
	}
	if broken, _ := check(t, "envelope", "The sea\nThe sea"); len(broken) != 1 {
		t.Errorf("a two-line envelope: %q", broken)
	}

	broken, hints := check(t, "rhyme", "Roses by the sea\na krab in the sand\nwaves running free\na claw in my hand")
	if len(broken)+len(hints) != 0 {
		t.Errorf("ABAB verse: %q %q", broken, hints)
	}
	rf, _ := ByID("rhyme")
	if r := rf.Check("Roses by the sea\na krab in the sand\nwaves running free\na claw in my hand")[1]; r.Note != "ABAB" {
		t.Errorf("the note should show the scheme: %q", r.Note)
	}
	if broken, _ := check(t, "rhyme", "Roses by the sea\na krab in the sand\nwaves running free\nand nothing rhymes with orange"); len(broken) != 1 {
		t.Errorf("a line without a partner holds the verse back: %q", broken)
	}
	if broken, _ := check(t, "rhyme", "one\ntwo"); len(broken) == 0 || broken[0] != "At least four lines." {
		t.Errorf("two lines: %q", broken)
	}
}

func TestFiguresOfSpeech(t *testing.T) {
	if broken, _ := check(t, "simile", "Her laugh was like a gull over the pier."); len(broken) != 0 {
		t.Errorf("simile with like: %q", broken)
	}
	if broken, _ := check(t, "simile", "The pier was as quiet as a closed shell."); len(broken) != 0 {
		t.Errorf("simile with as: %q", broken)
	}
	if broken, _ := check(t, "simile", "The pier was quiet. I liked it."); len(broken) != 1 {
		t.Errorf("no comparison (and 'liked' isn't 'like'): %q", broken)
	}
	if _, hints := check(t, "simile", "One. Two. Three sentences, like this."); len(hints) != 1 {
		t.Errorf("three sentences: %q", hints)
	}

	if broken, hints := check(t, "metaphor", "The city is a tide pool."); len(broken)+len(hints) != 0 {
		t.Errorf("a metaphor: %q %q", broken, hints)
	}
	if _, hints := check(t, "metaphor", "The city is like a tide pool."); len(hints) != 1 {
		t.Errorf("a simile in metaphor's clothing: %q", hints)
	}
	if _, hints := check(t, "metaphor", "Tide pools everywhere."); len(hints) != 1 {
		t.Errorf("nothing is said to be anything: %q", hints)
	}

	if _, hints := check(t, "analogy", "Tides are to the shore as deadlines are to a writer."); len(hints) != 0 {
		t.Errorf("an A is to B as C is to D analogy: %q", hints)
	}
	if _, hints := check(t, "analogy", "Just as the tide rearranges the shore, so a deadline rearranges a week."); len(hints) != 0 {
		t.Errorf("a just-as analogy: %q", hints)
	}
	if _, hints := check(t, "analogy", "Deadlines are bad."); len(hints) != 1 {
		t.Errorf("no analogy: %q", hints)
	}
}

func TestLetter(t *testing.T) {
	good := "Dear Squidward,\n\nThe reef is quieter without your clarinet.\n\nYours,\nSpongeBob"
	if broken, _ := check(t, "letter", good); len(broken) != 0 {
		t.Errorf("a letter: %q", broken)
	}
	if broken, _ := check(t, "letter", "Hey Squidward\n\nCome home.\n\nLove,\nSpongeBob"); len(broken) != 0 {
		t.Errorf("a letter opening with Hey: %q", broken)
	}
	if broken, _ := check(t, "letter", "The reef is quiet.\n\nYours,\nSpongeBob"); len(broken) != 1 || !strings.Contains(broken[0], "greeting") {
		t.Errorf("no greeting: %q", broken)
	}
	if broken, _ := check(t, "letter", "Dear Squidward,\n\nThe reef is quiet and I'm sad about it, so come home soon."); len(broken) != 1 || !strings.Contains(broken[0], "Sign off") {
		t.Errorf("no sign-off: %q", broken)
	}
}

func TestBlogAndEssay(t *testing.T) {
	post := "Why krabs walk sideways\n\nIt isn't stubbornness. Our legs bend that way.\n\nSo next time, step aside and let us pass."
	if broken, _ := check(t, "blog", post); len(broken) != 0 {
		t.Errorf("a blog post: %q", broken)
	}
	if broken, _ := check(t, "blog", "This is a whole paragraph that runs on and on without any title at the top, which is the whole problem here.\n\nSecond paragraph."); len(broken) != 1 {
		t.Errorf("no title: %q", broken)
	}
	if broken, _ := check(t, "blog", "A title\n\nOne paragraph only."); len(broken) != 1 {
		t.Errorf("one paragraph: %q", broken)
	}

	essay := "On tides\n\nThe tide is the only clock a krab needs.\n\nFirst, it tells the time.\n\nSecond, it tells the weather.\n\nSo throw away your watch."
	if broken, _ := check(t, "essay", essay); len(broken) != 0 {
		t.Errorf("an essay: %q", broken)
	}
	if broken, _ := check(t, "essay", "On tides\n\nIntro.\n\nBody.\n\nConclusion."); len(broken) != 1 {
		t.Errorf("three paragraphs: %q", broken)
	}
}

func TestThreePanel(t *testing.T) {
	joke := "A krab walks into a bar and orders a drink.\n\nThe bartender slides it down the counter, right past him.\n\n\"Sideways,\" sighs the krab. \"Always sideways.\""
	if broken, hints := check(t, "three-panel", joke); len(broken)+len(hints) != 0 {
		t.Errorf("a three-panel joke: %q %q", broken, hints)
	}
	if broken, _ := check(t, "three-panel", "Setup.\n\nPunchline."); len(broken) != 1 {
		t.Errorf("two panels: %q", broken)
	}
	if _, hints := check(t, "three-panel", "Setup.\n\nBuild.\n\nA punchline that goes on far longer than the panel before it."); len(hints) != 1 {
		t.Errorf("a long punchline: %q", hints)
	}
}

func TestPlainNoteChecksNothing(t *testing.T) {
	f, _ := ByID("")
	if got := f.Check("anything at all"); len(got) != 0 {
		t.Errorf("the note has no rules to check: %v", got)
	}
	if Broken(nil, false) != nil {
		t.Error("Broken(nil) must be nil")
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
	h, _ := ByID("haiku")
	if b := Broken(h.Check(Haiku(fillers, []string{"gm crabs"}, []string{"a new morning on the reef"}, []string{"low tide and big plans"})), false); len(b) != 0 {
		t.Errorf("a fitted haiku passes the haiku's own check: %s (%s)", b[0].Rule, b[0].Note)
	}
}

func TestLayouts(t *testing.T) {
	ids := regexp.MustCompile(`^[a-z-]+$`)
	modes := []string{ModeLines, ModeDropped, ModeCentered, ModeInline, ModeCounts, ModeSpaced, ModeIndented, ModeSignOff, ModeStrip}
	for _, f := range Forms() {
		seen := map[string]bool{}
		for _, l := range f.Layouts {
			if !ids.MatchString(l.ID) || seen[l.ID] || l.Name == "" || l.Note == "" || !slices.Contains(modes, l.Mode) {
				t.Errorf("form %q: bad layout %+v", f.ID, l)
			}
			seen[l.ID] = true
			if n := len(l.Indents); n > 0 && len(f.Shape.Lines) > 0 && !f.Shape.Grow && n != len(f.Shape.Lines) {
				t.Errorf("form %q layout %q: %d indents for %d lines", f.ID, l.ID, n, len(f.Shape.Lines))
			}
			// A layout only changes the look: the example still passes, laid out any way.
			if got := len(f.Set(l.ID, f.Example)); got != len(Lines(f.Example)) {
				t.Errorf("form %q layout %q: %d line sets for %d lines", f.ID, l.ID, got, len(Lines(f.Example)))
			}
		}
	}
	for _, id := range []string{"", "simile", "metaphor", "analogy"} {
		if f, _ := ByID(id); len(f.Layouts) != 0 {
			t.Errorf("%q should have no layouts", id)
		}
	}
	h, _ := ByID("haiku")
	if l, ok := h.Layout("stepped"); !ok || l.Name != "Stepped" {
		t.Errorf("stepped: %+v", l)
	}
	if l, ok := h.Layout("strip"); ok || l.ID != "flush" {
		t.Errorf("a layout the haiku doesn't have gives its default: %+v %v", l, ok)
	}
	if !Titled("On tides\n\nThe tide.") || Titled("two lines\nwith no title") {
		t.Error("Titled")
	}
}

func TestSet(t *testing.T) {
	indents := func(s []LineSet) []int {
		out := make([]int, len(s))
		for i, l := range s {
			out[i] = l.Indent
		}
		return out
	}
	where := func(s []LineSet, pick func(LineSet) bool) []int {
		var out []int
		for i, l := range s {
			if pick(l) {
				out = append(out, i)
			}
		}
		return out
	}
	gaps := func(s []LineSet) []int { return where(s, func(l LineSet) bool { return l.Gap }) }

	h, _ := ByID("haiku")
	if got := indents(h.Set("stepped", h.Example)); !slices.Equal(got, []int{0, 1, 2}) {
		t.Errorf("stepped: %v", got)
	}
	if got := indents(h.Set("flush", h.Example)); !slices.Equal(got, []int{0, 0, 0}) {
		t.Errorf("flush: %v", got)
	}
	if got := indents(h.Set("nonsense", h.Example)); !slices.Equal(got, []int{0, 0, 0}) {
		t.Errorf("an unknown layout sets every line flush: %v", got)
	}
	// The writer's own stanza break is kept, in any layout.
	if got := gaps(h.Set("stepped", "low tide at sunrise\n\na krab raises one wet claw\nhello to the gulls")); !slices.Equal(got, []int{0}) {
		t.Errorf("the writer's stanza break: %v", got)
	}

	c, _ := ByID("couplet")
	if got := indents(c.Set("second-in", c.Example)); !slices.Equal(got, []int{0, 1}) {
		t.Errorf("second line in: %v", got)
	}
	l, _ := ByID("limerick")
	if got := indents(l.Set("short-in", l.Example)); !slices.Equal(got, []int{0, 0, 1, 1, 0}) {
		t.Errorf("short lines in: %v", got)
	}
	if got := where(l.Set("lear", l.Example), func(l LineSet) bool { return l.Join }); !slices.Equal(got, []int{2}) {
		t.Errorf("Lear runs line 3 into line 4: %v", got)
	}
	s, _ := ByID("sonnet")
	if got := gaps(s.Set("quatrains", s.Example)); !slices.Equal(got, []int{3, 7, 11}) {
		t.Errorf("quatrains: %v", got)
	}
	cin := s.Set("couplet-in", s.Example)
	if got := where(cin, func(l LineSet) bool { return l.Indent > 0 }); !slices.Equal(got, []int{12, 13}) || !slices.Equal(gaps(cin), []int{3, 7, 11}) {
		t.Errorf("couplet in: %+v", cin)
	}
	e, _ := ByID("envelope")
	if got := indents(e.Set("framed", e.Example)); !slices.Equal(got, []int{0, 1, 1, 1, 0}) {
		t.Errorf("framed: %v", got)
	}
	r, _ := ByID("rhyme")
	if got := indents(r.Set("by-rhyme", "Roses by the sea\na krab in the sand\nwaves running free\na claw in my hand")); !slices.Equal(got, []int{0, 1, 0, 1}) {
		t.Errorf("by rhyme, ABAB: %v", got)
	}
	if got := indents(r.Set("by-rhyme", r.Example)); !slices.Equal(got, []int{0, 0, 1, 1}) {
		t.Errorf("by rhyme, AABB: %v", got)
	}
	if got := indents(r.Set("by-rhyme", "The sea comes back to me\nit always does, it did\nwhatever the moon had hid\nand sets the sea gulls free")); !slices.Equal(got, []int{0, 1, 1, 0}) {
		t.Errorf("by rhyme, ABBA: %v", got)
	}
	if len(h.Set("stepped", "")) != 0 {
		t.Error("nothing to set in an empty text")
	}
}
