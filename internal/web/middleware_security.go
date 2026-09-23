package web

import (
	"context"
	"crypto/subtle"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/justinas/nosurf"
)

// recoverPanic turns a panic into a logged 500.
func (app *App) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				w.Header().Set("Connection", "close")
				app.serverError(w, r, fmt.Errorf("panic: %v", rec))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// logRequests logs every error response and 5% of the rest, keeping CloudWatch
// Logs cost bounded under heavy traffic. Query strings, cookies and form data
// are never logged.
func (app *App) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)

		if strings.HasPrefix(r.URL.Path, "/static/") {
			return
		}
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		if status < 400 && rand.Float64() >= 0.05 { //nolint:gosec // log sampling
			return
		}
		app.log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"ms", time.Since(start).Milliseconds(),
			"ip", clientIP(r),
		)
	})
}

// originVerify rejects requests that didn't come through CloudFront, which
// adds a secret X-Origin-Verify header. The security group already allows only
// CloudFront's IP ranges; this closes the gap for anyone else using CloudFront.
func (app *App) originVerify(next http.Handler) http.Handler {
	if app.cfg.IsDev() {
		return next
	}
	secrets := make([][]byte, 0, len(app.cfg.OriginVerifySecrets))
	for _, s := range app.cfg.OriginVerifySecrets {
		secrets = append(secrets, []byte(s))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("X-Origin-Verify"))
		for _, s := range secrets {
			if subtle.ConstantTimeCompare(got, s) == 1 {
				next.ServeHTTP(w, r)
				return
			}
		}
		app.clientError(w, http.StatusNotFound)
	})
}

// withClientIP records the viewer's IP. Behind CloudFront (and only after
// originVerify has passed) it comes from CloudFront-Viewer-Address; the TCP
// peer is CloudFront or nginx, never the visitor.
func (app *App) withClientIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := ""
		if !app.cfg.IsDev() {
			ip = viewerIP(r.Header.Get("CloudFront-Viewer-Address"))
		}
		if ip == "" {
			ip, _, _ = net.SplitHostPort(r.RemoteAddr)
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxClientIP, ip)))
	})
}

// viewerIP parses CloudFront-Viewer-Address, "ip:port" for IPv4 and IPv6.
func viewerIP(v string) string {
	i := strings.LastIndexByte(v, ':')
	if i <= 0 {
		return ""
	}
	ip := strings.Trim(v[:i], "[]")
	if net.ParseIP(ip) == nil {
		return ""
	}
	return ip
}

func clientIP(r *http.Request) string {
	ip, _ := r.Context().Value(ctxClientIP).(string)
	return ip
}

// securityHeaders sets the browser hardening headers on every response.
func (app *App) securityHeaders(next http.Handler) http.Handler {
	scriptSrc, frameSrc := "'self'", "'none'"
	if app.cfg.TurnstileEnabled() {
		scriptSrc += " https://challenges.cloudflare.com"
		frameSrc = "https://challenges.cloudflare.com"
	}
	csp := strings.Join([]string{
		"default-src 'self'",
		"script-src " + scriptSrc,
		// Templates still use inline style attributes; scripts stay strict.
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data:",
		"font-src 'self'",
		"connect-src 'self'",
		"frame-src " + frameSrc,
		"form-action 'self'",
		"frame-ancestors 'none'",
		"base-uri 'none'",
		"object-src 'none'",
	}, "; ")
	prod := !app.cfg.IsDev()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		if prod {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		if !strings.HasPrefix(r.URL.Path, "/static/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// crossOriginProtection rejects cross-site state-changing requests using the
// browser's Sec-Fetch-Site and Origin headers. Rejections are 400, never 403:
// CloudFront shows the maintenance page for 403s (the WAF switch).
func (app *App) crossOriginProtection(next http.Handler) http.Handler {
	cop := http.NewCrossOriginProtection()
	if err := cop.AddTrustedOrigin(app.cfg.BaseURL.Scheme + "://" + app.cfg.BaseURL.Host); err != nil {
		panic(err)
	}
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		app.clientError(w, http.StatusBadRequest)
	}))
	return cop.Handler(next)
}

// csrf adds nosurf's per-session token check as a second layer.
func (app *App) csrf(next http.Handler) http.Handler {
	h := nosurf.New(next)
	h.SetBaseCookie(http.Cookie{ //nolint:gosec // Secure is off only for plain-HTTP localhost dev
		Name:     csrfCookieName(app.cfg.IsDev()),
		Path:     "/",
		HttpOnly: true,
		Secure:   !app.cfg.IsDev(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int((12 * time.Hour).Seconds()),
	})
	// CloudFront terminates TLS; the app sees plain HTTP from nginx.
	h.SetIsTLSFunc(func(*http.Request) bool { return !app.cfg.IsDev() })
	allowed, err := nosurf.StaticOrigins(app.cfg.BaseURL.Scheme + "://" + app.cfg.BaseURL.Host)
	if err != nil {
		panic(err)
	}
	h.SetIsAllowedOriginFunc(allowed)
	h.SetFailureHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		app.log.Info("csrf check failed", "path", r.URL.Path, "reason", nosurf.Reason(r))
		app.clientError(w, http.StatusBadRequest)
	}))
	return h
}

func csrfCookieName(dev bool) string {
	if dev {
		return "krabber_csrf"
	}
	return "__Host-krabber_csrf"
}

func csrfToken(r *http.Request) string { return nosurf.Token(r) }
