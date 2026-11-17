package store

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// bookmarkMarker records that a crab bookmarked a molt, and where the
// bookmark sits in the crab's list.
type bookmarkMarker struct {
	PK     string `dynamodbav:"PK"`
	SK     string `dynamodbav:"SK"`
	MoltID string `dynamodbav:"molt_id"`
	ListSK string `dynamodbav:"list_sk"`
}

// Bookmark adds m to the crab's bookmarks, newest first. Bookmarking twice
// returns ErrAlreadyExists.
func (s *Store) Bookmark(ctx context.Context, c *Crab, m *Molt) error {
	if m.Remolt || m.Deleted || m.Removed {
		return ErrNotAllowed
	}
	listSK := bookmarkListSK(newID())
	marker, err := marshal(bookmarkMarker{
		PK: bookmarkPK(c.ID), SK: bookmarkSK(m.ID), MoltID: m.ID, ListSK: listSK,
	})
	if err != nil {
		return err
	}
	entry, err := marshal(moltPointer{PK: bookmarkListPK(c.ID), SK: listSK, MoltPK: m.PK, MoltSK: m.SK})
	if err != nil {
		return err
	}
	err = s.transact(ctx, s.putNew(marker), s.putNew(entry))
	switch {
	case cancelledAt(err, 0):
		return ErrAlreadyExists
	case err != nil:
		return fmt.Errorf("bookmark: %w", err)
	}
	return nil
}

// Unbookmark removes m from the crab's bookmarks, or returns ErrNotFound.
func (s *Store) Unbookmark(ctx context.Context, c *Crab, moltID string) error {
	var marker bookmarkMarker
	if err := s.getItem(ctx, bookmarkPK(c.ID), bookmarkSK(moltID), &marker); err != nil {
		return err
	}
	err := s.transact(ctx,
		types.TransactWriteItem{Delete: &types.Delete{
			TableName:           s.tableName(),
			Key:                 keyOf(marker.PK, marker.SK),
			ConditionExpression: aws.String("attribute_exists(PK)"),
		}},
		types.TransactWriteItem{Delete: &types.Delete{
			TableName: s.tableName(),
			Key:       keyOf(bookmarkListPK(c.ID), marker.ListSK),
		}},
	)
	switch {
	case cancelledAt(err, 0):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("unbookmark: %w", err)
	}
	return nil
}

// ToggleBookmark bookmarks m, or removes the bookmark if it's there, and
// reports whether m is bookmarked afterwards.
func (s *Store) ToggleBookmark(ctx context.Context, c *Crab, m *Molt) (bool, error) {
	err := s.Bookmark(ctx, c, m)
	if errors.Is(err, ErrAlreadyExists) {
		if err := s.Unbookmark(ctx, c, m.ID); err != nil && !errors.Is(err, ErrNotFound) {
			return true, err
		}
		return false, nil
	}
	return err == nil, err
}

// Bookmarks returns the crab's bookmarked molts, most recently bookmarked
// first. Molts deleted since are skipped; their bookmarks stay until the
// crab's account is purged.
func (s *Store) Bookmarks(ctx context.Context, crabID string, limit int) ([]Molt, error) {
	p, err := s.BookmarksPage(ctx, crabID, "", limit)
	return p.Molts, err
}

// BookmarksPage is Bookmarks starting after the cursor from a previous page.
func (s *Store) BookmarksPage(ctx context.Context, crabID, after string, limit int) (Page, error) {
	keys, next, err := s.pointerPage(ctx, bookmarkListPK(crabID), false, after, limit)
	if err != nil {
		return Page{}, fmt.Errorf("bookmarks: %w", err)
	}
	molts, err := s.MoltsByKeys(ctx, keys)
	if err != nil {
		return Page{}, fmt.Errorf("bookmarks: %w", err)
	}
	rank := make(map[string]int, len(keys))
	for i, k := range keys {
		rank[k[1]] = i
	}
	sort.SliceStable(molts, func(i, j int) bool { return rank[molts[i].SK] < rank[molts[j].SK] })
	return Page{Molts: molts, Next: next}, nil
}
