package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// Marks is what a crab has done to a molt: liked it, remolted it (as the
// remolt with ID RemoltedAs), or bookmarked it.
type Marks struct {
	Liked      bool
	RemoltedAs string
	Bookmarked bool
}

// markSpan is the most molt time one range of marker queries covers. A query
// bills for every marker in its range, including markers on molts that
// aren't on the page, so a page whose molts are far apart is split.
const markSpan = 6 * time.Hour

// markKind is one of the crab's marker partitions.
type markKind struct {
	pk, prefix string
	set        func(*Marks, string) // the marker's molt_id
}

func markKinds(crabID string) []markKind {
	return []markKind{
		{likePK(crabID), likeSK(""), func(m *Marks, _ string) { m.Liked = true }},
		{remoltMarkerPK(crabID), remoltMarkerSK(""), func(m *Marks, remolt string) { m.RemoltedAs = remolt }},
		{bookmarkPK(crabID), bookmarkSK(""), func(m *Marks, _ string) { m.Bookmarked = true }},
	}
}

type markItem struct {
	PK     string `dynamodbav:"PK"`
	SK     string `dynamodbav:"SK"`
	MoltID string `dynamodbav:"molt_id"`
}

// MarksOn reports the crab's marks on each of moltIDs; molts with no marks
// are absent. A missing item costs as much to look up as a found one, so
// looking up three markers per molt bills three reads per molt even though
// most don't exist. Molt IDs sort by time, and a page's molts are usually
// close together, so each run of molts within markSpan is read with one
// range query per marker partition instead. A molt with no neighbours is
// looked up by key.
func (s *Store) MarksOn(ctx context.Context, crabID string, moltIDs []string) (map[string]Marks, error) {
	want := map[string]bool{}
	ids := make([]string, 0, len(moltIDs))
	for _, id := range moltIDs {
		if !want[id] {
			want[id] = true
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	out := map[string]Marks{}
	var lone []string
	for start := 0; start < len(ids); {
		end := start + 1
		if first, ok := idTime(ids[start]); ok {
			for end < len(ids) {
				t, ok := idTime(ids[end])
				if !ok || t.Sub(first) > markSpan {
					break
				}
				end++
			}
		}
		if end-start == 1 {
			lone = append(lone, ids[start])
		} else if err := s.marksInRange(ctx, crabID, ids[start], ids[end-1], want, out); err != nil {
			return nil, err
		}
		start = end
	}
	if err := s.marksByKey(ctx, crabID, lone, out); err != nil {
		return nil, err
	}
	return out, nil
}

// marksInRange queries the three marker partitions between two molt IDs, in
// parallel, keeping the markers on molts in want.
func (s *Store) marksInRange(ctx context.Context, crabID, lo, hi string, want map[string]bool, out map[string]Marks) error {
	kinds := markKinds(crabID)
	found := make([][]markItem, len(kinds))
	errs := make([]error, len(kinds))
	var wg sync.WaitGroup
	for i, k := range kinds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			found[i], errs[i] = queryAll[markItem](ctx, s.db, &dynamodb.QueryInput{
				TableName:              s.tableName(),
				KeyConditionExpression: aws.String("PK = :pk AND SK BETWEEN :lo AND :hi"),
				ExpressionAttributeValues: map[string]types.AttributeValue{
					":pk": str(k.pk), ":lo": str(k.prefix + lo), ":hi": str(k.prefix + hi),
				},
				ProjectionExpression: aws.String("PK, SK, molt_id"),
			}, 0)
		}()
	}
	wg.Wait()
	for i, k := range kinds {
		if errs[i] != nil {
			return fmt.Errorf("marks on molts: %w", errs[i])
		}
		for _, it := range found[i] {
			id := strings.TrimPrefix(it.SK, k.prefix)
			if !want[id] {
				continue
			}
			mk := out[id]
			k.set(&mk, it.MoltID)
			out[id] = mk
		}
	}
	return nil
}

// marksByKey batch-reads the three markers of each molt.
func (s *Store) marksByKey(ctx context.Context, crabID string, moltIDs []string, out map[string]Marks) error {
	kinds := markKinds(crabID)
	var keys []map[string]types.AttributeValue
	for _, id := range moltIDs {
		for _, k := range kinds {
			keys = append(keys, keyOf(k.pk, k.prefix+id))
		}
	}
	for start := 0; start < len(keys); start += 100 {
		req := map[string]types.KeysAndAttributes{s.table: {
			Keys:                 keys[start:min(start+100, len(keys))],
			ProjectionExpression: aws.String("PK, SK, molt_id"),
		}}
		err := retryUnprocessed(ctx, 5, func() (int, error) {
			res, err := s.db.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{RequestItems: req})
			if err != nil {
				return 0, err
			}
			var items []markItem
			if err := attributevalue.UnmarshalListOfMaps(res.Responses[s.table], &items); err != nil {
				return 0, err
			}
			for _, it := range items {
				for _, k := range kinds {
					if it.PK == k.pk {
						id := strings.TrimPrefix(it.SK, k.prefix)
						mk := out[id]
						k.set(&mk, it.MoltID)
						out[id] = mk
					}
				}
			}
			req = res.UnprocessedKeys
			return len(req[s.table].Keys), nil
		})
		if err != nil {
			return fmt.Errorf("marks on molts: %w", err)
		}
	}
	return nil
}
