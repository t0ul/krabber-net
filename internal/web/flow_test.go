package web

import (
	"context"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/segmentio/ksuid"

	"github.com/t0ul/krabber-net/internal/config"
	"github.com/t0ul/krabber-net/internal/mail"
	"github.com/t0ul/krabber-net/internal/platform"
	"github.com/t0ul/krabber-net/internal/store"
)

// These tests run the whole site over HTTPS against DynamoDB Local, with a
// client that behaves like a browser behind CloudFront (origin secret header,
// same-origin fetch metadata, cookie jar).

const originSecret = "test-origin-secret"

type capturedMail struct {
	mu   sync.Mutex
	msgs []mail.Message
}

func (c *capturedMail) Send(_ context.Context, m mail.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, m)
	return nil
}

func (c *capturedMail) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.msgs)
}

func (c *capturedMail) last(t *testing.T) mail.Message {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.msgs) == 0 {
		t.Fatal("no email sent")
	}
	return c.msgs[len(c.msgs)-1]
}

// testLog sends server logs to the test log (shown only when a test fails).
type testLog struct{ t *testing.T }

func (l testLog) Write(p []byte) (int, error) {
	l.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

type harness struct {
	t      *testing.T
	srv    *httptest.Server
	store  *store.Store
	mail   *capturedMail
	client *http.Client
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	endpoint := os.Getenv("KRABBER_TEST_DYNAMO_ENDPOINT")
	if endpoint == "" {
		t.Skip("KRABBER_TEST_DYNAMO_ENDPOINT not set; skipping DynamoDB Local test")
	}
	ctx := context.Background()
	awsCfg, err := platform.AWSConfig(ctx, "us-east-2")
	if err != nil {
		t.Fatal(err)
	}
	db := platform.DynamoDB(awsCfg, endpoint)
	table := "krabber-webtest-" + ksuid.New().String()
	if err := store.EnsureTable(ctx, db, table); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(table)})
	})
	st := store.New(db, table)

	srv := httptest.NewUnstartedServer(nil)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)

	log := slog.New(slog.NewTextHandler(testLog{t}, nil))
	captured := &capturedMail{}
	app, err := New(Deps{
		Config:   &config.Config{Env: "prod", BaseURL: base, TableName: table, OriginVerifySecrets: []string{originSecret}},
		Log:      log,
		Store:    st,
		Mailer:   mail.New(captured, st, 100, log),
		Fanout:   realQueue{t: t, s: st},
		Notifier: realQueue{t: t, s: st},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.Config.Handler = app.Routes()

	h := &harness{t: t, srv: srv, store: st, mail: captured}
	h.client = h.newClient()
	return h
}

// browser adds what a real browser and CloudFront would add to each request.
type browser struct {
	base   http.RoundTripper
	origin string
}

func (b browser) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-Origin-Verify", originSecret)
	if r.Header.Get("Sec-Fetch-Site") == "" {
		r.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	if r.Method == http.MethodPost && r.Header.Get("Origin") == "" {
		r.Header.Set("Origin", b.origin)
	}
	return b.base.RoundTrip(r)
}

func (h *harness) newClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{
		Jar:           jar,
		Transport:     browser{base: h.srv.Client().Transport, origin: h.srv.URL},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

var csrfRX = regexp.MustCompile(`name='csrf_token' value='([^']+)'`)

func (h *harness) get(path string) (int, string, http.Header) {
	h.t.Helper()
	res, err := h.client.Get(h.srv.URL + path)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b), res.Header
}

// csrf loads a page and returns its CSRF token.
func (h *harness) csrf(path string) string {
	h.t.Helper()
	_, body, _ := h.get(path)
	m := csrfRX.FindStringSubmatch(body)
	if m == nil {
		h.t.Fatalf("no csrf token on %s", path)
	}
	return html.UnescapeString(m[1])
}

func (h *harness) post(path string, form url.Values, headers ...string) (int, string, http.Header) {
	h.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b), res.Header
}

func (h *harness) sessionCookie() string {
	u, _ := url.Parse(h.srv.URL)
	for _, c := range h.client.Jar.Cookies(u) {
		if c.Name == "__Host-krabber_session" {
			return c.Value
		}
	}
	return ""
}

func (h *harness) signupAndActivate(name, email, password string) {
	h.t.Helper()
	tok := h.csrf("/crab/signup")
	status, body, hdr := h.post("/crab/signup", url.Values{"csrf_token": {tok}, "name": {name}, "email": {email}, "password": {password}})
	if status != http.StatusSeeOther || hdr.Get("Location") != "/crab/activate" {
		h.t.Fatalf("signup: %d %s %s", status, hdr.Get("Location"), body)
	}
	m := regexp.MustCompile(`token=([A-Z2-7]{26})`).FindStringSubmatch(h.mail.last(h.t).Text)
	if m == nil {
		h.t.Fatalf("no token in activation email: %s", h.mail.last(h.t).Text)
	}
	tok = h.csrf("/crab/activate?token=" + m[1])
	status, _, hdr = h.post("/crab/activate", url.Values{"csrf_token": {tok}, "token": {m[1]}})
	if status != http.StatusSeeOther || hdr.Get("Location") != "/crab/login" {
		h.t.Fatalf("activate: %d %s", status, hdr.Get("Location"))
	}
}

func (h *harness) login(email, password string) (int, string) {
	h.t.Helper()
	tok := h.csrf("/crab/login")
	status, body, _ := h.post("/crab/login", url.Values{"csrf_token": {tok}, "email": {email}, "password": {password}})
	return status, body
}

func TestSignupActivateLoginMoltLogout(t *testing.T) {
	h := newHarness(t)
	h.signupAndActivate("spongebob", "SpongeBob@Krabber.test", "jellyfishing!")

	if status, body := h.login("spongebob@krabber.test", "wrong-password"); status != http.StatusUnprocessableEntity || !strings.Contains(body, "Email or password is incorrect") {
		t.Fatalf("wrong password: %d", status)
	}

	before := h.sessionCookie()
	if status, _ := h.login("SPONGEBOB@krabber.test", "jellyfishing!"); status != http.StatusSeeOther {
		t.Fatalf("login: %d", status)
	}
	after := h.sessionCookie()
	if after == "" || after == before {
		t.Fatal("login must issue a new session token")
	}

	// The pre-login token is dead: a client presenting it is anonymous.
	if before != "" {
		old := h.newClient()
		u, _ := url.Parse(h.srv.URL)
		old.Jar.SetCookies(u, []*http.Cookie{{Name: "__Host-krabber_session", Value: before, Path: "/", Secure: true}})
		res, err := old.Get(h.srv.URL + "/trench")
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusSeeOther {
			t.Fatalf("old session token still works: %d", res.StatusCode)
		}
	}

	tok := h.csrf("/moltinTime")
	status, body, _ := h.post("/molt/create", url.Values{"csrf_token": {tok}, "content": {"I'm ready!"}}, "HX-Request", "true")
	if status != http.StatusOK || !strings.Contains(body, "I&#39;m ready!") {
		t.Fatalf("create molt: %d %s", status, body)
	}
	if status, body, _ := h.get("/moltinTime"); status != http.StatusOK || !strings.Contains(body, "I&#39;m ready!") {
		t.Fatalf("moltinTime: %d", status)
	}

	// No CSRF token: rejected with 400 (never 403).
	if status, _, _ := h.post("/molt/create", url.Values{"content": {"sneaky"}}); status != http.StatusBadRequest {
		t.Fatalf("missing csrf token: %d", status)
	}
	// Cross-site form post: rejected with 400.
	if status, _, _ := h.post("/molt/create", url.Values{"csrf_token": {tok}, "content": {"x"}},
		"Origin", "https://evil.example", "Sec-Fetch-Site", "cross-site"); status != http.StatusBadRequest {
		t.Fatalf("cross-site post: %d", status)
	}

	if status, _, _ := h.post("/crab/logout", url.Values{"csrf_token": {tok}}); status != http.StatusSeeOther {
		t.Fatalf("logout: %d", status)
	}
	if status, _, hdr := h.get("/trench"); status != http.StatusSeeOther || hdr.Get("Location") != "/crab/login" {
		t.Fatalf("after logout: %d %s", status, hdr.Get("Location"))
	}
}

// realQueue runs fan-out inline so tests see trench entries immediately.
type realQueue struct {
	t *testing.T
	s *store.Store
}

func (q realQueue) Enqueue(m *store.Molt) {
	if err := q.s.AddToTrenches(context.Background(), m, []string{m.OwnerID}); err != nil {
		q.t.Error(err)
	}
	err := q.s.EachFollowerPage(context.Background(), m.OwnerID, func(ids []string) error {
		return q.s.AddToTrenches(context.Background(), m, ids)
	})
	if err != nil {
		q.t.Error(err)
	}
}

func (q realQueue) Notify(n store.Notification) {
	if err := q.s.AddNotification(context.Background(), n); err != nil {
		q.t.Error(err)
	}
}

var likeCountRX = regexp.MustCompile(`(?s)class="mini-molt-action like[^"]*"[^>]*>.*?mini-molt-action-counter ml-1">(\d+)<`)

func TestFeedsProfileSearchAndLiveCounts(t *testing.T) {
	h := newHarness(t)
	h.signupAndActivate("sandy", "sandy@krabber.test", "karate-chop!")
	h.signupAndActivate("gary", "gary@krabber.test", "meow-meow-meow")
	sandy, _ := h.store.CrabByUsername(context.Background(), "sandy")

	if status, _ := h.login("gary@krabber.test", "meow-meow-meow"); status != http.StatusSeeOther {
		t.Fatalf("login: %d", status)
	}

	// Follow from the profile page; the button flips to "Following".
	status, body, _ := h.get("/crabs/SANDY")
	if status != http.StatusOK || !strings.Contains(body, "@sandy") || !strings.Contains(body, ">Follow<") {
		t.Fatalf("profile before follow: %d", status)
	}
	tok := h.csrf("/crabs/sandy")
	status, body, _ = h.post("/follow/"+sandy.ID, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
	if status != http.StatusOK || !strings.Contains(body, "Following") || !strings.Contains(body, "/unfollow/"+sandy.ID) {
		t.Fatalf("follow fragment: %d %s", status, body)
	}
	if _, body, _ := h.get("/crabs/sandy/followers"); !strings.Contains(body, "@gary") {
		t.Fatal("followers list is missing gary")
	}

	// A new molt comes back as a fragment and lands in the author's own trench.
	status, body, _ = h.post("/molt/create", url.Values{"csrf_token": {tok}, "content": {"meow"}}, "HX-Request", "true")
	if status != http.StatusOK || !strings.Contains(body, "meow") || !strings.Contains(body, "/molt/like/") {
		t.Fatalf("create fragment: %d %s", status, body)
	}
	if _, body, _ := h.get("/trench"); !strings.Contains(body, ">meow<") {
		t.Fatal("own molt missing from own trench")
	}

	// Liking returns the action bar with the new count and a filled heart.
	m, err := h.store.MoltsByOwner(context.Background(), currentID(t, h, "gary"), 1)
	if err != nil || len(m) != 1 {
		t.Fatalf("find molt: %v %v", m, err)
	}
	status, body, _ = h.post("/molt/like/"+m[0].ID, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
	if got := likeCountRX.FindStringSubmatch(body); status != http.StatusOK || got == nil || got[1] != "1" || !strings.Contains(body, "liked") {
		t.Fatalf("like fragment: %d %v", status, got)
	}
	// The sea shows the same live count.
	if _, body, _ := h.get("/sea"); likeCountRX.FindStringSubmatch(body) == nil || likeCountRX.FindStringSubmatch(body)[1] != "1" {
		t.Fatal("sea shows a stale like count")
	}
	// Liking again unlikes.
	_, body, _ = h.post("/molt/like/"+m[0].ID, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
	if got := likeCountRX.FindStringSubmatch(body); got == nil || got[1] != "0" {
		t.Fatalf("unlike fragment: %v", got)
	}

	// Search finds crabs by name and molts by content; times are relative.
	status, body, _ = h.get("/search?q=mEoW")
	if status != http.StatusOK || !strings.Contains(body, ">meow<") || !strings.Contains(body, "<time datetime=") || !strings.Contains(body, ">now<") {
		t.Fatalf("search molts: %d", status)
	}
	if _, body, _ := h.get("/search?q=and"); !strings.Contains(body, "@sandy") {
		t.Fatal("search crabs: sandy not found")
	}
}

func TestDeleteMolt(t *testing.T) {
	h := newHarness(t)
	h.signupAndActivate("pearl", "pearl@krabber.test", "whale-of-a-time")
	h.signupAndActivate("larry", "larry@krabber.test", "pump-it-up-now")
	ctx := context.Background()

	if status, _ := h.login("pearl@krabber.test", "whale-of-a-time"); status != http.StatusSeeOther {
		t.Fatalf("login: %d", status)
	}
	tok := h.csrf("/trench")
	h.post("/molt/create", url.Values{"csrf_token": {tok}, "content": {"going to the mall"}}, "HX-Request", "true")
	molts, _ := h.store.MoltsByOwner(ctx, currentID(t, h, "pearl"), 1)
	if len(molts) != 1 {
		t.Fatal("molt not created")
	}
	id := molts[0].ID

	// The author sees the menu; nobody else does.
	if _, body, _ := h.get("/molt/view/" + id); !strings.Contains(body, "/molt/delete/"+id+"?from=thread") {
		t.Fatal("author has no delete option on the thread")
	}

	h.post("/crab/logout", url.Values{"csrf_token": {tok}})
	h.login("larry@krabber.test", "pump-it-up-now")
	tok = h.csrf("/trench")
	if _, body, _ := h.get("/sea"); strings.Contains(body, "/molt/delete/"+id) {
		t.Fatal("another crab sees the delete option")
	}
	if status, _, _ := h.post("/molt/delete/"+id, url.Values{"csrf_token": {tok}}, "HX-Request", "true"); status != http.StatusNotFound {
		t.Fatalf("deleting someone else's molt: %d", status)
	}

	h.post("/crab/logout", url.Values{"csrf_token": {tok}})
	h.login("pearl@krabber.test", "whale-of-a-time")
	tok = h.csrf("/trench")
	status, _, hdr := h.post("/molt/delete/"+id, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
	if status != http.StatusOK || !strings.Contains(hdr.Get("HX-Trigger"), `"moltDeleted"`) {
		t.Fatalf("delete: %d %q", status, hdr.Get("HX-Trigger"))
	}
	if status, _, _ := h.get("/molt/view/" + id); status != http.StatusNotFound {
		t.Fatalf("deleted thread: %d", status)
	}
	if _, body, _ := h.get("/sea"); strings.Contains(body, "going to the mall") {
		t.Fatal("deleted molt still on the sea")
	}
}

var badgeRX = regexp.MustCompile(`class="kb-badge" aria-label="(\d+) unread"`)

func TestNotifications(t *testing.T) {
	h := newHarness(t)
	h.signupAndActivate("karen", "karen@krabber.test", "computer-wife!")
	h.signupAndActivate("plankton", "plankton@krabber.test", "formula-thief!")
	karen, _ := h.store.CrabByUsername(context.Background(), "karen")

	h.login("karen@krabber.test", "computer-wife!")
	tok := h.csrf("/trench")
	h.post("/molt/create", url.Values{"csrf_token": {tok}, "content": {"Plankton, dinner is ready"}}, "HX-Request", "true")
	molts, _ := h.store.MoltsByOwner(context.Background(), karen.ID, 1)
	id := molts[0].ID
	// Liking your own molt doesn't notify you.
	h.post("/molt/like/"+id, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
	h.post("/crab/logout", url.Values{"csrf_token": {tok}})

	h.login("plankton@krabber.test", "formula-thief!")
	tok = h.csrf("/trench")
	h.post("/follow/"+karen.ID, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
	for range 3 { // like, unlike, like: one notification
		h.post("/molt/like/"+id, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
	}
	h.post("/comment/"+id, url.Values{"csrf_token": {tok}, "comment": {"Coming, my love"}}, "HX-Request", "true")
	h.post("/crab/logout", url.Values{"csrf_token": {tok}})

	h.login("karen@krabber.test", "computer-wife!")
	_, body, _ := h.get("/trench")
	if got := badgeRX.FindStringSubmatch(body); got == nil || got[1] != "3" {
		t.Fatalf("badge before reading: %v", got)
	}
	_, body, _ = h.get("/notifications")
	for _, want := range []string{"followed you", "liked your molt", "commented on your molt", "Coming, my love", "/molt/view/" + id} {
		if !strings.Contains(body, want) {
			t.Errorf("notifications page missing %q", want)
		}
	}
	if strings.Count(body, "liked your molt") != 1 {
		t.Errorf("like notified %d times", strings.Count(body, "liked your molt"))
	}
	if _, body, _ := h.get("/notifications/badge"); badgeRX.MatchString(body) || !strings.Contains(body, `hx-trigger="every 60s"`) {
		t.Fatalf("badge after reading: %s", body)
	}
}

func TestSettings(t *testing.T) {
	h := newHarness(t)
	h.signupAndActivate("gary", "gary@krabber.test", "meow-meow-meow")
	h.login("gary@krabber.test", "meow-meow-meow")

	tok := h.csrf("/settings")
	status, body, _ := h.post("/settings/profile", url.Values{"csrf_token": {tok}, "display_name": {"Gary"}, "website": {"javascript:alert(1)"}})
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "Use a web address") {
		t.Fatalf("bad website: %d", status)
	}
	status, _, hdr := h.post("/settings/profile", url.Values{"csrf_token": {tok},
		"display_name": {"  Gary the Snail  "}, "bio": {"Meow.\r\nSnail."}, "location": {"Pineapple"}, "website": {"garythesnail.example/"}})
	if status != http.StatusSeeOther || hdr.Get("Location") != "/settings" {
		t.Fatalf("save profile: %d", status)
	}
	_, body, _ = h.get("/crabs/gary")
	for _, want := range []string{"Gary the Snail", "Meow.\nSnail.", "Pineapple", `href="https://garythesnail.example/"`, "</svg> garythesnail.example\n", "Edit profile"} {
		if !strings.Contains(body, want) {
			t.Errorf("profile missing %q", want)
		}
	}

	// Clearing a field removes it.
	h.post("/settings/profile", url.Values{"csrf_token": {tok}, "display_name": {"Gary the Snail"}})
	if c, _ := h.store.CrabByUsername(context.Background(), "gary"); c.Website != "" || c.Bio != "" || c.DisplayName != "Gary the Snail" {
		t.Fatalf("after clearing: %+v", c.Profile)
	}

	// A second device is signed out by a password change; this one isn't.
	mine := h.client
	h.client = h.newClient()
	h.login("gary@krabber.test", "meow-meow-meow")
	other := h.client
	h.client = mine

	status, body, _ = h.post("/settings/password", url.Values{"csrf_token": {tok}, "current_password": {"woof"}, "new_password": {"shell-polish-1"}, "confirm_password": {"shell-polish-1"}})
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "isn&#39;t your current password") {
		t.Fatalf("wrong current password: %d", status)
	}
	status, _, _ = h.post("/settings/password", url.Values{"csrf_token": {tok}, "current_password": {"meow-meow-meow"}, "new_password": {"shell-polish-1"}, "confirm_password": {"shell-polish-1"}})
	if status != http.StatusSeeOther {
		t.Fatalf("change password: %d", status)
	}
	if status, _, _ := h.get("/settings"); status != http.StatusOK {
		t.Fatalf("this session after change: %d", status)
	}
	h.client = other
	if status, _, _ := h.get("/settings"); status != http.StatusSeeOther {
		t.Fatalf("other session after change: %d", status)
	}
	if status, _ := h.login("gary@krabber.test", "meow-meow-meow"); status == http.StatusSeeOther {
		t.Fatal("old password still works")
	}
	if status, _ := h.login("gary@krabber.test", "shell-polish-1"); status != http.StatusSeeOther {
		t.Fatalf("new password: %d", status)
	}
}

func TestBlocking(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.signupAndActivate("sandy", "sandy@krabber.test", "karate-chop!")
	h.signupAndActivate("plankton", "plankton@krabber.test", "formula-thief!")
	sandyID, planktonID := currentID(t, h, "sandy"), currentID(t, h, "plankton")

	h.login("plankton@krabber.test", "formula-thief!")
	tok := h.csrf("/trench")
	h.post("/follow/"+sandyID, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
	h.post("/molt/create", url.Values{"csrf_token": {tok}, "content": {"Give me the formula"}}, "HX-Request", "true")
	h.post("/crab/logout", url.Values{"csrf_token": {tok}})

	h.login("sandy@krabber.test", "karate-chop!")
	tok = h.csrf("/trench")
	h.post("/molt/create", url.Values{"csrf_token": {tok}, "content": {"Hi-yah! Texas pride"}}, "HX-Request", "true")
	sandyMolts, _ := h.store.MoltsByOwner(ctx, sandyID, 1)
	if _, body, _ := h.get("/sea"); !strings.Contains(body, "Give me the formula") || !strings.Contains(body, "Block @plankton") {
		t.Fatal("before block: plankton's molt or its block option missing from the sea")
	}
	status, _, hdr := h.post("/block/"+planktonID, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
	if status != http.StatusNoContent || hdr.Get("HX-Refresh") != "true" {
		t.Fatalf("block: %d", status)
	}
	for _, path := range []string{"/sea", "/crabs", "/search?q=formula"} {
		if _, body, _ := h.get(path); strings.Contains(body, "Give me the formula") || strings.Contains(body, `href="/crabs/plankton"`) {
			t.Errorf("%s still shows plankton", path)
		}
	}
	if _, body, _ := h.get("/crabs/plankton"); !strings.Contains(body, "You blocked @plankton") || !strings.Contains(body, "/unblock/"+planktonID) {
		t.Error("blocked profile should offer unblock")
	}
	if _, body, _ := h.get("/settings"); !strings.Contains(body, "/unblock/"+planktonID) {
		t.Error("settings should list the block")
	}
	if c, _ := h.store.CrabByUsername(ctx, "sandy"); c.FollowerCount != 0 {
		t.Errorf("follow survived the block: %d followers", c.FollowerCount)
	}
	h.post("/crab/logout", url.Values{"csrf_token": {tok}})

	// Plankton can't find, follow or touch Sandy.
	h.login("plankton@krabber.test", "formula-thief!")
	tok = h.csrf("/trench")
	if status, _, _ := h.get("/crabs/sandy"); status != http.StatusNotFound {
		t.Errorf("blocker's profile: %d", status)
	}
	if _, body, _ := h.get("/sea"); strings.Contains(body, "Texas pride") {
		t.Error("blocked crab sees the blocker's molt")
	}
	if status, _, _ := h.post("/molt/like/"+sandyMolts[0].ID, url.Values{"csrf_token": {tok}}, "HX-Request", "true"); status != http.StatusNotFound {
		t.Errorf("like blocker's molt: %d", status)
	}
	if _, body, _ := h.post("/follow/"+sandyID, url.Values{"csrf_token": {tok}}, "HX-Request", "true"); !strings.Contains(body, ">Follow<") {
		t.Errorf("follow blocker: %s", body)
	}
	if ok, _ := h.store.IsFollowing(ctx, planktonID, sandyID); ok {
		t.Error("follow went through")
	}
	h.post("/crab/logout", url.Values{"csrf_token": {tok}})

	h.login("sandy@krabber.test", "karate-chop!")
	tok = h.csrf("/trench")
	h.post("/unblock/"+planktonID, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
	if _, body, _ := h.get("/sea"); !strings.Contains(body, "Give me the formula") {
		t.Error("unblock should bring plankton's molt back")
	}
}

func TestPasswordReset(t *testing.T) {
	h := newHarness(t)
	h.signupAndActivate("pearl", "pearl@krabber.test", "whale-of-a-time")
	h.login("pearl@krabber.test", "whale-of-a-time")
	signedIn := h.client
	h.client = h.newClient()
	sent := h.mail.count()

	// Unknown emails get the same answer and no email.
	tok := h.csrf("/crab/forgot")
	status, _, hdr := h.post("/crab/forgot", url.Values{"csrf_token": {tok}, "email": {"nobody@krabber.test"}})
	if status != http.StatusSeeOther || hdr.Get("Location") != "/crab/reset" || h.mail.count() != sent {
		t.Fatalf("unknown email: %d %s, %d emails", status, hdr.Get("Location"), h.mail.count()-sent)
	}
	h.post("/crab/forgot", url.Values{"csrf_token": {tok}, "email": {"PEARL@krabber.test"}})
	msg := h.mail.last(t)
	m := regexp.MustCompile(`token=([A-Z2-7]{26})`).FindStringSubmatch(msg.Text)
	if h.mail.count() != sent+1 || m == nil || !strings.Contains(msg.Subject, "Reset") {
		t.Fatalf("reset email: %+v", msg)
	}

	// A rejected password keeps the token usable.
	tok = h.csrf("/crab/reset?token=" + m[1])
	if status, _, _ := h.post("/crab/reset", url.Values{"csrf_token": {tok}, "token": {m[1]}, "password": {"short"}, "confirm_password": {"short"}}); status != http.StatusUnprocessableEntity {
		t.Fatalf("short password: %d", status)
	}
	status, _, hdr = h.post("/crab/reset", url.Values{"csrf_token": {tok}, "token": {m[1]}, "password": {"daddy-buy-me"}, "confirm_password": {"daddy-buy-me"}})
	if status != http.StatusSeeOther || hdr.Get("Location") != "/crab/login" {
		t.Fatalf("reset: %d %s", status, hdr.Get("Location"))
	}
	tok = h.csrf("/crab/reset")
	if status, body, _ := h.post("/crab/reset", url.Values{"csrf_token": {tok}, "token": {m[1]}, "password": {"again-and-again"}, "confirm_password": {"again-and-again"}}); status != http.StatusUnprocessableEntity || !strings.Contains(body, "invalid or has expired") {
		t.Fatalf("token reused: %d", status)
	}

	if status, _ := h.login("pearl@krabber.test", "whale-of-a-time"); status == http.StatusSeeOther {
		t.Fatal("old password still works")
	}
	if status, _ := h.login("pearl@krabber.test", "daddy-buy-me"); status != http.StatusSeeOther {
		t.Fatalf("new password: %d", status)
	}
	h.client = signedIn
	if status, _, _ := h.get("/trench"); status != http.StatusSeeOther {
		t.Fatalf("old session survived the reset: %d", status)
	}
}

func currentID(t *testing.T, h *harness, name string) string {
	t.Helper()
	c, err := h.store.CrabByUsername(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func TestLoginRules(t *testing.T) {
	h := newHarness(t)

	// Not activated yet: the right password still doesn't sign in.
	tok := h.csrf("/crab/signup")
	h.post("/crab/signup", url.Values{"csrf_token": {tok}, "name": {"plankton"}, "email": {"plankton@krabber.test"}, "password": {"formula-is-mine"}})
	if status, body := h.login("plankton@krabber.test", "formula-is-mine"); status != http.StatusUnprocessableEntity || !strings.Contains(body, "activate your account") {
		t.Fatalf("unactivated login: %d", status)
	}

	// Same email in another case, or same name, can't register again.
	tok = h.csrf("/crab/signup")
	if status, body, _ := h.post("/crab/signup", url.Values{"csrf_token": {tok}, "name": {"other"}, "email": {"PLANKTON@krabber.test"}, "password": {"formula-is-mine"}}); status != http.StatusUnprocessableEntity || !strings.Contains(body, "already in use") {
		t.Fatalf("duplicate email: %d", status)
	}

	h.signupAndActivate("karen", "karen@krabber.test", "computer-wife")

	// Five wrong passwords lock the email for the window.
	for i := 0; i < loginFailureLimit; i++ {
		h.login("karen@krabber.test", "nope")
	}
	if status, body := h.login("karen@krabber.test", "computer-wife"); status != http.StatusTooManyRequests || !strings.Contains(body, "Too many attempts") {
		t.Fatalf("after %d failures: %d", loginFailureLimit, status)
	}
}

func TestBanEndsSessions(t *testing.T) {
	h := newHarness(t)
	h.signupAndActivate("squidward", "squidward@krabber.test", "clarinet-solo")
	if status, _ := h.login("squidward@krabber.test", "clarinet-solo"); status != http.StatusSeeOther {
		t.Fatalf("login: %d", status)
	}
	if status, _, _ := h.get("/trench"); status != http.StatusOK {
		t.Fatalf("trench before ban: %d", status)
	}

	c, err := h.store.CrabByEmail(context.Background(), "squidward@krabber.test")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetBanned(context.Background(), c, true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if status, _, _ := h.get("/trench"); status != http.StatusSeeOther {
		t.Fatalf("trench after ban: %d", status)
	}
}

func TestRequestsWithoutOriginSecretAre404(t *testing.T) {
	h := newHarness(t)
	res, err := h.srv.Client().Get(h.srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("no origin secret: %d", res.StatusCode)
	}
	if status, body, _ := h.get("/healthz"); status != http.StatusOK || body != "ok\n" {
		t.Fatalf("healthz via CloudFront: %d %q", status, body)
	}
}
