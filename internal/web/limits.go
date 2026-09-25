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
