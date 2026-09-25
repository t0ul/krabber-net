// Package store is the only code that talks to DynamoDB. It knows nothing about
// HTTP; handlers and background jobs call it with a request context.
package store

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/segmentio/ksuid"
)

// Errors returned by store methods. Handlers map them to user-facing messages.
var (
	ErrNotFound          = errors.New("store: not found")
	ErrDuplicateEmail    = errors.New("store: email already registered")
	ErrDuplicateUsername = errors.New("store: username already taken")
	ErrAlreadyExists     = errors.New("store: already exists")
	ErrInvalidToken      = errors.New("store: invalid or expired token")
	ErrNotAllowed        = errors.New("store: not allowed")
)

// Store wraps the DynamoDB table.
type Store struct {
	db    *dynamodb.Client
	table string
	now   func() time.Time
}

// New returns a Store for the given table.
func New(db *dynamodb.Client, table string) *Store {
	return &Store{db: db, table: table, now: func() time.Time { return time.Now().UTC() }}
}

// Table returns the table name.
func (s *Store) Table() string { return s.table }

// newID returns a KSUID whose payload starts with the sub-second nanoseconds,
// so IDs sort by creation time down to the nanosecond, not just the second.
func newID() string {
	now := time.Now()
	payload := make([]byte, 16)
	binary.BigEndian.PutUint32(payload[:4], uint32(now.Nanosecond())) //nolint:gosec // < 1e9 always fits
	if _, err := rand.Read(payload[4:]); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	id, err := ksuid.FromParts(now, payload)
	if err != nil {
		panic(fmt.Sprintf("build ksuid: %v", err))
	}
	return id.String()
}

// idTime is when a KSUID was made, to the second; ok is false for anything
// that isn't one.
func idTime(id string) (t time.Time, ok bool) {
	k, err := ksuid.Parse(id)
	if err != nil {
		return time.Time{}, false
	}
	return k.Time(), true
}

// ksuidFloor is the smallest KSUID for a moment in time. KSUID strings sort in
// time order, so comparing against it selects IDs created before t.
func ksuidFloor(t time.Time) string {
	id, err := ksuid.FromParts(t, make([]byte, 16))
	if err != nil {
		return ksuid.Nil.String()
	}
	return id.String()
}

func (s *Store) tableName() *string { return aws.String(s.table) }

// pageLimit converts a caller's limit to a DynamoDB page size.
func pageLimit(n int) *int32 {
	const maxPage = 1000
	return aws.Int32(int32(min(max(n, 1), maxPage))) //nolint:gosec // clamped to 1..1000
}

func keyOf(pk, sk string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: pk},
		"SK": &types.AttributeValueMemberS{Value: sk},
	}
}

func str(v string) *types.AttributeValueMemberS { return &types.AttributeValueMemberS{Value: v} }
func num(v int64) *types.AttributeValueMemberN {
	return &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", v)}
}
func boolean(v bool) *types.AttributeValueMemberBOOL {
	return &types.AttributeValueMemberBOOL{Value: v}
}

// addCounter returns an update that atomically adds delta to a numeric
// attribute on an existing item (ADD creates the attribute if it's missing).
func (s *Store) addCounter(pk, sk, attr string, delta int64) types.TransactWriteItem {
	return types.TransactWriteItem{
		Update: &types.Update{
			TableName:                 s.tableName(),
			Key:                       keyOf(pk, sk),
			UpdateExpression:          aws.String("ADD #c :d"),
			ConditionExpression:       aws.String("attribute_exists(PK)"),
			ExpressionAttributeNames:  map[string]string{"#c": attr},
			ExpressionAttributeValues: map[string]types.AttributeValue{":d": num(delta)},
		},
	}
}

func (s *Store) putNew(item map[string]types.AttributeValue) types.TransactWriteItem {
	return types.TransactWriteItem{
		Put: &types.Put{
			TableName:           s.tableName(),
			Item:                item,
			ConditionExpression: aws.String("attribute_not_exists(PK)"),
		},
	}
}

func (s *Store) transact(ctx context.Context, items ...types.TransactWriteItem) error {
	_, err := s.db.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items})
	return err
}

// cancelledAt reports whether a transaction failed because the condition on
// item i failed.
func cancelledAt(err error, i int) bool {
	var tce *types.TransactionCanceledException
	if !errors.As(err, &tce) || i >= len(tce.CancellationReasons) {
		return false
	}
	return aws.ToString(tce.CancellationReasons[i].Code) == "ConditionalCheckFailed"
}

func conditionFailed(err error) bool {
	var ccf *types.ConditionalCheckFailedException
	return errors.As(err, &ccf)
}

func marshal(v any) (map[string]types.AttributeValue, error) {
	item, err := attributevalue.MarshalMap(v)
	if err != nil {
		return nil, fmt.Errorf("marshal %T: %w", v, err)
	}
	return item, nil
}

func (s *Store) getItem(ctx context.Context, pk, sk string, out any) error {
	res, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      s.tableName(),
		Key:            keyOf(pk, sk),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return err
	}
	if res.Item == nil {
		return ErrNotFound
	}
	return attributevalue.UnmarshalMap(res.Item, out)
}

// queryAll runs a query across all pages, stopping once limit items have been
// collected (0 means no limit).
func queryAll[T any](ctx context.Context, db *dynamodb.Client, in *dynamodb.QueryInput, limit int) ([]T, error) {
	var out []T
	p := dynamodb.NewQueryPaginator(db, in)
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		var items []T
		if err := attributevalue.UnmarshalListOfMaps(page.Items, &items); err != nil {
			return nil, err
		}
		out = append(out, items...)
		if limit > 0 && len(out) >= limit {
			return out[:limit], nil
		}
	}
	return out, nil
}

// scanRate is the read rate (units a second) that full scans of the krab
// index keep to, under the index's read cap (PLAN.md section 4.1). A scan
// reads 1 MB (125 units) per page as fast as it can, which would trip the
// cap and be throttled; paced, the hourly directory reload of 10,000 krabs
// takes about 12 seconds.
const scanRate = 100.0

// pace waits long enough after a scan page to keep to scanRate.
func pace(ctx context.Context, used *types.ConsumedCapacity) error {
	if used == nil || used.CapacityUnits == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Duration(*used.CapacityUnits / scanRate * float64(time.Second))):
		return nil
	}
}

// retryUnprocessed retries a batch operation with exponential backoff until
// nothing is left or attempts run out.
func retryUnprocessed(ctx context.Context, attempts int, fn func() (remaining int, err error)) error {
	backoff := 50 * time.Millisecond
	for i := 0; i < attempts; i++ {
		remaining, err := fn()
		if err != nil {
			return err
		}
		if remaining == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	return errors.New("store: batch items still unprocessed after retries")
}
