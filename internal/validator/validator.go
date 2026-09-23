// Package validator collects form validation errors for re-rendering forms.
package validator

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Validator holds errors for a form. Embed it in a form struct.
type Validator struct {
	NonFieldErrors []string
	FieldErrors    map[string]string
}

// EmailRX is a pragmatic email format check (the activation email is the real test).
var EmailRX = regexp.MustCompile("^[a-zA-Z0-9.!#$%&'*+/=?^_`{|}~-]+@[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(?:\\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$")

// UsernameRX allows 3–20 letters, digits and underscores.
var UsernameRX = regexp.MustCompile(`^[A-Za-z0-9_]{3,20}$`)

// Valid reports whether no errors were recorded.
func (v *Validator) Valid() bool {
	return len(v.FieldErrors) == 0 && len(v.NonFieldErrors) == 0
}

// AddFieldError records the first error for a field.
func (v *Validator) AddFieldError(key, message string) {
	if v.FieldErrors == nil {
		v.FieldErrors = make(map[string]string)
	}
	if _, exists := v.FieldErrors[key]; !exists {
		v.FieldErrors[key] = message
	}
}

// AddNonFieldError records an error that isn't tied to one field.
func (v *Validator) AddNonFieldError(message string) {
	v.NonFieldErrors = append(v.NonFieldErrors, message)
}

// CheckField records message for key when ok is false.
func (v *Validator) CheckField(ok bool, key, message string) {
	if !ok {
		v.AddFieldError(key, message)
	}
}

// NotBlank reports whether value has non-whitespace content.
func NotBlank(value string) bool { return strings.TrimSpace(value) != "" }

// MinChars reports whether value has at least n characters.
func MinChars(value string, n int) bool { return utf8.RuneCountInString(value) >= n }

// MaxChars reports whether value has at most n characters.
func MaxChars(value string, n int) bool { return utf8.RuneCountInString(value) <= n }

// MaxBytes reports whether value is at most n bytes.
func MaxBytes(value string, n int) bool { return len(value) <= n }

// Matches reports whether value matches rx.
func Matches(value string, rx *regexp.Regexp) bool { return rx.MatchString(value) }
