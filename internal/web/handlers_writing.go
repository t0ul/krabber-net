package web

import (
	"fmt"
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
	Blank   bool   // nothing written yet, so the rules show without marks
	Message string // why the text can't be posted; "" when it can
}

// checkMolt checks a molt's text against the writing form it names: the
// form must exist, the text can't be blank or longer than the form allows,
// and no exact rule may be broken. Hints (syllables, rhyme) never stop a molt.
func checkMolt(f moltForm) moltCheck {
	form, ok := writing.ByID(f.Form)
	if !ok {
		return moltCheck{Message: "That is not a form Krabber knows."}
	}
	c := moltCheck{Form: form, Results: form.Check(f.Content), Blank: !validator.NotBlank(f.Content)}
	switch {
	case c.Blank:
		c.Message = "Molt text cannot be blank."
	case !validator.MaxChars(f.Content, form.MaxChars):
		c.Message = fmt.Sprintf("%s can be up to %s characters.", formSubject(form), commas(form.MaxChars))
	default:
		if broken := writing.Broken(c.Results); len(broken) > 0 {
			c.Message = fmt.Sprintf("Not quite %s yet: %s", strings.ToLower(formSubject(form)), broken[0].Rule)
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
	c := checkMolt(f)
	if c.Blank { // nothing to say about an empty box; the rules alone show
		c.Message = ""
	}
	app.renderTemplate(w, r, http.StatusOK, fragmentPage, "form-feedback", c)
}

// formsPage explains every writing form and its rules.
func (app *App) formsPage(w http.ResponseWriter, r *http.Request) {
	app.render(w, r, http.StatusOK, "forms.html", app.newTemplateData(r))
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
