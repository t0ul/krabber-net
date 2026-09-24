package avatar

import (
	"fmt"
	"strings"
)

// Brand shell colours stay in Crabber's red/coral/orange family.
var shellColors = [Shells]string{
	"#e63946", "#f07167", "#e85d04", "#c44536",
	"#ff8c69", "#b23a48", "#ff6b35", "#d94f70",
}

var bgColors = [Backgrounds]string{
	"#12355b", "#1b6b93", "#0e4d5c", "#1a535c",
	"#14213d", "#0d3b4c",
}

var accentColors = [Backgrounds]string{
	"#1e6091", "#2a9d8f", "#14746f", "#248277",
	"#1d3557", "#1a5f7a",
}

// SVG renders a circular crab. code must be Valid.
func SVG(code string) (string, bool) {
	t, ok := Decode(code)
	if !ok {
		return "", false
	}
	shell := shellColors[t.Shell]
	dark := shade(shell, 0.72)
	light := shade(shell, 1.18)
	bg := bgColors[t.Background]
	accent := accentColors[t.Background]

	var b strings.Builder
	b.Grow(1800)
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 128 128" width="128" height="128">`)
	fmt.Fprintf(&b, `<circle cx="64" cy="64" r="64" fill="%s"/>`, bg)
	fmt.Fprintf(&b, `<circle cx="28" cy="30" r="18" fill="%s" opacity=".25"/>`, accent)
	fmt.Fprintf(&b, `<circle cx="100" cy="96" r="22" fill="%s" opacity=".2"/>`, accent)

	writeClaws(&b, t.Claws, shell, dark)
	writeLegs(&b, dark)
	writeShell(&b, t.Pattern, shell, dark, light)
	writeEyes(&b, t.Eyes, dark)
	writeMouth(&b, t.Expression, dark)
	writeAccessory(&b, t.Accessory, shell, light)
	b.WriteString(`</svg>`)
	return b.String(), true
}

func writeClaws(b *strings.Builder, kind int, shell, dark string) {
	switch kind {
	case 1: // big
		fmt.Fprintf(b, `<ellipse cx="28" cy="78" rx="16" ry="12" fill="%s" transform="rotate(-28 28 78)"/>`, shell)
		fmt.Fprintf(b, `<ellipse cx="100" cy="78" rx="16" ry="12" fill="%s" transform="rotate(28 100 78)"/>`, shell)
		fmt.Fprintf(b, `<path d="M20 72c-6-2-10 4-8 9 4 2 10 0 12-4" fill="%s"/>`, dark)
		fmt.Fprintf(b, `<path d="M108 72c6-2 10 4 8 9-4 2-10 0-12-4" fill="%s"/>`, dark)
	case 2: // skinny
		fmt.Fprintf(b, `<ellipse cx="30" cy="80" rx="10" ry="7" fill="%s" transform="rotate(-35 30 80)"/>`, shell)
		fmt.Fprintf(b, `<ellipse cx="98" cy="80" rx="10" ry="7" fill="%s" transform="rotate(35 98 80)"/>`, shell)
	case 3: // rounded
		fmt.Fprintf(b, `<circle cx="30" cy="80" r="11" fill="%s"/>`, shell)
		fmt.Fprintf(b, `<circle cx="98" cy="80" r="11" fill="%s"/>`, shell)
	case 4: // raised
		fmt.Fprintf(b, `<ellipse cx="32" cy="62" rx="12" ry="9" fill="%s" transform="rotate(-50 32 62)"/>`, shell)
		fmt.Fprintf(b, `<ellipse cx="96" cy="62" rx="12" ry="9" fill="%s" transform="rotate(50 96 62)"/>`, shell)
	case 5: // small
		fmt.Fprintf(b, `<ellipse cx="34" cy="82" rx="8" ry="6" fill="%s" transform="rotate(-20 34 82)"/>`, shell)
		fmt.Fprintf(b, `<ellipse cx="94" cy="82" rx="8" ry="6" fill="%s" transform="rotate(20 94 82)"/>`, shell)
	default: // classic
		fmt.Fprintf(b, `<ellipse cx="30" cy="80" rx="13" ry="10" fill="%s" transform="rotate(-30 30 80)"/>`, shell)
		fmt.Fprintf(b, `<ellipse cx="98" cy="80" rx="13" ry="10" fill="%s" transform="rotate(30 98 80)"/>`, shell)
		fmt.Fprintf(b, `<path d="M22 74c-5-1-8 4-6 8 3 2 8 0 10-3" fill="%s"/>`, dark)
		fmt.Fprintf(b, `<path d="M106 74c5-1 8 4 6 8-3 2-8 0-10-3" fill="%s"/>`, dark)
	}
}

func writeLegs(b *strings.Builder, dark string) {
	b.WriteString(`<g stroke="` + dark + `" stroke-width="3" stroke-linecap="round" fill="none">`)
	b.WriteString(`<path d="M40 96c-8 8-14 14-16 20"/><path d="M50 100c-4 10-6 16-6 22"/>`)
	b.WriteString(`<path d="M88 96c8 8 14 14 16 20"/><path d="M78 100c4 10 6 16 6 22"/>`)
	b.WriteString(`</g>`)
}

func writeShell(b *strings.Builder, pattern int, shell, dark, light string) {
	fmt.Fprintf(b, `<ellipse cx="64" cy="76" rx="30" ry="24" fill="%s"/>`, shell)
	fmt.Fprintf(b, `<ellipse cx="64" cy="72" rx="26" ry="18" fill="%s" opacity=".35"/>`, light)
	switch pattern {
	case 1: // spots
		fmt.Fprintf(b, `<circle cx="52" cy="70" r="4" fill="%s" opacity=".45"/>`, dark)
		fmt.Fprintf(b, `<circle cx="72" cy="68" r="3.5" fill="%s" opacity=".45"/>`, dark)
		fmt.Fprintf(b, `<circle cx="64" cy="82" r="3" fill="%s" opacity=".4"/>`, dark)
	case 2: // stripes
		fmt.Fprintf(b, `<path d="M40 68c16 4 32 4 48 0" stroke="%s" stroke-width="3" fill="none" opacity=".4"/>`, dark)
		fmt.Fprintf(b, `<path d="M42 78c14 3 28 3 44 0" stroke="%s" stroke-width="3" fill="none" opacity=".4"/>`, dark)
	case 3: // speckles
		for _, p := range [][3]float64{{48, 72, 1.6}, {58, 66, 1.3}, {70, 70, 1.5}, {78, 80, 1.2}, {54, 84, 1.4}} {
			fmt.Fprintf(b, `<circle cx="%g" cy="%g" r="%g" fill="%s" opacity=".5"/>`, p[0], p[1], p[2], dark)
		}
	case 4: // bands
		fmt.Fprintf(b, `<path d="M38 76h52" stroke="%s" stroke-width="6" opacity=".25"/>`, dark)
	case 5: // mottled
		fmt.Fprintf(b, `<ellipse cx="54" cy="74" rx="10" ry="8" fill="%s" opacity=".2"/>`, dark)
		fmt.Fprintf(b, `<ellipse cx="74" cy="78" rx="8" ry="6" fill="%s" opacity=".2"/>`, light)
	case 6: // edge
		fmt.Fprintf(b, `<ellipse cx="64" cy="76" rx="28" ry="22" fill="none" stroke="%s" stroke-width="3" opacity=".35"/>`, dark)
	case 7: // belly
		fmt.Fprintf(b, `<ellipse cx="64" cy="84" rx="14" ry="8" fill="%s" opacity=".35"/>`, light)
	}
}

func writeEyes(b *strings.Builder, kind int, dark string) {
	fmt.Fprintf(b, `<path d="M50 54c0-12 2-20 6-26" stroke="%s" stroke-width="3" fill="none"/>`, dark)
	fmt.Fprintf(b, `<path d="M78 54c0-12-2-20-6-26" stroke="%s" stroke-width="3" fill="none"/>`, dark)
	switch kind {
	case 1: // wide
		b.WriteString(`<circle cx="54" cy="28" r="8" fill="#fff"/><circle cx="74" cy="28" r="8" fill="#fff"/>`)
		b.WriteString(`<circle cx="55" cy="29" r="3.5" fill="#1a1a1a"/><circle cx="75" cy="29" r="3.5" fill="#1a1a1a"/>`)
	case 2: // sleepy
		fmt.Fprintf(b, `<path d="M48 30h12" stroke="%s" stroke-width="3" stroke-linecap="round"/>`, dark)
		fmt.Fprintf(b, `<path d="M68 30h12" stroke="%s" stroke-width="3" stroke-linecap="round"/>`, dark)
	case 3: // angry
		b.WriteString(`<circle cx="54" cy="30" r="6" fill="#fff"/><circle cx="74" cy="30" r="6" fill="#fff"/>`)
		b.WriteString(`<circle cx="54" cy="31" r="2.5" fill="#1a1a1a"/><circle cx="74" cy="31" r="2.5" fill="#1a1a1a"/>`)
		fmt.Fprintf(b, `<path d="M46 24l14 4M82 24l-14 4" stroke="%s" stroke-width="2"/>`, dark)
	case 4: // tiny
		b.WriteString(`<circle cx="54" cy="30" r="4" fill="#fff"/><circle cx="74" cy="30" r="4" fill="#fff"/>`)
		b.WriteString(`<circle cx="54" cy="30" r="1.6" fill="#1a1a1a"/><circle cx="74" cy="30" r="1.6" fill="#1a1a1a"/>`)
	case 5: // googly
		b.WriteString(`<circle cx="54" cy="28" r="8" fill="#fff"/><circle cx="74" cy="28" r="8" fill="#fff"/>`)
		b.WriteString(`<circle cx="51" cy="26" r="3" fill="#1a1a1a"/><circle cx="77" cy="31" r="3" fill="#1a1a1a"/>`)
	case 6: // wink
		b.WriteString(`<circle cx="54" cy="30" r="6" fill="#fff"/><circle cx="54" cy="30" r="2.4" fill="#1a1a1a"/>`)
		fmt.Fprintf(b, `<path d="M68 30h12" stroke="%s" stroke-width="3" stroke-linecap="round"/>`, dark)
	case 7: // star
		b.WriteString(`<circle cx="54" cy="30" r="6" fill="#fff"/><circle cx="74" cy="30" r="6" fill="#fff"/>`)
		fmt.Fprintf(b, `<path d="M54 26l1.2 2.4 2.6.4-1.9 1.8.4 2.6L54 32.2 51.7 33.2l.4-2.6-1.9-1.8 2.6-.4z" fill="%s"/>`, dark)
		fmt.Fprintf(b, `<path d="M74 26l1.2 2.4 2.6.4-1.9 1.8.4 2.6L74 32.2 71.7 33.2l.4-2.6-1.9-1.8 2.6-.4z" fill="%s"/>`, dark)
	default: // round
		b.WriteString(`<circle cx="54" cy="30" r="7" fill="#fff"/><circle cx="74" cy="30" r="7" fill="#fff"/>`)
		b.WriteString(`<circle cx="55" cy="31" r="3" fill="#1a1a1a"/><circle cx="75" cy="31" r="3" fill="#1a1a1a"/>`)
	}
}

func writeMouth(b *strings.Builder, kind int, dark string) {
	switch kind {
	case 1:
		fmt.Fprintf(b, `<path d="M58 78c4 4 8 4 12 0" stroke="%s" stroke-width="2" fill="none" stroke-linecap="round"/>`, dark)
	case 2:
		fmt.Fprintf(b, `<path d="M58 82c4-4 8-4 12 0" stroke="%s" stroke-width="2" fill="none" stroke-linecap="round"/>`, dark)
	case 3:
		fmt.Fprintf(b, `<path d="M60 80h8" stroke="%s" stroke-width="2" stroke-linecap="round"/>`, dark)
	case 4:
		fmt.Fprintf(b, `<path d="M56 78c6 8 10 8 16 0" stroke="%s" stroke-width="2" fill="none" stroke-linecap="round"/>`, dark)
	case 5:
		fmt.Fprintf(b, `<ellipse cx="64" cy="80" rx="4" ry="3" fill="%s"/>`, dark)
	default:
		fmt.Fprintf(b, `<path d="M58 80c4 3 8 3 12 0" stroke="%s" stroke-width="2" fill="none" stroke-linecap="round"/>`, dark)
	}
}

func writeAccessory(b *strings.Builder, kind int, shell, light string) {
	switch kind {
	case 1: // snorkel
		b.WriteString(`<rect x="74" y="18" width="4" height="22" rx="2" fill="#2a9d8f"/>`)
		b.WriteString(`<circle cx="76" cy="16" r="5" fill="#2a9d8f"/><circle cx="76" cy="16" r="2.5" fill="#e9f5f3"/>`)
	case 2: // sailor hat
		b.WriteString(`<ellipse cx="64" cy="16" rx="18" ry="5" fill="#f1faee"/>`)
		b.WriteString(`<rect x="52" y="6" width="24" height="11" rx="3" fill="#e63946"/>`)
		b.WriteString(`<rect x="52" y="12" width="24" height="3" fill="#f1faee"/>`)
	case 3: // pearl
		b.WriteString(`<circle cx="64" cy="16" r="5" fill="#f8f0e3"/><circle cx="62" cy="14" r="1.6" fill="#fff"/>`)
	case 4: // bandana
		fmt.Fprintf(b, `<path d="M44 40c12-10 28-10 40 0-16-4-24-4-40 0z" fill="%s"/>`, light)
		fmt.Fprintf(b, `<path d="M44 40l-6 8 8-2" fill="%s"/>`, light)
	case 5: // monocle
		b.WriteString(`<circle cx="74" cy="30" r="9" fill="none" stroke="#f4d35e" stroke-width="2"/>`)
		b.WriteString(`<path d="M83 34c6 8 6 14 2 20" stroke="#f4d35e" stroke-width="1.5" fill="none"/>`)
	case 6: // kelp
		b.WriteString(`<path d="M42 18c4 8-2 12 2 20" stroke="#2a9d8f" stroke-width="3" fill="none"/>`)
		b.WriteString(`<path d="M86 16c-3 9 3 12-1 22" stroke="#2a9d8f" stroke-width="3" fill="none"/>`)
	case 7: // bubble
		b.WriteString(`<circle cx="96" cy="22" r="8" fill="#8ecae6" opacity=".55"/><circle cx="93" cy="19" r="2" fill="#fff"/>`)
	case 8: // antenna
		fmt.Fprintf(b, `<path d="M54 18c-4-10-10-12-16-10" stroke="%s" stroke-width="2" fill="none"/>`, shell)
		fmt.Fprintf(b, `<path d="M74 18c4-10 10-12 16-10" stroke="%s" stroke-width="2" fill="none"/>`, shell)
		fmt.Fprintf(b, `<circle cx="38" cy="8" r="3" fill="%s"/><circle cx="90" cy="8" r="3" fill="%s"/>`, light, light)
	case 9: // bow
		b.WriteString(`<path d="M56 14l-8-6v12zM72 14l8-6v12z" fill="#e63946"/><circle cx="64" cy="14" r="3" fill="#b23a48"/>`)
	}
}

// shade multiplies a #rrggbb colour. Values above 1 lighten.
func shade(hex string, m float64) string {
	if len(hex) != 7 || hex[0] != '#' {
		return hex
	}
	clamp := func(v float64) int {
		if v < 0 {
			return 0
		}
		if v > 255 {
			return 255
		}
		return int(v)
	}
	var r, g, b int
	_, _ = fmt.Sscanf(hex, "#%02x%02x%02x", &r, &g, &b)
	return fmt.Sprintf("#%02x%02x%02x", clamp(float64(r)*m), clamp(float64(g)*m), clamp(float64(b)*m))
}
