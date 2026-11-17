package avatar

import (
	"fmt"
	"strings"
)

// Banner scenes stay brighter than the avatar discs so they read on a dark page.
var bannerSky = [Backgrounds]string{
	"#3d6ea8", "#4cc0d9", "#2a9d8f", "#3a8f7a", "#3d405b", "#7ed6df",
}
var bannerWater = [Backgrounds]string{
	"#1b4b7a", "#1b6b93", "#14746f", "#1a535c", "#22223b", "#00b4d8",
}
var bannerFloor = [Backgrounds]string{
	"#0d2744", "#0d3b4c", "#0b3d3a", "#0e3630", "#12121f", "#f2d0a4",
}

// Banner draws a 3:1 ocean environment from the same trait code as the crab.
// The scene is built only from the tables; a request never reaches the markup.
func Banner(code string) (string, bool) {
	t, ok := Decode(code)
	if !ok {
		return "", false
	}
	sky := bannerSky[t.Background]
	water := bannerWater[t.Background]
	floor := bannerFloor[t.Background]
	shell := shellColors[t.Shell]
	foam := "#e9f5f3"

	var b strings.Builder
	b.Grow(2400)
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 360 120" width="360" height="120" preserveAspectRatio="xMidYMid slice">`)
	fmt.Fprintf(&b, `<rect width="360" height="120" fill="%s"/>`, water)
	fmt.Fprintf(&b, `<rect width="360" height="40" fill="%s"/>`, sky)
	writeBannerLight(&b, t, shell, foam)
	writeBannerWaves(&b, t.Pattern, foam)
	writeBannerFloor(&b, t, floor, shell, foam)
	writeBannerAccessory(&b, t.Accessory, shell, foam)
	b.WriteString(`</svg>`)
	return b.String(), true
}

func writeBannerLight(b *strings.Builder, t Traits, shell, foam string) {
	x := 40 + t.Eyes*42
	if t.Background == 4 { // night moon
		fmt.Fprintf(b, `<circle cx="%d" cy="18" r="12" fill="%s"/>`, x, foam)
		fmt.Fprintf(b, `<circle cx="%d" cy="16" r="12" fill="%s"/>`, x+5, bannerSky[4])
		for _, p := range [][2]int{{16, 8}, {88, 14}, {150, 6}, {210, 16}, {270, 10}, {330, 12}} {
			fmt.Fprintf(b, `<circle cx="%d" cy="%d" r="1.3" fill="%s"/>`, p[0]+t.Expression, p[1], foam)
		}
		return
	}
	fmt.Fprintf(b, `<circle cx="%d" cy="16" r="11" fill="%s"/>`, x, shell)
	fmt.Fprintf(b, `<circle cx="%d" cy="16" r="6" fill="%s" opacity=".85"/>`, x, foam)
}

func writeBannerWaves(b *strings.Builder, pattern int, foam string) {
	amp := 5
	if pattern >= 4 {
		amp = 8
	}
	fmt.Fprintf(b, `<path d="M0 40`)
	for x := 0; x <= 360; x += 24 {
		fmt.Fprintf(b, "c8 0 8-%d 16-%d s8 %d 16 %d", amp, amp, amp, amp)
	}
	fmt.Fprintf(b, `V0H0z" fill="%s" opacity=".22"/>`, foam)
	fmt.Fprintf(b, `<path d="M0 42`)
	for x := 0; x <= 360; x += 20 {
		fmt.Fprintf(b, "c7 0 7-4 14-4s7 4 14 4")
	}
	fmt.Fprintf(b, `" fill="none" stroke="%s" stroke-width="2"/>`, foam)
}

func writeBannerFloor(b *strings.Builder, t Traits, floor, shell, foam string) {
	switch t.Background {
	case 0: // deep trench: V-cut
		left := 100 + t.Claws*6
		right := 260 - t.Claws*6
		fmt.Fprintf(b, `<path d="M0 88h%dL180 120L%d 88H360V120H0z" fill="%s"/>`, left, right, floor)
		fmt.Fprintf(b, `<path d="M%d 88L180 118L%d 88" fill="none" stroke="%s" stroke-width="2.2"/>`, left, right, foam)
		writeBannerSpecks(b, t, foam)
	case 2: // reef
		fmt.Fprintf(b, `<path d="M0 94c50-16 90-6 130-14s80 12 140-4 70 8 90 2V120H0z" fill="%s"/>`, floor)
		for i, p := range [][4]int{{36, 98, 14, 20}, {78, 102, 11, 16}, {168, 96, 16, 22}, {250, 100, 13, 18}, {320, 98, 12, 16}} {
			c := shell
			if i%2 == 0 {
				c = "#2a9d8f"
			}
			fmt.Fprintf(b, `<ellipse cx="%d" cy="%d" rx="%d" ry="%d" fill="%s"/>`, p[0]+t.Claws*2, p[1], p[2], p[3], c)
		}
	case 3: // kelp forest
		fmt.Fprintf(b, `<path d="M0 102h360V120H0z" fill="%s"/>`, floor)
		for i, x := range []int{20, 52, 86, 128, 176, 220, 268, 316} {
			h := 50 + (i+t.Pattern)%5*7
			fmt.Fprintf(b, `<path d="M%d 108c8-%d-6-%d 4-%d" fill="none" stroke="%s" stroke-width="3.2" stroke-linecap="round"/>`, x, h/2, h-10, h, "#2a9d8f")
		}
	case 5: // shallow lagoon
		fmt.Fprintf(b, `<ellipse cx="180" cy="116" rx="210" ry="26" fill="%s"/>`, floor)
		fmt.Fprintf(b, `<ellipse cx="%d" cy="108" rx="64" ry="12" fill="%s" opacity=".55"/>`, 80+t.Claws*22, shell)
		writeBannerSpecks(b, t, foam)
	default: // open sea and night swell
		fmt.Fprintf(b, `<path d="M0 100c70-12 130 10 190 0s110-12 170 4V120H0z" fill="%s"/>`, floor)
		writeBannerSpecks(b, t, foam)
	}
}

func writeBannerSpecks(b *strings.Builder, t Traits, foam string) {
	for i, p := range [][2]int{{64, 78}, {140, 86}, {210, 76}, {292, 84}} {
		if i == t.Expression {
			continue
		}
		fmt.Fprintf(b, `<circle cx="%d" cy="%d" r="2" fill="%s" opacity=".75"/>`, p[0], p[1], foam)
	}
}

func writeBannerAccessory(b *strings.Builder, kind int, shell, foam string) {
	switch kind {
	case 1: // snorkel
		b.WriteString(`<rect x="314" y="20" width="5" height="30" rx="2" fill="#2a9d8f"/><circle cx="316.5" cy="18" r="6" fill="#2a9d8f"/><circle cx="316.5" cy="18" r="2.5" fill="#e9f5f3"/>`)
	case 2: // sailor — a small boat
		b.WriteString(`<path d="M284 38l8-16h6l8 16z" fill="#f1faee"/><path d="M276 38h40l-4 8h-32z" fill="#e63946"/>`)
	case 3: // pearl
		fmt.Fprintf(b, `<circle cx="300" cy="70" r="7" fill="%s"/><circle cx="298" cy="68" r="2.2" fill="#fff"/>`, foam)
	case 4: // buoy
		b.WriteString(`<rect x="310" y="26" width="6" height="28" fill="#f1faee"/><circle cx="313" cy="22" r="8" fill="#e63946"/>`)
	case 5: // monocle disc
		fmt.Fprintf(b, `<circle cx="320" cy="26" r="10" fill="none" stroke="%s" stroke-width="2.4"/>`, foam)
	case 6: // foreground kelp
		b.WriteString(`<path d="M26 118c10-30-8-42 6-72" fill="none" stroke="#2a9d8f" stroke-width="4"/><path d="M44 118c-8-26 10-38-2-64" fill="none" stroke="#2a9d8f" stroke-width="3"/>`)
	case 7: // bubbles
		b.WriteString(`<circle cx="322" cy="68" r="8" fill="#8ecae6" opacity=".7"/><circle cx="334" cy="48" r="5" fill="#8ecae6" opacity=".65"/><circle cx="318" cy="40" r="2.4" fill="#fff"/>`)
	case 8: // antenna lights
		fmt.Fprintf(b, `<circle cx="36" cy="76" r="4.5" fill="%s"/><circle cx="322" cy="82" r="4.5" fill="%s"/>`, foam, foam)
	case 9: // flag
		fmt.Fprintf(b, `<path d="M302 16v38" stroke="%s" stroke-width="2"/><path d="M302 16l20 8-20 8z" fill="%s"/>`, foam, shell)
	}
}
