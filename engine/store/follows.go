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

// Follow is one follower relationship. Both usernames are stored so follower
// and following lists render without looking up each crab.
type Follow struct {
	PK           string    `dynamodbav:"PK"`
	SK           string    `dynamodbav:"SK"`
	GSI6PK       string    `dynamodbav:"GSI6PK"`
	GSI6SK       string    `dynamodbav:"GSI6SK"`
	FollowerID   string    `dynamodbav:"follower_id"`
	FollowerName string    `dynamodbav:"follower_name"`
	FolloweeID   string    `dynamodbav:"followee_id"`
	FolloweeName string    `dynamodbav:"followee_name"`
	CreatedAt    time.Time `dynamodbav:"created_at"`
}

// Follow makes follower follow followee and updates both counters.
func (s *Store) Follow(ctx context.Context, follower, followee *Crab) error {
	if follower.ID == followee.ID {
		return ErrNotAllowed
	}
	item, err := marshal(Follow{
		PK:           followPK(follower.ID),
		SK:           followSK(followee.ID),
		GSI6PK:       followersKey(followee.ID),
		GSI6SK:       followPK(follower.ID),
		FollowerID:   follower.ID,
		FollowerName: follower.UserName,
		FolloweeID:   followee.ID,
		FolloweeName: followee.UserName,
		CreatedAt:    s.now(),
	})
	if err != nil {
		return err
	}
	items := append([]types.TransactWriteItem{
		s.putNew(item),
		s.addCounter(follower.PK, follower.SK, "following_count", 1),
		s.addCounter(followee.PK, followee.SK, "follower_count", 1),
	}, s.notBlocked(follower.ID, followee.ID)...)
	err = s.transact(ctx, items...)
	switch {
	case cancelledAt(err, 0):
		return ErrAlreadyExists
	case cancelledAt(err, 3), cancelledAt(err, 4):
		return ErrBlocked
	case err != nil:
		return fmt.Errorf("follow: %w", err)
	}
	return nil
}

// Unfollow removes the relationship and updates both counters.
func (s *Store) Unfollow(ctx context.Context, follower, followee *Crab) error {
	err := s.transact(ctx,
		types.TransactWriteItem{Delete: &types.Delete{
			TableName:           s.tableName(),
			Key:                 keyOf(followPK(follower.ID), followSK(followee.ID)),
			ConditionExpression: aws.String("attribute_exists(PK)"),
		}},
		s.addCounter(follower.PK, follower.SK, "following_count", -1),
		s.addCounter(followee.PK, followee.SK, "follower_count", -1),
	)
	switch {
	case cancelledAt(err, 0):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("unfollow: %w", err)
	}
	return nil
}

// IsFollowing reports whether follower follows followee.
func (s *Store) IsFollowing(ctx context.Context, followerID, followeeID string) (bool, error) {
	var f Follow
	err := s.getItem(ctx, followPK(followerID), followSK(followeeID), &f)
	switch {
	case errors.Is(err, ErrNotFound):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("is following: %w", err)
	}
	return true, nil
}

// FollowingIDs returns the set of crab IDs that crabID follows.
func (s *Store) FollowingIDs(ctx context.Context, crabID string) (map[string]bool, error) {
	rows, err := queryAll[Follow](ctx, s.db, &dynamodb.QueryInput{
		TableName:                 s.tableName(),
		KeyConditionExpression:    aws.String("PK = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":pk": str(followPK(crabID))},
		ProjectionExpression:      aws.String("followee_id"),
	}, 0)
	if err != nil {
		return nil, fmt.Errorf("following ids: %w", err)
	}
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		out[r.FolloweeID] = true
	}
	return out, nil
}

// Following lists the crabs that crabID follows.
func (s *Store) Following(ctx context.Context, crabID string, limit int) ([]Follow, error) {
	rows, err := queryAll[Follow](ctx, s.db, &dynamodb.QueryInput{
		TableName:                 s.tableName(),
		KeyConditionExpression:    aws.String("PK = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":pk": str(followPK(crabID))},
	}, limit)
	if err != nil {
		return nil, fmt.Errorf("following: %w", err)
	}
	return rows, nil
}

// Followers lists the crabs that follow crabID.
func (s *Store) Followers(ctx context.Context, crabID string, limit int) ([]Follow, error) {
	rows, err := queryAll[Follow](ctx, s.db, &dynamodb.QueryInput{
		TableName:                 s.tableName(),
		IndexName:                 aws.String(gsiFollowers),
		KeyConditionExpression:    aws.String("GSI6PK = :f"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":f": str(followersKey(crabID))},
	}, limit)
	if err != nil {
		return nil, fmt.Errorf("followers: %w", err)
	}
	return rows, nil
}

// EachFollowerPage calls fn with the IDs of crabID's followers, one page at a
// time, so fan-out never holds every follower in memory.
func (s *Store) EachFollowerPage(ctx context.Context, crabID string, fn func(ids []string) error) error {
	p := dynamodb.NewQueryPaginator(s.db, &dynamodb.QueryInput{
		TableName:                 s.tableName(),
		IndexName:                 aws.String(gsiFollowers),
		KeyConditionExpression:    aws.String("GSI6PK = :f"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":f": str(followersKey(crabID))},
		ProjectionExpression:      aws.String("follower_id"),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("followers: %w", err)
		}
		var rows []Follow
		if err := attributevalue.UnmarshalListOfMaps(page.Items, &rows); err != nil {
			return fmt.Errorf("followers: %w", err)
		}
		ids := make([]string, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.FollowerID)
		}
		if len(ids) > 0 {
			if err := fn(ids); err != nil {
				return err
			}
		}
	}
	return nil
}
