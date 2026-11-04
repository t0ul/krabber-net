package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"github.com/t0ul/krabber-net/internal/store"
	"github.com/t0ul/krabber-net/internal/validator"
	"github.com/t0ul/krabber-net/internal/writing"
)

// moltCheck is what the composer's feedback box shows: the chosen writing
// form, how the text measures up to each of its rules, and why the molt
// can't be posted yet.
type moltCheck struct {
	Form    writing.Form
	Results []writing.Result
	Lines   []writing.LineMeasure // each line's syllables and rhyme, for the composer's meters
	Blank   bool                  // nothing written yet, so the rules show without marks
	Message string                // why the text can't be posted; "" when it can
	// Overridable: only sound rules (syllables, rhyme) hold the molt back,
	// and the writer may post it anyway by vouching for their own count.
	Overridable bool
}

// shapesJSON is every form's shape by ID, for the composer's script.
var shapesJSON = func() template.JS {
	shapes := map[string]writing.Shape{}
	for _, f := range writing.Forms() {
		if !f.Shape.Free() {
			shapes[f.ID] = f.Shape
		}
	}
	b, err := json.Marshal(shapes)
	if err != nil {
		panic(err)
	}
	return template.JS(b) //nolint:gosec // our own catalogue, marshalled by encoding/json
}()

// jsonAttr marshals v for a data attribute; the template escapes it.
func jsonAttr(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

// formAllowed reports whether a molt may take form f when forms are required
// (MOLT_FORMS=required): any form in the catalogue, letters and essays
// included. For a poetry-only site, make it f.Group == writing.Poetry.
func formAllowed(f writing.Form) bool { return f.ID != "" }

// defaultForm is the form the composer starts on when forms are required.
const defaultForm = "haiku"

// pickable lists the forms the composer offers under a group: all of them,
// or only those formAllowed admits when forms are required.
func pickable(group string, required bool) []writing.Form {
	forms := writing.InGroup(group)
	if !required {
		return forms
	}
	var out []writing.Form
	for _, f := range forms {
		if formAllowed(f) {
			out = append(out, f)
		}
	}
	return out
}

// freshCheck is what a composer's feedback box shows before anything is
// written: nothing for a plain note, otherwise the rules of the form the
// picker starts on.
func freshCheck(id string) moltCheck {
	f, ok := writing.ByID(id)
	if !ok || f.ID == "" {
		return moltCheck{}
	}
	return moltCheck{Form: f, Results: f.Check(""), Blank: true}
}

// startForm is the form a krab's new molts start on: their default, a haiku
// unless they chose otherwise in their settings. A choice the catalogue no
// longer has, or a plain note where forms are required, falls back to a
// haiku.
func (app *App) startForm(c *store.Crab) string {
	if c == nil {
		return ""
	}
	id := c.DefaultForm
	switch id {
	case "":
		id = defaultForm
	case store.FormNote:
		id = ""
	}
	if f, ok := writing.ByID(id); !ok || (app.cfg.FormsRequired() && !formAllowed(f)) {
		return defaultForm
	}
	return id
}

// checkMolt checks a molt's text against the writing form it names: the
// form must exist, the text can't be blank or longer than the form allows,
// no exact rule may be broken, and the sound rules (syllables, rhyme) must
// hold unless the writer vouched for their own count (f.Override). When
// forms are required, a plain note is refused as well.
func (app *App) checkMolt(f moltForm) moltCheck {
	return checkMoltForm(f, app.cfg.FormsRequired())
}

// checkMoltForm is checkMolt with the site's mode spelled out.
func checkMoltForm(f moltForm, required bool) moltCheck {
	form, ok := writing.ByID(f.Form)
	if !ok {
		return moltCheck{Message: "That is not a form Krabber knows."}
	}
	if required && !formAllowed(form) {
		if form.ID == "" {
			return moltCheck{Message: "Pick a form."}
		}
		return moltCheck{Message: fmt.Sprintf("Pick another form: %s is not written here.", strings.ToLower(formSubject(form)))}
	}
	c := moltCheck{Form: form, Results: form.Check(f.Content), Lines: form.Measure(f.Content), Blank: !validator.NotBlank(f.Content)}
	switch {
	case c.Blank:
		c.Message = "Molt text cannot be blank."
	case !validator.MaxChars(f.Content, form.MaxChars):
		c.Message = fmt.Sprintf("%s can be up to %s characters.", formSubject(form), commas(form.MaxChars))
	default:
		if broken := writing.Broken(c.Results, f.Override); len(broken) > 0 {
			rule := strings.TrimSuffix(broken[0].Rule, ".")
			if broken[0].Note != "" {
				rule += " (" + broken[0].Note + ")"
			}
			c.Message = fmt.Sprintf("Not quite %s yet: %s.", strings.ToLower(formSubject(form)), rule)
			c.Overridable = len(writing.Broken(c.Results, true)) == 0
		}
	}
	return c
}

// formSubject names a form as the subject of a sentence: "Molts", "A haiku",
// "An essay".
func formSubject(f writing.Form) string {
	if f.ID == "" {
		return "Molts"
	}
	if strings.ContainsRune("aeiou", rune(strings.ToLower(f.Name)[0])) {
		return "An " + strings.ToLower(f.Name)
	}
	return "A " + strings.ToLower(f.Name)
}

// rejectMolt answers a compose or reply the check refused. htmx gets the
// checklist, aimed at the form's own feedback box instead of the feed; a
// plain post gets the reason.
func (app *App) rejectMolt(w http.ResponseWriter, r *http.Request, c moltCheck) {
	if !isHTMX(r) {
		http.Error(w, c.Message, http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("HX-Retarget", "find .kb-feedback")
	w.Header().Set("HX-Reswap", "outerHTML")
	app.renderTemplate(w, r, http.StatusUnprocessableEntity, fragmentPage, "form-feedback", c)
}

// moltCheckPost is the composer's live check: the checklist for the text as
// it stands, a moment after typing pauses. It reads nothing from the table.
func (app *App) moltCheckPost(w http.ResponseWriter, r *http.Request) {
	f, ok := app.decodeMoltForm(w, r)
	if !ok {
		return
	}
	c := app.checkMolt(f)
	if c.Blank { // nothing to say about an empty box; the rules alone show
		c.Message = ""
	}
	app.renderTemplate(w, r, http.StatusOK, fragmentPage, "form-feedback", c)
}

// formsPage explains every writing form and its rules.
func (app *App) formsPage(w http.ResponseWriter, r *http.Request) {
	data := app.newTemplateData(r)
	app.describe(r, &data, "Every form a molt can take, with its rules and an example: haiku, couplet, limerick, sonnet, simile, metaphor, letter, blog post, essay and the three-panel joke.", "website")
	app.render(w, r, http.StatusOK, "forms.html", data)
}

// formOf is the writing form a molt names; the plain note for "", and an
// empty form (no name, no rules) for an ID the catalogue no longer has.
func formOf(id string) writing.Form {
	f, _ := writing.ByID(id)
	return f
}

// verse reports whether a form is laid out as poetry.
func verse(id string) bool {
	f, ok := writing.ByID(id)
	return ok && f.Group == writing.Poetry
}

// Feeds fold a molt longer than this, in characters or lines, behind a
// "read the whole thing" link; the thread page shows it all.
const (
	foldChars = 700
	foldLines = 16
)

// folded reports whether a feed shows only the start of a molt. Templates
// hold molts both by value and by pointer.
func folded(m any) bool {
	var content string
	switch m := m.(type) {
	case store.Molt:
		content = m.Content
	case *store.Molt:
		if m == nil {
			return false
		}
		content = m.Content
	default:
		return false
	}
	return !validator.MaxChars(content, foldChars) || strings.Count(content, "\n") >= foldLines
}
