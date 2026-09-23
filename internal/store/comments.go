package store

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// MaxCommentLength is the longest comment accepted, in characters.
const MaxCommentLength = 280

// Comment is a reply on a molt, stored in the molt's partition.
type Comment struct {
	PK        string    `dynamodbav:"PK"`
	SK        string    `dynamodbav:"SK"`
	ID        string    `dynamodbav:"id"`
	MoltID    string    `dynamodbav:"molt_id"`
	AuthorID  string    `dynamodbav:"author_id"`
	Author    string    `dynamodbav:"author"`
	Content   string    `dynamodbav:"content"`
	CreatedAt time.Time `dynamodbav:"created_at"`
}

// AddComment stores a comment and bumps the molt's counter.
func (s *Store) AddComment(ctx context.Context, author *Crab, m *Molt, content string) (*Comment, error) {
	id := newID()
	c := &Comment{
		PK:        commentPK(m.ID),
		SK:        commentSK(id),
		ID:        id,
		MoltID:    m.ID,
		AuthorID:  author.ID,
		Author:    author.UserName,
		Content:   content,
		CreatedAt: s.now(),
	}
	item, err := marshal(c)
	if err != nil {
		return nil, err
	}
	err = s.transact(ctx, s.putNew(item), s.addCounter(m.PK, m.SK, "comment_count", 1))
	switch {
	case cancelledAt(err, 1):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("add comment: %w", err)
	}
	return c, nil
}

// CommentsOn returns a molt's comments, oldest first.
func (s *Store) CommentsOn(ctx context.Context, moltID string, limit int) ([]Comment, error) {
	comments, err := queryAll[Comment](ctx, s.db, &dynamodb.QueryInput{
		TableName:                 s.tableName(),
		KeyConditionExpression:    aws.String("PK = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":pk": str(commentPK(moltID))},
		Limit:                     pageLimit(limit),
	}, limit)
	if err != nil {
		return nil, fmt.Errorf("comments on molt: %w", err)
	}
	return comments, nil
}
