// Package auth hashes and checks passwords.
package auth

import (
	"errors"

	"golang.org/x/crypto/bcrypt"
)

// Password length limits, in bytes. bcrypt ignores everything past 72 bytes,
// so longer passwords are rejected rather than silently truncated.
const (
	MinPasswordLength = 8
	MaxPasswordLength = 72
)

const cost = 12

// dummyHash is compared against when an account doesn't exist, so a login for
// an unknown email takes as long as one for a real account.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("krabber-timing-equalizer"), cost)

// HashPassword returns a bcrypt hash of the password.
func HashPassword(plaintext string) ([]byte, error) {
	return bcrypt.GenerateFromPassword([]byte(plaintext), cost)
}

// CheckPassword reports whether plaintext matches hash. A nil hash is compared
// against a dummy hash and always fails.
func CheckPassword(hash []byte, plaintext string) (bool, error) {
	if hash == nil {
		hash = dummyHash
		plaintext += "\x00" // never matches the dummy
	}
	err := bcrypt.CompareHashAndPassword(hash, []byte(plaintext))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, bcrypt.ErrMismatchedHashAndPassword):
		return false, nil
	default:
		return false, err
	}
}
