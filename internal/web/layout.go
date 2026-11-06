package web

import (
	"fmt"
	"html"
	"html/template"
	"strings"

	"github.com/t0ul/krabber-net/internal/store"
	"github.com/t0ul/krabber-net/internal/writing"
)

// layoutFor picks the layout a molt in a form is set in: the one asked for
// if the form has it, else the krab's default for the form, else the form's
// own. It gives "" for the form's own default, which is how a molt stores it.
func layoutFor(c *store.Crab, formID, asked string) string {
	f := formOf(formID)
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

// layoutDefaults is the layout the krab's molts in each form start in, by
// form ID, for the composer's script; the form's own default where they
// chose none.
func layoutDefaults(c *store.Crab) template.JS {
	out := map[string]string{}
	for _, f := range writing.Forms() {
		if len(f.Layouts) == 0 {
			continue
		}
		id := layoutFor(c, f.ID, "")
		if id == "" {
			id = f.Layouts[0].ID
		}
		out[f.ID] = id
	}
	return jsonScript(out)
}

// arrange sets a molt's text in the layout its writer chose, for reading:
// lines stepped in, the lines above set invisibly in front of a dropped line,
// slashes, syllable counts, paragraphs set in, panels side by side. The words
// are the molt's own HTML (mentions and crabtags linked), cut at its line
// breaks. It gives the HTML back unchanged for the form's own layout, for a
// reader who asked for plain layouts, and for anything it can't line up.
func arrange(m *store.Molt, plain bool) template.HTML {
	if plain || m.Layout == "" || m.ContentHTML == "" {
		return m.ContentHTML
	}
	f, ok := writing.ByID(m.Form)
	if !ok || len(f.Layouts) == 0 {
		return m.ContentHTML
	}
	l, ok := f.Layout(m.Layout)
	if !ok || l.ID == f.Layouts[0].ID {
		return m.ContentHTML
	}
	raw := strings.Split(m.Content, "\n")
	marked := strings.Split(string(m.ContentHTML), "\n")
	if len(raw) != len(marked) {
		return m.ContentHTML
	}
	var out string
	switch l.Mode {
	case writing.ModeIndented, writing.ModeSignOff, writing.ModeStrip:
		out = arrangeProse(l, m.Content, raw, marked)
	default:
		out = arrangeVerse(f, l, m.Content, raw, marked)
	}
	if out == "" {
		return m.ContentHTML
	}
	return template.HTML(out) //nolint:gosec // the molt's own escaped HTML, our class names and escaped plain text
}

// arrangeVerse sets a poem line by line.
func arrangeVerse(f writing.Form, l writing.Layout, content string, raw, marked []string) string {
	var plain, lines []string
	for i, r := range raw {
		if strings.TrimSpace(r) == "" {
			continue
		}
		plain = append(plain, strings.TrimSpace(r))
		lines = append(lines, strings.TrimSpace(marked[i]))
	}
	sets := f.Set(l.ID, content)
	if len(lines) == 0 || len(sets) != len(lines) {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<span class="kb-arranged kb-layout-` + l.ID + `">`)
	switch l.Mode {
	case writing.ModeInline:
		for i, line := range lines {
			if i > 0 {
				sep := " / "
				if sets[i-1].Gap {
					sep = " // "
				}
				b.WriteString(`<span class="kb-slash">` + sep + `</span>`)
			}
			b.WriteString(line)
		}
	case writing.ModeCounts:
		// Counts that would argue with the writer (a poem posted past the
		// counter) aren't shown.
		heard := f.Measure(content)
		if len(heard) != len(lines) {
			return ""
		}
		for _, h := range heard {
			if !h.SyllablesOK {
				return ""
			}
		}
		for i, line := range lines {
			fmt.Fprintf(&b, `<span class="kb-vl%s"><span class="kb-vt">%s</span><span class="kb-count" title="Syllables heard">%d</span></span>`,
				lineFlags(sets[i]), line, heard[i].Syllables)
		}
	default:
		open := false
		for i, line := range lines {
			if open {
				b.WriteString(" ")
			} else {
				fmt.Fprintf(&b, `<span class="kb-vl kb-in-%d%s">`, sets[i].Indent, lineFlags(sets[i]))
				open = true
				if l.Mode == writing.ModeDropped && i > 0 {
					b.WriteString(`<span class="kb-ghost" aria-hidden="true">` + html.EscapeString(strings.Join(plain[:i], " ")+" ") + `</span>`)
				}
			}
			b.WriteString(line)
			if !sets[i].Join {
				b.WriteString(`</span>`)
				open = false
			}
		}
		if open {
			b.WriteString(`</span>`)
		}
	}
	b.WriteString(`</span>`)
	return b.String()
}

func lineFlags(s writing.LineSet) string {
	var out string
	if s.Gap {
		out += " kb-gap"
	}
	if s.Join {
		out += " kb-join"
	}
	return out
}

// arrangeProse sets a letter, a post or a joke paragraph by paragraph.
func arrangeProse(l writing.Layout, content string, raw, marked []string) string {
	var paras [][]string
	var cur []string
	for i, r := range raw {
		if strings.TrimSpace(r) == "" {
			if cur != nil {
				paras, cur = append(paras, cur), nil
			}
			continue
		}
		cur = append(cur, strings.TrimSpace(marked[i]))
	}
	if cur != nil {
		paras = append(paras, cur)
	}
	if len(paras) == 0 {
		return ""
	}
	titled := writing.Titled(content) && len(paras) > 1
	var b strings.Builder
	b.WriteString(`<span class="kb-arranged kb-layout-` + l.ID + `">`)
	for i, p := range paras {
		class := "kb-para"
		switch l.Mode {
		case writing.ModeIndented:
			first := 0
			if titled {
				first = 1
			}
			switch {
			case titled && i == 0:
				class += " kb-para-title"
			case i > first:
				class += " kb-indent"
			}
		case writing.ModeSignOff:
			if i == len(paras)-1 && len(paras) > 1 {
				class += " kb-signoff"
			}
		case writing.ModeStrip:
			class += " kb-panel"
		}
		b.WriteString(`<span class="` + class + `">` + strings.Join(p, "\n") + `</span>`)
	}
	b.WriteString(`</span>`)
	return b.String()
}
