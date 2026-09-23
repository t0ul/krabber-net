package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// Hit adds one to a fixed-window counter and returns the new count. Windows
// are aligned to the Unix epoch, so a 24-hour window runs midnight to midnight
// UTC. Counters delete themselves (TTL) after their window ends.
func (s *Store) Hit(ctx context.Context, action, key string, window time.Duration) (int, error) {
	start := s.now().Truncate(window)
	res, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                s.tableName(),
		Key:                      keyOf(rateLimitPK(action, key), rateLimitSK(start)),
		UpdateExpression:         aws.String("ADD #n :one SET expires_at = :exp"),
		ExpressionAttributeNames: map[string]string{"#n": "n"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":one": num(1),
			":exp": num(start.Add(window).Add(time.Hour).Unix()),
		},
		ReturnValues: types.ReturnValueUpdatedNew,
	})
	if err != nil {
		return 0, fmt.Errorf("rate limit %s: %w", action, err)
	}
	return counterValue(res.Attributes["n"])
}

// Count returns a fixed-window counter's current value without changing it.
func (s *Store) Count(ctx context.Context, action, key string, window time.Duration) (int, error) {
	start := s.now().Truncate(window)
	var it struct {
		N int `dynamodbav:"n"`
	}
	err := s.getItem(ctx, rateLimitPK(action, key), rateLimitSK(start), &it)
	switch {
	case errors.Is(err, ErrNotFound):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("rate limit %s: %w", action, err)
	}
	return it.N, nil
}

func counterValue(av types.AttributeValue) (int, error) {
	n, ok := av.(*types.AttributeValueMemberN)
	if !ok {
		return 0, errors.New("rate limit: counter missing from response")
	}
	return strconv.Atoi(n.Value)
}
