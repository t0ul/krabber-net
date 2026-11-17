package store

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// Pin shows m at the top of its author's profile, replacing any earlier pin.
// Only the author can pin a molt, and remolts can't be pinned.
func (s *Store) Pin(ctx context.Context, c *Crab, m *Molt) error {
	if m.AuthorID != c.ID || m.Remolt || m.Deleted || m.Removed {
		return ErrNotAllowed
	}
	_, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           s.tableName(),
		Key:                 keyOf(c.PK, c.SK),
		UpdateExpression:    aws.String("SET pinned_molt_id = :id, pinned_molt_pk = :pk, pinned_molt_sk = :sk"),
		ConditionExpression: aws.String("attribute_exists(PK)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":id": str(m.ID), ":pk": str(m.PK), ":sk": str(m.SK),
		},
	})
	if conditionFailed(err) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("pin: %w", err)
	}
	c.PinnedMoltID, c.PinnedMoltPK, c.PinnedMoltSK = m.ID, m.PK, m.SK
	return nil
}

// Unpin clears the crab's pin if it's still moltID, so an unpin can't undo
// a newer pin. It returns ErrNotFound when moltID isn't pinned.
func (s *Store) Unpin(ctx context.Context, c *Crab, moltID string) error {
	_, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 s.tableName(),
		Key:                       keyOf(c.PK, c.SK),
		UpdateExpression:          aws.String("REMOVE pinned_molt_id, pinned_molt_pk, pinned_molt_sk"),
		ConditionExpression:       aws.String("pinned_molt_id = :id"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":id": str(moltID)},
	})
	if conditionFailed(err) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("unpin: %w", err)
	}
	c.PinnedMoltID, c.PinnedMoltPK, c.PinnedMoltSK = "", "", ""
	return nil
}

// PinnedMolt returns the crab's pinned molt, or nil when there's none or it
// was deleted or removed since.
func (s *Store) PinnedMolt(ctx context.Context, c *Crab) (*Molt, error) {
	if c.PinnedMoltPK == "" {
		return nil, nil //nolint:nilnil // no pin is not an error
	}
	molts, err := s.MoltsByKeys(ctx, [][2]string{{c.PinnedMoltPK, c.PinnedMoltSK}})
	if err != nil || len(molts) == 0 {
		return nil, err
	}
	return &molts[0], nil
}
