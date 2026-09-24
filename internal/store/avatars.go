package store

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/t0ul/krabber-net/internal/avatar"
)

const avatarAttempts = 24

type avatarMarker struct {
	PK     string `dynamodbav:"PK"`
	SK     string `dynamodbav:"SK"`
	CrabID string `dynamodbav:"crab_id"`
}

func (s *Store) avatarItem(code, crabID string) (map[string]types.AttributeValue, error) {
	return marshal(avatarMarker{PK: avatarPK(code), SK: avatarSK(), CrabID: crabID})
}

// EnsureAvatar gives c a unique trait code if it does not already have one.
func (s *Store) EnsureAvatar(ctx context.Context, c *Crab) (string, error) {
	if avatar.Valid(c.Avatar) {
		return c.Avatar, nil
	}
	if c.PK == "" || c.SK == "" {
		full, err := s.CrabByID(ctx, c.ID)
		if err != nil {
			return "", err
		}
		c = full
		if avatar.Valid(c.Avatar) {
			return c.Avatar, nil
		}
	}
	return s.assignAvatar(ctx, c)
}

// RerollAvatar replaces the crab's avatar with a new unique code and frees
// the old combination.
func (s *Store) RerollAvatar(ctx context.Context, c *Crab) (string, error) {
	if c.PK == "" || c.SK == "" {
		full, err := s.CrabByID(ctx, c.ID)
		if err != nil {
			return "", err
		}
		c = full
	}
	return s.assignAvatar(ctx, c)
}

func (s *Store) assignAvatar(ctx context.Context, c *Crab) (string, error) {
	old := c.Avatar
	for range avatarAttempts {
		code, err := avatar.Random()
		if err != nil {
			return "", err
		}
		if code == old {
			continue
		}
		item, err := s.avatarItem(code, c.ID)
		if err != nil {
			return "", err
		}
		update := types.TransactWriteItem{Update: &types.Update{
			TableName:           s.tableName(),
			Key:                 keyOf(c.PK, c.SK),
			UpdateExpression:    aws.String("SET avatar = :n"),
			ConditionExpression: aws.String("attribute_exists(PK)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":n": str(code),
			},
		}}
		items := []types.TransactWriteItem{s.putNew(item), update}
		if avatar.Valid(old) {
			items = append(items, types.TransactWriteItem{Delete: &types.Delete{
				TableName: s.tableName(),
				Key:       keyOf(avatarPK(old), avatarSK()),
			}})
		}
		err = s.transact(ctx, items...)
		switch {
		case cancelledAt(err, 0):
			continue
		case cancelledAt(err, 1):
			return "", ErrNotFound
		case err != nil:
			return "", fmt.Errorf("assign avatar: %w", err)
		}
		c.Avatar = code
		return code, nil
	}
	return "", fmt.Errorf("assign avatar: no free combination")
}
