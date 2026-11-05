// Package writing is the catalogue of forms a molt can take: a haiku, a
// sonnet, a simile, a letter, a three-panel joke. Each form has rules in plain
// words, a Shape the composer lays it out in, and a Check that tells the
// writer which rules their text follows.
//
// Rules come at levels. Exact rules (three lines, a blank line between
// panels) always hold a molt back. Sound rules (syllables, rhyme) are
// measured from English spelling, so they hold a molt back until the writer
// vouches for their own count. Nudges (lines about the same length) only
// show, and advice is just words.
package writing

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxLength is the longest text any form accepts, in characters; the plain
// note keeps the molt limit of 280.
const MaxLength = 8000

// Form is a way of writing a molt.
type Form struct {
	ID       string // "" for the plain note; otherwise lowercase letters and dashes
	Name     string
	Group    string // one of Groups(); "" for the plain note
	Tagline  string // what the form is for, in a line
	Example  string // a short example that follows every rule the check holds a molt to
	MaxChars int
	Rules    []string // the rules as shown to the writer, in order
	Shape    Shape    // how the composer lays the form out
	rules    []rule
}

// Shape is how the composer lays a form out as it's written: one box per
// line, each measured against what the line should sound like (a poem);
// named boxes joined by blank lines (a letter, a joke, an essay); or, when
// it has neither, one free box (a sentence, a note).
type Shape struct {
	Lines []LineSpec `json:"lines,omitempty"`
	// Grow lets the writer add lines past those listed (an envelope, rhymed
	// verse); the added lines take the last spec. 0 means the count is fixed.
	Grow bool `json:"grow,omitempty"`
	// Seal repeats the first line as the last (an envelope).
	Seal  bool       `json:"seal,omitempty"`
	Parts []PartSpec `json:"parts,omitempty"`
}

// Free reports whether the form is written in one box.
func (s Shape) Free() bool { return len(s.Lines) == 0 && len(s.Parts) == 0 }

// LineSpec is what one line of a poem should sound like.
type LineSpec struct {
	// Syllables is the count wanted, as a range; [0, 0] when it doesn't matter.
	Syllables [2]int `json:"syllables,omitempty"`
	// Rhyme is the letter of the scheme the line rhymes as ("A"), "*" when
	// it must rhyme with some other line, or "" when rhyme doesn't matter.
	Rhyme string `json:"rhyme,omitempty"`
}

// PartSpec is one box of a prose form.
type PartSpec struct {
	Label string `json:"label"`
	Hint  string `json:"hint,omitempty"`  // shown in the empty box
	Multi bool   `json:"multi,omitempty"` // several lines or paragraphs, not one line
}

// Level is how firmly a rule holds a molt back.
type Level int

// The levels, softest first.
const (
	Advice Level = iota // words for the writer; nothing is checked
	Nudge               // checked, but a miss only shows (lines about the same length)
	Sound               // syllables, rhyme: measured from spelling, so a miss holds the molt back until the writer vouches for the count
	Exact               // a line count, a greeting: a miss always holds the molt back
)

// Result is one rule of a form checked against a text.
type Result struct {
	Rule  string
	Level Level
	OK    bool   // followed as far as the check can tell; true for advice
	Note  string // what the check saw, for the writer ("6-7-5 syllables")
}

// Checked reports whether a machine could tell; otherwise the rule is advice.
func (r Result) Checked() bool { return r.Level != Advice }

// Soft reports whether a miss only nudges.
func (r Result) Soft() bool { return r.Level == Nudge }

type rule struct {
	text  string
	level Level
	check func(text string) (ok bool, note string) // nil: advice only
}

func advice(text string) rule { return rule{text: text} }

func must(text string, check func(string) (bool, string)) rule {
	return rule{text: text, level: Exact, check: check}
}

func sound(text string, check func(string) (bool, string)) rule {
	return rule{text: text, level: Sound, check: check}
}

func hint(text string, check func(string) (bool, string)) rule {
	return rule{text: text, level: Nudge, check: check}
}

// Check reports every rule of the form against text, in the order of Rules.
func (f Form) Check(text string) []Result {
	out := make([]Result, 0, len(f.rules))
	for _, r := range f.rules {
		res := Result{Rule: r.text, Level: r.level, OK: true}
		if r.check != nil {
			res.OK, res.Note = r.check(text)
		}
		out = append(out, res)
	}
	return out
}

// Broken returns the rules that hold the text back: every exact rule it
// misses, and the sound rules it misses unless the writer vouched for the
// sound (the counts are guesses from spelling, and the writer may know
// better).
func Broken(results []Result, vouched bool) []Result {
	var out []Result
	for _, r := range results {
		if !r.OK && (r.Level == Exact || (r.Level == Sound && !vouched)) {
			out = append(out, r)
		}
	}
	return out
}

// LineMeasure is what the counter hears in one line of a poem.
type LineMeasure struct {
	Syllables   int    `json:"syllables"`
	SyllablesOK bool   `json:"syllables_ok"` // within the line's range; true when the form sets none
	Rhyme       string `json:"rhyme"`        // the letter the line rhymes as, in the text's own scheme
	RhymeOK     bool   `json:"rhyme_ok"`     // rhymes as the form wants; true when the form doesn't care
}

// Measure listens to each line of text as a line of the form: its syllables
// against the line's count, and whether it rhymes with the lines the form
// pairs it with. Only forms written line by line measure anything.
func (f Form) Measure(text string) []LineMeasure {
	specs := f.Shape.Lines
	if len(specs) == 0 {
		return nil
	}
	lines := Lines(text)
	scheme := Scheme(lines)
	counts := map[byte]int{}
	for i := range len(scheme) {
		counts[scheme[i]]++
	}
	out := make([]LineMeasure, len(lines))
	for i, l := range lines {
		spec := specs[min(i, len(specs)-1)]
		if i >= len(specs) && !f.Shape.Grow {
			spec = LineSpec{}
		}
		m := LineMeasure{Syllables: LineSyllables(l), SyllablesOK: true, Rhyme: string(scheme[i]), RhymeOK: true}
		if spec.Syllables[1] > 0 {
			m.SyllablesOK = m.Syllables >= spec.Syllables[0] && m.Syllables <= spec.Syllables[1]
		}
		switch spec.Rhyme {
		case "":
		case "*":
			m.RhymeOK = counts[scheme[i]] > 1
		default:
			for j := range i {
				if partner := specs[min(j, len(specs)-1)]; partner.Rhyme == spec.Rhyme {
					m.RhymeOK = Rhymes(lines[j], l)
					break
				}
			}
		}
		out[i] = m
	}
	return out
}

// Group names, in display order.
const (
	Poetry  = "Poetry"
	Figures = "Figures of speech"
	Prose   = "Prose"
	Humor   = "Humor"
)

var groups = []string{Poetry, Figures, Prose, Humor}

// Groups lists the groups the forms are shown under, in order.
func Groups() []string { return groups }

// Forms lists every form, the plain note first.
func Forms() []Form { return forms }

// InGroup lists the forms shown under a group.
func InGroup(group string) []Form {
	var out []Form
	for _, f := range forms {
		if f.ID != "" && f.Group == group {
			out = append(out, f)
		}
	}
	return out
}

// ByID finds a form; "" is the plain note.
func ByID(id string) (Form, bool) {
	for _, f := range forms {
		if f.ID == id {
			return f, true
		}
	}
	return Form{}, false
}

func form(id, name, group, tagline string, maxChars int, example string, shape Shape, rules ...rule) Form {
	f := Form{ID: id, Name: name, Group: group, Tagline: tagline, Example: example, MaxChars: maxChars, Shape: shape, rules: rules}
	for _, r := range rules {
		f.Rules = append(f.Rules, r.text)
	}
	return f
}

// Lines are the non-blank lines of a text, trimmed.
func Lines(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// Stanzas are the runs of lines separated by blank lines: the stanzas of a
// poem, the paragraphs of prose, the panels of a joke.
func Stanzas(text string) [][]string {
	var out [][]string
	var cur []string
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			if len(cur) > 0 {
				out = append(out, cur)
				cur = nil
			}
			continue
		}
		cur = append(cur, l)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// letters lowercases a word and keeps only its letters, dropping a
// possessive 's.
func letters(word string) string {
	w := strings.ToLower(word)
	w = strings.TrimSuffix(strings.TrimSuffix(w, "'s"), "’s")
	var b strings.Builder
	for _, r := range w {
		if unicode.IsLetter(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// syllableExceptions are common words the spelling rules get wrong.
var syllableExceptions = map[string]int{
	"people": 2, "every": 3, "business": 2, "something": 2, "someone": 2, "everyone": 3,
}

func isVowel(c byte) bool { return strings.IndexByte("aeiouy", c) >= 0 }

// Syllables estimates the syllables in an English word from its spelling:
// vowel groups, less a silent e, plus the vowel pairs that are usually said
// as two. It's a guide for haiku and sonnets, not a dictionary.
func Syllables(word string) int {
	w := letters(word)
	if w == "" {
		return 0
	}
	if n, ok := syllableExceptions[w]; ok {
		return n
	}
	n := 0
	prev := false
	for i := 0; i < len(w); i++ {
		v := isVowel(w[i]) && (w[i] != 'y' || i != 0) // a leading y is a consonant (yes, yellow)
		if v && !prev {
			n++
		}
		prev = v
	}
	// Pairs that are usually two syllables (radio, dial, video, actual, poem),
	// except after the consonants that fuse them (nation, special, guard).
	for i := 0; i+1 < len(w); i++ {
		pair := w[i : i+2]
		before := byte(0)
		if i > 0 {
			before = w[i-1]
		}
		switch pair {
		case "io", "ia":
			if before != 't' && before != 's' && before != 'c' {
				n++
			}
		case "eo":
			n++
		case "ua":
			if before != 'g' && before != 'q' {
				n++
			}
		case "oe":
			if i+2 < len(w) && w[i+2:] != "s" { // not shoe, goes
				n++
			}
		}
	}
	last := w[len(w)-1]
	consonantBefore := func(i int) bool { return i > 0 && !isVowel(w[i-1]) }
	switch {
	case len(w) > 3 && strings.HasSuffix(w, "ed"):
		// loved, walked: the e is silent; wanted, needed, scuttled: it isn't.
		if c := w[len(w)-3]; c != 't' && c != 'd' && (c != 'l' || !consonantBefore(len(w)-3)) {
			n--
		}
	case len(w) > 3 && strings.HasSuffix(w, "es"):
		// waves, goes: silent; boxes, raises, wishes, candles: said.
		c := w[len(w)-3]
		if c != 's' && c != 'x' && c != 'z' && !strings.HasSuffix(w[:len(w)-2], "ch") && !strings.HasSuffix(w[:len(w)-2], "sh") &&
			(c != 'l' || !consonantBefore(len(w)-3)) {
			n--
		}
	case len(w) > 2 && last == 'e' && !isVowel(w[len(w)-2]):
		// tide, fire, the: silent; table, little: the -le is said.
		if w[len(w)-2] != 'l' || !consonantBefore(len(w)-2) {
			n--
		}
	}
	return max(n, 1)
}

// LineSyllables estimates the syllables in a line.
func LineSyllables(line string) int {
	n := 0
	for _, w := range strings.Fields(line) {
		n += Syllables(w)
	}
	return n
}

// lastWord is the last word of a line, letters only.
func lastWord(line string) string {
	words := strings.Fields(line)
	for i := len(words) - 1; i >= 0; i-- {
		if w := letters(words[i]); w != "" {
			return w
		}
	}
	return ""
}

// soundings respell the vowel groups that share a sound, so that sea and
// free, day and away, moon and soon come out the same.
var soundings = []struct{ from, to string }{
	{"igh", "i"}, {"ough", "u"}, {"oor", "or"}, {"oo", "u"}, {"ue", "u"}, {"ew", "u"},
	{"ea", "e"}, {"ee", "e"}, {"ie", "e"}, {"ei", "e"},
	{"ay", "a"}, {"ai", "a"}, {"ey", "a"}, {"y", "i"}, {"ck", "k"},
}

// rhymePart is the end of a word from its last vowel sound on, respelled.
func rhymePart(w string) string {
	if len(w) > 2 && w[len(w)-1] == 'e' && !isVowel(w[len(w)-2]) && w[len(w)-2] != 'l' {
		w = w[:len(w)-1] // silent e: love, time, white
	}
	for _, s := range soundings {
		w = strings.ReplaceAll(w, s.from, s.to)
	}
	for i := len(w) - 1; i >= 0; i-- {
		if isVowel(w[i]) {
			for i > 0 && isVowel(w[i-1]) {
				i--
			}
			return w[i:]
		}
	}
	return ""
}

// Rhymes reports whether two lines (or words) end in rhyme, as far as
// spelling can tell. Plurals rhyme with their singulars' rhymes (moons, tunes).
func Rhymes(a, b string) bool {
	wa, wb := lastWord(a), lastWord(b)
	if wa == "" || wb == "" {
		return false
	}
	if rhymePart(wa) == rhymePart(wb) {
		return true
	}
	strip := func(w string) string {
		if len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") {
			return w[:len(w)-1]
		}
		return w
	}
	return rhymePart(strip(wa)) == rhymePart(strip(wb))
}

// Scheme labels each line by what it rhymes with: "ABAB", "AABB", "ABBA".
func Scheme(lines []string) string {
	letters := make([]byte, 0, len(lines))
	next := byte('A')
	for i, l := range lines {
		var letter byte
		for j := range i {
			if Rhymes(lines[j], l) {
				letter = letters[j]
				break
			}
		}
		if letter == 0 {
			letter, next = next, next+1
			if next > 'Z' {
				next = 'a'
			}
		}
		letters = append(letters, letter)
	}
	return string(letters)
}

// FitLine fits a line to n syllables by this package's own counter, for
// writers that compose from data (the bots, @system). The first candidate
// that counts n wins. Otherwise the candidate nearest below n is padded with
// fillers (the largest that fits, each joined with a comma) up to n; when
// every candidate runs over, the shortest is cut word by word from its end
// until it fits, then padded. Give candidates best first.
func FitLine(n int, fillers []string, candidates ...string) string {
	best, bestN := "", -1
	shortest, shortN := "", int(^uint(0)>>1)
	for _, c := range candidates {
		k := LineSyllables(c)
		if k == n {
			return c
		}
		if k < n && k > bestN {
			best, bestN = c, k
		}
		if k < shortN {
			shortest, shortN = c, k
		}
	}
	line := best
	if bestN < 0 {
		line = shortest
		for LineSyllables(line) > n {
			words := strings.Fields(line)
			if len(words) <= 1 {
				break
			}
			line = strings.TrimRight(strings.Join(words[:len(words)-1], " "), ",;:")
		}
	}
	for {
		need := n - LineSyllables(line)
		if need <= 0 {
			break
		}
		pick, pickN := "", 0
		for _, f := range fillers {
			if k := LineSyllables(f); k > 0 && k <= need && k > pickN {
				pick, pickN = f, k
			}
		}
		if pick == "" {
			break
		}
		if strings.TrimSpace(line) == "" {
			line = pick
		} else {
			line = strings.TrimRight(line, ",;: ") + ", " + pick
		}
	}
	return line
}

// Haiku fits three lines to five, seven and five syllables, each from its
// own candidates (see FitLine).
func Haiku(fillers []string, first, second, third []string) string {
	return FitLine(5, fillers, first...) + "\n" + FitLine(7, fillers, second...) + "\n" + FitLine(5, fillers, third...)
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

func exactLines(n int) func(string) (bool, string) {
	return func(t string) (bool, string) {
		got := len(Lines(t))
		return got == n, plural(got, "line")
	}
}

func atLeastLines(n int) func(string) (bool, string) {
	return func(t string) (bool, string) {
		got := len(Lines(t))
		return got >= n, plural(got, "line")
	}
}

func exactStanzas(n int, what string) func(string) (bool, string) {
	return func(t string) (bool, string) {
		got := len(Stanzas(t))
		return got == n, plural(got, what)
	}
}

// syllablePattern checks each line's syllables against want and reports what
// it counted, "5-7-5 syllables".
func syllablePattern(want ...int) func(string) (bool, string) {
	return func(t string) (bool, string) {
		lines := Lines(t)
		counts := make([]string, 0, len(lines))
		ok := len(lines) == len(want)
		for i, l := range lines {
			n := LineSyllables(l)
			counts = append(counts, fmt.Sprint(n))
			if i >= len(want) || n != want[i] {
				ok = false
			}
		}
		return ok, strings.Join(counts, "-") + " syllables"
	}
}

// syllableRange checks that the lines picked by which (all when nil) run
// between lo and hi syllables.
func syllableRange(lo, hi int, which ...int) func(string) (bool, string) {
	return func(t string) (bool, string) {
		lines := Lines(t)
		pick := which
		if pick == nil {
			pick = make([]int, len(lines))
			for i := range lines {
				pick[i] = i
			}
		}
		for _, i := range pick {
			if i >= len(lines) {
				return false, plural(len(lines), "line")
			}
			if n := LineSyllables(lines[i]); n < lo || n > hi {
				return false, fmt.Sprintf("line %d has %s", i+1, plural(n, "syllable"))
			}
		}
		return true, fmt.Sprintf("lines of %d–%d syllables", lo, hi)
	}
}

// evenLines checks that no two lines differ by more than spread syllables.
func evenLines(spread int) func(string) (bool, string) {
	return func(t string) (bool, string) {
		lines := Lines(t)
		if len(lines) == 0 {
			return false, "no lines yet"
		}
		lo, hi := LineSyllables(lines[0]), LineSyllables(lines[0])
		for _, l := range lines[1:] {
			n := LineSyllables(l)
			lo, hi = min(lo, n), max(hi, n)
		}
		return hi-lo <= spread, fmt.Sprintf("lines of %d–%d syllables", lo, hi)
	}
}

// rhymePairs checks that each pair of lines (1-based) rhymes and reports the
// scheme it hears, spaced as given (4-4-4-2 for a sonnet).
func rhymePairs(spacing []int, pairs ...[2]int) func(string) (bool, string) {
	return func(t string) (bool, string) {
		lines := Lines(t)
		ok := true
		for _, p := range pairs {
			if p[0] > len(lines) || p[1] > len(lines) || !Rhymes(lines[p[0]-1], lines[p[1]-1]) {
				ok = false
			}
		}
		return ok, "sounds like " + spaced(Scheme(lines), spacing)
	}
}

// spaced breaks a scheme into groups: "ABABCDCDEFEFGG" → "ABAB CDCD EFEF GG".
func spaced(scheme string, groups []int) string {
	var parts []string
	for _, n := range groups {
		if len(scheme) == 0 {
			break
		}
		n = min(n, len(scheme))
		parts = append(parts, scheme[:n])
		scheme = scheme[n:]
	}
	if scheme != "" {
		parts = append(parts, scheme)
	}
	return strings.Join(parts, " ")
}

// everyLineRhymes checks that each line has a rhyme partner.
func everyLineRhymes(t string) (bool, string) {
	lines := Lines(t)
	scheme := Scheme(lines)
	counts := map[byte]int{}
	for i := range len(scheme) {
		counts[scheme[i]]++
	}
	for i := range len(scheme) {
		if counts[scheme[i]] < 2 {
			return false, scheme
		}
	}
	return len(lines) > 0, scheme
}

// enclosed checks that the first line comes back as the last.
func enclosed(t string) (bool, string) {
	lines := Lines(t)
	if len(lines) < 2 {
		return false, "needs a first and a last line"
	}
	first, last := Lines(lines[0]), Lines(lines[len(lines)-1])
	if lettersOf(first[0]) == lettersOf(last[0]) {
		return true, "the first line returns"
	}
	return false, "the last line is not the first"
}

func lettersOf(line string) string {
	var b strings.Builder
	for _, w := range strings.Fields(line) {
		b.WriteString(letters(w))
		b.WriteByte(' ')
	}
	return strings.TrimSpace(b.String())
}

var (
	likeOrAs    = regexp.MustCompile(`\b(like|as)\b`)
	linkingVerb = regexp.MustCompile(`\b(is|are|was|were|am|be|been|become|became)\b|'s\b|’s\b`)
	analogyRX   = regexp.MustCompile(`\b(is|are|was|were) to\b.*\bas\b.*\b(is|are|was|were) to\b|\bjust as\b.*,\s*so\b`)
	sentenceEnd = regexp.MustCompile(`[.!?]+`)
	greetingRX  = regexp.MustCompile(`^(dear|dearest|my dear|hi|hello|hey|to|yo)\b`)
	signOffRX   = regexp.MustCompile(`^(yours|love|sincerely|best|cheers|warmly|regards|kind regards|truly|faithfully|xo|xoxo|hugs|see you|take care|later|bye)\b`)
)

func contains(rx *regexp.Regexp, found, missing string) func(string) (bool, string) {
	return func(t string) (bool, string) {
		if rx.MatchString(strings.ToLower(t)) {
			return true, found
		}
		return false, missing
	}
}

func lacks(rx *regexp.Regexp, found, missing string) func(string) (bool, string) {
	return func(t string) (bool, string) {
		if rx.MatchString(strings.ToLower(t)) {
			return false, found
		}
		return true, missing
	}
}

func sentences(limit int) func(string) (bool, string) {
	return func(t string) (bool, string) {
		n := len(sentenceEnd.FindAllString(t, -1))
		if n == 0 && strings.TrimSpace(t) != "" {
			n = 1
		}
		return n <= limit, plural(n, "sentence")
	}
}

func greeting(t string) (bool, string) {
	lines := Lines(t)
	if len(lines) == 0 {
		return false, "no greeting yet"
	}
	first := strings.ToLower(lines[0])
	if greetingRX.MatchString(first) || strings.HasSuffix(first, ",") {
		return true, "opens with " + lines[0]
	}
	return false, "the first line isn't a greeting"
}

func signOff(t string) (bool, string) {
	lines := Lines(t)
	for i := max(1, len(lines)-2); i < len(lines); i++ {
		l := strings.ToLower(lines[i])
		if signOffRX.MatchString(l) || (strings.HasSuffix(l, ",") && len(strings.Fields(l)) <= 4) {
			return true, "signs off with " + lines[i]
		}
	}
	return false, "no sign-off at the end"
}

// titled reports whether the text opens with a title: a line of its own, up
// to 80 characters, followed by a blank line.
func titled(t string) bool {
	st := Stanzas(t)
	return len(st) > 0 && len(st[0]) == 1 && utf8.RuneCountInString(st[0][0]) <= 80
}

func title(t string) (bool, string) {
	if titled(t) {
		return true, "titled " + Stanzas(t)[0][0]
	}
	return false, "the first line should be a short title, with a blank line after it"
}

// paragraphs counts the stanzas after the title, when there is one.
func paragraphs(atLeast int) func(string) (bool, string) {
	return func(t string) (bool, string) {
		n := len(Stanzas(t))
		if titled(t) {
			n--
		}
		return n >= atLeast, plural(n, "paragraph")
	}
}

func shortPunchline(t string) (bool, string) {
	st := Stanzas(t)
	if len(st) < 2 {
		return false, "no punchline yet"
	}
	last, before := strings.Join(st[len(st)-1], " "), strings.Join(st[len(st)-2], " ")
	if utf8.RuneCountInString(last) < utf8.RuneCountInString(before) {
		return true, "the punchline is the shorter panel"
	}
	return false, "the punchline runs longer than the panel before it"
}

// metered builds the lines of a poem with a syllable count and a rhyme
// scheme: counts gives each line's range (one pair for all, or a pair per
// line), scheme a letter per line ("" for none).
func metered(scheme string, counts ...[2]int) []LineSpec {
	n := max(len(scheme), len(counts))
	out := make([]LineSpec, n)
	for i := range out {
		if len(counts) == 1 {
			out[i].Syllables = counts[0]
		} else if i < len(counts) {
			out[i].Syllables = counts[i]
		}
		if i < len(scheme) {
			out[i].Rhyme = string(scheme[i])
		}
	}
	return out
}

// rhymedLines are n lines that each need a rhyme partner somewhere.
func rhymedLines(n int, rhyme string) []LineSpec {
	out := make([]LineSpec, n)
	for i := range out {
		out[i].Rhyme = rhyme
	}
	return out
}

var (
	free      = Shape{}
	haikuForm = Shape{Lines: metered("", [2]int{5, 5}, [2]int{7, 7}, [2]int{5, 5})}
	couplet   = Shape{Lines: metered("AA")}
	limerick  = Shape{Lines: metered("AABBA", [2]int{7, 10}, [2]int{7, 10}, [2]int{4, 7}, [2]int{4, 7}, [2]int{7, 10})}
	sonnet    = Shape{Lines: metered("ABABCDCDEFEFGG", [2]int{9, 11})}
	envelope  = Shape{Lines: rhymedLines(4, ""), Grow: true, Seal: true}
	verse     = Shape{Lines: rhymedLines(4, "*"), Grow: true}
	letter    = Shape{Parts: []PartSpec{{Label: "Greeting", Hint: "Dear Squidward,"}, {Label: "Letter", Hint: "Say why you're writing, then what you want them to know.", Multi: true}, {Label: "Sign-off", Hint: "Yours,\nSpongeBob", Multi: true}}}
	blogShape = Shape{Parts: []PartSpec{{Label: "Title", Hint: "Why krabs walk sideways"}, {Label: "Post", Hint: "Paragraphs, with a blank line between them.", Multi: true}}}
	essayed   = Shape{Parts: []PartSpec{{Label: "Title", Hint: "On tides"}, {Label: "Essay", Hint: "Your thesis, at least two body paragraphs, and a conclusion, a blank line between each.", Multi: true}}}
	panels    = Shape{Parts: []PartSpec{{Label: "Setup", Hint: "A krab walks into a bar and orders a drink.", Multi: true}, {Label: "Build", Hint: "The bartender slides it down the counter, right past him.", Multi: true}, {Label: "Punchline", Hint: "\"Sideways,\" sighs the krab.", Multi: true}}}
)

var forms = []Form{
	{Name: "Note", Tagline: "Just a molt: a thought, a link, whatever's on your mind.", MaxChars: 280},

	form("haiku", "Haiku", Poetry, "Three short lines that catch one moment, the way a photo does.", 280,
		"low tide at sunrise\na krab raises one wet claw\nhello to the gulls", haikuForm,
		must("Three lines.", exactLines(3)),
		sound("Five, seven, five syllables.", syllablePattern(5, 7, 5)),
		advice("One moment, in the present tense; a season word if one fits."),
		advice("No title and no rhyme: let the last line turn the first two.")),

	form("couplet", "Couplet", Poetry, "Two lines that rhyme: a thought with a snap at the end.", 280,
		"The tide goes out, the tide comes in,\nthe krab just sits and wears a grin.", couplet,
		must("Two lines.", exactLines(2)),
		sound("The lines end in rhyme.", rhymePairs([]int{2}, [2]int{1, 2})),
		hint("Keep the lines about the same length.", evenLines(3)),
		advice("Say the ordinary thing in the first line and the surprise in the second.")),

	form("limerick", "Limerick", Poetry, "Five bouncing lines that end on a joke.", 400,
		"A krab who was tired of the shore\ndecided to look for some more.\nHe packed up his shell,\nand said, \"Wish me well,\"\nand scuttled straight out of the door.", limerick,
		must("Five lines.", exactLines(5)),
		sound("Lines 1, 2 and 5 rhyme; lines 3 and 4 rhyme (AABBA).", rhymePairs([]int{5}, [2]int{1, 2}, [2]int{1, 5}, [2]int{3, 4})),
		sound("Lines 1, 2 and 5 are long (7–10 syllables); 3 and 4 are short (4–7).", func(t string) (bool, string) {
			if ok, note := syllableRange(7, 10, 0, 1, 4)(t); !ok {
				return ok, note
			}
			return syllableRange(4, 7, 2, 3)(t)
		}),
		advice("Start with a person or a place; save the punch for the last line.")),

	form("sonnet", "Sonnet", Poetry, "Fourteen lines that build an argument and turn it at the end.", 1200,
		"When morning drags the water from the sand,\nthe krab comes out to see what tide has left:\na shell, a bottle, and a rubber band,\nand sorts them with a claw that's small but deft.\n"+
			"The gulls complain, the way that gulls complain,\nthe sun climbs up to do its daily chore,\nand no one asks the krab to come again,\nit only comes to tidy up the shore.\n"+
			"It cannot read the labels on the glass,\nit cannot tell a treasure from a toy,\nbut every morning, as the hours pass,\nit does the little job it does with joy.\n"+
			"So if your life feels small beside the sea,\nremember: tides go out, and so can we.", sonnet,
		must("Fourteen lines.", exactLines(14)),
		sound("Ten syllables a line: da-DUM, five times.", syllableRange(9, 11)),
		sound("Rhyme ABAB CDCD EFEF GG, ending on a couplet.", rhymePairs([]int{4, 4, 4, 2}, [2]int{1, 3}, [2]int{2, 4}, [2]int{5, 7}, [2]int{6, 8}, [2]int{9, 11}, [2]int{10, 12}, [2]int{13, 14})),
		advice("Turn around line nine: answer, twist or widen what came before, and let the couplet land it.")),

	form("envelope", "Envelope", Poetry, "A poem that closes on the line it opened with.", 800,
		"The sea comes back.\nIt always does, it always did,\nwhatever the moon had hid\nor the long day took, or the cold wind bid.\nThe sea comes back.", envelope,
		must("At least four lines.", atLeastLines(4)),
		must("The first line comes back as the last, sealing the poem.", enclosed),
		advice("Rhyme the lines in between if you like (ABBA), and let the return mean something new.")),

	form("rhyme", "Rhymed verse", Poetry, "Any length, any scheme, as long as every line finds its rhyme.", 1200,
		"The tide rolls in, the tide rolls out,\nthe gulls all argue, shriek and shout,\nthe krab says nothing, holds his ground,\nand that is how the day goes round.", verse,
		must("At least four lines.", atLeastLines(4)),
		sound("Every line rhymes with another; the scheme shows as you write.", everyLineRhymes),
		hint("Keep a steady beat: lines of about the same length.", evenLines(4)),
		advice("Pick a scheme (AABB, ABAB, ABBA) and hold it from stanza to stanza.")),

	form("simile", "Simile", Figures, "Show what something was like by setting it beside something else.", 280,
		"Her laugh was like a gull over the pier: loud, sudden and gone.", free,
		must("Compare one thing to another using like or as.", contains(likeOrAs, "has its like or as", "needs a like or an as")),
		hint("Keep it to a sentence or two.", sentences(2)),
		advice("Pick a comparison that shows how it felt, not only what it was.")),

	form("metaphor", "Metaphor", Figures, "Say one thing is another, and mean it.", 280,
		"The city is a tide pool: a thousand small lives waiting for the water to return.", free,
		hint("Say one thing is another.", contains(linkingVerb, "says what is what", "nothing is said to be anything yet")),
		hint("No like, no as: that would be a simile.", lacks(likeOrAs, "a like or an as makes it a simile", "no like or as")),
		advice("One image, carried all the way through.")),

	form("analogy", "Analogy", Figures, "Explain one thing through another the reader already knows.", 280,
		"Tides are to the shore as deadlines are to a writer: they arrive on schedule and leave everything rearranged.", free,
		hint("Set the two side by side: A is to B as C is to D, or just as A …, so C ….", contains(analogyRX, "both sides are named", "name both sides: A is to B as C is to D")),
		hint("Keep it to a sentence or two.", sentences(2)),
		advice("Say what the two share, and where the likeness stops.")),

	form("letter", "Letter", Prose, "Written to one person, with a greeting and a sign-off.", 2000,
		"Dear Squidward,\n\nThe reef is quieter without your clarinet. The gulls have stopped complaining and started looking worried. Come home before the tide turns.\n\nYours,\nSpongeBob", letter,
		must("Open with a greeting on its own line (Dear …, Hi …).", greeting),
		advice("Say why you're writing, then what you want them to know."),
		must("Sign off on its own line, with your name (Yours, … / Love, …).", signOff)),

	form("blog", "Blog post", Prose, "A titled piece with a point, for anyone passing by.", 5000,
		"Why krabs walk sideways\n\nIt isn't stubbornness, and it isn't a dance. Our legs bend that way, and the sand is easier to cross on the diagonal.\n\nSo next time one of us crosses your path, step aside and let us pass. We're not avoiding you. We're just built for the shore.", blogShape,
		must("A title on the first line, with a blank line after it.", title),
		must("At least two paragraphs, separated by blank lines.", paragraphs(2)),
		advice("Open with the point, close with what to do about it.")),

	form("essay", "Essay", Prose, "A titled argument: a thesis, the case for it, and where it leaves us.", 8000,
		"On tides\n\nThe tide is the only clock a krab needs, and it keeps better time than the ones on the pier.\n\nFirst, it tells the time: twice a day, without fail, the water comes and goes.\n\nSecond, it tells the weather: a tide that runs high and fast means a storm is on its way.\n\nSo throw away your watch, find a rock, and wait. The sea will tell you everything you need to know.", essayed,
		must("A title on the first line, with a blank line after it.", title),
		must("An introduction with your thesis, at least two body paragraphs, and a conclusion: four paragraphs or more.", paragraphs(4)),
		advice("Each paragraph makes one point and backs it up.")),

	form("three-panel", "Three-panel joke", Humor, "Setup, build, punchline: a comic strip in words, made for replies.", 500,
		"A krab walks into a bar and orders a drink.\n\nThe bartender slides it down the counter, right past him.\n\n\"Sideways,\" sighs the krab. \"Always sideways.\"", panels,
		must("Three panels, separated by blank lines: setup, build, punchline.", exactStanzas(3, "panel")),
		hint("Keep the punchline short: shorter than the panel before it.", shortPunchline),
		advice("Keep the setup ordinary; the surprise is the third panel's job.")),
}
