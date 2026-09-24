package store

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// trenchEntry points a follower's feed at a molt. It stores the molt's table
// key so a page of the feed is one BatchGetItem.
type trenchEntry struct {
	PK        string `dynamodbav:"PK"`
	SK        string `dynamodbav:"SK"`
	MoltPK    string `dynamodbav:"molt_pk"`
	MoltSK    string `dynamodbav:"molt_sk"`
	ExpiresAt int64  `dynamodbav:"expires_at"`
}

// AddToTrenches writes molt m into each follower's trench, 25 items per
// request. Writes are idempotent, so retrying a partly done fan-out is safe.
func (s *Store) AddToTrenches(ctx context.Context, m *Molt, followerIDs []string) error {
	expires := s.now().Add(trenchRetention).Unix()
	for start := 0; start < len(followerIDs); start += 25 {
		end := min(start+25, len(followerIDs))
		var writes []types.WriteRequest
		for _, id := range followerIDs[start:end] {
			item, err := marshal(trenchEntry{
				PK:        trenchPK(id),
				SK:        trenchSK(m.ID),
				MoltPK:    m.PK,
				MoltSK:    m.SK,
				ExpiresAt: expires,
			})
			if err != nil {
				return err
			}
			writes = append(writes, types.WriteRequest{PutRequest: &types.PutRequest{Item: item}})
		}

		req := map[string][]types.WriteRequest{s.table: writes}
		err := retryUnprocessed(ctx, 8, func() (int, error) {
			res, err := s.db.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{RequestItems: req})
			if err != nil {
				return 0, err
			}
			req = res.UnprocessedItems
			return len(req[s.table]), nil
		})
		if err != nil {
			return fmt.Errorf("add to trenches: %w", err)
		}
	}
	return nil
}

// Trench returns the newest molts from the crabs that crabID follows.
func (s *Store) Trench(ctx context.Context, crabID string, limit int) ([]Molt, error) {
	p, err := s.TrenchPage(ctx, crabID, "", limit)
	return p.Molts, err
}

// TrenchPage is Trench starting after the cursor from a previous page.
func (s *Store) TrenchPage(ctx context.Context, crabID, after string, limit int) (Page, error) {
	entries, next, err := queryPage[trenchEntry](ctx, s.db, &dynamodb.QueryInput{
		TableName:                 s.tableName(),
		KeyConditionExpression:    aws.String("PK = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":pk": str(trenchPK(crabID))},
		ScanIndexForward:          aws.Bool(false),
	}, after, limit)
	if err != nil {
		return Page{}, fmt.Errorf("trench: %w", err)
	}
	keys := make([][2]string, 0, len(entries))
	for _, e := range entries {
		keys = append(keys, [2]string{e.MoltPK, e.MoltSK})
	}
	molts, err := s.MoltsByKeys(ctx, keys)
	if err != nil {
		return Page{}, fmt.Errorf("trench: %w", err)
	}
	return Page{Molts: molts, Next: next}, nil
}

// TrenchNewer is how many trench entries are newer than since (a molt ID).
// An empty since counts from the top of the feed. The count stops at limit.
func (s *Store) TrenchNewer(ctx context.Context, crabID, since string, limit int) (int, error) {
	in := &dynamodb.QueryInput{
		TableName:                 s.tableName(),
		ExpressionAttributeValues: map[string]types.AttributeValue{":pk": str(trenchPK(crabID))},
	}
	if since != "" {
		in.KeyConditionExpression = aws.String("PK = :pk AND SK > :sk")
		in.ExpressionAttributeValues[":sk"] = str(trenchSK(since))
	} else {
		in.KeyConditionExpression = aws.String("PK = :pk")
	}
	n, err := queryCount(ctx, s.db, in, limit)
	if err != nil {
		return 0, fmt.Errorf("trench newer: %w", err)
	}
	return n, nil
}
