package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/t0ul/krabber-net/internal/auth"
	"github.com/t0ul/krabber-net/internal/store"
)

// apiPost sends a JSON body to the API with an optional bearer key. It uses the
// same browser transport as the rest of the harness, so the CloudFront origin
// header originVerify needs is present.
func (h *harness) apiPost(path, bearer, body string) (int, string) {
	h.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestAPICreateMolt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// A bot account (it never signs in through the web) with one write-molts
	// key and one read-only key.
	hash, err := auth.HashPassword("unused-bot-password")
	if err != nil {
		t.Fatal(err)
	}
	bot, err := h.store.CreateCrab(ctx, "scuttle", "scuttle@krabber.test", hash)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.ActivateCrab(ctx, bot.ID); err != nil {
		t.Fatal(err)
	}
	writeKey, keyRec, err := h.store.CreateAPIKey(ctx, bot, "scuttle bot", []string{store.ScopeWriteMolts})
	if err != nil {
		t.Fatal(err)
	}
	readKey, _, err := h.store.CreateAPIKey(ctx, bot, "read only", []string{"read:molts"})
	if err != nil {
		t.Fatal(err)
	}

	// Happy path: the molt is created and posted as the bot.
	status, body := h.apiPost("/api/v1/molts", writeKey,
		`{"text":"Daily Scuttle: high tide 6:42a, low 12:58p. Sunny, 54F. %nyc"}`)
	if status != http.StatusCreated {
		t.Fatalf("create molt: %d %s", status, body)
	}
	var created struct{ ID, URL string }
	if err := json.Unmarshal([]byte(body), &created); err != nil || created.ID == "" {
		t.Fatalf("response not {id,url}: %s", body)
	}
	if !strings.HasSuffix(created.URL, "/molt/view/"+created.ID) {
		t.Fatalf("url doesn't point at the molt: %s", created.URL)
	}

	// It renders on its page, authored by the bot, with a linked %nyc crabtag.
	st, page, _ := h.get("/molt/view/" + created.ID)
	if st != http.StatusOK {
		t.Fatalf("molt page: %d", st)
	}
	// The bot was created straight in the store, so this also checks the API
	// post registered it in the directory: its molt shows its generated avatar,
	// not the default image.
	for _, want := range []string{"high tide 6:42a", "scuttle", `/krabtag/nyc`, "/avatar/" + bot.Avatar + ".svg"} {
		if !strings.Contains(page, want) {
			t.Fatalf("molt page missing %q", want)
		}
	}

	// Rejections.
	cases := []struct {
		name, bearer, body string
		want               int
	}{
		{"no token", "", `{"text":"x"}`, http.StatusUnauthorized},
		{"bad token", "kb_not-a-real-key", `{"text":"x"}`, http.StatusUnauthorized},
		{"wrong scope", readKey, `{"text":"x"}`, http.StatusForbidden},
		{"blank text", writeKey, `{"text":"   "}`, http.StatusUnprocessableEntity},
		{"too long", writeKey, `{"text":"` + strings.Repeat("x", 281) + `"}`, http.StatusUnprocessableEntity},
		{"unknown field", writeKey, `{"text":"hi","bogus":1}`, http.StatusBadRequest},
		{"not json", writeKey, `nope`, http.StatusBadRequest},
	}
	for _, c := range cases {
		if status, body := h.apiPost("/api/v1/molts", c.bearer, c.body); status != c.want {
			t.Fatalf("%s: got %d want %d (%s)", c.name, status, c.want, body)
		}
	}

	// A revoked key stops working.
	if err := h.store.RevokeAPIKey(ctx, bot.ID, keyRec.ID); err != nil {
		t.Fatal(err)
	}
	if status, body := h.apiPost("/api/v1/molts", writeKey, `{"text":"after revoke"}`); status != http.StatusUnauthorized {
		t.Fatalf("revoked key: got %d want 401 (%s)", status, body)
	}
}

// TestAPIKeyNotInheritedAfterEmailReuse checks that deleting an account frees
// its email without handing its API keys to whoever registers that email next.
func TestAPIKeyNotInheritedAfterEmailReuse(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	hash, err := auth.HashPassword("unused")
	if err != nil {
		t.Fatal(err)
	}
	first, err := h.store.CreateCrab(ctx, "owner1", "shared@krabber.test", hash)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.ActivateCrab(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	key, _, err := h.store.CreateAPIKey(ctx, first, "k", []string{store.ScopeWriteMolts})
	if err != nil {
		t.Fatal(err)
	}

	// Delete the first account (frees the email) and register a new account on
	// that same email. The old key must not authenticate as the new account.
	if _, err := h.store.DeleteAccount(ctx, first); err != nil {
		t.Fatal(err)
	}
	second, err := h.store.CreateCrab(ctx, "owner2", "shared@krabber.test", hash)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.ActivateCrab(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("precondition: the two accounts should have different IDs")
	}
	if status, body := h.apiPost("/api/v1/molts", key, `{"text":"not yours"}`); status != http.StatusUnauthorized {
		t.Fatalf("stale key authenticated as the new account: got %d want 401 (%s)", status, body)
	}
}

// TestAPIKeyRevokedByPasswordChange checks a password change invalidates keys
// minted before it, the same stamp (SessionsValidAfter) that ends sessions.
func TestAPIKeyRevokedByPasswordChange(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	hash, err := auth.HashPassword("unused")
	if err != nil {
		t.Fatal(err)
	}
	c, err := h.store.CreateCrab(ctx, "owner", "owner@krabber.test", hash)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.ActivateCrab(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	key, _, err := h.store.CreateAPIKey(ctx, c, "k", []string{store.ScopeWriteMolts})
	if err != nil {
		t.Fatal(err)
	}
	if status, body := h.apiPost("/api/v1/molts", key, `{"text":"before"}`); status != http.StatusCreated {
		t.Fatalf("before password change: got %d want 201 (%s)", status, body)
	}

	newHash, err := auth.HashPassword("brand-new-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.SetPassword(ctx, c, newHash); err != nil {
		t.Fatal(err)
	}
	if status, body := h.apiPost("/api/v1/molts", key, `{"text":"after"}`); status != http.StatusUnauthorized {
		t.Fatalf("key still worked after password change: got %d want 401 (%s)", status, body)
	}
}

// TestAPIMentionRespectsBlocks checks an API molt's mentions don't notify a
// crab that blocked the author (the website filters this; the API must too).
func TestAPIMentionRespectsBlocks(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	hash, err := auth.HashPassword("unused")
	if err != nil {
		t.Fatal(err)
	}
	author, err := h.store.CreateCrab(ctx, "author", "author@krabber.test", hash)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.ActivateCrab(ctx, author.ID); err != nil {
		t.Fatal(err)
	}
	key, _, err := h.store.CreateAPIKey(ctx, author, "k", []string{store.ScopeWriteMolts})
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := h.store.CreateCrab(ctx, "blocker", "blocker@krabber.test", hash)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.ActivateCrab(ctx, blocker.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Block(ctx, blocker, author); err != nil {
		t.Fatal(err)
	}

	if status, body := h.apiPost("/api/v1/molts", key, `{"text":"hey @blocker"}`); status != http.StatusCreated {
		t.Fatalf("post: got %d want 201 (%s)", status, body)
	}
	notes, err := h.store.Notifications(ctx, blocker.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range notes {
		if n.Type == store.NotifyMention {
			t.Fatalf("blocked crab got a mention notification: %+v", n)
		}
	}
}

// TestAPIRateLimitPerNetwork checks the per-network request cap, before any key
// lookup, returns 429.
func TestAPIRateLimitPerNetwork(t *testing.T) {
	h := newHarness(t)
	defer func(n int) { apiRatePerMin = n }(apiRatePerMin)
	apiRatePerMin = 3

	var got429 bool
	for i := 0; i < 5; i++ {
		// No token: these are rejected at auth, but each still counts toward the
		// per-network cap, which is checked first.
		if status, _ := h.apiPost("/api/v1/molts", "", `{"text":"x"}`); status == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatal("expected a 429 after exceeding the per-network rate limit")
	}
}
