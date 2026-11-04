// Package auth hashes and checks passwords.
package auth

import (
	"errors"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// Password length limits, in bytes. bcrypt ignores everything past 72 bytes,
// so longer passwords are rejected rather than silently truncated.
const (
	MinPasswordLength = 8
	MaxPasswordLength = 72
)

// fullCost is the bcrypt cost of every stored password.
const fullCost = 12

// cost is what HashPassword uses: fullCost, except in a test binary that
// asked for fast hashing.
var cost = fullCost

// dummyHash is compared against when an account doesn't exist, so a login for
// an unknown email takes as long as one for a real account.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("krabber-timing-equalizer"), fullCost)

// setCost sets the bcrypt cost and re-makes the dummy hash at it.
func setCost(c int) {
	cost = c
	dummyHash, _ = bcrypt.GenerateFromPassword([]byte("krabber-timing-equalizer"), c)
}

// FastHashingForTests lowers the bcrypt cost to the minimum for the rest of a
// test binary's run. A suite that signs up and logs in hundreds of krabs
// otherwise spends its time hashing: under the race detector a full-cost hash
// takes seconds, and CI ran out its ten minutes on them. It panics outside a
// test binary, so production always hashes at full cost.
func FastHashingForTests() {
	if !testing.Testing() {
		panic("auth: FastHashingForTests called outside a test binary")
	}
	setCost(bcrypt.MinCost)
}

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
