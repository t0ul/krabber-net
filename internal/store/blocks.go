package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// ErrBlocked means one of the two crabs has blocked the other.
var ErrBlocked = errors.New("store: blocked")

// blockRow is one side of a block. A block writes an outgoing row in the
// blocker's partition and an incoming row in the blocked crab's, so one query
// tells a crab everyone it must not see or be seen by.
type blockRow struct {
	PK        string    `dynamodbav:"PK"`
	SK        string    `dynamodbav:"SK"`
	OtherID   string    `dynamodbav:"other_id"`
	OtherName string    `dynamodbav:"other_name"`
	CreatedAt time.Time `dynamodbav:"created_at"`
}

// Blocks is everything one crab's blocks hide.
type Blocks struct {
	Blocking  map[string]string // crabs this crab blocked: ID to username
	BlockedBy map[string]bool   // crabs that blocked this crab
}

// Hides reports whether a crab is hidden in either direction.
func (b Blocks) Hides(crabID string) bool {
	_, blocking := b.Blocking[crabID]
	return blocking || b.BlockedBy[crabID]
}

// Block makes blocker block blocked and removes follows in both directions.
func (s *Store) Block(ctx context.Context, blocker, blocked *Crab) error {
	if blocker.ID == blocked.ID {
		return ErrNotAllowed
	}
	now := s.now()
	out, err := marshal(blockRow{PK: blockPK(blocker.ID), SK: blockOutSK(blocked.ID), OtherID: blocked.ID, OtherName: blocked.UserName, CreatedAt: now})
	if err != nil {
		return err
	}
	in, err := marshal(blockRow{PK: blockPK(blocked.ID), SK: blockInSK(blocker.ID), OtherID: blocker.ID, OtherName: blocker.UserName, CreatedAt: now})
	if err != nil {
		return err
	}
	err = s.transact(ctx,
		s.putNew(out),
		types.TransactWriteItem{Put: &types.Put{TableName: s.tableName(), Item: in}},
		s.addCounter(blocker.PK, blocker.SK, "block_links", 1),
		s.addCounter(blocked.PK, blocked.SK, "block_links", 1),
	)
	switch {
	case cancelledAt(err, 0):
		return ErrAlreadyExists
	case err != nil:
		return fmt.Errorf("block: %w", err)
	}
	for _, pair := range [][2]*Crab{{blocker, blocked}, {blocked, blocker}} {
		if err := s.Unfollow(ctx, pair[0], pair[1]); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	return nil
}

// Unblock removes blocker's block on blocked.
func (s *Store) Unblock(ctx context.Context, blocker, blocked *Crab) error {
	err := s.transact(ctx,
		types.TransactWriteItem{Delete: &types.Delete{
			TableName:           s.tableName(),
			Key:                 keyOf(blockPK(blocker.ID), blockOutSK(blocked.ID)),
			ConditionExpression: aws.String("attribute_exists(PK)"),
		}},
		types.TransactWriteItem{Delete: &types.Delete{
			TableName: s.tableName(),
			Key:       keyOf(blockPK(blocked.ID), blockInSK(blocker.ID)),
		}},
		s.addCounter(blocker.PK, blocker.SK, "block_links", -1),
		s.addCounter(blocked.PK, blocked.SK, "block_links", -1),
	)
	switch {
	case cancelledAt(err, 0):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("unblock: %w", err)
	}
	return nil
}

// BlocksOf loads a crab's blocks. Callers skip it when c.BlockLinks is 0.
func (s *Store) BlocksOf(ctx context.Context, crabID string) (Blocks, error) {
	rows, err := queryAll[blockRow](ctx, s.db, &dynamodb.QueryInput{
		TableName:                 s.tableName(),
		KeyConditionExpression:    aws.String("PK = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":pk": str(blockPK(crabID))},
	}, 0)
	if err != nil {
		return Blocks{}, fmt.Errorf("blocks: %w", err)
	}
	b := Blocks{Blocking: map[string]string{}, BlockedBy: map[string]bool{}}
	for _, r := range rows {
		if strings.HasPrefix(r.SK, blockOutSK("")) {
			b.Blocking[r.OtherID] = r.OtherName
		} else {
			b.BlockedBy[r.OtherID] = true
		}
	}
	return b, nil
}

// notBlocked is a transaction check that neither crab has blocked the other.
func (s *Store) notBlocked(aID, bID string) []types.TransactWriteItem {
	check := func(sk string) types.TransactWriteItem {
		return types.TransactWriteItem{ConditionCheck: &types.ConditionCheck{
			TableName:           s.tableName(),
			Key:                 keyOf(blockPK(aID), sk),
			ConditionExpression: aws.String("attribute_not_exists(PK)"),
		}}
	}
	return []types.TransactWriteItem{check(blockOutSK(bID)), check(blockInSK(bID))}
}
