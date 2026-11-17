package store

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// ModAction is one entry in the moderation log.
type ModAction struct {
	PK          string    `dynamodbav:"PK"`
	SK          string    `dynamodbav:"SK"`
	ModeratorID string    `dynamodbav:"moderator_id"`
	Moderator   string    `dynamodbav:"moderator"`
	Action      string    `dynamodbav:"action"`
	CrabID      string    `dynamodbav:"crab_id,omitempty"`
	Crab        string    `dynamodbav:"crab,omitempty"`
	MoltID      string    `dynamodbav:"molt_id,omitempty"`
	Note        string    `dynamodbav:"note,omitempty"` // ban reason, warning text, cleared value
	CreatedAt   time.Time `dynamodbav:"created_at"`
}

// LogModAction appends an entry to this month's moderation log.
func (s *Store) LogModAction(ctx context.Context, a ModAction) error {
	a.CreatedAt = s.now()
	a.PK, a.SK = modLogPK(a.CreatedAt), modLogSK(newID())
	item, err := marshal(a)
	if err != nil {
		return err
	}
	if _, err := s.db.PutItem(ctx, &dynamodb.PutItemInput{TableName: s.tableName(), Item: item}); err != nil {
		return fmt.Errorf("log mod action: %w", err)
	}
	return nil
}

// ModLog returns this month's and last month's moderation log, newest first.
func (s *Store) ModLog(ctx context.Context, limit int) ([]ModAction, error) {
	now := s.now()
	var out []ModAction
	for _, month := range []time.Time{now, now.AddDate(0, 0, -now.Day())} {
		rows, err := queryAll[ModAction](ctx, s.db, &dynamodb.QueryInput{
			TableName:                 s.tableName(),
			KeyConditionExpression:    aws.String("PK = :pk"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":pk": str(modLogPK(month))},
			ScanIndexForward:          aws.Bool(false),
			Limit:                     pageLimit(limit),
		}, limit)
		if err != nil {
			return nil, fmt.Errorf("mod log: %w", err)
		}
		out = append(out, rows...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].SK > out[j].SK })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
