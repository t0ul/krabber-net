package web

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"
)

const (
	suggestionTTL      = time.Hour // a viewer's own follow or unfollow clears it sooner
	suggestionSample   = 20        // follows whose own follows are read
	suggestionParallel = 5         // queries in flight at once
	suggestionEntries  = 20_000    // viewers cached before the cache starts over
	suggestionKept     = 50        // ranked krabs kept per viewer
)

// suggestions caches each viewer's friends-of-friends ranking, as Crabber
// suggests who to follow: crabs followed by the crabs you follow, most shared
// first. Working it out takes one query per followed crab, so it's done at
// most every few minutes per viewer rather than on every page.
type suggestions struct {
	mu      sync.Mutex
	entries map[string]suggestionEntry
}

type suggestionEntry struct {
	ranked []string // crab IDs, best first
	at     time.Time
}

// forget drops a viewer's ranking after they follow or unfollow someone.
func (s *suggestions) forget(viewerID string) {
	s.mu.Lock()
	delete(s.entries, viewerID)
	s.mu.Unlock()
}

func (s *suggestions) get(viewerID string) ([]string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[viewerID]
	if !ok || time.Since(e.at) > suggestionTTL {
		return nil, false
	}
	return e.ranked, true
}

func (s *suggestions) put(viewerID string, ranked []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil || len(s.entries) >= suggestionEntries {
		s.entries = map[string]suggestionEntry{}
	}
	s.entries[viewerID] = suggestionEntry{ranked: ranked, at: time.Now()}
}

// friendsOfFriends ranks the crabs that the viewer's follows follow, by how
// many of them do, leaving out the viewer and crabs they already follow.
func (app *App) friendsOfFriends(r *http.Request, viewerID string, followed map[string]bool) []string {
	if len(followed) == 0 {
		return nil
	}
	if ranked, ok := app.fof.get(viewerID); ok {
		return ranked
	}
	sample := make([]string, 0, min(len(followed), suggestionSample))
	for id := range followed {
		if len(sample) == suggestionSample {
			break
		}
		sample = append(sample, id)
	}

	counts := map[string]int{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, suggestionParallel)
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	for _, id := range sample {
		wg.Add(1)
		slots <- struct{}{}
		go func(id string) {
			defer wg.Done()
			defer func() { <-slots }()
			theirs, err := app.store.FollowingIDs(ctx, id)
			if err != nil {
				app.log.Warn("friends of friends", "err", err, "crab", id)
				return
			}
			mu.Lock()
			for other := range theirs {
				if other != viewerID && !followed[other] {
					counts[other]++
				}
			}
			mu.Unlock()
		}(id)
	}
	wg.Wait()

	ranked := make([]string, 0, len(counts))
	for id := range counts {
		ranked = append(ranked, id)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if counts[ranked[i]] != counts[ranked[j]] {
			return counts[ranked[i]] > counts[ranked[j]]
		}
		return ranked[i] < ranked[j]
	})
	ranked = ranked[:min(len(ranked), suggestionKept)]
	app.fof.put(viewerID, ranked)
	return ranked
}
