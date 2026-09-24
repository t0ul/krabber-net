package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// Quote stores a molt that comments on quoted. A quote is an ordinary molt
// (it goes to the Sea and trenches) that also points at the quoted molt,
// whose quote_count it bumps.
func (s *Store) Quote(ctx context.Context, author *Crab, quoted *Molt, content string) (*Molt, error) {
	if quoted.Remolt || quoted.Deleted || quoted.Removed {
		return nil, ErrNotAllowed
	}
	m := s.newMolt(author, author.ID, author.UserName, content)
	m.QuoteOf = quoted.ID
	m.QuoteOfPK, m.QuoteOfSK = quoted.PK, quoted.SK
	item, err := marshal(m)
	if err != nil {
		return nil, err
	}
	pointer, err := marshal(moltPointer{
		PK: quotePointerPK(quoted.ID), SK: quotePointerSK(m.ID), MoltPK: m.PK, MoltSK: m.SK,
	})
	if err != nil {
		return nil, err
	}
	tags, err := s.tagPointers(m)
	if err != nil {
		return nil, err
	}
	err = s.transact(ctx, append([]types.TransactWriteItem{
		s.putNew(item),
		s.putNew(pointer),
		s.addCounter(quoted.PK, quoted.SK, "quote_count", 1),
		s.addCounter(author.PK, author.SK, "molt_count", 1),
	}, tags...)...)
	switch {
	case cancelledAt(err, 2):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("quote: %w", err)
	}
	return m, nil
}

// Quotes returns the quotes of a molt, newest first, skipping deleted ones.
func (s *Store) Quotes(ctx context.Context, quotedID string, limit int) ([]Molt, error) {
	p, err := s.QuotesPage(ctx, quotedID, "", limit)
	return p.Molts, err
}

// QuotesPage is Quotes starting after the cursor from a previous page.
func (s *Store) QuotesPage(ctx context.Context, quotedID, after string, limit int) (Page, error) {
	p, err := s.pointedPage(ctx, quotePointerPK(quotedID), false, after, limit)
	if err != nil {
		return Page{}, fmt.Errorf("quotes: %w", err)
	}
	return p, nil
}

// UndoRemolt deletes the crab's remolt of original. It returns the ID of the
// deleted remolt, or ErrNotFound when the crab hasn't remolted it.
func (s *Store) UndoRemolt(ctx context.Context, c *Crab, original *Molt) (string, error) {
	var marker struct {
		MoltID string `dynamodbav:"molt_id"`
	}
	if err := s.getItem(ctx, remoltMarkerPK(c.ID), remoltMarkerSK(original.ID), &marker); err != nil {
		return "", err
	}
	re, err := s.MoltByKey(ctx, moltPK(c.ID), moltSK(marker.MoltID))
	if errors.Is(err, ErrNotFound) {
		return "", ErrNotFound
	} else if err != nil {
		return "", err
	}
	if err := s.DeleteMolt(ctx, c, re); err != nil {
		return "", err
	}
	return re.ID, nil
}
