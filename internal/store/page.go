package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// Page is one screen of molts and the cursor for the next, if any. Next is
// opaque: handlers pass it back as ?after=.
type Page struct {
	Molts []Molt
	Next  string
}

// queryPage runs one Query of up to limit items, starting after the cursor.
func queryPage[T any](ctx context.Context, db *dynamodb.Client, in *dynamodb.QueryInput, after string, limit int) ([]T, string, error) {
	if after != "" {
		start, err := decodeCursor(after)
		if err != nil {
			return nil, "", err
		}
		in.ExclusiveStartKey = start
	}
	in.Limit = pageLimit(limit)
	res, err := db.Query(ctx, in)
	if err != nil {
		return nil, "", err
	}
	var items []T
	if err := attributevalue.UnmarshalListOfMaps(res.Items, &items); err != nil {
		return nil, "", err
	}
	return items, encodeCursor(res.LastEvaluatedKey), nil
}

func encodeCursor(key map[string]types.AttributeValue) string {
	if len(key) == 0 {
		return ""
	}
	m := make(map[string]string, len(key))
	for k, v := range key {
		s, ok := v.(*types.AttributeValueMemberS)
		if !ok {
			return ""
		}
		m[k] = s.Value
	}
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string) (map[string]types.AttributeValue, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("%w: cursor", ErrNotFound)
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil || len(m) == 0 {
		return nil, fmt.Errorf("%w: cursor", ErrNotFound)
	}
	key := make(map[string]types.AttributeValue, len(m))
	for k, v := range m {
		key[k] = str(v)
	}
	return key, nil
}

func queryCount(ctx context.Context, db *dynamodb.Client, in *dynamodb.QueryInput, limit int) (int, error) {
	in.Select = types.SelectCount
	in.Limit = pageLimit(limit)
	res, err := db.Query(ctx, in)
	if err != nil {
		return 0, err
	}
	return int(res.Count), nil
}

func (s *Store) queryMolts(ctx context.Context, in *dynamodb.QueryInput, after string, limit int) (Page, error) {
	molts, next, err := queryPage[Molt](ctx, s.db, in, after, limit)
	if err != nil {
		return Page{}, err
	}
	return Page{Molts: withoutDeleted(molts), Next: next}, nil
}

func (s *Store) pointedPage(ctx context.Context, pk string, oldest bool, after string, limit int) (Page, error) {
	keys, next, err := s.pointerPage(ctx, pk, oldest, after, limit)
	if err != nil {
		return Page{}, err
	}
	molts, err := s.MoltsByKeys(ctx, keys)
	if err != nil {
		return Page{}, err
	}
	return Page{Molts: molts, Next: next}, nil
}

func (s *Store) pointerPage(ctx context.Context, pk string, oldest bool, after string, limit int) ([][2]string, string, error) {
	pointers, next, err := queryPage[moltPointer](ctx, s.db, &dynamodb.QueryInput{
		TableName:                 s.tableName(),
		KeyConditionExpression:    aws.String("PK = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":pk": str(pk)},
		ScanIndexForward:          aws.Bool(oldest),
	}, after, limit)
	if err != nil {
		return nil, "", err
	}
	keys := make([][2]string, 0, len(pointers))
	for _, p := range pointers {
		keys = append(keys, [2]string{p.MoltPK, p.MoltSK})
	}
	return keys, next, nil
}
