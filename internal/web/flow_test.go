package web

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/segmentio/ksuid"

	"github.com/t0ul/krabber-net/internal/auth"
	"github.com/t0ul/krabber-net/internal/avatar"
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
	cards  *cardRecorder
	app    *App
	client *http.Client
}

// cardRecorder keeps the link cards asked for, instead of fetching them.
type cardRecorder struct {
	mu   sync.Mutex
	urls []string
}

func (c *cardRecorder) EnqueueCard(u string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.urls = append(c.urls, u)
}

func (c *cardRecorder) asked(u string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Contains(c.urls, u)
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessWith(t, nil, nil)
}

// newHarnessWith is newHarness with a config tweak and an extra log handler.
func newHarnessWith(t *testing.T, tweak func(*config.Config), extraLog slog.Handler) *harness {
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

	var logHandler slog.Handler = slog.NewTextHandler(testLog{t}, nil)
	if extraLog != nil {
		logHandler = extraLog
	}
	log := slog.New(logHandler)
	captured := &capturedMail{}
	cards := &cardRecorder{}
	cfg := &config.Config{Env: "prod", BaseURL: base, TableName: table, OriginVerifySecrets: []string{originSecret}}
	if tweak != nil {
		tweak(cfg)
	}
	app, err := New(Deps{
		Config:   cfg,
		Log:      log,
		Store:    st,
		Mailer:   mail.New(captured, st, 100, log),
		Fanout:   realQueue{t: t, s: st},
		Notifier: realQueue{t: t, s: st},
		Cards:    cards,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.Config.Handler = app.Routes()

	h := &harness{t: t, srv: srv, store: st, mail: captured, cards: cards, app: app}
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

func (h *harness) get(path string, headers ...string) (int, string, http.Header) {
	h.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, h.srv.URL+path, nil)
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
	tok := h.csrf("/krab/signup")
	status, body, hdr := h.post("/krab/signup", url.Values{"csrf_token": {tok}, "name": {name}, "email": {email}, "password": {password}})
	if status != http.StatusSeeOther || hdr.Get("Location") != "/krab/activate" {
		h.t.Fatalf("signup: %d %s %s", status, hdr.Get("Location"), body)
	}
	m := regexp.MustCompile(`token=([A-Z2-7]{26})`).FindStringSubmatch(h.mail.last(h.t).Text)
	if m == nil {
		h.t.Fatalf("no token in activation email: %s", h.mail.last(h.t).Text)
	}
	tok = h.csrf("/krab/activate?token=" + m[1])
	status, _, hdr = h.post("/krab/activate", url.Values{"csrf_token": {tok}, "token": {m[1]}})
	if status != http.StatusSeeOther || hdr.Get("Location") != "/krab/login" {
		h.t.Fatalf("activate: %d %s", status, hdr.Get("Location"))
	}
}

func (h *harness) login(email, password string) (int, string) {
	h.t.Helper()
	tok := h.csrf("/krab/login")
	status, body, _ := h.post("/krab/login", url.Values{"csrf_token": {tok}, "email": {email}, "password": {password}})
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

	tok := h.csrf("/trench")
	status, body, _ := h.post("/molt/create", url.Values{"csrf_token": {tok}, "content": {"I'm ready!"}}, "HX-Request", "true")
	if status != http.StatusOK || !strings.Contains(body, "I&#39;m ready!") {
		t.Fatalf("create molt: %d %s", status, body)
	}
	if status, body, _ := h.get("/trench"); status != http.StatusOK || !strings.Contains(body, "I&#39;m ready!") {
		t.Fatalf("trench: %d", status)
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

	if status, _, _ := h.post("/krab/logout", url.Values{"csrf_token": {tok}}); status != http.StatusSeeOther {
		t.Fatalf("logout: %d", status)
	}
	if status, _, hdr := h.get("/trench"); status != http.StatusSeeOther || hdr.Get("Location") != "/krab/login" {
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

	// Other capitalization goes to the profile's own address.
	if status, _, hdr := h.get("/krabs/SANDY"); status != http.StatusMovedPermanently || hdr.Get("Location") != "/krabs/sandy" {
		t.Fatalf("/krabs/SANDY: %d %q", status, hdr.Get("Location"))
	}
	// Follow from the profile page; the button flips to "Following".
	status, body, _ := h.get("/krabs/sandy")
	if status != http.StatusOK || !strings.Contains(body, "@sandy") || !strings.Contains(body, ">Follow<") {
		t.Fatalf("profile before follow: %d", status)
	}
	tok := h.csrf("/krabs/sandy")
	status, body, _ = h.post("/follow/"+sandy.ID, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
	if status != http.StatusOK || !strings.Contains(body, "Following") || !strings.Contains(body, "/unfollow/"+sandy.ID) {
		t.Fatalf("follow fragment: %d %s", status, body)
	}
	if _, body, _ := h.get("/krabs/sandy/followers"); !strings.Contains(body, `data-name="gary"`) {
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

	h.post("/krab/logout", url.Values{"csrf_token": {tok}})
	h.login("larry@krabber.test", "pump-it-up-now")
	tok = h.csrf("/trench")
	if _, body, _ := h.get("/sea"); strings.Contains(body, "/molt/delete/"+id) {
		t.Fatal("another crab sees the delete option")
	}
	if status, _, _ := h.post("/molt/delete/"+id, url.Values{"csrf_token": {tok}}, "HX-Request", "true"); status != http.StatusNotFound {
		t.Fatalf("deleting someone else's molt: %d", status)
	}

	h.post("/krab/logout", url.Values{"csrf_token": {tok}})
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
	h.post("/krab/logout", url.Values{"csrf_token": {tok}})

	h.login("plankton@krabber.test", "formula-thief!")
	tok = h.csrf("/trench")
	h.post("/follow/"+karen.ID, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
	for range 3 { // like, unlike, like: one notification
		h.post("/molt/like/"+id, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
	}
	h.post("/molt/reply/"+id, url.Values{"csrf_token": {tok}, "content": {"Coming, my love"}}, "HX-Request", "true")
	h.post("/krab/logout", url.Values{"csrf_token": {tok}})

	h.login("karen@krabber.test", "computer-wife!")
	_, body, _ := h.get("/trench")
	// Follow, like, reply, and the Baby Krab and Social Newbie trophies.
	if got := badgeRX.FindStringSubmatch(body); got == nil || got[1] != "5" {
		t.Fatalf("badge before reading: %v", got)
	}
	_, body, _ = h.get("/notifications")
	for _, want := range []string{"followed you", "liked your molt", "replied to your molt", "Coming, my love", "/molt/view/" + id} {
		if !strings.Contains(body, want) {
			t.Errorf("notifications page missing %q", want)
		}
	}
	if strings.Count(body, "liked your molt") != 1 {
		t.Errorf("like notified %d times", strings.Count(body, "liked your molt"))
	}
	if _, body, _ := h.get("/notifications/badge"); badgeRX.MatchString(body) || !strings.Contains(body, `hx-trigger="kb:poll from:body"`) {
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
	_, body, _ = h.get("/krabs/gary")
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

func TestFunFacts(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.signupAndActivate("gary", "gary@krabber.test", "meow-meow-meow")
	h.login("gary@krabber.test", "meow-meow-meow")
	if _, body, _ := h.get("/krabs/gary"); !strings.Contains(body, "Full bio") || !strings.Contains(body, "filled out their bio") {
		t.Fatal("an empty full bio should say so")
	}

	tok := h.csrf("/settings")
	status, body, _ := h.post("/settings/profile", url.Values{"csrf_token": {tok}, "fun_emoji": {strings.Repeat("🐌", 33)}})
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "Keep it under 32 characters") {
		t.Fatalf("long emoji: %d", status)
	}
	status, _, _ = h.post("/settings/profile", url.Values{"csrf_token": {tok},
		"display_name": {"Gary"}, "fun_pronouns": {"  he/him "}, "fun_jam": {"Meow\nMeow"}, "fun_emoji": {"🐌"}})
	if status != http.StatusSeeOther {
		t.Fatalf("save: %d", status)
	}
	gary, _ := h.store.CrabByUsername(ctx, "gary")
	if gary.Pronouns != "he/him" || gary.Jam != "MeowMeow" || gary.Emoji != "🐌" || gary.DisplayName != "Gary" {
		t.Fatalf("stored %+v", gary.Profile)
	}
	_, body, _ = h.get("/krabs/gary")
	for _, want := range []string{"<th>Pronouns</th><td>he/him</td>", "<th>My jam</th><td>MeowMeow</td>", "<th>Favorite emoji</th><td>🐌</td>"} {
		if !strings.Contains(body, want) {
			t.Errorf("profile missing %s", want)
		}
	}
	if strings.Contains(body, "<th>Age</th>") {
		t.Error("empty fun facts should be left out")
	}
	// A failed password change keeps the fun facts in the re-shown profile form.
	_, body, _ = h.post("/settings/password", url.Values{"csrf_token": {tok}, "current_password": {"nope"}, "new_password": {"a-new-one-1"}, "confirm_password": {"a-new-one-1"}})
	if !strings.Contains(body, `value="he/him"`) {
		t.Error("the profile form lost the fun facts")
	}

	hash, _ := auth.HashPassword("secret-boss")
	boss, _ := h.store.CreateCrab(ctx, "boss", "boss@krabber.test", hash)
	if err := h.store.ActivateCrab(ctx, boss.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetRole(ctx, boss, store.RoleModerator); err != nil {
		t.Fatal(err)
	}
	h.client = h.newClient()
	h.login("boss@krabber.test", "secret-boss")
	tok = h.csrf("/krabmin/krabs/gary")
	h.post("/krabmin/krabs/"+gary.ID, url.Values{"csrf_token": {tok}, "action": {"clear_fun_facts"}})
	if c, _ := h.store.CrabByUsername(ctx, "gary"); !c.Empty() || c.DisplayName != "Gary" {
		t.Fatalf("after clearing: %+v", c.Profile)
	}
	if _, body, _ := h.get("/krabmin/log"); !strings.Contains(body, "cleared the fun facts of") {
		t.Error("clearing not logged")
	}
}

func TestDeleteAccountFlow(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.signupAndActivate("sandy", "sandy@krabber.test", "karate-chop!")
	h.signupAndActivate("squidward", "squidward@krabber.test", "clarinet-solo")
	sandyID, squidID := currentID(t, h, "sandy"), currentID(t, h, "squidward")

	h.login("sandy@krabber.test", "karate-chop!")
	tok := h.csrf("/trench")
	h.post("/molt/create", url.Values{"csrf_token": {tok}, "content": {"Hi-yah! Texas pride"}}, "HX-Request", "true")
	sandyMolts, _ := h.store.MoltsByOwner(ctx, sandyID, 1)
	h.post("/krab/logout", url.Values{"csrf_token": {tok}})

	h.login("squidward@krabber.test", "clarinet-solo")
	tok = h.csrf("/trench")
	h.post("/molt/create", url.Values{"csrf_token": {tok}, "content": {"Clarinet recital tonight"}}, "HX-Request", "true")
	h.post("/molt/reply/"+sandyMolts[0].ID, url.Values{"csrf_token": {tok}, "content": {"Keep it down"}}, "HX-Request", "true")
	squidMolts, _ := h.store.MoltsByOwner(ctx, squidID, 1)

	status, body, _ := h.post("/settings/delete", url.Values{"csrf_token": {tok}, "password": {"wrong-note"}})
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "isn&#39;t your current password") {
		t.Fatalf("wrong password: %d", status)
	}
	if status, _, _ := h.get("/settings"); status != http.StatusOK {
		t.Fatalf("still signed in after a wrong password: %d", status)
	}
	status, _, hdr := h.post("/settings/delete", url.Values{"csrf_token": {tok}, "password": {"clarinet-solo"}})
	if status != http.StatusSeeOther || hdr.Get("Location") != "/" {
		t.Fatalf("delete: %d %s", status, hdr.Get("Location"))
	}
	if status, _, _ := h.get("/settings"); status != http.StatusSeeOther {
		t.Fatalf("still signed in after deleting: %d", status)
	}
	if status, _ := h.login("squidward@krabber.test", "clarinet-solo"); status == http.StatusSeeOther {
		t.Fatal("deleted account can sign in")
	}

	// Before the purge runs, everything by the crab is already hidden.
	h.login("sandy@krabber.test", "karate-chop!")
	if _, body, _ := h.get("/sea"); strings.Contains(body, "Clarinet recital") {
		t.Error("sea still shows the deleted crab's molt")
	}
	if _, body, _ := h.get("/molt/view/" + sandyMolts[0].ID); strings.Contains(body, "Keep it down") {
		t.Error("the deleted crab's reply is still shown")
	}
	for _, path := range []string{"/krabs/squidward", "/molt/view/" + squidMolts[0].ID} {
		if status, _, _ := h.get(path); status != http.StatusNotFound {
			t.Errorf("%s: %d", path, status)
		}
	}

	ids, err := h.store.PendingPurges(ctx, 10)
	if err != nil || len(ids) != 1 || ids[0] != squidID {
		t.Fatalf("purge queue: %v %v", ids, err)
	}
	if err := h.store.PurgeCrab(ctx, squidID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.MoltByKey(ctx, squidMolts[0].PK, squidMolts[0].SK); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("molt after purge: %v", err)
	}
}

func TestReporting(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, name := range []string{"mrkrabs", "sandy", "plankton"} {
		hash, _ := auth.HashPassword("secret-" + name)
		c, err := h.store.CreateCrab(ctx, name, name+"@krabber.test", hash)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.store.ActivateCrab(ctx, c.ID); err != nil {
			t.Fatal(err)
		}
	}
	boss, _ := h.store.CrabByUsername(ctx, "mrkrabs")
	if err := h.store.SetRole(ctx, boss, store.RoleModerator); err != nil {
		t.Fatal(err)
	}
	troll, _ := h.store.CrabByUsername(ctx, "plankton")
	m, err := h.store.CreateMolt(ctx, troll, "The Krusty Krab is closed forever, come to the Chum Bucket")
	if err != nil {
		t.Fatal(err)
	}

	h.login("plankton@krabber.test", "secret-plankton")
	if status, _, _ := h.get("/molt/report/" + m.ID); status != http.StatusNotFound {
		t.Fatalf("reporting your own molt: %d", status)
	}
	h.client = h.newClient()

	h.login("sandy@krabber.test", "secret-sandy")
	if _, body, _ := h.get("/molt/view/" + m.ID); !strings.Contains(body, "/molt/report/"+m.ID) {
		t.Fatal("no report link in the molt menu")
	}
	tok := h.csrf("/molt/report/" + m.ID)
	if status, body, _ := h.post("/molt/report/"+m.ID, url.Values{"csrf_token": {tok}}); status != http.StatusUnprocessableEntity || !strings.Contains(body, "Choose a reason") {
		t.Fatalf("no reason: %d", status)
	}
	status, _, hdr := h.post("/molt/report/"+m.ID, url.Values{"csrf_token": {tok}, "reason": {"spam"}, "note": {"Lies about the Krusty Krab"}})
	if status != http.StatusSeeOther || hdr.Get("Location") != "/molt/view/"+m.ID {
		t.Fatalf("report: %d", status)
	}
	if _, body, _ := h.get("/molt/view/" + m.ID); !strings.Contains(body, "Thanks for the report") {
		t.Error("no thanks after reporting")
	}
	h.post("/molt/report/"+m.ID, url.Values{"csrf_token": {tok}, "reason": {"hate"}})
	if _, body, _ := h.get("/molt/view/" + m.ID); !strings.Contains(body, "already reported") {
		t.Error("a second report should say it's already reported")
	}
	if status, _, _ := h.get("/krabmin/reports"); status != http.StatusNotFound {
		t.Fatalf("report queue for a regular crab: %d", status)
	}
	h.client = h.newClient()

	h.login("mrkrabs@krabber.test", "secret-mrkrabs")
	_, body, _ := h.get("/krabmin/reports")
	for _, want := range []string{"Chum Bucket", "Spam or scam", "1 report "} {
		if !strings.Contains(body, want) {
			t.Errorf("queue missing %q", want)
		}
	}
	_, body, _ = h.get("/krabmin/molts/" + m.ID)
	if !strings.Contains(body, "@sandy") || !strings.Contains(body, "Lies about the Krusty Krab") {
		t.Error("molt page doesn't show the report")
	}
	tok = h.csrf("/krabmin/reports")
	status, _, hdr = h.post("/krabmin/molts/"+m.ID, url.Values{"csrf_token": {tok}, "action": {"dismiss"}})
	if status != http.StatusSeeOther || hdr.Get("Location") != "/krabmin/reports" {
		t.Fatalf("dismiss: %d", status)
	}
	if _, body, _ := h.get("/krabmin/reports"); strings.Contains(body, `action="/krabmin/molts/`+m.ID) || !strings.Contains(body, "No open reports") {
		t.Error("dismissed report still queued")
	}
	if _, body, _ := h.get("/krabmin/log"); !strings.Contains(body, "dismissed reports on a molt by") {
		t.Error("dismissal not logged")
	}

	// Banning emails the crab the reason.
	sent := h.mail.count()
	h.post("/krabmin/krabs/"+troll.ID, url.Values{"csrf_token": {tok}, "action": {"ban"}, "note": {"Spam about the Krusty Krab"}})
	if h.mail.count() != sent+1 {
		t.Fatal("no ban email")
	}
	if msg := h.mail.last(t); msg.To != "plankton@krabber.test" || !strings.Contains(msg.Text, "Spam about the Krusty Krab") || !strings.Contains(msg.Subject, "banned") {
		t.Fatalf("ban email: %+v", msg)
	}
}

func TestReplyThread(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.signupAndActivate("karen", "karen@krabber.test", "computer-wife!")
	h.signupAndActivate("plankton", "plankton@krabber.test", "formula-thief!")
	karenID, planktonID := currentID(t, h, "karen"), currentID(t, h, "plankton")

	h.login("karen@krabber.test", "computer-wife!")
	tok := h.csrf("/trench")
	h.post("/molt/create", url.Values{"csrf_token": {tok}, "content": {"Dinner is ready"}}, "HX-Request", "true")
	molts, _ := h.store.MoltsByOwner(ctx, karenID, 1)
	root := molts[0].ID
	h.post("/krab/logout", url.Values{"csrf_token": {tok}})

	h.login("plankton@krabber.test", "formula-thief!")
	tok = h.csrf("/trench")
	if status, _, _ := h.post("/molt/reply/"+root, url.Values{"csrf_token": {tok}, "content": {" "}}, "HX-Request", "true"); status != http.StatusUnprocessableEntity {
		t.Fatalf("blank reply: %d", status)
	}
	status, body, _ := h.post("/molt/reply/"+root, url.Values{"csrf_token": {tok}, "content": {"Is it chum again?"}}, "HX-Request", "true")
	if status != http.StatusOK || !strings.Contains(body, "Is it chum again?") || !strings.Contains(body, `id="thread-actions" hx-swap-oob="true"`) {
		t.Fatalf("reply: %d %s", status, body)
	}
	replies, _ := h.store.RepliesByOwner(ctx, planktonID, 1)
	reply := replies[0].ID
	if _, body, _ := h.get("/krabs/plankton"); strings.Contains(body, "Is it chum again?") {
		t.Error("the reply shows on the Molts timeline")
	}
	if _, body, _ := h.get("/sea"); strings.Contains(body, "/molt/view/"+reply) {
		t.Error("the reply shows in the Sea")
	}
	h.post("/krab/logout", url.Values{"csrf_token": {tok}})

	h.login("karen@krabber.test", "computer-wife!")
	tok = h.csrf("/trench")
	h.post("/molt/reply/"+reply, url.Values{"csrf_token": {tok}, "content": {"Yes. Eat it."}}, "HX-Request", "true")
	if _, body, _ := h.get("/molt/view/" + root); !strings.Contains(body, "Is it chum again?") || strings.Contains(body, "No replies yet") {
		t.Error("the thread doesn't list the reply")
	}
	nested, _ := h.store.RepliesByOwner(ctx, karenID, 1)
	_, body, _ = h.get("/molt/view/" + nested[0].ID)
	for _, want := range []string{"Dinner is ready", "Is it chum again?", "Yes. Eat it.", "replying to", `href="/molt/view/` + reply + `"`} {
		if !strings.Contains(body, want) {
			t.Errorf("nested reply's thread is missing %q", want)
		}
	}

	// Deleting the root leaves the replies, with a note where it was.
	h.post("/molt/delete/"+root, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
	if _, body, _ := h.get("/molt/view/" + reply); !strings.Contains(body, "was deleted or isn't available") || !strings.Contains(body, "Is it chum again?") {
		t.Error("a reply to a deleted molt doesn't say so")
	}
}

func TestProfileTabs(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.signupAndActivate("karen", "karen@krabber.test", "computer-wife!")
	h.signupAndActivate("plankton", "plankton@krabber.test", "formula-thief!")
	karenID, planktonID := currentID(t, h, "karen"), currentID(t, h, "plankton")

	h.login("karen@krabber.test", "computer-wife!")
	tok := h.csrf("/trench")
	h.post("/molt/create", url.Values{"csrf_token": {tok}, "content": {"Dinner is ready"}}, "HX-Request", "true")
	h.post("/krab/logout", url.Values{"csrf_token": {tok}})
	molts, _ := h.store.MoltsByOwner(ctx, karenID, 1)
	root := `data-molt-id="` + molts[0].ID + `"`

	h.login("plankton@krabber.test", "formula-thief!")
	tok = h.csrf("/trench")
	h.post("/molt/create", url.Values{"csrf_token": {tok}, "content": {"Phase one"}}, "HX-Request", "true")
	h.post("/molt/reply/"+molts[0].ID, url.Values{"csrf_token": {tok}, "content": {"Is it chum?"}}, "HX-Request", "true")
	h.post("/molt/like/"+molts[0].ID, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
	own, _ := h.store.MoltsByOwner(ctx, planktonID, 1)
	replies, _ := h.store.RepliesByOwner(ctx, planktonID, 1)
	mine, reply := `data-molt-id="`+own[0].ID+`"`, `data-molt-id="`+replies[0].ID+`"`

	for path, want := range map[string]struct {
		has, hasNot []string
		active      string
	}{
		"/krabs/plankton":         {[]string{mine, `name="content"`}, []string{reply, root}, `/krabs/plankton" aria-current="page"`},
		"/krabs/plankton/replies": {[]string{reply, "replying to"}, []string{mine, root, `hx-post="/molt/create"`}, `/krabs/plankton/replies" aria-current="page"`},
		"/krabs/plankton/likes":   {[]string{root, "liked"}, []string{mine, reply}, `/krabs/plankton/likes" aria-current="page"`},
		"/krabs/karen/replies":    {[]string{"hasn&#39;t replied to anyone yet"}, []string{reply}, `/krabs/karen/replies" aria-current="page"`},
	} {
		status, body, _ := h.get(path)
		if status != http.StatusOK || !strings.Contains(body, want.active) {
			t.Errorf("%s: %d, active tab missing", path, status)
		}
		for _, s := range want.has {
			if !strings.Contains(body, s) {
				t.Errorf("%s: missing %q", path, s)
			}
		}
		for _, s := range want.hasNot {
			if strings.Contains(body, s) {
				t.Errorf("%s: shouldn't have %q", path, s)
			}
		}
	}
}

// TestAuthorNames checks molts show the author's current display name, looked
// up by ID, and that profiles say when the crab follows you.
func TestAuthorNames(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.signupAndActivate("karen", "karen@krabber.test", "computer-wife!")
	h.signupAndActivate("plankton", "plankton@krabber.test", "formula-thief!")
	karenID := currentID(t, h, "karen")

	h.login("karen@krabber.test", "computer-wife!")
	tok := h.csrf("/trench")
	_, body, _ := h.post("/molt/create", url.Values{"csrf_token": {tok}, "content": {"Dinner is ready"}}, "HX-Request", "true")
	if !strings.Contains(body, `mini-molt-display-name zindex-front" href="/krabs/karen">karen<`) {
		t.Errorf("new molt doesn't fall back to the username: %s", body)
	}
	molts, _ := h.store.MoltsByOwner(ctx, karenID, 1)
	root := molts[0].ID
	h.post("/settings/profile", url.Values{"csrf_token": {tok}, "display_name": {"Karen 2.0"}})
	h.post("/krab/logout", url.Values{"csrf_token": {tok}})

	h.login("plankton@krabber.test", "formula-thief!")
	tok = h.csrf("/trench")
	if _, body, _ := h.get("/krabs/karen"); strings.Contains(body, "Follows you") {
		t.Error("karen doesn't follow plankton yet")
	}
	h.post("/follow/"+karenID, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
	h.post("/molt/reply/"+root, url.Values{"csrf_token": {tok}, "content": {"Is it chum?"}}, "HX-Request", "true")
	h.post("/remolt/"+root, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
	h.post("/settings/profile", url.Values{"csrf_token": {tok}, "display_name": {"Sheldon"}})
	_, body, _ = h.get("/krabs/plankton")
	for _, want := range []string{">Karen 2.0</a>", "@karen", ">Sheldon</a> Remolted"} {
		if !strings.Contains(body, want) {
			t.Errorf("profile is missing %q", want)
		}
	}
	if _, body, _ := h.get("/krabs/plankton/replies"); !strings.Contains(body, "Karen 2.0</a></small>") {
		t.Error("reply doesn't name who it's replying to")
	}
	h.post("/krab/logout", url.Values{"csrf_token": {tok}})

	h.login("karen@krabber.test", "computer-wife!")
	_, body, _ = h.get("/krabs/plankton")
	if !strings.Contains(body, `<small class="follows-you">Follows you</small>`) {
		t.Error("plankton follows karen but the badge is missing")
	}
	if _, body, _ := h.get("/krabs/plankton/replies"); !strings.Contains(body, ">you</a></small>") {
		t.Error("a reply to you should say \"replying to you\"")
	}
}

func TestQuotes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.signupAndActivate("karen", "karen@krabber.test", "computer-wife!")
	h.signupAndActivate("plankton", "plankton@krabber.test", "formula-thief!")
	karenID := currentID(t, h, "karen")

	h.login("karen@krabber.test", "computer-wife!")
	tok := h.csrf("/trench")
	h.post("/molt/create", url.Values{"csrf_token": {tok}, "content": {"Dinner is ready"}}, "HX-Request", "true")
	molts, _ := h.store.MoltsByOwner(ctx, karenID, 1)
	root := molts[0].ID
	h.post("/krab/logout", url.Values{"csrf_token": {tok}})

	h.login("plankton@krabber.test", "formula-thief!")
	tok = h.csrf("/trench")
	if _, body, _ := h.get("/sea"); !strings.Contains(body, `href="/molt/quote/`+root+`"`) || strings.Contains(body, "Liked by") {
		t.Error("the remolt menu has no Quote Molt link, or the old Liked by link is back")
	}
	if status, body, _ := h.get("/molt/quote/" + root); status != http.StatusOK || !strings.Contains(body, "Quoting") || !strings.Contains(body, `data-quoted-id="`+root+`"`) {
		t.Fatalf("quote page: %d", status)
	}
	if status, body, _ := h.post("/molt/quote/"+root, url.Values{"csrf_token": {tok}, "content": {"  "}}); status != http.StatusUnprocessableEntity || !strings.Contains(body, "Say something about it.") {
		t.Fatalf("blank quote: %d", status)
	}
	status, _, hdr := h.post("/molt/quote/"+root, url.Values{"csrf_token": {tok}, "content": {"Chum again?"}})
	if status != http.StatusSeeOther || !strings.HasPrefix(hdr.Get("Location"), "/molt/view/") {
		t.Fatalf("quote: %d %q", status, hdr.Get("Location"))
	}
	quote := strings.TrimPrefix(hdr.Get("Location"), "/molt/view/")
	if _, body, _ := h.get("/molt/view/" + quote); !strings.Contains(body, "Chum again?") || !strings.Contains(body, `data-quoted-id="`+root+`"`) {
		t.Error("the quote's thread doesn't show the quoted molt")
	}
	if _, body, _ := h.get("/sea"); !strings.Contains(body, `data-molt-id="`+quote+`"`) || !strings.Contains(body, `data-quoted-id="`+root+`"`) {
		t.Error("the quote isn't in the Sea with its preview")
	}
	if _, body, _ := h.get("/molt/view/" + root); !strings.Contains(body, `href="/molt/view/`+root+`/quotes"`) {
		t.Error("the thread doesn't link to its quotes")
	}
	if _, body, _ := h.get("/molt/view/" + root + "/quotes"); !strings.Contains(body, `data-molt-id="`+quote+`"`) {
		t.Error("the quotes page doesn't list the quote")
	}

	// The remolt menu turns into Undo Remolt, and back.
	_, body, _ := h.post("/remolt/"+root, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
	if !strings.Contains(body, "active-remolt") || !strings.Contains(body, `hx-post="/unremolt/`+root+`"`) {
		t.Fatalf("after remolt: %s", body)
	}
	_, body, hdr = h.post("/unremolt/"+root, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
	if strings.Contains(body, "active-remolt") || !strings.Contains(body, `hx-post="/remolt/`+root+`"`) || !strings.Contains(hdr.Get("HX-Trigger"), "moltDeleted") {
		t.Fatalf("after undo: %s", body)
	}
	if got, _ := h.store.MoltByID(ctx, root); got.RemoltCount != 0 || got.QuoteCount != 1 {
		t.Fatalf("counts: %d remolts, %d quotes", got.RemoltCount, got.QuoteCount)
	}
	h.post("/krab/logout", url.Values{"csrf_token": {tok}})

	h.login("karen@krabber.test", "computer-wife!")
	tok = h.csrf("/trench")
	if _, body, _ := h.get("/notifications"); !strings.Contains(body, "quoted your molt") {
		t.Error("no quote notification")
	}
	h.post("/molt/delete/"+root, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
	if _, body, _ := h.get("/molt/view/" + quote); !strings.Contains(body, "This molt is unavailable.") || !strings.Contains(body, "Chum again?") {
		t.Error("a quote of a deleted molt doesn't say so")
	}
}

func TestMentionsAndCrabtags(t *testing.T) {
	h := newHarness(t)
	h.signupAndActivate("karen", "karen@krabber.test", "computer-wife!")
	h.signupAndActivate("plankton", "plankton@krabber.test", "formula-thief!")

	h.login("karen@krabber.test", "computer-wife!")
	tok := h.csrf("/trench")
	_, body, _ := h.post("/molt/create", url.Values{"csrf_token": {tok}, "content": {"Try the %KrabbyPatty, @Plankton & @nobody <3"}}, "HX-Request", "true")
	for _, want := range []string{
		`<a href="/krabtag/krabbypatty" class="crabtag zindex-front">%KrabbyPatty</a>`,
		`<a href="/krabs/plankton" class="mention zindex-front">@Plankton</a>`,
		"&amp; @nobody &lt;3",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("new molt is missing %q: %s", want, body)
		}
	}
	_, body, _ = h.get("/sea")
	if !strings.Contains(body, `href="/krabtag/krabbypatty"`) || !strings.Contains(body, "%krabbypatty</span>") || !strings.Contains(body, "Used by 1 krab recently.") {
		t.Error("the Sea or the trending panel is missing the crabtag")
	}
	status, body, _ := h.get("/krabtag/KrabbyPatty")
	if status != http.StatusOK || !strings.Contains(body, "Try the") || !strings.Contains(body, "Exploring") {
		t.Errorf("crabtag page: %d", status)
	}
	if status, body, _ := h.get("/krabtag/nothing"); status != http.StatusOK || !strings.Contains(body, "No molts use %nothing yet.") {
		t.Errorf("empty crabtag page: %d", status)
	}
	if status, _, _ := h.get("/krabtag/not-a-tag"); status != http.StatusNotFound {
		t.Errorf("bad crabtag: %d", status)
	}
	h.post("/krab/logout", url.Values{"csrf_token": {tok}})

	h.login("plankton@krabber.test", "formula-thief!")
	tok = h.csrf("/trench")
	if _, body, _ := h.get("/notifications"); !strings.Contains(body, "mentioned you in a Molt") {
		t.Error("plankton wasn't told about the mention")
	}
	molts, _ := h.store.MoltsByOwner(context.Background(), currentID(t, h, "karen"), 1)
	h.post("/molt/reply/"+molts[0].ID, url.Values{"csrf_token": {tok}, "content": {"@karen never!"}}, "HX-Request", "true")
	h.post("/krab/logout", url.Values{"csrf_token": {tok}})

	h.login("karen@krabber.test", "computer-wife!")
	if _, body, _ := h.get("/notifications"); !strings.Contains(body, "replied to your molt") || strings.Contains(body, "mentioned you") {
		t.Error("a reply that mentions its parent's author should notify once, as a reply")
	}
}

func TestBookmarks(t *testing.T) {
	h := newHarness(t)
	h.signupAndActivate("karen", "karen@krabber.test", "computer-wife!")
	h.signupAndActivate("plankton", "plankton@krabber.test", "formula-thief!")
	h.login("karen@krabber.test", "computer-wife!")
	tok := h.csrf("/trench")
	h.post("/molt/create", url.Values{"csrf_token": {tok}, "content": {"Secret formula, don't look"}}, "HX-Request", "true")
	h.post("/krab/logout", url.Values{"csrf_token": {tok}})
	molts, _ := h.store.MoltsByOwner(context.Background(), currentID(t, h, "karen"), 1)
	id := molts[0].ID

	h.login("plankton@krabber.test", "formula-thief!")
	tok = h.csrf("/trench")
	if _, body, _ := h.get("/bookmarks"); !strings.Contains(body, "@plankton") || !strings.Contains(body, "You have no bookmarks.") {
		t.Error("empty bookmarks page")
	}
	if _, body, _ := h.get("/sea"); !strings.Contains(body, "Add Molt to Bookmarks") {
		t.Error("the molt menu has no bookmark item")
	}
	_, body, _ := h.post("/molt/bookmark/"+id, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
	if !strings.Contains(body, "Remove Molt from Bookmarks") || strings.Contains(body, "<html") {
		t.Errorf("bookmark response: %s", body)
	}
	if _, body, _ := h.get("/bookmarks"); !strings.Contains(body, "Secret formula") || !strings.Contains(body, "Remove Molt from Bookmarks") {
		t.Error("the bookmarked molt isn't on the bookmarks page")
	}
	_, body, _ = h.post("/molt/bookmark/"+id, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
	if !strings.Contains(body, "Add Molt to Bookmarks") {
		t.Errorf("unbookmark response: %s", body)
	}
	if _, body, _ := h.get("/bookmarks"); strings.Contains(body, "Secret formula") {
		t.Error("the removed bookmark is still listed")
	}
	if status, _, _ := h.post("/molt/bookmark/nope", url.Values{"csrf_token": {tok}}, "HX-Request", "true"); status != http.StatusNotFound {
		t.Errorf("bookmark a missing molt: %d", status)
	}
	h.post("/krab/logout", url.Values{"csrf_token": {tok}})
	if status, _, hdr := h.get("/bookmarks"); status/100 != 3 || !strings.HasPrefix(hdr.Get("Location"), "/krab/login") {
		t.Errorf("signed-out bookmarks: %d %s", status, hdr.Get("Location"))
	}
}

func TestPins(t *testing.T) {
	h := newHarness(t)
	h.signupAndActivate("karen", "karen@krabber.test", "computer-wife!")
	h.signupAndActivate("plankton", "plankton@krabber.test", "formula-thief!")
	h.login("karen@krabber.test", "computer-wife!")
	tok := h.csrf("/trench")
	for _, text := range []string{"Oldest thought", "Newest thought"} {
		h.post("/molt/create", url.Values{"csrf_token": {tok}, "content": {text}}, "HX-Request", "true")
	}
	molts, _ := h.store.MoltsByOwner(context.Background(), currentID(t, h, "karen"), 2)
	oldest := molts[1].ID

	if _, body, _ := h.get("/sea"); strings.Contains(body, "/molt/pin/") {
		t.Error("Pin shows outside your own profile")
	}
	if _, body, _ := h.get("/krabs/karen"); !strings.Contains(body, "/molt/pin/"+oldest) || strings.Contains(body, "Pinned Molt") {
		t.Error("own profile should offer Pin and show no pin yet")
	}
	status, _, hdr := h.post("/molt/pin/"+oldest, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
	if status != http.StatusNoContent || hdr.Get("HX-Refresh") != "true" {
		t.Fatalf("pin: %d %v", status, hdr)
	}
	_, body, _ := h.get("/krabs/karen")
	pin, list := strings.Index(body, `id="pinned-molt"`), strings.Index(body, `id="molt-list"`)
	if pin < 0 || pin > list || !strings.Contains(body[pin:list], "Pinned Molt") || !strings.Contains(body[pin:list], "Oldest thought") {
		t.Error("the pinned molt isn't above the list")
	}
	if !strings.Contains(body, "/molt/unpin/"+oldest) {
		t.Error("the pinned molt's menu should offer Unpin")
	}
	if _, body, _ := h.get("/krabs/karen/replies"); strings.Contains(body, "Pinned Molt") {
		t.Error("the pin shows on the Replies tab")
	}
	h.post("/krab/logout", url.Values{"csrf_token": {tok}})

	h.login("plankton@krabber.test", "formula-thief!")
	tok = h.csrf("/trench")
	if _, body, _ := h.get("/krabs/karen"); !strings.Contains(body, "Pinned Molt") || strings.Contains(body, "/molt/pin/") || strings.Contains(body, "/molt/unpin/") {
		t.Error("visitors should see the pin but not Pin or Unpin")
	}
	if status, _, _ := h.post("/molt/pin/"+oldest, url.Values{"csrf_token": {tok}}, "HX-Request", "true"); status != http.StatusNotFound {
		t.Errorf("pin someone else's molt: %d", status)
	}
	h.post("/krab/logout", url.Values{"csrf_token": {tok}})

	h.login("karen@krabber.test", "computer-wife!")
	tok = h.csrf("/trench")
	h.post("/molt/unpin/"+oldest, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
	if _, body, _ := h.get("/krabs/karen"); strings.Contains(body, "Pinned Molt") {
		t.Error("unpin left the pin")
	}
	h.post("/molt/pin/"+oldest, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
	h.post("/molt/delete/"+oldest, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
	if c, _ := h.store.CrabByUsername(context.Background(), "karen"); c.PinnedMoltID != "" {
		t.Error("deleting the pinned molt should clear the pin")
	}
}

// TestNavLayout keeps the nav in Crabber's order: main pages, the Molt button,
// then the muted extras.
func TestNavLayout(t *testing.T) {
	h := newHarness(t)
	h.signupAndActivate("karen", "karen@krabber.test", "computer-wife!")
	h.login("karen@krabber.test", "computer-wife!")
	_, body, _ := h.get("/trench")
	nav := body[strings.Index(body, `id="nav-panel"`):strings.Index(body, `id="add-panel"`)]
	last := -1
	for _, want := range []string{`href="/trench"`, `href="/sea"`, `href="/notifications"`, `href="/bookmarks"`, `href="/krabs/karen"`, `id="molt-btn"`, `href="/stats"`, `href="/settings"`, `action="/krab/logout"`} {
		i := strings.Index(nav, want)
		if i <= last {
			t.Fatalf("nav: %s is missing or out of order", want)
		}
		last = i
	}
	if strings.Contains(nav, `href="/krabs"`) || strings.Contains(nav, `href="/krabmin"`) || strings.Contains(nav, `href="/search"`) {
		t.Error("nav shows Crabs, Search, or Crabmin to a crab who isn't a moderator")
	}
	side := body[strings.Index(body, `id="add-panel"`):]
	if !strings.Contains(side, `action="/search"`) || strings.Index(side, `name="q"`) > strings.Index(side, `id="trending"`) {
		t.Error("the search box should sit at the top of the sidebar")
	}
	h.post("/krab/logout", url.Values{"csrf_token": {h.csrf("/trench")}})
	if _, body, _ = h.get("/sea"); strings.Contains(body, `href="/search"`) {
		t.Error("signed-out nav shows Search")
	}
}

func TestSitePages(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/no-such-page", "/molt/view/nope", "/krabs/nobody"} {
		status, body, hdr := h.get(path)
		if status != http.StatusNotFound || !strings.Contains(body, "sank to the bottom of the sea") || !strings.HasPrefix(hdr.Get("Content-Type"), "text/html") {
			t.Errorf("%s: %d %q", path, status, hdr.Get("Content-Type"))
		}
	}
	for path, want := range map[string]string{
		"/terms":      "The rules",
		"/privacy":    "What we store",
		"/robots.txt": "Disallow: /krabmin",
	} {
		if status, body, _ := h.get(path); status != http.StatusOK || !strings.Contains(body, want) {
			t.Errorf("%s: %d, missing %q", path, status, want)
		}
	}
	if status, _, hdr := h.get("/favicon.ico"); status != http.StatusMovedPermanently || !strings.Contains(hdr.Get("Location"), "favicon.svg") {
		t.Errorf("/favicon.ico: %d %q", status, hdr.Get("Location"))
	}
	if _, body, _ := h.get("/krab/signup"); !strings.Contains(body, `href="/terms"`) || !strings.Contains(body, `href="/privacy"`) {
		t.Error("signup doesn't link the terms and privacy policy")
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
	h.post("/krab/logout", url.Values{"csrf_token": {tok}})

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
	for _, path := range []string{"/sea", "/krabs", "/search?q=formula"} {
		if _, body, _ := h.get(path); strings.Contains(body, "Give me the formula") || strings.Contains(body, `href="/krabs/plankton"`) {
			t.Errorf("%s still shows plankton", path)
		}
	}
	if _, body, _ := h.get("/krabs/plankton"); !strings.Contains(body, "You blocked @plankton") || !strings.Contains(body, "/unblock/"+planktonID) {
		t.Error("blocked profile should offer unblock")
	}
	if _, body, _ := h.get("/settings"); !strings.Contains(body, "/unblock/"+planktonID) {
		t.Error("settings should list the block")
	}
	if c, _ := h.store.CrabByUsername(ctx, "sandy"); c.FollowerCount != 0 {
		t.Errorf("follow survived the block: %d followers", c.FollowerCount)
	}
	h.post("/krab/logout", url.Values{"csrf_token": {tok}})

	// Plankton can't find, follow or touch Sandy.
	h.login("plankton@krabber.test", "formula-thief!")
	tok = h.csrf("/trench")
	if status, _, _ := h.get("/krabs/sandy"); status != http.StatusNotFound {
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
	h.post("/krab/logout", url.Values{"csrf_token": {tok}})

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
	tok := h.csrf("/krab/forgot")
	status, _, hdr := h.post("/krab/forgot", url.Values{"csrf_token": {tok}, "email": {"nobody@krabber.test"}})
	if status != http.StatusSeeOther || hdr.Get("Location") != "/krab/reset" || h.mail.count() != sent {
		t.Fatalf("unknown email: %d %s, %d emails", status, hdr.Get("Location"), h.mail.count()-sent)
	}
	h.post("/krab/forgot", url.Values{"csrf_token": {tok}, "email": {"PEARL@krabber.test"}})
	msg := h.mail.last(t)
	m := regexp.MustCompile(`token=([A-Z2-7]{26})`).FindStringSubmatch(msg.Text)
	if h.mail.count() != sent+1 || m == nil || !strings.Contains(msg.Subject, "Reset") {
		t.Fatalf("reset email: %+v", msg)
	}

	// A rejected password keeps the token usable.
	tok = h.csrf("/krab/reset?token=" + m[1])
	if status, _, _ := h.post("/krab/reset", url.Values{"csrf_token": {tok}, "token": {m[1]}, "password": {"short"}, "confirm_password": {"short"}}); status != http.StatusUnprocessableEntity {
		t.Fatalf("short password: %d", status)
	}
	status, _, hdr = h.post("/krab/reset", url.Values{"csrf_token": {tok}, "token": {m[1]}, "password": {"daddy-buy-me"}, "confirm_password": {"daddy-buy-me"}})
	if status != http.StatusSeeOther || hdr.Get("Location") != "/krab/login" {
		t.Fatalf("reset: %d %s", status, hdr.Get("Location"))
	}
	tok = h.csrf("/krab/reset")
	if status, body, _ := h.post("/krab/reset", url.Values{"csrf_token": {tok}, "token": {m[1]}, "password": {"again-and-again"}, "confirm_password": {"again-and-again"}}); status != http.StatusUnprocessableEntity || !strings.Contains(body, "invalid or has expired") {
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

func TestCrabmin(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, name := range []string{"boss", "mod", "peer", "troll"} {
		hash, _ := auth.HashPassword("shell-game-" + name)
		c, err := h.store.CreateCrab(ctx, name, name+"@krabber.test", hash)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.store.ActivateCrab(ctx, c.ID); err != nil {
			t.Fatal(err)
		}
	}
	crab := func(name string) *store.Crab {
		c, err := h.store.CrabByUsername(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	for name, role := range map[string]string{"boss": store.RoleAdmin, "mod": store.RoleModerator, "peer": store.RoleModerator} {
		if err := h.store.SetRole(ctx, crab(name), role); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.store.UpdateProfile(ctx, crab("troll"), store.Profile{Bio: "I am a menace"}); err != nil {
		t.Fatal(err)
	}

	h.login("troll@krabber.test", "shell-game-troll")
	trollClient := h.client
	tok := h.csrf("/trench")
	h.post("/molt/create", url.Values{"csrf_token": {tok}, "content": {"Barnacles to all of you"}}, "HX-Request", "true")
	molts, _ := h.store.MoltsByOwner(ctx, crab("troll").ID, 1)
	moltID := molts[0].ID
	if status, _, _ := h.get("/krabmin"); status != http.StatusNotFound {
		t.Fatalf("crabmin for a regular crab: %d", status)
	}
	if _, body, _ := h.get("/trench"); strings.Contains(body, `href="/krabmin"`) {
		t.Error("regular crab sees the Crabmin nav button")
	}

	h.client = h.newClient()
	h.login("mod@krabber.test", "shell-game-mod")
	if _, body, _ := h.get("/sea"); !strings.Contains(body, `href="/krabmin"`) || !strings.Contains(body, "/krabmin/molts/"+moltID) {
		t.Error("moderator should see the Crabmin nav button and the molt menu entry")
	}
	if status, _, hdr := h.get("/krabmin?q=%40Troll"); status != http.StatusSeeOther || hdr.Get("Location") != "/krabmin/krabs/troll" {
		t.Fatalf("look up by username: %d %s", status, hdr.Get("Location"))
	}
	if _, _, hdr := h.get("/krabmin?q=" + moltID); hdr.Get("Location") != "/krabmin/molts/"+moltID {
		t.Fatalf("look up by molt ID: %s", hdr.Get("Location"))
	}
	tok = h.csrf("/krabmin/krabs/troll")
	act := func(target, action, note string) string {
		t.Helper()
		status, _, hdr := h.post("/krabmin/krabs/"+crab(target).ID, url.Values{"csrf_token": {tok}, "action": {action}, "note": {note}})
		if status != http.StatusSeeOther {
			t.Fatalf("%s %s: %d", action, target, status)
		}
		_, body, _ := h.get(hdr.Get("Location"))
		return body
	}

	act("troll", "warn", "Please be kind to the other crabs")
	if notes, _ := h.store.Notifications(ctx, crab("troll").ID, 10); len(notes) == 0 || notes[0].Type != store.NotifyWarning {
		t.Fatalf("warning: %+v", notes)
	}
	act("troll", "clear_bio", "")
	if c := crab("troll"); c.Bio != "" {
		t.Fatalf("bio not cleared: %q", c.Bio)
	}

	tok = h.csrf("/krabmin/molts/" + moltID)
	h.post("/krabmin/molts/"+moltID, url.Values{"csrf_token": {tok}, "action": {"remove"}})
	if _, body, _ := h.get("/sea"); strings.Contains(body, "Barnacles to all of you") {
		t.Error("removed molt still in the sea")
	}
	if status, _, _ := h.get("/molt/view/" + moltID); status != http.StatusNotFound {
		t.Errorf("removed molt page: %d", status)
	}
	if _, body, _ := h.get("/krabmin/molts/" + moltID); !strings.Contains(body, "Removed by a moderator") {
		t.Error("crabmin should still show the removed molt")
	}
	h.post("/krabmin/molts/"+moltID, url.Values{"csrf_token": {tok}, "action": {"restore"}})
	if status, _, _ := h.get("/molt/view/" + moltID); status != http.StatusOK {
		t.Errorf("restored molt page: %d", status)
	}

	// A moderator can't ban another moderator or appoint one.
	if body := act("peer", "ban", "coup"); !strings.Contains(body, "Not allowed") || crab("peer").Banned {
		t.Error("moderator banned a moderator")
	}
	if body := act("troll", "make_moderator", ""); !strings.Contains(body, "Only admins") || crab("troll").Role != "" {
		t.Error("moderator appointed a moderator")
	}

	if body := act("troll", "ban", "Repeated harassment"); !strings.Contains(body, "Repeated harassment") {
		t.Error("ban reason not shown")
	}
	if c := crab("troll"); !c.Banned || c.BanReason != "Repeated harassment" {
		t.Fatalf("after ban: %+v", c)
	}
	if _, body, _ := h.get("/sea"); strings.Contains(body, "Barnacles to all of you") {
		t.Error("banned crab's molt still in the sea")
	}
	_, logPage, _ := h.get("/krabmin/log")
	for _, want := range []string{"banned", "warned", "cleared the bio of", "removed a molt by", "restored a molt by", "tried to ban", "tried to make a moderator:"} {
		if !strings.Contains(logPage, want) {
			t.Errorf("log missing %q", want)
		}
	}
	mine := h.client
	h.client = trollClient
	if status, _, _ := h.get("/trench"); status != http.StatusSeeOther {
		t.Errorf("banned crab still signed in: %d", status)
	}

	// The admin can moderate moderators, but nobody touches an admin from the web.
	h.client = h.newClient()
	h.login("boss@krabber.test", "shell-game-boss")
	tok = h.csrf("/krabmin/krabs/peer")
	act("peer", "remove_moderator", "")
	if crab("peer").Role != "" {
		t.Error("admin couldn't remove a moderator")
	}
	act("troll", "unban", "")
	if crab("troll").Banned {
		t.Error("admin couldn't unban")
	}
	h.client = mine
	tok = h.csrf("/krabmin/krabs/boss")
	if body := act("boss", "ban", "mutiny"); !strings.Contains(body, "Not allowed") || crab("boss").Banned {
		t.Error("moderator banned the admin")
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
	tok := h.csrf("/krab/signup")
	h.post("/krab/signup", url.Values{"csrf_token": {tok}, "name": {"plankton"}, "email": {"plankton@krabber.test"}, "password": {"formula-is-mine"}})
	if status, body := h.login("plankton@krabber.test", "formula-is-mine"); status != http.StatusUnprocessableEntity || !strings.Contains(body, "activate your account") {
		t.Fatalf("unactivated login: %d", status)
	}

	// Same email in another case, or same name, can't register again.
	tok = h.csrf("/krab/signup")
	if status, body, _ := h.post("/krab/signup", url.Values{"csrf_token": {tok}, "name": {"other"}, "email": {"PLANKTON@krabber.test"}, "password": {"formula-is-mine"}}); status != http.StatusUnprocessableEntity || !strings.Contains(body, "already in use") {
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

	// Someone else's wrong guesses don't lock karen out from her own network.
	tok = h.csrf("/krab/login")
	status, _, _ := h.post("/krab/login", url.Values{"csrf_token": {tok}, "email": {"karen@krabber.test"}, "password": {"computer-wife"}},
		"CloudFront-Viewer-Address", "[2001:db8:77::1]:443")
	if status != http.StatusSeeOther {
		t.Fatalf("karen from another network: %d", status)
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
	if err := h.store.SetBanned(context.Background(), c, true, "spam"); err != nil {
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

func TestEditMolt(t *testing.T) {
	h := newHarness(t)
	h.signupAndActivate("karen", "karen@krabber.test", "computer-wife!")
	h.signupAndActivate("plankton", "plankton@krabber.test", "formula-thief!")
	h.signupAndActivate("sandy", "sandy@krabber.test", "karate-chop!!")
	h.login("plankton@krabber.test", "formula-thief!")
	tok := h.csrf("/trench")
	h.post("/molt/create", url.Values{"csrf_token": {tok}, "content": {"Hey @karen, %plan one"}}, "HX-Request", "true")
	molts, _ := h.store.MoltsByOwner(context.Background(), currentID(t, h, "plankton"), 1)
	id := molts[0].ID

	if _, body, _ := h.get("/sea"); !strings.Contains(body, `href="/molt/edit/`+id+`"`) {
		t.Error("your fresh molt has no edit button")
	}
	status, body, _ := h.get("/molt/edit/" + id)
	if status != http.StatusOK || !strings.Contains(body, "Editing your own Molt") || !strings.Contains(body, "Hey @karen, %plan one</textarea>") {
		t.Fatalf("edit page: %d", status)
	}
	for text, want := range map[string]string{
		"   ":                    "Molt text cannot be blank",
		"Hey @karen, %plan one":  "No changes were made",
		strings.Repeat("x", 281): "Molts can be up to 280 characters.",
	} {
		if status, body, _ := h.post("/molt/edit/"+id, url.Values{"csrf_token": {tok}, "content": {text}}); status != http.StatusUnprocessableEntity || !strings.Contains(body, want) {
			t.Errorf("edit to %q: %d, want %q", text[:min(len(text), 20)], status, want)
		}
	}
	status, _, hdr := h.post("/molt/edit/"+id, url.Values{"csrf_token": {tok}, "content": {"Hey @karen and @sandy, %plan two"}})
	if status != http.StatusSeeOther || hdr.Get("Location") != "/molt/view/"+id {
		t.Fatalf("save: %d %s", status, hdr.Get("Location"))
	}
	if _, body, _ := h.get("/molt/view/" + id); !strings.Contains(body, "%plan</a> two") || !strings.Contains(body, "This molt has been edited") {
		t.Error("the thread doesn't show the edit")
	}
	if _, body, _ := h.get("/krabtag/plan"); !strings.Contains(body, "two") {
		t.Error("the crabtag page doesn't show the new text")
	}
	h.post("/krab/logout", url.Values{"csrf_token": {tok}})

	for name, want := range map[string]int{"karen": 1, "sandy": 1} {
		n, err := h.store.Notifications(context.Background(), currentID(t, h, name), 10)
		if err != nil || len(n) != want {
			t.Errorf("%s has %d notifications, want %d (%v)", name, len(n), want, err)
		}
	}

	h.login("karen@krabber.test", "computer-wife!")
	tok = h.csrf("/trench")
	if _, body, _ := h.get("/sea"); strings.Contains(body, "/molt/edit/") {
		t.Error("the edit button shows on someone else's molt")
	}
	if status, _, _ := h.get("/molt/edit/" + id); status != http.StatusNotFound {
		t.Errorf("edit page for someone else's molt: %d", status)
	}
	if status, _, _ := h.post("/molt/edit/"+id, url.Values{"csrf_token": {tok}, "content": {"Mine now"}}); status != http.StatusNotFound {
		t.Errorf("edit someone else's molt: %d", status)
	}
}

func TestEditable(t *testing.T) {
	fresh := store.Molt{CreatedAt: time.Now().Add(-time.Minute)}
	old := store.Molt{CreatedAt: time.Now().Add(-store.EditWindow)}
	gone := store.Molt{CreatedAt: time.Now(), Deleted: true}
	if !editable(fresh) || !editable(&fresh) || editable(old) || editable(gone) || editable((*store.Molt)(nil)) {
		t.Error("editable is wrong about the edit window")
	}
}

func TestComposeModals(t *testing.T) {
	h := newHarness(t)
	h.signupAndActivate("karen", "karen@krabber.test", "computer-wife!")
	tok := h.csrf("/krab/login")
	if status, _, hdr := h.post("/krab/login", url.Values{"csrf_token": {tok}, "email": {"karen@krabber.test"}, "password": {"computer-wife!"}}); status != http.StatusSeeOther || hdr.Get("Location") != "/trench" {
		t.Fatalf("login lands on %d %s, want the Trench", status, hdr.Get("Location"))
	}
	if status, _, _ := h.get("/moltinTime"); status != http.StatusNotFound {
		t.Errorf("/moltinTime: %d", status)
	}
	_, body, _ := h.get("/sea")
	if !strings.Contains(body, `data-open-modal="compose-modal"`) || !strings.Contains(body, `<dialog class="kb-modal" id="compose-modal"`) ||
		!strings.Contains(body, `id="molt-modal-content"`) || !strings.Contains(body, `class="mini-character-counter`) {
		t.Error("the page is missing the compose modal, the molt modal, or the counter")
	}

	// Line breaks arrive as CRLF but count once, as in the counter.
	tok = h.csrf("/trench")
	text := strings.Repeat("ab\r\n", 70)
	if status, _, _ := h.post("/molt/create", url.Values{"csrf_token": {tok}, "content": {text}}); status != http.StatusSeeOther {
		t.Fatalf("a 280-character molt with line breaks: %d", status)
	}
	molts, _ := h.store.MoltsByOwner(context.Background(), currentID(t, h, "karen"), 1)
	id := molts[0].ID
	if molts[0].Content != strings.Repeat("ab\n", 70) {
		t.Errorf("stored %q", molts[0].Content)
	}

	for _, kind := range []string{"quote", "edit"} {
		path := "/molt/" + kind + "/" + id
		status, body, _ := h.get(path, "HX-Request", "true")
		if status != http.StatusOK || strings.Contains(body, "<html") || !strings.Contains(body, "data-close-modal") || !strings.Contains(body, `hx-post="`+path+`"`) {
			t.Errorf("%s modal: %d %s", kind, status, body)
		}
		status, body, _ = h.post(path, url.Values{"csrf_token": {tok}, "content": {"  "}}, "HX-Request", "true")
		if status != http.StatusUnprocessableEntity || strings.Contains(body, "<html") || strings.Contains(body, "data-close-modal") || !strings.HasPrefix(strings.TrimSpace(body), "<form") {
			t.Errorf("rejected %s: %d %s", kind, status, body)
		}
		if status, body, _ := h.get(path); status != http.StatusOK || !strings.Contains(body, "<html") {
			t.Errorf("%s page: %d", kind, status)
		}
	}
	status, _, hdr := h.post("/molt/edit/"+id, url.Values{"csrf_token": {tok}, "content": {"Fixed it"}}, "HX-Request", "true")
	if status != http.StatusNoContent || hdr.Get("HX-Redirect") != "/molt/view/"+id {
		t.Errorf("save from the modal: %d %v", status, hdr)
	}
}

func TestLoadMore(t *testing.T) {
	old := pageSize
	pageSize = 2
	t.Cleanup(func() { pageSize = old })

	h := newHarness(t)
	h.signupAndActivate("karen", "karen@krabber.test", "computer-wife!")
	h.login("karen@krabber.test", "computer-wife!")
	c, err := h.store.CrabByUsername(context.Background(), "karen")
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"oldest", "middle", "newest"} {
		if _, err := h.store.CreateMolt(context.Background(), c, text); err != nil {
			t.Fatal(err)
		}
	}

	status, body, _ := h.get("/sea")
	if status != http.StatusOK || !strings.Contains(body, "newest") || !strings.Contains(body, "middle") ||
		strings.Contains(body, "oldest") || !strings.Contains(body, `id="load-more"`) || !strings.Contains(body, `id="content-body"`) {
		t.Fatalf("first page: %d", status)
	}
	chunk := body[strings.Index(body, `id="load-more"`):]
	after := chunk[strings.Index(chunk, `hx-get="`)+8:]
	after = after[:strings.Index(after, `"`)]
	if !strings.HasPrefix(after, "/sea?after=") {
		t.Fatalf("load more href: %s", after)
	}

	status, more, _ := h.get(after, "HX-Request", "true")
	if status != http.StatusOK || strings.Contains(more, "<html") || !strings.Contains(more, "oldest") ||
		strings.Contains(more, "newest") || strings.Contains(more, `id="load-more"`) {
		t.Fatalf("next page: %d %s", status, more)
	}
	if status, _, _ := h.get("/sea?after=not-a-cursor"); status != http.StatusNotFound {
		t.Errorf("bad cursor: %d", status)
	}
}

func TestNewMolts(t *testing.T) {
	h := newHarness(t)
	h.signupAndActivate("karen", "karen@krabber.test", "computer-wife!")
	c, err := h.store.CrabByUsername(context.Background(), "karen")
	if err != nil {
		t.Fatal(err)
	}
	first, err := h.store.CreateMolt(context.Background(), c, "first")
	if err != nil {
		t.Fatal(err)
	}

	// Strangers get no poller, and polling needs an account.
	if _, body, _ := h.get("/sea"); strings.Contains(body, `id="new-molts"`) {
		t.Error("the signed-out Sea polls")
	}
	for _, path := range []string{"/sea/new", "/trench/new"} {
		if status, _, _ := h.get(path); status != http.StatusSeeOther {
			t.Errorf("%s signed out: %d", path, status)
		}
	}

	h.login("karen@krabber.test", "computer-wife!")
	status, body, _ := h.get("/sea")
	if status != http.StatusOK || !strings.Contains(body, `id="new-molts"`) ||
		!strings.Contains(body, `/sea/new?since=`+first.ID) || strings.Contains(body, "click to refresh") {
		t.Fatalf("sea poller: %d", status)
	}

	second, err := h.store.CreateMolt(context.Background(), c, "second")
	if err != nil {
		t.Fatal(err)
	}
	status, body, _ = h.get("/sea/new?since=" + first.ID)
	if status != http.StatusOK || strings.Contains(body, "<html") ||
		!strings.Contains(body, ">1</strong> new molt ") || !strings.Contains(body, "click to refresh") {
		t.Fatalf("one new: %d %s", status, body)
	}

	status, body, _ = h.get("/sea/new?since=" + second.ID)
	if status != http.StatusOK || strings.Contains(body, "click to refresh") {
		t.Fatalf("none newer: %d %s", status, body)
	}

	if err := h.store.AddToTrenches(context.Background(), first, []string{c.ID}); err != nil {
		t.Fatal(err)
	}
	status, body, _ = h.get("/trench")
	if status != http.StatusOK || !strings.Contains(body, `/trench/new?since=`) {
		t.Fatalf("trench poller: %d", status)
	}
	// A feed page's poll brings the badge along; elsewhere the badge polls itself.
	if strings.Contains(body, `hx-get="/notifications/badge"`) {
		t.Error("the badge polls on a feed page, which polls already")
	}
	if _, body, _ := h.get("/trench/new?since=" + first.ID); !strings.Contains(body, `id="kb-badge-slot" hx-swap-oob="true"`) {
		t.Errorf("trench poll without the badge: %s", body)
	}
	if _, body, _ := h.get("/bookmarks"); !strings.Contains(body, `hx-get="/notifications/badge" hx-trigger="kb:poll from:body"`) {
		t.Error("the badge doesn't poll on a page without a feed")
	}
}

func TestGeneratedAvatars(t *testing.T) {
	h := newHarness(t)
	h.signupAndActivate("karen", "karen@krabber.test", "computer-wife!")
	h.login("karen@krabber.test", "computer-wife!")

	status, body, _ := h.get("/settings")
	i := strings.Index(body, `/avatar/`)
	if status != http.StatusOK || i < 0 {
		t.Fatalf("settings avatar: %d", status)
	}
	code := body[i+len(`/avatar/`):]
	code = code[:strings.Index(code, `.svg`)]
	if !avatar.Valid(code) {
		t.Fatalf("code on settings: %q", code)
	}

	status, svg, hdr := h.get("/avatar/" + code + ".svg")
	if status != http.StatusOK || !strings.Contains(svg, "<svg") || hdr.Get("Content-Type") != "image/svg+xml; charset=utf-8" {
		t.Fatalf("svg: %d %s", status, hdr.Get("Content-Type"))
	}
	status, banner, hdr := h.get("/banner/" + code + ".svg")
	if status != http.StatusOK || !strings.Contains(banner, `viewBox="0 0 360 120"`) || hdr.Get("Content-Type") != "image/svg+xml; charset=utf-8" {
		t.Fatalf("banner: %d %s", status, hdr.Get("Content-Type"))
	}
	if _, body, _ := h.get("/krabs/karen"); !strings.Contains(body, "/banner/"+code+".svg") {
		t.Fatal("profile missing banner")
	}
	if status, _, _ := h.get("/avatar/not-a-crab.svg"); status != http.StatusNotFound {
		t.Errorf("junk code: %d", status)
	}
	if status, _, _ := h.get("/banner/not-a-crab.svg"); status != http.StatusNotFound {
		t.Errorf("junk banner: %d", status)
	}

	tok := h.csrf("/settings")
	status, _, _ = h.post("/settings/avatar", url.Values{"csrf_token": {tok}})
	if status != http.StatusSeeOther {
		t.Fatalf("reroll: %d", status)
	}
	_, body, _ = h.get("/settings")
	if strings.Contains(body, "/avatar/"+code+".svg") {
		t.Fatal("reroll left the old crab")
	}

	for i := 0; i < 2; i++ {
		tok = h.csrf("/settings")
		if status, _, _ := h.post("/settings/avatar", url.Values{"csrf_token": {tok}}); status != http.StatusSeeOther {
			t.Fatalf("reroll %d: %d", i+2, status)
		}
	}
	tok = h.csrf("/settings")
	status, body, _ = h.post("/settings/avatar", url.Values{"csrf_token": {tok}})
	if status != http.StatusTooManyRequests || !strings.Contains(body, "enough rerolls") {
		t.Fatalf("fourth reroll: %d %s", status, body)
	}
}

func TestNSFW(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.signupAndActivate("karen", "karen@krabber.test", "computer-wife!")
	h.signupAndActivate("sandy", "sandy@krabber.test", "karate-chop!")
	hash, _ := auth.HashPassword("secret-mrkrabs")
	boss, err := h.store.CreateCrab(ctx, "mrkrabs", "mrkrabs@krabber.test", hash)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.ActivateCrab(ctx, boss.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetRole(ctx, boss, store.RoleModerator); err != nil {
		t.Fatal(err)
	}
	const veil = "This molt is marked NSFW"

	h.login("karen@krabber.test", "computer-wife!")
	if _, body, _ := h.get("/trench"); !strings.Contains(body, `name="nsfw" value="true"`) {
		t.Fatal("compose has no NSFW switch")
	}
	tok := h.csrf("/trench")
	if status, _, _ := h.post("/molt/create", url.Values{"csrf_token": {tok}, "content": {"the secret formula"}, "nsfw": {"true"}}); status != http.StatusSeeOther {
		t.Fatalf("create: %d", status)
	}
	molts, _ := h.store.MoltsByOwner(ctx, currentID(t, h, "karen"), 1)
	id := molts[0].ID
	if !molts[0].NSFW {
		t.Fatal("the switch didn't label the molt")
	}
	_, body, _ := h.get("/molt/view/" + id)
	if !strings.Contains(body, `class="nsfw-badge"`) || strings.Contains(body, veil) || !strings.Contains(body, "Remove NSFW label") {
		t.Error("the author should see the badge, the text, and Remove NSFW label")
	}
	h.post("/molt/reply/"+id, url.Values{"csrf_token": {tok}, "content": {"more formula"}, "nsfw": {"true"}})
	if replies, _ := h.store.Replies(ctx, id, 5); len(replies) != 1 || !replies[0].NSFW {
		t.Fatalf("reply label: %+v", replies)
	}

	h.client = h.newClient()
	for _, path := range []string{"/sea", "/molt/view/" + id} {
		if _, body, _ := h.get(path); !strings.Contains(body, veil) || !strings.Contains(body, `class="kb-nsfw`) {
			t.Errorf("signed out, %s isn't veiled", path)
		}
	}

	h.login("sandy@krabber.test", "karate-chop!")
	if _, body, _ := h.get("/sea"); !strings.Contains(body, veil) || strings.Contains(body, "Label NSFW") {
		t.Error("sandy should see the veil and no label action on karen's molt")
	}
	tok = h.csrf("/settings")
	if status, _, _ := h.post("/molt/nsfw/"+id, url.Values{"csrf_token": {tok}}); status != http.StatusNotFound {
		t.Errorf("unlabelling someone else's molt: %d", status)
	}
	if status, _, _ := h.post("/settings/content", url.Values{"csrf_token": {tok}, "show_nsfw": {"true"}}); status != http.StatusSeeOther {
		t.Fatalf("preference: %d", status)
	}
	if _, body, _ := h.get("/sea"); strings.Contains(body, veil) || !strings.Contains(body, "the secret formula") {
		t.Error("opted in, the molt is still veiled")
	}
	if _, body, _ := h.get("/settings"); !strings.Contains(body, `id="show-nsfw" name="show_nsfw" value="true" checked`) {
		t.Error("settings doesn't show the preference")
	}
	h.post("/settings/content", url.Values{"csrf_token": {tok}})
	if _, body, _ := h.get("/sea"); !strings.Contains(body, veil) {
		t.Error("opted out again, the molt isn't veiled")
	}

	h.client = h.newClient()
	h.login("karen@krabber.test", "computer-wife!")
	tok = h.csrf("/trench")
	status, _, hdr := h.post("/molt/nsfw/"+id, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
	if status != http.StatusOK || hdr.Get("HX-Refresh") != "true" {
		t.Fatalf("remove label: %d %v", status, hdr)
	}
	if m, _ := h.store.MoltByID(ctx, id); m.NSFW {
		t.Fatal("label still on")
	}

	h.client = h.newClient()
	h.login("mrkrabs@krabber.test", "secret-mrkrabs")
	tok = h.csrf("/krabmin/molts/" + id)
	if status, _, _ := h.post("/krabmin/molts/"+id, url.Values{"csrf_token": {tok}, "action": {"nsfw"}}); status != http.StatusSeeOther {
		t.Fatalf("mark nsfw: %d", status)
	}
	if m, _ := h.store.MoltByID(ctx, id); !m.NSFW {
		t.Fatal("moderator label not stored")
	}
	if _, body, _ := h.get("/krabmin/molts/" + id); !strings.Contains(body, "Mark SFW") {
		t.Error("crabmin should offer Mark SFW")
	}
	if _, body, _ := h.get("/krabmin/log"); !strings.Contains(body, "marked NSFW a molt by") {
		t.Error("label not logged")
	}
}

func TestMutedWords(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.signupAndActivate("karen", "karen@krabber.test", "computer-wife!")
	h.signupAndActivate("sandy", "sandy@krabber.test", "karate-chop!")
	karen, _ := h.store.CrabByUsername(ctx, "karen")
	sandy, _ := h.store.CrabByUsername(ctx, "sandy")
	chum, err := h.store.CreateMolt(ctx, karen, "Fresh CHUM at the Chum Bucket")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.CreateMolt(ctx, karen, "Krabby Patties are overrated"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.CreateMolt(ctx, sandy, "I brought chum for lunch"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Remolt(ctx, sandy, chum); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Reply(ctx, sandy, chum, "more chum please"); err != nil {
		t.Fatal(err)
	}

	h.login("sandy@krabber.test", "karate-chop!")
	tok := h.csrf("/settings")
	if status, _, _ := h.post("/settings/content", url.Values{"csrf_token": {tok}, "muted_words": {" Chum ,, chum, secret formula "}}); status != http.StatusSeeOther {
		t.Fatalf("save: %d", status)
	}
	if c, _ := h.store.CrabByID(ctx, sandy.ID); !slices.Equal(c.MutedWords, []string{"chum", "secret formula"}) {
		t.Fatalf("stored %q", c.MutedWords)
	}
	if _, body, _ := h.get("/settings"); !strings.Contains(body, ">chum, secret formula</textarea>") {
		t.Error("settings doesn't show the muted words")
	}
	_, body, _ := h.get("/sea")
	if strings.Contains(body, "Chum Bucket") || !strings.Contains(body, "overrated") {
		t.Error("the Sea should drop karen's chum molt (and its remolt) but keep the others")
	}
	if !strings.Contains(body, "I brought chum for lunch") {
		t.Error("sandy's own molt should never be muted")
	}
	if _, body, _ := h.get("/krabs/karen"); strings.Contains(body, "Chum Bucket") {
		t.Error("profile list shows a muted molt")
	}
	if _, body, _ := h.get("/search?q=bucket"); strings.Contains(body, "Chum Bucket") {
		t.Error("search shows a muted molt")
	}
	if status, body, _ := h.get("/molt/view/" + chum.ID); status != http.StatusOK || !strings.Contains(body, "Chum Bucket") || !strings.Contains(body, "more chum please") {
		t.Error("opening the molt itself should still work, and sandy's reply stays")
	}

	h.client = h.newClient()
	h.login("karen@krabber.test", "computer-wife!")
	if _, body, _ := h.get("/sea"); !strings.Contains(body, "Chum Bucket") {
		t.Error("other crabs' mutes leaked into karen's Sea")
	}
}

func TestLinkCards(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.signupAndActivate("karen", "karen@krabber.test", "computer-wife!")
	h.login("karen@krabber.test", "computer-wife!")
	if err := h.store.PutLinkCard(ctx, store.LinkCard{URL: "https://krabber.net/", Title: "Krabber <3", Description: "Molts from the deep", Host: "krabber.net"}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.PutLinkCard(ctx, store.LinkCard{URL: "https://dead.test/", Failed: true}); err != nil {
		t.Fatal(err)
	}

	tok := h.csrf("/trench")
	for _, text := range []string{"come see https://Krabber.net!", "new page https://fresh.test/a#frag", "gone https://dead.test", "insecure http://plain.test"} {
		if status, _, _ := h.post("/molt/create", url.Values{"csrf_token": {tok}, "content": {text}}); status != http.StatusSeeOther {
			t.Fatalf("create %q: %d", text, status)
		}
	}
	if !h.cards.asked("https://fresh.test/a") {
		t.Errorf("posting didn't ask for the new card: %v", h.cards.urls)
	}
	if h.cards.asked("http://plain.test") || h.cards.asked("http://plain.test/") {
		t.Error("asked for a card over plain http")
	}

	_, body, _ := h.get("/sea")
	for _, want := range []string{
		`<a class="link-card zindex-front rounded-media mb-2" href="https://krabber.net/"`,
		`<span class="card-title">Krabber &lt;3</span>`,
		`Molts from the deep`,
		`href="https://Krabber.net" class="mention zindex-front" target="_blank" rel="nofollow ugc noopener noreferrer">Krabber.net</a>!`,
		`href="http://plain.test" class="mention`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Sea missing %s", want)
		}
	}
	if n := strings.Count(body, `class="link-card`); n != 1 {
		t.Errorf("%d cards on the Sea, want 1 (failed and unfetched pages have none)", n)
	}
}

func TestYouTube(t *testing.T) {
	h := newHarness(t)
	h.signupAndActivate("karen", "karen@krabber.test", "computer-wife!")
	h.login("karen@krabber.test", "computer-wife!")
	tok := h.csrf("/trench")
	text := "never gonna https://youtu.be/dQw4w9WgXcQ?si=x and https://krabber.net/about"
	if status, _, _ := h.post("/molt/create", url.Values{"csrf_token": {tok}, "content": {text}}); status != http.StatusSeeOther {
		t.Fatalf("create: %d", status)
	}
	if h.cards.asked("https://youtu.be/dQw4w9WgXcQ?si=x") || !h.cards.asked("https://krabber.net/about") {
		t.Errorf("the card should be for the non-YouTube link: %v", h.cards.urls)
	}
	molts, _ := h.store.MoltsByOwner(context.Background(), currentID(t, h, "karen"), 1)
	for _, path := range []string{"/sea", "/molt/view/" + molts[0].ID} {
		_, body, _ := h.get(path)
		if !strings.Contains(body, `data-youtube="dQw4w9WgXcQ"`) || !strings.Contains(body, `href="https://www.youtube.com/watch?v=dQw4w9WgXcQ"`) {
			t.Errorf("%s has no YouTube placeholder", path)
		}
		if strings.Contains(body, "<iframe") {
			t.Errorf("%s loads the player before a click", path)
		}
	}
}

func TestStats(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.signupAndActivate("karen", "karen@krabber.test", "computer-wife!")
	h.signupAndActivate("sandy", "sandy@krabber.test", "karate-chop!")
	h.signupAndActivate("gary", "gary@krabber.test", "meow-meow-meow")
	karen, _ := h.store.CrabByUsername(ctx, "karen")
	sandy, _ := h.store.CrabByUsername(ctx, "sandy")
	gary, _ := h.store.CrabByUsername(ctx, "gary")
	for _, fan := range []*store.Crab{sandy, gary} {
		if err := h.store.Follow(ctx, fan, karen); err != nil {
			t.Fatal(err)
		}
	}
	liked, err := h.store.CreateMolt(ctx, karen, "Computer, %science is the answer")
	if err != nil {
		t.Fatal(err)
	}
	chatty, err := h.store.CreateMolt(ctx, sandy, "Who wants to talk %science?")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []*store.Crab{sandy, gary} {
		if _, err := h.store.ToggleLike(ctx, c, liked); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []*store.Crab{karen, gary} {
		if _, err := h.store.Reply(ctx, c, chatty, "me!"); err != nil {
			t.Fatal(err)
		}
	}

	status, body, _ := h.get("/stats")
	if status != http.StatusOK {
		t.Fatalf("stats: %d", status)
	}
	section := func(title string) string {
		i := strings.Index(body, ">"+title+"</h1>")
		if i < 0 {
			t.Fatalf("no %s section", title)
		}
		rest := body[i:]
		if j := strings.Index(rest[1:], `class="crab-king`); j >= 0 {
			rest = rest[:j+1]
		}
		return rest
	}
	if !strings.Contains(body, `<h1 class="user-count text-center">3</h1>`) {
		t.Error("active crab count")
	}
	if s := section("Krab King"); !strings.Contains(s, "@karen") || !strings.Contains(s, "2 followers") {
		t.Error("Krab King should be karen with 2 followers")
	}
	if s := section("Baby Krab"); !strings.Contains(s, "@gary") {
		t.Error("Baby Krab should be the newest crab, gary")
	}
	if s := section("Best Molt"); !strings.Contains(s, "</a> is the answer") {
		t.Error("Best Molt should be karen's liked molt")
	}
	if s := section("Talked About"); !strings.Contains(s, "Who wants to talk") {
		t.Error("Talked About should be sandy's molt with two replies")
	}
	if s := section("Trendy"); !strings.Contains(s, `href="/krabtag/science">%science</a>`) {
		t.Error("Trendy should be the science crabtag")
	}

	// A crab who blocked karen doesn't see her crowned.
	h.login("sandy@krabber.test", "karate-chop!")
	tok := h.csrf("/stats")
	h.post("/block/"+karen.ID, url.Values{"csrf_token": {tok}})
	_, body, _ = h.get("/stats")
	if stats := body[strings.Index(body, `id="stats-body"`):]; strings.Contains(stats, "@karen") || strings.Contains(stats, "</a> is the answer") {
		t.Error("stats show a blocked crab")
	}
}

func TestVerifiedBadge(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, name := range []string{"boss", "mod", "karen"} {
		hash, _ := auth.HashPassword("secret-" + name)
		c, err := h.store.CreateCrab(ctx, name, name+"@krabber.test", hash)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.store.ActivateCrab(ctx, c.ID); err != nil {
			t.Fatal(err)
		}
	}
	crab := func(name string) *store.Crab {
		c, err := h.store.CrabByUsername(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if err := h.store.SetRole(ctx, crab("boss"), store.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetRole(ctx, crab("mod"), store.RoleModerator); err != nil {
		t.Fatal(err)
	}
	karen := crab("karen")
	if _, err := h.store.CreateMolt(ctx, karen, "I'm a computer"); err != nil {
		t.Fatal(err)
	}
	const badge = `aria-label="Verified"`

	h.login("mod@krabber.test", "secret-mod")
	tok := h.csrf("/krabmin/krabs/karen")
	if _, body, _ := h.get("/krabmin/krabs/karen"); strings.Contains(body, `value="verify"`) {
		t.Error("moderators shouldn't get the Verify button")
	}
	h.post("/krabmin/krabs/"+karen.ID, url.Values{"csrf_token": {tok}, "action": {"verify"}})
	if crab("karen").Verified {
		t.Fatal("a moderator verified a crab")
	}

	h.client = h.newClient()
	h.login("boss@krabber.test", "secret-boss")
	tok = h.csrf("/krabmin/krabs/karen")
	if status, _, _ := h.post("/krabmin/krabs/"+karen.ID, url.Values{"csrf_token": {tok}, "action": {"verify"}}); status != http.StatusSeeOther {
		t.Fatalf("verify: %d", status)
	}
	if !crab("karen").Verified {
		t.Fatal("not verified")
	}
	for _, path := range []string{"/krabs/karen", "/sea", "/krabs"} {
		if _, body, _ := h.get(path); !strings.Contains(body, badge) {
			t.Errorf("%s has no badge", path)
		}
	}
	_, body, _ := h.get("/krabmin/log")
	if !strings.Contains(body, "tried to verify") || !strings.Contains(body, "verified") {
		t.Error("verification attempts and actions should be logged")
	}
	h.post("/krabmin/krabs/"+karen.ID, url.Values{"csrf_token": {tok}, "action": {"unverify"}})
	if _, body, _ := h.get("/krabs/karen"); strings.Contains(body, badge) {
		t.Error("badge still on the profile after unverify")
	}
}

func TestTrophies(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, name := range []string{"boss", "karen", "plankton"} {
		hash, _ := auth.HashPassword("secret-" + name)
		c, err := h.store.CreateCrab(ctx, name, name+"@krabber.test", hash)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.store.ActivateCrab(ctx, c.ID); err != nil {
			t.Fatal(err)
		}
	}
	crab := func(name string) *store.Crab {
		c, err := h.store.CrabByUsername(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	has := func(name string) []string {
		got, err := h.store.Trophies(ctx, crab(name).ID)
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, len(got))
		for _, a := range got {
			ids = append(ids, a.TrophyID)
		}
		return ids
	}
	if err := h.store.SetRole(ctx, crab("boss"), store.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	karen := crab("karen")
	rogen, err := h.store.CreateMolt(ctx, karen, "Seth Rogen laugh: heh heh heh")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.Follow(ctx, karen, crab("plankton")); err != nil {
		t.Fatal(err)
	}

	h.login("plankton@krabber.test", "secret-plankton")
	tok := h.csrf("/sea")
	h.post("/molt/create", url.Values{"csrf_token": {tok}, "content": {"my plan is %420 friendly"}})
	h.post("/molt/create", url.Values{"csrf_token": {tok}, "content": {"again %420"}})
	h.post("/follow/"+karen.ID, url.Values{"csrf_token": {tok}})
	h.post("/molt/like/"+rogen.ID, url.Values{"csrf_token": {tok}})
	h.post("/unfollow/"+karen.ID, url.Values{"csrf_token": {tok}})

	if got := has("plankton"); !slices.Equal(got, []string{"baby-krab", "pineapple-express", "rogen"}) {
		t.Errorf("plankton's trophies: %v", got)
	}
	if got := has("karen"); !slices.Equal(got, []string{"social-newbie", "back-krabber"}) {
		t.Errorf("karen's trophies: %v", got)
	}
	if n := crab("plankton").Trophies; n != 3 {
		t.Errorf("plankton's trophy count: %d", n)
	}
	_, body, _ := h.get("/krabs/plankton/trophies")
	if !strings.Contains(body, "Pineapple Express") || !strings.Contains(body, "<strong>3</strong> <span class=\"text-muted\">trophies") {
		t.Error("trophy tab doesn't show plankton's trophies")
	}
	if _, body, _ := h.get("/notifications"); !strings.Contains(body, "You earned the trophy: <strong>Baby Krab</strong>") {
		t.Error("no trophy notification")
	}
	if _, body, _ := h.get("/krabs/boss/trophies"); !strings.Contains(body, "hasn't earned any trophies") {
		t.Error("empty trophy case message missing")
	}

	if status, _, _ := h.post("/krabmin/krabs/"+karen.ID, url.Values{"csrf_token": {tok}, "action": {"award_trophy"}, "trophy": {"contributor"}}); status != http.StatusForbidden && status != http.StatusNotFound {
		t.Errorf("a regular krab reached Krabmin: %d", status)
	}

	h.client = h.newClient()
	h.login("boss@krabber.test", "secret-boss")
	tok = h.csrf("/krabmin/krabs/karen")
	h.post("/krabmin/krabs/"+karen.ID, url.Values{"csrf_token": {tok}, "action": {"award_trophy"}, "trophy": {"mingler"}})
	h.post("/krabmin/krabs/"+karen.ID, url.Values{"csrf_token": {tok}, "action": {"award_trophy"}, "trophy": {"contributor"}})
	if got := has("karen"); !slices.Contains(got, "contributor") || slices.Contains(got, "mingler") {
		t.Errorf("after awards: %v (only manual trophies can be handed out)", got)
	}
	h.post("/krabmin/krabs/"+karen.ID, url.Values{"csrf_token": {tok}, "action": {"revoke_trophy"}, "trophy": {"contributor"}})
	if got := has("karen"); slices.Contains(got, "contributor") {
		t.Errorf("contributor not revoked: %v", got)
	}
	h.post("/krabmin/krabs/"+karen.ID, url.Values{"csrf_token": {tok}, "action": {"make_moderator"}})
	if got := has("karen"); !slices.Contains(got, "unlimited-power") {
		t.Errorf("new moderator has no Unlimited Power: %v", got)
	}
	if _, body, _ := h.get("/krabmin/log"); !strings.Contains(body, "awarded a trophy to") || !strings.Contains(body, "took a trophy back from") {
		t.Error("trophy actions aren't logged")
	}
}

func TestChangeUsername(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.signupAndActivate("karen", "karen@krabber.test", "computer-wife!")
	h.signupAndActivate("sandy", "sandy@krabber.test", "karate-chop!")
	karen, _ := h.store.CrabByUsername(ctx, "karen")
	sandy, _ := h.store.CrabByUsername(ctx, "sandy")
	if _, err := h.store.CreateMolt(ctx, karen, "Hi, I'm @karen"); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Follow(ctx, sandy, karen); err != nil {
		t.Fatal(err)
	}

	h.login("karen@krabber.test", "computer-wife!")
	tok := h.csrf("/settings")
	for name, want := range map[string]string{"sandy": "That name is taken", "no spaces": "Use 3–20 letters", "karen": "already your username"} {
		if status, body, _ := h.post("/settings/username", url.Values{"csrf_token": {tok}, "username": {name}}); status != http.StatusUnprocessableEntity || !strings.Contains(body, want) {
			t.Errorf("rename to %q: %d, want %q", name, status, want)
		}
	}
	status, body, _ := h.post("/settings/username", url.Values{"csrf_token": {tok}, "username": {"@Computer"}})
	if status != http.StatusSeeOther {
		t.Fatalf("rename: %d %s", status, body)
	}
	if _, body, _ := h.get("/settings"); !strings.Contains(body, "You&#39;re now @Computer.") || !strings.Contains(body, "change it again on") {
		t.Error("settings should confirm the rename and show when the next one is allowed")
	}
	if status, _, _ := h.post("/settings/username", url.Values{"csrf_token": {tok}, "username": {"computer2"}}); status != http.StatusTooManyRequests {
		t.Errorf("second rename: %d", status)
	}

	noFollow := h.newClient()
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for old, want := range map[string]string{"/krabs/karen": "/krabs/Computer", "/krabs/karen/followers": "/krabs/Computer/followers", "/krabs/computer": "/krabs/Computer"} {
		res, err := noFollow.Get(h.srv.URL + old)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusMovedPermanently || res.Header.Get("Location") != want {
			t.Errorf("%s: %d → %q, want %q", old, res.StatusCode, res.Header.Get("Location"), want)
		}
	}

	_, body, _ = h.get("/sea")
	if !strings.Contains(body, `href="/krabs/Computer"`) || strings.Contains(body, `href="/krabs/karen"`) {
		t.Error("molts should link to the new name")
	}
	if !strings.Contains(body, "Hi, I&#39;m @karen") {
		t.Error("old mentions stay as written")
	}
	h.client = h.newClient()
	h.login("sandy@krabber.test", "karate-chop!")
	if _, body, _ := h.get("/krabs/sandy/following"); !strings.Contains(body, `data-name="Computer"`) || strings.Contains(body, `data-name="karen"`) {
		t.Error("sandy's following list should show the new name")
	}
}

func TestAppearance(t *testing.T) {
	h := newHarness(t)
	h.signupAndActivate("gary", "gary@krabber.test", "meow-meow-meow")
	h.login("gary@krabber.test", "meow-meow-meow")
	if _, body, _ := h.get("/sea"); strings.Contains(body, "light_mode.css") || strings.Contains(body, "dyslexic_mode.css") {
		t.Fatal("themes should be off by default")
	}
	tok := h.csrf("/settings")
	if status, _, _ := h.post("/settings/appearance", url.Values{"csrf_token": {tok}, "light_mode": {"true"}, "dyslexic_mode": {"true"}}); status != http.StatusSeeOther {
		t.Fatalf("save: %d", status)
	}
	_, body, _ := h.get("/sea")
	if !strings.Contains(body, "/static/css/light_mode.css?v=") || !strings.Contains(body, "/static/css/dyslexic_mode.css?v=") {
		t.Error("the theme stylesheets aren't linked")
	}
	if strings.Index(body, "light_mode.css") < strings.Index(body, "app.css") {
		t.Error("light mode must load after app.css to win")
	}
	if _, body, _ := h.get("/settings"); !strings.Contains(body, `id="light-mode" name="light_mode" value="true" checked`) {
		t.Error("settings don't show light mode on")
	}
	for _, path := range []string{"/static/css/light_mode.css", "/static/css/dyslexic_mode.css", "/static/fonts/OpenDyslexic-Regular.otf"} {
		if status, _, _ := h.get(path); status != http.StatusOK {
			t.Errorf("%s: %d", path, status)
		}
	}
	h.post("/settings/appearance", url.Values{"csrf_token": {tok}, "dyslexic_mode": {"true"}})
	if _, body, _ := h.get("/sea"); strings.Contains(body, "light_mode.css") || !strings.Contains(body, "dyslexic_mode.css") {
		t.Error("turning light mode off should keep dyslexic mode")
	}
	h.post("/krab/logout", url.Values{"csrf_token": {h.csrf("/sea")}})
	if _, body, _ := h.get("/sea"); strings.Contains(body, "dyslexic_mode.css") {
		t.Error("signed-out visitors get the default theme")
	}
}

func TestInviteCodes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.signupAndActivate("karen", "karen@krabber.test", "computer-wife!")
	h.login("karen@krabber.test", "computer-wife!")
	_, body, _ := h.get("/settings")
	m := regexp.MustCompile(`id="invite-code" class="form-control" value="([a-z0-9]{8})"`).FindStringSubmatch(body)
	if m == nil || !strings.Contains(body, "/krab/signup?code="+m[1]) || !strings.Contains(body, "Nobody has joined") {
		t.Fatalf("settings has no invite code and link")
	}
	code := m[1]
	h.client = h.newClient()

	ip := 0
	signup := func(name, invite string) (int, string) {
		ip++ // a new visitor each time, under the per-IP signup limit
		tok := h.csrf("/krab/login")
		status, body, _ := h.post("/krab/signup", url.Values{"csrf_token": {tok}, "name": {name}, "email": {name + "@krabber.test"}, "password": {"shell-polish-1"}, "code": {invite}},
			"CloudFront-Viewer-Address", fmt.Sprintf("198.51.100.%d:443", ip))
		return status, body
	}
	if _, body, _ := h.get("/krab/signup?code=" + strings.ToUpper(code)); !strings.Contains(body, `value="`+code+`"`) || !strings.Contains(body, "(optional)") {
		t.Error("the invite link should fill in the code, optional in open mode")
	}
	if status, body := signup("gary", "wrongcode"); status != http.StatusUnprocessableEntity || !strings.Contains(body, "invite code doesn&#39;t work") {
		t.Errorf("bad code: %d", status)
	}
	if status, _ := signup("sandy", code); status != http.StatusSeeOther {
		t.Fatalf("signup with code: %d", status)
	}
	if status, _ := signup("patrick", ""); status != http.StatusSeeOther {
		t.Fatalf("open mode without a code: %d", status)
	}
	sandy, _ := h.store.CrabByUsername(ctx, "sandy")
	karen, _ := h.store.CrabByUsername(ctx, "karen")
	if sandy.InvitedBy != karen.ID || karen.Invites != 1 {
		t.Fatalf("invited_by %q, invites %d", sandy.InvitedBy, karen.Invites)
	}
	if _, body, _ := h.get("/krabs/karen"); !strings.Contains(body, "Invited 1 krab to Krabber") {
		t.Error("profile doesn't show the invite count")
	}
	if _, body, _ := h.get("/stats"); !strings.Contains(body, ">Party Starter</h1>") || !strings.Contains(body, "1 invite<") {
		t.Error("stats have no Party Starter")
	}

	h.app.cfg.SignupMode = config.SignupInvite
	if status, body := signup("gary", ""); status != http.StatusUnprocessableEntity || !strings.Contains(body, "need an invite code") {
		t.Errorf("invite mode without a code: %d", status)
	}
	if status, _ := signup("gary", code); status != http.StatusSeeOther {
		t.Errorf("invite mode with a code: %d", status)
	}

	hash, _ := auth.HashPassword("secret-boss")
	boss, _ := h.store.CreateCrab(ctx, "boss", "boss@krabber.test", hash)
	if err := h.store.ActivateCrab(ctx, boss.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetRole(ctx, boss, store.RoleModerator); err != nil {
		t.Fatal(err)
	}
	h.login("boss@krabber.test", "secret-boss")
	if _, body, _ := h.get("/krabmin/krabs/sandy"); !strings.Contains(body, `Invited by <a href="/krabmin/krabs/karen">@karen</a>`) {
		t.Error("krabmin doesn't show who invited sandy")
	}
	tok := h.csrf("/krabmin/krabs/karen")
	h.post("/krabmin/krabs/"+karen.ID, url.Values{"csrf_token": {tok}, "action": {"disable_invites"}})
	h.client = h.newClient()
	if status, _ := signup("squidward", code); status != http.StatusUnprocessableEntity {
		t.Errorf("disabled code: %d", status)
	}
	h.login("karen@krabber.test", "computer-wife!")
	if _, body, _ := h.get("/settings"); !strings.Contains(body, "disabled by a moderator") {
		t.Error("karen should see her code is disabled")
	}

	h.app.cfg.SignupMode = config.SignupClosed
	h.client = h.newClient()
	if _, body, _ := h.get("/krab/signup"); !strings.Contains(body, "Registration is temporarily closed") || strings.Contains(body, `action="/krab/signup"`) {
		t.Error("closed signup should show the notice, not the form")
	}
	if status, _ := signup("squidward", ""); status != http.StatusUnprocessableEntity {
		t.Errorf("closed signup post: %d", status)
	}

	// MAX_KRABS closes signups once that many krabs can sign in.
	h.app.cfg.SignupMode = config.SignupOpen
	h.app.cfg.MaxKrabs = h.app.activeKrabs(httptest.NewRequest(http.MethodGet, "/", nil))
	if _, body, _ := h.get("/krab/signup"); !strings.Contains(body, "Krabber is full for now") || strings.Contains(body, `action="/krab/signup"`) {
		t.Error("a full Krabber should show the notice, not the form")
	}
	if status, body := signup("squidward", ""); status != http.StatusUnprocessableEntity || !strings.Contains(body, "Krabber is full") {
		t.Errorf("signup past the cap: %d", status)
	}
	h.app.cfg.MaxKrabs++
	if _, body, _ := h.get("/krab/signup"); !strings.Contains(body, `action="/krab/signup"`) {
		t.Error("signup should reopen below the cap")
	}
}

func TestOnboardingAndBackfill(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.signupAndActivate("karen", "karen@krabber.test", "computer-wife!")
	sandy, err := h.store.CreateCrab(ctx, "sandy", "sandy@krabber.test", []byte("h"))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.ActivateCrab(ctx, sandy.ID); err != nil {
		t.Fatal(err)
	}
	sandy, _ = h.store.CrabByKey(ctx, sandy.PK, sandy.SK)
	if _, err := h.store.CreateMolt(ctx, sandy, "hi-yah! karate time"); err != nil {
		t.Fatal(err)
	}

	h.login("karen@krabber.test", "computer-wife!")
	_, body, _ := h.get("/trench")
	if !strings.Contains(body, `id="onboarding"`) || !strings.Contains(body, "/krabs/sandy") || strings.Contains(body, "karate time") {
		t.Fatal("a krab who follows nobody should see starter krabs and an empty Trench")
	}
	h.post("/follow/"+sandy.ID, url.Values{"csrf_token": {h.csrf("/trench")}}, "HX-Request", "true")
	_, body, _ = h.get("/trench")
	if strings.Contains(body, `id="onboarding"`) || !strings.Contains(body, "karate time") {
		t.Error("after following, the welcome goes away and sandy's earlier molt is in the Trench")
	}
}

func TestSystemKrab(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.signupAndActivate("karen", "karen@krabber.test", "computer-wife!")
	h.app.version = "abc1234"

	c, err := h.app.systemKrab(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := h.app.systemKrab(ctx); err != nil || again.ID != c.ID {
		t.Fatalf("second load made another krab: %v", err)
	}
	if !c.Verified || c.DisplayName != "Krabber System" {
		t.Errorf("system krab: %+v", c)
	}

	// Two servers posting the same molt: only one gets through.
	for range 2 {
		h.app.systemMolt(ctx, c, "system#version#abc1234", time.Hour, func() string { return "new version abc1234" })
	}
	h.app.systemMolt(ctx, c, "system#daily#test", time.Hour, func() string { return h.app.dailyReport(ctx, time.Now().UTC()) })
	molts, err := h.store.MoltsByOwner(ctx, c.ID, 10)
	if err != nil || len(molts) != 2 {
		t.Fatalf("system molts: %d %v", len(molts), err)
	}
	report := molts[0].Content
	for _, want := range []string{"all systems nominal", "2 krabs (2 new)", "abc1234", "%status"} {
		if !strings.Contains(report, want) {
			t.Errorf("daily report %q lacks %q", report, want)
		}
	}
	if len([]rune(report)) > store.MaxMoltLength {
		t.Errorf("daily report is %d characters", len([]rune(report)))
	}
	if _, body, _ := h.get("/krabs/system"); !strings.Contains(body, "Krabber System") || !strings.Contains(body, `aria-label="Verified"`) {
		t.Error("@system's profile should show the name and the badge")
	}
}

func TestMoltMenuEndsTheActionBar(t *testing.T) {
	h := newHarness(t)
	h.signupAndActivate("karen", "karen@krabber.test", "computer-wife!")
	karen, _ := h.store.CrabByUsername(context.Background(), "karen")
	m, err := h.store.CreateMolt(context.Background(), karen, "menu check")
	if err != nil {
		t.Fatal(err)
	}
	h.login("karen@krabber.test", "computer-wife!")
	for _, path := range []string{"/sea", "/molt/view/" + m.ID} {
		_, body, _ := h.get(path)
		bar := body[strings.Index(body, m.ID):]
		bar = bar[strings.Index(bar, `class="mini-molt-actions`):]
		if i := strings.Index(bar[1:], `class="mini-molt-actions`); i > 0 {
			bar = bar[:i]
		}
		if strings.Count(body, `aria-label="More options"`) != 1 || !strings.Contains(bar, `aria-label="More options"`) {
			t.Errorf("%s: the … menu should be the action bar's last item, once (%d menus, in bar: %v)", path,
				strings.Count(body, `aria-label="More options"`), strings.Contains(bar, `aria-label="More options"`))
		}
	}
}

func TestStrangersSeeTheFirstPageOnly(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.signupAndActivate("karen", "karen@krabber.test", "computer-wife!")
	karen, _ := h.store.CrabByUsername(ctx, "karen")
	for i := range pageSize + 5 {
		if _, err := h.store.CreateMolt(ctx, karen, fmt.Sprintf("molt %d %%krabs", i)); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"/sea", "/krabs/karen", "/krabtag/krabs"} {
		_, body, _ := h.get(path)
		if strings.Contains(body, `id="load-more"`) || !strings.Contains(body, `id="members-more"`) {
			t.Errorf("%s signed out should end in the join prompt, not Load more", path)
		}
		if status, _, hdr := h.get(path + "?after=anything"); status != http.StatusSeeOther || hdr.Get("Location") != "/krab/login" {
			t.Errorf("%s?after= signed out: %d %s", path, status, hdr.Get("Location"))
		}
	}
	// Strangers share the Sea for a minute: a molt written meanwhile shows up later.
	if _, err := h.store.CreateMolt(ctx, karen, "brand new"); err != nil {
		t.Fatal(err)
	}
	if _, body, _ := h.get("/sea"); strings.Contains(body, "brand new") {
		t.Error("the strangers' Sea wasn't kept")
	}

	h.login("karen@krabber.test", "computer-wife!")
	_, body, _ := h.get("/sea")
	if !strings.Contains(body, "brand new") || !strings.Contains(body, `id="load-more"`) || strings.Contains(body, `id="members-more"`) {
		t.Error("signed in, the Sea should be current and page on")
	}
}

func TestWriteLimits(t *testing.T) {
	h := newHarness(t)
	h.signupAndActivate("karen", "karen@krabber.test", "computer-wife!")
	karen, _ := h.store.CrabByUsername(context.Background(), "karen")
	m, err := h.store.CreateMolt(context.Background(), karen, "like me")
	if err != nil {
		t.Fatal(err)
	}
	old := writeLimits["like"]
	writeLimits["like"] = 2
	t.Cleanup(func() { writeLimits["like"] = old })

	h.login("karen@krabber.test", "computer-wife!")
	tok := h.csrf("/trench")
	for i, want := range []int{http.StatusOK, http.StatusOK, http.StatusTooManyRequests} {
		status, body, hdr := h.post("/molt/like/"+m.ID, url.Values{"csrf_token": {tok}}, "HX-Request", "true")
		if status != want {
			t.Fatalf("like %d: %d %s", i+1, status, body)
		}
		if status == http.StatusTooManyRequests && (hdr.Get("Retry-After") == "" || !strings.Contains(body, "going a bit fast")) {
			t.Errorf("limit response: %q %q", hdr.Get("Retry-After"), body)
		}
	}
	// Other kinds of writes have their own count.
	if status, _, _ := h.post("/molt/bookmark/"+m.ID, url.Values{"csrf_token": {tok}}, "HX-Request", "true"); status != http.StatusOK {
		t.Errorf("bookmark after the like limit: %d", status)
	}
}

func TestFriendsOfFriends(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.signupAndActivate("karen", "karen@krabber.test", "computer-wife!")
	crabs := map[string]*store.Crab{}
	for _, name := range []string{"sandy", "gary", "patrick", "squidward", "plankton", "pearl"} {
		c, err := h.store.CreateCrab(ctx, name, name+"@krabber.test", []byte("h"))
		if err != nil {
			t.Fatal(err)
		}
		if err := h.store.ActivateCrab(ctx, c.ID); err != nil {
			t.Fatal(err)
		}
		crabs[name] = c
	}
	karen, _ := h.store.CrabByUsername(ctx, "karen")
	follow := func(who *store.Crab, whom string) {
		t.Helper()
		if err := h.store.Follow(ctx, who, crabs[whom]); err != nil {
			t.Fatal(err)
		}
	}
	// Patrick is the most followed; gary and pearl are only followed by sandy,
	// whom karen follows.
	follow(crabs["squidward"], "patrick")
	follow(crabs["plankton"], "patrick")
	follow(crabs["pearl"], "patrick")
	follow(crabs["sandy"], "gary")
	follow(crabs["sandy"], "pearl")
	follow(karen, "sandy")

	h.login("karen@krabber.test", "computer-wife!")
	panel := func() []string {
		t.Helper()
		_, body, _ := h.get("/sea")
		i := strings.Index(body, `id="recommended-crabs"`)
		if i < 0 {
			t.Fatal("no Who to follow panel")
		}
		body = body[i:]
		body = body[:strings.Index(body, "See all krabs")]
		var names []string
		for _, m := range regexp.MustCompile(`data-name="(\w+)"`).FindAllStringSubmatch(body, -1) {
			names = append(names, m[1])
		}
		return names
	}
	got := panel()
	if len(got) < 3 || !slices.Contains(got[:2], "gary") || !slices.Contains(got[:2], "pearl") || got[2] != "patrick" {
		t.Fatalf("who to follow = %v, want gary and pearl (friends of friends) before patrick", got)
	}

	tok := h.csrf("/sea")
	h.post("/follow/"+crabs["gary"].ID, url.Values{"csrf_token": {tok}})
	if got := panel(); slices.Contains(got, "gary") || got[0] != "pearl" {
		t.Fatalf("after following gary: %v", got)
	}
}

func TestLegacyCrabURLsRedirect(t *testing.T) {
	h := newHarness(t)
	noFollow := h.newClient()
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for old, want := range map[string]string{
		"/crabs/karen/likes":           "/krabs/karen/likes",
		"/crabs":                       "/krabs",
		"/crab/activate?token=ABC":     "/krab/activate?token=ABC",
		"/crabtag/krabbypatty":         "/krabtag/krabbypatty",
		"/crabmin/crabs/karen":         "/krabmin/krabs/karen",
		"/crabmin":                     "/krabmin",
		"/crab/reset?token=X&next=%2F": "/krab/reset?token=X&next=%2F",
	} {
		res, err := noFollow.Get(h.srv.URL + old)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusPermanentRedirect || res.Header.Get("Location") != want {
			t.Errorf("%s: %d → %q, want 308 → %q", old, res.StatusCode, res.Header.Get("Location"), want)
		}
	}
	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/crab/login", strings.NewReader("x=1"))
	res, err := noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusPermanentRedirect || res.Header.Get("Location") != "/krab/login" {
		t.Errorf("POST /crab/login: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	if status, _, _ := h.get("/crabsalad"); status != http.StatusNotFound {
		t.Errorf("/crabsalad isn't an old URL: %d", status)
	}
}

func TestCommas(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", -4200: "-4,200"} {
		if got := commas(n); got != want {
			t.Errorf("commas(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestParseMutedWords(t *testing.T) {
	got := parseMutedWords("Chum, ,chum,  Secret   Formula ,\tplankton\n," + strings.Repeat("x", 65))
	if want := []string{"chum", "secret formula", "plankton"}; !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	many := strings.Repeat("a,b,", 100)
	if n := len(parseMutedWords(many)); n != 2 {
		t.Fatalf("repeats: %d", n)
	}
	var sb strings.Builder
	for i := range 300 {
		fmt.Fprintf(&sb, "w%d,", i)
	}
	if n := len(parseMutedWords(sb.String())); n != 100 {
		t.Fatalf("cap: %d", n)
	}
}
