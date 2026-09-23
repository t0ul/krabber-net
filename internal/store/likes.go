package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// Like records that a crab liked a molt.
type Like struct {
	PK        string    `dynamodbav:"PK"`
	SK        string    `dynamodbav:"SK"`
	GSI7PK    string    `dynamodbav:"GSI7PK"`
	GSI7SK    string    `dynamodbav:"GSI7SK"`
	CrabID    string    `dynamodbav:"crab_id"`
	CrabName  string    `dynamodbav:"crab_name"`
	MoltID    string    `dynamodbav:"molt_id"`
	MoltPK    string    `dynamodbav:"molt_pk,omitempty"`
	MoltSK    string    `dynamodbav:"molt_sk,omitempty"`
	CreatedAt time.Time `dynamodbav:"created_at"`
}

// LikeMolt records a like and bumps the molt's counter. Liking twice returns
// ErrAlreadyExists.
func (s *Store) LikeMolt(ctx context.Context, c *Crab, m *Molt) error {
	item, err := marshal(Like{
		PK:        likePK(c.ID),
		SK:        likeSK(m.ID),
		GSI7PK:    likesOnKey(m.ID),
		GSI7SK:    likePK(c.ID),
		CrabID:    c.ID,
		CrabName:  c.UserName,
		MoltID:    m.ID,
		MoltPK:    m.PK,
		MoltSK:    m.SK,
		CreatedAt: s.now(),
	})
	if err != nil {
		return err
	}
	err = s.transact(ctx, s.putNew(item), s.addCounter(m.PK, m.SK, "like_count", 1))
	switch {
	case cancelledAt(err, 0):
		return ErrAlreadyExists
	case cancelledAt(err, 1):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("like molt: %w", err)
	}
	return nil
}

// UnlikeMolt removes a like and lowers the counter. Returns ErrNotFound if
// the crab hadn't liked the molt.
func (s *Store) UnlikeMolt(ctx context.Context, c *Crab, m *Molt) error {
	err := s.transact(ctx,
		types.TransactWriteItem{Delete: &types.Delete{
			TableName:           s.tableName(),
			Key:                 keyOf(likePK(c.ID), likeSK(m.ID)),
			ConditionExpression: aws.String("attribute_exists(PK)"),
		}},
		s.addCounter(m.PK, m.SK, "like_count", -1),
	)
	switch {
	case cancelledAt(err, 0):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("unlike molt: %w", err)
	}
	return nil
}

// ToggleLike likes the molt, or unlikes it if already liked, and reports
// whether it's liked afterwards.
func (s *Store) ToggleLike(ctx context.Context, c *Crab, m *Molt) (bool, error) {
	err := s.LikeMolt(ctx, c, m)
	if errors.Is(err, ErrAlreadyExists) {
		if err := s.UnlikeMolt(ctx, c, m); err != nil && !errors.Is(err, ErrNotFound) {
			return true, err
		}
		return false, nil
	}
	return err == nil, err
}

// LikedIDs reports which of the given molts the crab has liked.
func (s *Store) LikedIDs(ctx context.Context, crabID string, moltIDs []string) (map[string]bool, error) {
	out := map[string]bool{}
	for start := 0; start < len(moltIDs); start += 100 {
		end := min(start+100, len(moltIDs))
		var ka types.KeysAndAttributes
		seen := map[string]bool{}
		for _, id := range moltIDs[start:end] {
			if !seen[id] {
				seen[id] = true
				ka.Keys = append(ka.Keys, keyOf(likePK(crabID), likeSK(id)))
			}
		}
		ka.ProjectionExpression = aws.String("molt_id")
		req := map[string]types.KeysAndAttributes{s.table: ka}
		err := retryUnprocessed(ctx, 5, func() (int, error) {
			res, err := s.db.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{RequestItems: req})
			if err != nil {
				return 0, err
			}
			var likes []Like
			if err := attributevalue.UnmarshalListOfMaps(res.Responses[s.table], &likes); err != nil {
				return 0, err
			}
			for _, l := range likes {
				out[l.MoltID] = true
			}
			req = res.UnprocessedKeys
			return len(req[s.table].Keys), nil
		})
		if err != nil {
			return nil, fmt.Errorf("liked ids: %w", err)
		}
	}
	return out, nil
}

// LikesOn returns who liked a molt.
func (s *Store) LikesOn(ctx context.Context, moltID string, limit int) ([]Like, error) {
	likes, err := queryAll[Like](ctx, s.db, &dynamodb.QueryInput{
		TableName:                 s.tableName(),
		IndexName:                 aws.String(gsiLikesOnMolt),
		KeyConditionExpression:    aws.String("GSI7PK = :m"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":m": str(likesOnKey(moltID))},
	}, limit)
	if err != nil {
		return nil, fmt.Errorf("likes on molt: %w", err)
	}
	return likes, nil
}

// LikedMolts returns the molts and replies a crab liked, newest molt first
// (the like partition is sorted by molt ID, not by when the like happened).
func (s *Store) LikedMolts(ctx context.Context, crabID string, limit int) ([]Molt, error) {
	likes, err := queryAll[Like](ctx, s.db, &dynamodb.QueryInput{
		TableName:                 s.tableName(),
		KeyConditionExpression:    aws.String("PK = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":pk": str(likePK(crabID))},
		ScanIndexForward:          aws.Bool(false),
		Limit:                     pageLimit(limit),
	}, limit)
	if err != nil {
		return nil, fmt.Errorf("liked molts: %w", err)
	}
	keys := make([][2]string, 0, len(likes))
	for _, l := range likes {
		if l.MoltPK == "" { // liked before likes stored the molt's key
			if l.MoltPK, l.MoltSK, err = s.moltKey(ctx, l.MoltID); errors.Is(err, ErrNotFound) {
				continue
			} else if err != nil {
				return nil, err
			}
		}
		keys = append(keys, [2]string{l.MoltPK, l.MoltSK})
	}
	return s.MoltsByKeys(ctx, keys)
}
