package store

import (
	"context"
	"fmt"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/t0ul/krabber-net/internal/richtext"
)

// moltPointer lists a reply on its parent's thread, or a quote on the quoted
// molt's quotes page.
type moltPointer struct {
	PK     string `dynamodbav:"PK"`
	SK     string `dynamodbav:"SK"`
	MoltPK string `dynamodbav:"molt_pk"`
	MoltSK string `dynamodbav:"molt_sk"`
}

// Reply stores a reply to parent. A reply is a molt in its author's partition
// (so it's likeable, remoltable and deleted with the account) that stays out
// of the Sea and trenches.
func (s *Store) Reply(ctx context.Context, author *Crab, parent *Molt, content string) (*Molt, error) {
	if parent.Remolt || parent.Deleted || parent.Removed {
		return nil, ErrNotAllowed
	}
	id := newID()
	m := &Molt{
		PK:              moltPK(author.ID),
		SK:              replySK(id),
		GSI5PK:          moltIDKey(id),
		GSI5SK:          moltIDKey(id),
		ID:              id,
		OwnerID:         author.ID,
		AuthorID:        author.ID,
		Author:          author.UserName,
		Content:         content,
		CreatedAt:       s.now(),
		Tags:            richtext.Tags(content),
		Mentions:        richtext.Mentions(content),
		ReplyTo:         parent.ID,
		ReplyToPK:       parent.PK,
		ReplyToSK:       parent.SK,
		ReplyToAuthor:   parent.Author,
		ReplyToAuthorID: parent.AuthorID,
	}
	item, err := marshal(m)
	if err != nil {
		return nil, err
	}
	pointer, err := marshal(moltPointer{
		PK: replyPointerPK(parent.ID), SK: replyPointerSK(id), MoltPK: m.PK, MoltSK: m.SK,
	})
	if err != nil {
		return nil, err
	}
	tags, err := s.tagPointers(m)
	if err != nil {
		return nil, err
	}
	err = s.transact(ctx, append([]types.TransactWriteItem{
		s.putNew(item),
		s.putNew(pointer),
		s.addCounter(parent.PK, parent.SK, "reply_count", 1),
		s.addCounter(author.PK, author.SK, "molt_count", 1),
	}, tags...)...)
	switch {
	case cancelledAt(err, 2):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("reply: %w", err)
	}
	return m, nil
}

// Replies returns the replies on a molt, oldest first, skipping deleted ones.
func (s *Store) Replies(ctx context.Context, parentID string, limit int) ([]Molt, error) {
	replies, err := s.pointedMolts(ctx, replyPointerPK(parentID), true, limit)
	if err != nil {
		return nil, fmt.Errorf("replies: %w", err)
	}
	slices.Reverse(replies)
	return replies, nil
}

// pointedMolts loads the molts listed in a pointer partition, newest first.
// oldest picks which end of the partition a limit keeps.
func (s *Store) pointedMolts(ctx context.Context, pk string, oldest bool, limit int) ([]Molt, error) {
	pointers, err := queryAll[moltPointer](ctx, s.db, &dynamodb.QueryInput{
		TableName:                 s.tableName(),
		KeyConditionExpression:    aws.String("PK = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":pk": str(pk)},
		ScanIndexForward:          aws.Bool(oldest),
		Limit:                     pageLimit(limit),
	}, limit)
	if err != nil {
		return nil, err
	}
	keys := make([][2]string, 0, len(pointers))
	for _, p := range pointers {
		keys = append(keys, [2]string{p.MoltPK, p.MoltSK})
	}
	return s.MoltsByKeys(ctx, keys)
}
