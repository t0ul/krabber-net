// Package avatar draws a unique crab from a short trait code.
//
// The code is seven digits (shell, pattern, eyes, claws, expression,
// accessory, background). The SVG is built only from these tables; a
// request never reaches the markup.
package avatar

import (
	"crypto/rand"
	"fmt"
)

// Trait counts are fixed by the tables below.
const (
	Shells      = 8
	Patterns    = 8
	Eyes        = 8
	Claws       = 6
	Expressions = 6
	Accessories = 10
	Backgrounds = 6
	CodeLen     = 7
)

// Traits is one crab's look. Zero values are the first of each table.
type Traits struct {
	Shell, Pattern, Eyes, Claws, Expression, Accessory, Background int
}

// Valid reports whether code is a seven-digit trait string in range.
func Valid(code string) bool {
	_, ok := Decode(code)
	return ok
}

// Decode reads a code. ok is false when any digit is missing or out of range.
func Decode(code string) (Traits, bool) {
	if len(code) != CodeLen {
		return Traits{}, false
	}
	d := make([]int, CodeLen)
	for i := 0; i < CodeLen; i++ {
		c := code[i]
		if c < '0' || c > '9' {
			return Traits{}, false
		}
		d[i] = int(c - '0')
	}
	t := Traits{d[0], d[1], d[2], d[3], d[4], d[5], d[6]}
	if t.Shell >= Shells || t.Pattern >= Patterns || t.Eyes >= Eyes ||
		t.Claws >= Claws || t.Expression >= Expressions ||
		t.Accessory >= Accessories || t.Background >= Backgrounds {
		return Traits{}, false
	}
	return t, true
}

// Encode writes Traits as a seven-digit code. Out-of-range fields wrap.
func Encode(t Traits) string {
	return fmt.Sprintf("%d%d%d%d%d%d%d",
		t.Shell%Shells, t.Pattern%Patterns, t.Eyes%Eyes,
		t.Claws%Claws, t.Expression%Expressions,
		t.Accessory%Accessories, t.Background%Backgrounds)
}

// Random picks a code from crypto/rand.
func Random() (string, error) {
	var b [CodeLen]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("avatar: %w", err)
	}
	t := Traits{
		Shell:      int(b[0]) % Shells,
		Pattern:    int(b[1]) % Patterns,
		Eyes:       int(b[2]) % Eyes,
		Claws:      int(b[3]) % Claws,
		Expression: int(b[4]) % Expressions,
		Accessory:  int(b[5]) % Accessories,
		Background: int(b[6]) % Backgrounds,
	}
	return Encode(t), nil
}

// Path is the public URL for a valid crab, or empty.
func Path(code string) string {
	if !Valid(code) {
		return ""
	}
	return "/avatar/" + code + ".svg"
}

// BannerPath is the public URL for that crab's stretch of ocean, or empty.
func BannerPath(code string) string {
	if !Valid(code) {
		return ""
	}
	return "/banner/" + code + ".svg"
}
