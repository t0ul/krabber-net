package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/t0ul/krabber-net/internal/config"
)

func testApp(cfg *config.Config) *App {
	return &App{cfg: cfg, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func prodConfig() *config.Config {
	u, _ := url.Parse("https://krabber.net")
	return &config.Config{Env: "prod", BaseURL: u, OriginVerifySecrets: []string{"current", "previous"}}
}

func ok() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

func TestOriginVerify(t *testing.T) {
	h := testApp(prodConfig()).originVerify(ok())
	for _, tc := range []struct {
		header string
		want   int
	}{
		{"", http.StatusNotFound},
		{"wrong", http.StatusNotFound},
		{"current", http.StatusOK},
		{"previous", http.StatusOK},
	} {
		r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		if tc.header != "" {
			r.Header.Set("X-Origin-Verify", tc.header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != tc.want {
			t.Errorf("header %q: got %d, want %d", tc.header, rec.Code, tc.want)
		}
	}
}

func TestViewerIP(t *testing.T) {
	for in, want := range map[string]string{
		"198.51.100.10:46532":      "198.51.100.10",
		"2001:db8::1:46532":        "2001:db8::1",
		"[2001:db8::1]:46532":      "2001:db8::1",
		"not-an-ip:1":              "",
		"":                         "",
		"198.51.100.10":            "",
		"198.51.100.10:46532:junk": "",
	} {
		if got := viewerIP(in); got != want {
			t.Errorf("viewerIP(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := testApp(prodConfig()).securityHeaders(ok())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/trench", nil))

	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"script-src 'self'", "frame-ancestors 'none'", "object-src 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q missing %q", csp, want)
		}
	}
	if strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP must not allow unsafe-eval: %q", csp)
	}
	for k, want := range map[string]string{
		"X-Frame-Options":           "DENY",
		"X-Content-Type-Options":    "nosniff",
		"Strict-Transport-Security": "max-age=31536000; includeSubDomains",
		"Cache-Control":             "no-store",
	} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestCrossOriginPostIsRejectedWith400(t *testing.T) {
	h := testApp(prodConfig()).crossOriginProtection(ok())
	for _, tc := range []struct {
		name   string
		setup  func(*http.Request)
		status int
	}{
		{"cross-site", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, http.StatusBadRequest},
		{"foreign origin", func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, http.StatusBadRequest},
		{"same-origin", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-origin") }, http.StatusOK},
		// API Gateway or a proxy rewriting Host must still accept our origin.
		{"trusted origin, other host", func(r *http.Request) {
			r.Host = "abc.execute-api.us-east-2.amazonaws.com"
			r.Header.Set("Origin", "https://krabber.net")
		}, http.StatusOK},
	} {
		r := httptest.NewRequest(http.MethodPost, "/molt/create", nil)
		tc.setup(r)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != tc.status {
			t.Errorf("%s: got %d, want %d", tc.name, rec.Code, tc.status)
		}
	}
}

func TestTemplatesParse(t *testing.T) {
	cache, err := newTemplateCache()
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range []string{"welcome.html", "trench.html", "sea.html", "view.html",
		"likes.html", "crabs.html", "login.html", "signup.html", "activate.html", "crabmin.html",
		"settings.html", "notifications.html", "profile.html", "results.html", "quote.html", "edit.html"} {
		if cache[page] == nil {
			t.Errorf("missing template %s", page)
		}
	}
	for _, fragment := range []string{"molt", "molt-actions", "follow-button", "quote-modal", "quote-form", "edit-modal", "edit-form"} {
		if cache[fragmentPage].Lookup(fragment) == nil {
			t.Errorf("%s must define the %s fragment used by htmx responses", fragmentPage, fragment)
		}
	}
}

func TestAgo(t *testing.T) {
	now := time.Now()
	for in, want := range map[time.Duration]string{
		10 * time.Second:   "now",
		5 * time.Minute:    "5m",
		3 * time.Hour:      "3h",
		2 * 24 * time.Hour: "2d",
	} {
		if got := ago(now.Add(-in)); got != want {
			t.Errorf("ago(-%v) = %q, want %q", in, got, want)
		}
	}
	old := time.Date(2020, 3, 4, 0, 0, 0, 0, time.UTC)
	if got := ago(old); got != "Mar 4, 2020" {
		t.Errorf("ago(2020) = %q", got)
	}
	if humanDate(old) != "Mar 4, 2020 at 12:00 AM" {
		t.Errorf("humanDate = %q", humanDate(old))
	}
}
