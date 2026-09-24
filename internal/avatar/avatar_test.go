package avatar

import (
	"strings"
	"testing"
)

func TestCodeRoundTrip(t *testing.T) {
	code := Encode(Traits{Shell: 3, Pattern: 7, Eyes: 2, Claws: 5, Expression: 1, Accessory: 9, Background: 4})
	if code != "3725194" {
		t.Fatalf("encode: %s", code)
	}
	got, ok := Decode(code)
	if !ok || got.Accessory != 9 || got.Background != 4 {
		t.Fatalf("decode: %+v ok=%v", got, ok)
	}
}

func TestValidRejectsJunk(t *testing.T) {
	for _, code := range []string{"", "123", "8888888", "0000066", "abcdefg", "3725194.svg", "../0000000"} {
		if Valid(code) {
			t.Errorf("accepted %q", code)
		}
	}
	if !Valid("0000000") || !Valid("3725194") {
		t.Fatal("rejected a good code")
	}
}

func TestRandomAndSVG(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 40; i++ {
		code, err := Random()
		if err != nil || !Valid(code) {
			t.Fatalf("random: %s %v", code, err)
		}
		svg, ok := SVG(code)
		if !ok || !strings.HasPrefix(svg, "<svg ") || !strings.Contains(svg, "viewBox") {
			t.Fatalf("svg %s: ok=%v len=%d", code, ok, len(svg))
		}
		if strings.Contains(svg, code) {
			t.Fatal("svg included the raw code")
		}
		seen[code] = true
	}
	if len(seen) < 10 {
		t.Fatalf("random stuck: %d distinct", len(seen))
	}
	if _, ok := SVG("not-a-crab"); ok {
		t.Fatal("svg accepted junk")
	}
}

func TestPath(t *testing.T) {
	if Path("0000000") != "/avatar/0000000.svg" || Path("nope") != "" {
		t.Fatal("path")
	}
}
