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
		if broken := Broken(f.Check(f.Example)); len(broken) > 0 {
			t.Errorf("form %q: its own example breaks %q", f.ID, broken[0].Rule)
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
		case !r.Checked || r.OK:
		case r.Hint:
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
	if len(broken) != 1 || !strings.Contains(broken[0], "Three lines") {
		t.Errorf("a two-line haiku: broken %q", broken)
	}
	broken, hints = check(t, "haiku", "this line has too many syllables in it\nseven here, if you count them out\nfive to finish up")
	if len(broken) != 0 || len(hints) != 1 {
		t.Errorf("wrong syllables are a hint, not a block: broken %q, hints %q", broken, hints)
	}
	f, _ := ByID("haiku")
	if r := f.Check("one\ntwo\nthree")[1]; r.Note != "1-1-1 syllables" {
		t.Errorf("the syllable note should say what was counted: %q", r.Note)
	}
}

func TestCouplet(t *testing.T) {
	if broken, hints := check(t, "couplet", "The tide goes out, the tide comes in,\nthe krab just sits and wears a grin."); len(broken)+len(hints) != 0 {
		t.Errorf("a good couplet: %q %q", broken, hints)
	}
	if broken, _ := check(t, "couplet", "one line"); len(broken) != 1 {
		t.Errorf("one line: %q", broken)
	}
	if _, hints := check(t, "couplet", "The tide goes out and in,\nthe krab just sits there on the sand all day."); len(hints) != 2 {
		t.Errorf("no rhyme and uneven lines should be two hints: %q", hints)
	}
}

func TestSonnet(t *testing.T) {
	f, _ := ByID("sonnet")
	if broken, _ := check(t, "sonnet", f.Example); len(broken) != 0 {
		t.Errorf("the example sonnet: %q", broken)
	}
	if broken, _ := check(t, "sonnet", strings.Repeat("a line\n", 13)); len(broken) != 1 {
		t.Errorf("thirteen lines: %q", broken)
	}
	lines := strings.Split(strings.TrimSpace(f.Example), "\n")
	lines[1], lines[2] = lines[2], lines[1] // the rhymes stay but the scheme becomes AABB
	_, hints := check(t, "sonnet", strings.Join(lines, "\n"))
	if !slices.ContainsFunc(hints, func(h string) bool { return strings.Contains(h, "ABAB") }) {
		t.Errorf("a broken scheme should be hinted: %q", hints)
	}
}

func TestLimerickEnvelopeAndRhyme(t *testing.T) {
	f, _ := ByID("limerick")
	if broken, hints := check(t, "limerick", f.Example); len(broken)+len(hints) != 0 {
		t.Errorf("the example limerick: %q %q", broken, hints)
	}
	if broken, _ := check(t, "limerick", "one\ntwo\nthree\nfour"); len(broken) != 1 {
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

	_, hints := check(t, "rhyme", "Roses by the sea\na krab in the sand\nwaves running free\na claw in my hand")
	if len(hints) != 0 {
		t.Errorf("ABAB verse: %q", hints)
	}
	rf, _ := ByID("rhyme")
	if r := rf.Check("Roses by the sea\na krab in the sand\nwaves running free\na claw in my hand")[1]; r.Note != "ABAB" {
		t.Errorf("the note should show the scheme: %q", r.Note)
	}
	if _, hints := check(t, "rhyme", "Roses by the sea\na krab in the sand\nwaves running free\nand nothing rhymes with orange"); len(hints) != 1 {
		t.Errorf("a line without a partner: %q", hints)
	}
	if broken, _ := check(t, "rhyme", "one\ntwo"); len(broken) != 1 {
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
	if Broken(nil) != nil {
		t.Error("Broken(nil) must be nil")
	}
}
