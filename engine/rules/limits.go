package rules

import (
	"sync"
	"time"
)

// Limits are the hourly write limits per krab. They're far above what a
// person does, and they bound what one account (or a stolen session) can
// cost: a molt is written once more for every follower, so molts are the
// tightest. Tests may lower one for a while.
var Limits = map[string]int{
	"molt":     100, // molts, replies, quotes and remolts
	"like":     1000,
	"follow":   300,
	"bookmark": 300,
	"block":    100, // each block also touches the other krab's item
	"edit":     200, // pins and NSFW labels
	"export":   20,  // downloads of everything a krab wrote: each reads their whole timeline
}

// Limiter counts each krab's writes this hour against Limits. The counts
// live in memory, per process: each front end keeps its own, and a restart
// only resets them, so they cost nothing to keep.
type Limiter struct {
	mu     sync.Mutex
	hour   time.Time
	counts map[string]int
}

// Allow counts one write of kind by crabID and reports whether it is within
// this hour's limit.
func (l *Limiter) Allow(crabID, kind string) bool {
	hour := time.Now().Truncate(time.Hour)
	l.mu.Lock()
	defer l.mu.Unlock()
	if !hour.Equal(l.hour) {
		l.hour, l.counts = hour, map[string]int{}
	}
	key := kind + "#" + crabID
	if l.counts[key] >= Limits[kind] {
		return false
	}
	l.counts[key]++
	return true
}
