package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/t0ul/krabber-net/internal/store"
	"github.com/t0ul/krabber-net/internal/validator"
	"github.com/t0ul/krabber-net/internal/writing"
)

// exportForm is the Settings form that downloads what a krab has written.
type exportForm struct {
	Forms               []string `form:"form"`    // the writing forms to include, by ID; store.FormNote for plain notes
	Replies             bool     `form:"replies"` // their replies too
	Format              string   `form:"format"`  // a key of exportFormats
	validator.Validator `form:"-"`
}

// exportFormats are the files an export is written as: text to read, JSON
// to keep or carry somewhere else.
var exportFormats = map[string]struct{ contentType, ext string }{
	"txt":  {"text/plain; charset=utf-8", "txt"},
	"json": {"application/json", "json"},
}

// export is everything one download holds.
type export struct {
	Krab    *store.Crab
	When    time.Time
	Forms   []string // the forms asked for, as the page names them (store.FormNote for notes), sorted
	Replies bool
	Molts   []store.Molt // in the order the file lists them: form by form, oldest first
}

// settingsExportPost writes the signed-in krab's molts to a file they
// download: the forms they ticked, with or without their replies, as text or
// JSON. Remolts are other krabs' words, so they're never in it. It reads the
// krab's whole timeline, so it's counted like a write.
func (app *App) settingsExportPost(w http.ResponseWriter, r *http.Request) {
	var f exportForm
	if err := app.decodePostForm(w, r, &f); err != nil {
		app.clientError(w, http.StatusBadRequest)
		return
	}
	format, ok := exportFormats[f.Format]
	if !ok {
		app.clientError(w, http.StatusBadRequest)
		return
	}
	c := currentCrab(r)
	chosen, names := chosenForms(f.Forms)
	if len(chosen) == 0 {
		f.AddFieldError("form", "Pick at least one form.")
		app.renderSettings(w, r, http.StatusUnprocessableEntity, settingsForms{Profile: profileFormFor(c.Profile), Export: f})
		return
	}
	if !app.underWriteLimit(w, r, "export") {
		return
	}

	// With every form ticked, a molt in a form the catalogue no longer has
	// comes along too: everything means everything.
	all := len(chosen) == len(writing.Forms())
	x := export{Krab: c, When: time.Now().UTC(), Forms: names, Replies: f.Replies, Molts: []store.Molt{}}
	err := app.store.EachMoltByOwner(r.Context(), c.ID, f.Replies, func(m store.Molt) error {
		if all || chosen[m.Form] {
			x.Molts = append(x.Molts, m)
		}
		return nil
	})
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	if err := app.withQuoted(r, x.Molts); err != nil {
		app.serverError(w, r, err)
		return
	}
	sortExport(x.Molts)

	// Written whole before the first byte goes out, so a failure can't leave
	// half a file behind a 200.
	var buf bytes.Buffer
	if f.Format == "json" {
		err = app.writeExportJSON(&buf, x)
	} else {
		app.writeExportText(&buf, x)
	}
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", format.contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="krabber-%s-%s.%s"`, c.UserName, x.When.Format("2006-01-02"), format.ext))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = buf.WriteTo(w)
}

// chosenForms reads the ticked forms: the set of form IDs to keep ("" for a
// plain note) and the names the file lists them by, sorted. A value that
// isn't a form is dropped.
func chosenForms(values []string) (map[string]bool, []string) {
	set := map[string]bool{}
	var names []string
	for _, v := range values {
		id, name := v, v
		if v == store.FormNote || v == "" {
			id, name = "", store.FormNote
		}
		if _, ok := writing.ByID(id); !ok || set[id] {
			continue
		}
		set[id] = true
		names = append(names, name)
	}
	sort.Strings(names)
	return set, names
}

// formRank orders the forms the way the forms page lists them, group by
// group, with the plain note last. A form the catalogue no longer has sorts
// before the note.
var formRank = func() map[string]int {
	rank := map[string]int{}
	for _, g := range writing.Groups() {
		for _, f := range writing.InGroup(g) {
			rank[f.ID] = len(rank) + 1
		}
	}
	rank[""] = len(rank) + 2
	return rank
}()

func rankOf(formID string) int {
	if r, ok := formRank[formID]; ok {
		return r
	}
	return formRank[""] - 1
}

// sortExport puts molts in the order the file lists them: form by form, the
// way the forms page lists them, and oldest first within a form.
func sortExport(molts []store.Molt) {
	sort.Slice(molts, func(i, j int) bool {
		a, b := molts[i], molts[j]
		if ra, rb := rankOf(a.Form), rankOf(b.Form); ra != rb {
			return ra < rb
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.ID < b.ID
	})
}

// formName names a molt's form for a reader: Note for a plain molt, and the
// ID itself for a form the catalogue no longer has.
func formName(id string) string {
	if id == "" {
		return "Note"
	}
	if f, ok := writing.ByID(id); ok {
		return f.Name
	}
	return id
}

// writeExportText writes the molts as a file to read: a heading, then a
// section for each form, each molt under a line giving its date, form,
// layout and link, with its words exactly as written.
func (app *App) writeExportText(w *bytes.Buffer, x export) {
	fmt.Fprintf(w, "Molts by @%s", x.Krab.UserName)
	if name := x.Krab.Name(); name != x.Krab.UserName {
		fmt.Fprintf(w, " (%s)", name)
	}
	fmt.Fprintf(w, " on Krabber\n%s\n", app.absolute("/krabs/"+x.Krab.UserName))
	fmt.Fprintf(w, "Exported %s: %d %s", x.When.Format("January 2, 2006"), len(x.Molts), plural(len(x.Molts), "molt", "molts"))
	if x.Replies {
		w.WriteString(", replies included")
	}
	w.WriteString(".\n")

	counts := map[string]int{}
	for _, m := range x.Molts {
		counts[m.Form]++
	}
	const rule = "========================================"
	form, started := "", false
	for _, m := range x.Molts {
		if !started || m.Form != form {
			form, started = m.Form, true
			title := strings.ToUpper(formName(form))
			if form == "" {
				title = "NOTES"
			}
			fmt.Fprintf(w, "\n%s\n%s (%d)\n%s\n\n", rule, title, counts[form], rule)
		}
		w.WriteString(app.exportHeader(m))
		w.WriteString(m.Content)
		if !strings.HasSuffix(m.Content, "\n") {
			w.WriteByte('\n')
		}
		w.WriteByte('\n')
	}
}

// exportHeader is the line above a molt's words in the text file: when it was
// written, its form and layout, its flags and its link; then, set in, what
// it answers or quotes.
func (app *App) exportHeader(m store.Molt) string {
	var b strings.Builder
	fmt.Fprintf(&b, "--- %s UTC · %s", humanDate(m.CreatedAt), formName(m.Form))
	if l, ok := formOf(m.Form).Layout(m.Layout); ok && m.Layout != "" {
		fmt.Fprintf(&b, " · %s layout", l.Name)
	}
	if m.NSFW {
		b.WriteString(" · NSFW")
	}
	if m.Edited {
		b.WriteString(" · edited")
	}
	fmt.Fprintf(&b, " · %s\n", app.absolute("/molt/view/"+m.ID))
	if m.ReplyTo != "" {
		b.WriteString("    in reply to ")
		if m.ReplyToAuthor != "" {
			fmt.Fprintf(&b, "@%s · ", m.ReplyToAuthor)
		}
		fmt.Fprintf(&b, "%s\n", app.absolute("/molt/view/"+m.ReplyTo))
	}
	if m.QuoteOf != "" {
		b.WriteString("    quoting ")
		if m.Quoted != nil {
			fmt.Fprintf(&b, "@%s", m.Quoted.Author)
		} else {
			b.WriteString("a molt that's gone")
		}
		fmt.Fprintf(&b, " · %s\n", app.absolute("/molt/view/"+m.QuoteOf))
	}
	return b.String()
}

// The JSON file: every field of every molt, for machines and for carrying
// the poems somewhere else.
type (
	exportJSON struct {
		ExportedAt time.Time    `json:"exported_at"`
		Krab       exportKrab   `json:"krab"`
		Forms      []string     `json:"forms"`
		Replies    bool         `json:"replies"`
		Molts      []exportMolt `json:"molts"`
	}
	exportKrab struct {
		Username string `json:"username"`
		Name     string `json:"name"`
		URL      string `json:"url"`
	}
	exportRef struct {
		ID     string `json:"id"`
		Author string `json:"author,omitempty"` // username; missing when the molt is gone or its author hidden
		URL    string `json:"url"`
	}
	exportMolt struct {
		ID        string     `json:"id"`
		URL       string     `json:"url"`
		CreatedAt time.Time  `json:"created_at"`
		Form      string     `json:"form"` // "" for a plain note
		FormName  string     `json:"form_name"`
		Layout    string     `json:"layout,omitempty"` // missing for the form's own layout
		Text      string     `json:"text"`
		NSFW      bool       `json:"nsfw,omitempty"`
		Edited    bool       `json:"edited,omitempty"`
		Tags      []string   `json:"tags,omitempty"`
		Mentions  []string   `json:"mentions,omitempty"`
		ReplyTo   *exportRef `json:"reply_to,omitempty"`
		QuoteOf   *exportRef `json:"quote_of,omitempty"`
		Likes     int        `json:"likes"`
		Replies   int        `json:"replies"`
		Remolts   int        `json:"remolts"`
		Quotes    int        `json:"quotes"`
	}
)

func (app *App) writeExportJSON(w *bytes.Buffer, x export) error {
	out := exportJSON{
		ExportedAt: x.When,
		Krab:       exportKrab{Username: x.Krab.UserName, Name: x.Krab.Name(), URL: app.absolute("/krabs/" + x.Krab.UserName)},
		Forms:      x.Forms,
		Replies:    x.Replies,
		Molts:      make([]exportMolt, 0, len(x.Molts)),
	}
	for _, m := range x.Molts {
		e := exportMolt{
			ID: m.ID, URL: app.absolute("/molt/view/" + m.ID), CreatedAt: m.CreatedAt.UTC(),
			Form: m.Form, FormName: formName(m.Form), Layout: m.Layout, Text: m.Content,
			NSFW: m.NSFW, Edited: m.Edited, Tags: m.Tags, Mentions: m.Mentions,
			Likes: m.LikeCount, Replies: m.ReplyCount, Remolts: m.RemoltCount, Quotes: m.QuoteCount,
		}
		if m.ReplyTo != "" {
			e.ReplyTo = &exportRef{ID: m.ReplyTo, Author: m.ReplyToAuthor, URL: app.absolute("/molt/view/" + m.ReplyTo)}
		}
		if m.QuoteOf != "" {
			e.QuoteOf = &exportRef{ID: m.QuoteOf, URL: app.absolute("/molt/view/" + m.QuoteOf)}
			if m.Quoted != nil {
				e.QuoteOf.Author = m.Quoted.Author
			}
		}
		out.Molts = append(out.Molts, e)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(out)
}

// plural picks the word for a count.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
