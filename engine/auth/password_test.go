package auth

import (
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestHashAndCheck(t *testing.T) {
	hash, err := HashPassword("jellyfishing!")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := CheckPassword(hash, "jellyfishing!"); err != nil || !ok {
		t.Errorf("the right password: %v %v", ok, err)
	}
	if ok, err := CheckPassword(hash, "jellyfishing?"); err != nil || ok {
		t.Errorf("a wrong password: %v %v", ok, err)
	}
	// No account: compared against the dummy hash, and never a match.
	if ok, err := CheckPassword(nil, "jellyfishing!"); err != nil || ok {
		t.Errorf("no hash: %v %v", ok, err)
	}
}

// Passwords are hashed at cost 12 unless a test binary asks for fast hashing,
// which also re-makes the dummy hash so an unknown email still costs the same
// as a real one.
func TestCost(t *testing.T) {
	hash, err := HashPassword("jellyfishing!")
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := bcrypt.Cost(hash); c != 12 {
		t.Fatalf("default cost %d, want 12", c)
	}
	if c, _ := bcrypt.Cost(dummyHash); c != 12 {
		t.Fatalf("dummy hash cost %d, want 12", c)
	}

	t.Cleanup(func() { setCost(12) })
	FastHashingForTests()
	fast, err := HashPassword("jellyfishing!")
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := bcrypt.Cost(fast); c != bcrypt.MinCost {
		t.Errorf("fast cost %d, want %d", c, bcrypt.MinCost)
	}
	if c, _ := bcrypt.Cost(dummyHash); c != bcrypt.MinCost {
		t.Errorf("fast dummy hash cost %d, want %d", c, bcrypt.MinCost)
	}
	// A hash made at full cost still checks out.
	if ok, err := CheckPassword(hash, "jellyfishing!"); err != nil || !ok {
		t.Errorf("a full-cost hash after switching: %v %v", ok, err)
	}
	if ok, err := CheckPassword(nil, "jellyfishing!"); err != nil || ok {
		t.Errorf("no hash, fast: %v %v", ok, err)
	}
}
