package store

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// Notification types.
const (
	NotifyLike    = "like"
	NotifyRemolt  = "remolt"
	NotifyFollow  = "follow"
	NotifyComment = "comment"
)

const (
	notificationRetention = 90 * 24 * time.Hour
	notificationSnippet   = 80
)

// Notification tells a crab that someone acted on them or their molt.
type Notification struct {
	PK string `dynamodbav:"PK"`
	SK string `dynamodbav:"SK"`

	ID          string    `dynamodbav:"id"`
	RecipientID string    `dynamodbav:"recipient_id"`
	Type        string    `dynamodbav:"type"`
	ActorID     string    `dynamodbav:"actor_id"`
	Actor       string    `dynamodbav:"actor"`
	MoltID      string    `dynamodbav:"molt_id,omitempty"`
	Snippet     string    `dynamodbav:"snippet,omitempty"`
	CreatedAt   time.Time `dynamodbav:"created_at"`
	ExpiresAt   int64     `dynamodbav:"expires_at"`
}

type notificationCounter struct {
	Unread int `dynamodbav:"unread"`
}

// Snippet shortens molt text for a notification.
func Snippet(content string) string {
	if utf8.RuneCountInString(content) <= notificationSnippet {
		return content
	}
	r := []rune(content)
	return string(r[:notificationSnippet-1]) + "…"
}

// AddNotification stores n and bumps the recipient's unread count. Likes,
// remolts and follows are recorded once per actor and target, so toggling a
// like or re-following doesn't notify again. Acting on yourself is ignored.
func (s *Store) AddNotification(ctx context.Context, n Notification) error {
	if n.RecipientID == "" || n.RecipientID == n.ActorID {
		return nil
	}
	now := s.now()
	n.ID = newID()
	n.PK, n.SK = notificationPK(n.RecipientID), notificationSK(n.ID)
	n.CreatedAt = now
	n.ExpiresAt = now.Add(notificationRetention).Unix()
	item, err := marshal(n)
	if err != nil {
		return err
	}

	var items []types.TransactWriteItem
	if n.Type != NotifyComment {
		marker, err := marshal(map[string]any{
			"PK":         notificationOncePK(n.RecipientID),
			"SK":         notificationOnceSK(n.Type, n.ActorID, n.MoltID),
			"expires_at": n.ExpiresAt,
		})
		if err != nil {
			return err
		}
		items = append(items, s.putNew(marker))
	}
	items = append(items,
		types.TransactWriteItem{Put: &types.Put{TableName: s.tableName(), Item: item}},
		types.TransactWriteItem{Update: &types.Update{
			TableName:                 s.tableName(),
			Key:                       keyOf(notificationCounterPK(n.RecipientID), notificationCounterSK()),
			UpdateExpression:          aws.String("ADD unread :one"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":one": num(1)},
		}},
	)
	err = s.transact(ctx, items...)
	switch {
	case n.Type != NotifyComment && cancelledAt(err, 0):
		return nil // already notified
	case err != nil:
		return fmt.Errorf("add notification: %w", err)
	}
	return nil
}

// Notifications returns a crab's newest notifications.
func (s *Store) Notifications(ctx context.Context, crabID string, limit int) ([]Notification, error) {
	out, err := queryAll[Notification](ctx, s.db, &dynamodb.QueryInput{
		TableName:                 s.tableName(),
		KeyConditionExpression:    aws.String("PK = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":pk": str(notificationPK(crabID))},
		ScanIndexForward:          aws.Bool(false),
		Limit:                     pageLimit(limit),
	}, limit)
	if err != nil {
		return nil, fmt.Errorf("notifications: %w", err)
	}
	return out, nil
}

// UnreadNotifications returns how many notifications arrived since the crab
// last opened the notifications page.
func (s *Store) UnreadNotifications(ctx context.Context, crabID string) (int, error) {
	var c notificationCounter
	err := s.getItem(ctx, notificationCounterPK(crabID), notificationCounterSK(), &c)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return 0, fmt.Errorf("unread notifications: %w", err)
	}
	return max(c.Unread, 0), nil
}

// MarkNotificationsRead resets the unread count.
func (s *Store) MarkNotificationsRead(ctx context.Context, crabID string) error {
	_, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 s.tableName(),
		Key:                       keyOf(notificationCounterPK(crabID), notificationCounterSK()),
		UpdateExpression:          aws.String("SET unread = :zero"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":zero": num(0)},
	})
	if err != nil {
		return fmt.Errorf("mark notifications read: %w", err)
	}
	return nil
}
