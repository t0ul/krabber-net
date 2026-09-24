package store

import "context"

// Sea returns the newest molts from everyone, with remolts resolved to their
// originals. It reads the day index directly: a query returns up to 1 MB for
// one or two read units, so a live read is both fresh and cheaper than keeping
// a cache in sync.
func (s *Store) Sea(ctx context.Context, limit int) ([]Molt, error) {
	p, err := s.SeaPage(ctx, "", limit)
	return p.Molts, err
}

// SeaPage is Sea starting after the cursor from a previous page.
func (s *Store) SeaPage(ctx context.Context, after string, limit int) (Page, error) {
	p, err := s.LatestMoltsPage(ctx, after, limit)
	if err != nil {
		return Page{}, err
	}
	molts, err := s.ResolveRemolts(ctx, p.Molts)
	if err != nil {
		return Page{}, err
	}
	return Page{Molts: molts, Next: p.Next}, nil
}
