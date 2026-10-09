package web

import (
	"net/http"
	"sync"
	"time"
)

// Hourly write limits per krab. They're far above what a person does, and
// they bound what one account (or a stolen session) can cost: a molt is
// written once more for every follower, so molts are the tightest.
var writeLimits = map[string]int{
	"molt":     100, // molts, replies, quotes and remolts
	"like":     1000,
	"follow":   300,
	"bookmark": 300,
	"block":    100, // each block also touches the other krab's item
	"edit":     200, // pins and NSFW labels
}

// writeLimiter counts each krab's writes this hour. The counts live in
// memory: the site runs on one instance and a restart only resets them,
// so they cost nothing to keep.
type writeLimiter struct {
	mu     sync.Mutex
	hour   time.Time
	counts map[string]int
}

func (l *writeLimiter) allow(crabID, kind string) bool {
	hour := time.Now().Truncate(time.Hour)
	l.mu.Lock()
	defer l.mu.Unlock()
	if !hour.Equal(l.hour) {
		l.hour, l.counts = hour, map[string]int{}
	}
	key := kind + "#" + crabID
	if l.counts[key] >= writeLimits[kind] {
		return false
	}
	l.counts[key]++
	return true
}

// apiRatePerMin caps API requests from one client network a minute. It bounds
// the cost of spraying tokens at the bearer-auth endpoint, checked before any
// table read. CloudFront and the WAF rate-limit the same traffic by IP; this
// bounds what reaches the origin. It's a var so tests can lower it.
var apiRatePerMin = 120

// minuteLimiter is a fixed one-minute-window counter (writeLimiter is the
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
	if app.writes.allow(currentCrab(r).ID, kind) {
		return true
	}
	app.log.Warn("write limit reached", "crab", currentCrab(r).ID, "kind", kind)
	w.Header().Set("Retry-After", "3600")
	http.Error(w, "You're going a bit fast. Take a breather and try again later.", http.StatusTooManyRequests)
	return false
}
