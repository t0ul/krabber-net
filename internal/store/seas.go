package store

import "context"

// Sea returns the newest molts from everyone, with remolts resolved to their
// originals. It reads the day index directly: a query returns up to 1 MB for
// one or two read units, so a live read is both fresh and cheaper than keeping
// a cache in sync.
func (s *Store) Sea(ctx context.Context, limit int) ([]Molt, error) {
	molts, err := s.LatestMolts(ctx, limit)
	if err != nil {
		return nil, err
	}
	return s.ResolveRemolts(ctx, molts)
}
