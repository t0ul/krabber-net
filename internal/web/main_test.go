package web

import (
	"os"
	"testing"

	"github.com/t0ul/krabber-net/internal/auth"
)

// TestMain hashes passwords at bcrypt's minimum cost for the whole package.
// These tests sign up and log in a couple of hundred krabs; at the production
// cost the race detector makes each hash take seconds, and CI spent its ten
// minutes on them. internal/auth's own tests hold the production cost to 12.
func TestMain(m *testing.M) {
	auth.FastHashingForTests()
	os.Exit(m.Run())
}
