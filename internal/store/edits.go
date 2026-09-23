package store

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/t0ul/krabber-net/internal/richtext"
)

// EditWindow is how long after posting a molt's author can still edit it.
const EditWindow = 5 * time.Minute

// Editable reports whether the molt can still be edited at now.
func (m *Molt) Editable(now time.Time) bool {
	return !m.Remolt && !m.Deleted && !m.Removed && now.Sub(m.CreatedAt) < EditWindow
}

// EditMolt replaces the text of c's molt m, re-reading its crabtags and
// mentions and marking it edited. It fails with ErrNotAllowed when m isn't
// c's or is past EditWindow, and ErrNotFound when m was deleted or changed
// since it was read.
func (s *Store) EditMolt(ctx context.Context, c *Crab, m *Molt, content string) (*Molt, error) {
	if m.AuthorID != c.ID || !m.Editable(s.now()) {
		return nil, ErrNotAllowed
	}
	edited := *m
	edited.Content = content
	edited.Tags = richtext.Tags(content)
	edited.Mentions = richtext.Mentions(content)
	edited.Edited = true

	set := "SET content = :c, edited = :t"
	var remove []string
	values := map[string]types.AttributeValue{
		":c": str(content), ":old": str(m.Content), ":t": boolean(true), ":f": boolean(false),
	}
	for attr, list := range map[string][]string{"tags": edited.Tags, "mentions": edited.Mentions} {
		if len(list) == 0 {
			remove = append(remove, attr)
			continue
		}
		values[":"+attr] = stringList(list)
		set += ", " + attr + " = :" + attr
	}
	expr := set
	if len(remove) > 0 {
		slices.Sort(remove)
		expr += " REMOVE " + remove[0]
		for _, attr := range remove[1:] {
			expr += ", " + attr
		}
	}
	items := []types.TransactWriteItem{{Update: &types.Update{
		TableName:                 s.tableName(),
		Key:                       keyOf(m.PK, m.SK),
		UpdateExpression:          aws.String(expr),
		ConditionExpression:       aws.String("content = :old AND deleted = :f AND (attribute_not_exists(removed) OR removed = :f)"),
		ExpressionAttributeValues: values,
	}}}
	for _, tag := range m.Tags {
		if !slices.Contains(edited.Tags, tag) {
			items = append(items, types.TransactWriteItem{Delete: &types.Delete{
				TableName: s.tableName(), Key: keyOf(tagPK(tag), tagSK(m.ID)),
			}})
		}
	}
	added := edited
	added.Tags = nil
	for _, tag := range edited.Tags {
		if !slices.Contains(m.Tags, tag) {
			added.Tags = append(added.Tags, tag)
		}
	}
	puts, err := s.tagPointers(&added)
	if err != nil {
		return nil, err
	}
	err = s.transact(ctx, append(items, puts...)...)
	switch {
	case cancelledAt(err, 0):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("edit molt: %w", err)
	}
	return &edited, nil
}

// stringList is list as a DynamoDB list, the way attributevalue stores a
// []string field.
func stringList(list []string) *types.AttributeValueMemberL {
	items := make([]types.AttributeValue, 0, len(list))
	for _, v := range list {
		items = append(items, str(v))
	}
	return &types.AttributeValueMemberL{Value: items}
}
