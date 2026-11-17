package web

import (
	"net/http"
	"sync"
	"time"
)

// The hourly write limits per krab are the engine's (rules.Limits); each
// front end counts against them with its own rules.Limiter.

// apiRatePerMin caps API requests from one client network a minute. It bounds
// the cost of spraying tokens at the bearer-auth endpoint, checked before any
// table read. CloudFront and the WAF rate-limit the same traffic by IP; this
// bounds what reaches the origin. It's a var so tests can lower it.
var apiRatePerMin = 120

// minuteLimiter is a fixed one-minute-window counter (rules.Limiter is the
// hourly version). The counts live in memory and reset on restart.
type minuteLimiter struct {
	mu     sync.Mutex
	minute time.Time
	counts map[string]int
}

func (l *minuteLimiter) allow(key string, limit int) bool {
	minute := time.Now().Truncate(time.Minute)
	l.mu.Lock()
	defer l.mu.Unlock()
	if !minute.Equal(l.minute) {
		l.minute, l.counts = minute, map[string]int{}
	}
	if l.counts[key] >= limit {
		return false
	}
	l.counts[key]++
	return true
}

// underWriteLimit counts one write of kind by the signed-in krab and, past
// the limit, answers 429 and returns false.
func (app *App) underWriteLimit(w http.ResponseWriter, r *http.Request, kind string) bool {
	if app.writes.Allow(currentCrab(r).ID, kind) {
		return true
	}
	app.log.Warn("write limit reached", "crab", currentCrab(r).ID, "kind", kind)
	w.Header().Set("Retry-After", "3600")
	http.Error(w, "You're going a bit fast. Take a breather and try again later.", http.StatusTooManyRequests)
	return false
}
