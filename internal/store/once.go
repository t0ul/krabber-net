package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// ClaimOnce records that the job named key has run and reports whether this
// call was the first to claim it. The record expires after ttl. Every server
// runs the same jobs (two run at once during a deploy), so a job that must
// happen once, such as a status molt, claims its key first.
func (s *Store) ClaimOnce(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	item, err := marshal(map[string]any{
		"PK":         oncePK(key),
		"SK":         onceSK(),
		"expires_at": s.now().Add(ttl).Unix(),
	})
	if err != nil {
		return false, err
	}
	_, err = s.db.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           s.tableName(),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(PK)"),
	})
	var taken *types.ConditionalCheckFailedException
	switch {
	case errors.As(err, &taken):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("claim %s: %w", key, err)
	}
	return true, nil
}
