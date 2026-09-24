package store

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// Sea returns the newest molts from everyone, with remolts resolved to their
// originals. It reads the day index directly: a query returns up to 1 MB for
// one or two read units, so a live read is both fresh and cheaper than keeping
// a cache in sync.
func (s *Store) Sea(ctx context.Context, limit int) ([]Molt, error) {
	p, err := s.SeaPage(ctx, "", limit)
	return p.Molts, err
}

// SeaPage is Sea starting after the cursor from a previous page.
func (s *Store) SeaPage(ctx context.Context, after string, limit int) (Page, error) {
	p, err := s.LatestMoltsPage(ctx, after, limit)
	if err != nil {
		return Page{}, err
	}
	molts, err := s.ResolveRemolts(ctx, p.Molts)
	if err != nil {
		return Page{}, err
	}
	return Page{Molts: molts, Next: p.Next}, nil
}

// SeaNewer is how many Sea molts are newer than since (a molt ID). An empty
// since counts from the top. The count walks the same seven-day window as
// Sea and stops at limit.
func (s *Store) SeaNewer(ctx context.Context, since string, limit int) (int, error) {
	now := s.now()
	total := 0
	for d := 0; d < 7 && total < limit; d++ {
		dayKey := moltDayKey(now.AddDate(0, 0, -d))
		in := &dynamodb.QueryInput{
			TableName: s.tableName(),
			IndexName: aws.String(gsiMoltsByDay),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":day": str(dayKey),
			},
		}
		if since != "" {
			in.KeyConditionExpression = aws.String("GSI3PK = :day AND GSI3SK > :sk")
			in.ExpressionAttributeValues[":sk"] = str(moltSK(since))
		} else {
			in.KeyConditionExpression = aws.String("GSI3PK = :day")
		}
		n, err := queryCount(ctx, s.db, in, limit-total)
		if err != nil {
			return 0, fmt.Errorf("sea newer: %w", err)
		}
		total += n
	}
	return total, nil
}
