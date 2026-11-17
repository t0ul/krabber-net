package store

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const (
	linkCardTTL   = 30 * 24 * time.Hour
	linkCardRetry = 24 * time.Hour // a failed fetch is tried again after this
)

// LinkCard is the text preview of a web page, cached per URL. A failed card
// records that the page had nothing to show, so it isn't fetched again soon.
type LinkCard struct {
	PK          string `dynamodbav:"PK"`
	SK          string `dynamodbav:"SK"`
	URL         string `dynamodbav:"url"`
	Title       string `dynamodbav:"title,omitempty"`
	Description string `dynamodbav:"description,omitempty"`
	Host        string `dynamodbav:"host,omitempty"`
	Failed      bool   `dynamodbav:"failed,omitempty"`
	ExpiresAt   int64  `dynamodbav:"expires_at"`
}

// PutLinkCard caches a card (or a failure) for its URL.
func (s *Store) PutLinkCard(ctx context.Context, c LinkCard) error {
	c.PK, c.SK = linkCardPK(c.URL), linkCardSK()
	ttl := linkCardTTL
	if c.Failed {
		c.Title, c.Description, c.Host, ttl = "", "", "", linkCardRetry
	}
	c.ExpiresAt = s.now().Add(ttl).Unix()
	item, err := marshal(c)
	if err != nil {
		return err
	}
	if _, err := s.db.PutItem(ctx, &dynamodb.PutItemInput{TableName: s.tableName(), Item: item}); err != nil {
		return fmt.Errorf("put link card: %w", err)
	}
	return nil
}

// LinkCards returns the cached cards (including failures) for urls, by URL.
// URLs with nothing cached, or only an expired entry, are missing.
func (s *Store) LinkCards(ctx context.Context, urls []string) (map[string]LinkCard, error) {
	out := make(map[string]LinkCard, len(urls))
	seen := map[string]bool{}
	var keys []map[string]types.AttributeValue
	for _, u := range urls {
		if u != "" && !seen[u] {
			seen[u] = true
			keys = append(keys, keyOf(linkCardPK(u), linkCardSK()))
		}
	}
	now := s.now().Unix()
	for start := 0; start < len(keys); start += 100 {
		req := map[string]types.KeysAndAttributes{s.table: {Keys: keys[start:min(start+100, len(keys))]}}
		err := retryUnprocessed(ctx, 5, func() (int, error) {
			res, err := s.db.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{RequestItems: req})
			if err != nil {
				return 0, err
			}
			var cards []LinkCard
			if err := attributevalue.UnmarshalListOfMaps(res.Responses[s.table], &cards); err != nil {
				return 0, err
			}
			for _, c := range cards {
				if c.ExpiresAt > now {
					out[c.URL] = c
				}
			}
			req = res.UnprocessedKeys
			return len(req[s.table].Keys), nil
		})
		if err != nil {
			return nil, fmt.Errorf("link cards: %w", err)
		}
	}
	return out, nil
}
