package store

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// TrophyAward is one trophy in a crab's trophy case.
type TrophyAward struct {
	PK        string    `dynamodbav:"PK"`
	SK        string    `dynamodbav:"SK"`
	TrophyID  string    `dynamodbav:"trophy_id"`
	AwardedAt time.Time `dynamodbav:"awarded_at"`
}

// AwardTrophy puts a trophy in c's trophy case and counts it on the crab.
// It reports false, without error, if c already has it.
func (s *Store) AwardTrophy(ctx context.Context, c *Crab, trophyID string) (bool, error) {
	item, err := marshal(TrophyAward{PK: trophyPK(c.ID), SK: trophySK(trophyID), TrophyID: trophyID, AwardedAt: s.now()})
	if err != nil {
		return false, err
	}
	err = s.transact(ctx, s.putNew(item), s.addCounter(c.PK, c.SK, "trophies", 1))
	switch {
	case cancelledAt(err, 0):
		return false, nil
	case cancelledAt(err, 1):
		return false, ErrNotFound
	case err != nil:
		return false, fmt.Errorf("award trophy: %w", err)
	}
	return true, nil
}

// RevokeTrophy takes a trophy back (for a moderator's mistake).
func (s *Store) RevokeTrophy(ctx context.Context, c *Crab, trophyID string) error {
	err := s.transact(ctx,
		types.TransactWriteItem{Delete: &types.Delete{
			TableName:           s.tableName(),
			Key:                 keyOf(trophyPK(c.ID), trophySK(trophyID)),
			ConditionExpression: aws.String("attribute_exists(PK)"),
		}},
		s.addCounter(c.PK, c.SK, "trophies", -1),
	)
	switch {
	case cancelledAt(err, 0):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("revoke trophy: %w", err)
	}
	return nil
}

// CrabsWhere returns the crabs that can sign in and match keep, with only
// their keys, ID, username, created_at and trophy count read. It scans the
// crab index, so it's for daily jobs, not requests.
func (s *Store) CrabsWhere(ctx context.Context, keep func(Crab) bool) ([]Crab, error) {
	var out []Crab
	p := dynamodb.NewScanPaginator(s.db, &dynamodb.ScanInput{
		TableName:              s.tableName(),
		IndexName:              aws.String(gsiCrabByID),
		ReturnConsumedCapacity: types.ReturnConsumedCapacityTotal,
		ProjectionExpression:   aws.String("PK, SK, #id, #un, #ca, #act, #ban, #del, #tr"),
		ExpressionAttributeNames: map[string]string{
			"#id":  "id",
			"#un":  "user_name",
			"#ca":  "created_at",
			"#act": "activated",
			"#ban": "banned",
			"#del": "deleted",
			"#tr":  "trophies",
		},
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("scan crabs: %w", err)
		}
		var crabs []Crab
		if err := attributevalue.UnmarshalListOfMaps(page.Items, &crabs); err != nil {
			return nil, fmt.Errorf("scan crabs: %w", err)
		}
		for _, c := range crabs {
			if c.CanSignIn() && keep(c) {
				out = append(out, c)
			}
		}
		if p.HasMorePages() {
			if err := pace(ctx, page.ConsumedCapacity); err != nil {
				return nil, fmt.Errorf("scan crabs: %w", err)
			}
		}
	}
	return out, nil
}

// Trophies returns a crab's trophy case, oldest award first.
func (s *Store) Trophies(ctx context.Context, crabID string) ([]TrophyAward, error) {
	out, err := queryAll[TrophyAward](ctx, s.db, &dynamodb.QueryInput{
		TableName:                 s.tableName(),
		KeyConditionExpression:    aws.String("PK = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":pk": str(trophyPK(crabID))},
	}, 0)
	if err != nil {
		return nil, fmt.Errorf("trophies: %w", err)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].AwardedAt.Before(out[j].AwardedAt) })
	return out, nil
}
