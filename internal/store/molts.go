package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// MaxMoltLength is the longest molt accepted, in characters.
const MaxMoltLength = 280

// Molt is a post. A remolt is a separate molt owned by the remolting crab that
// points at the original and names its author; it doesn't copy the text, so
// deleting the original erases it everywhere.
type Molt struct {
	PK     string `dynamodbav:"PK"`
	SK     string `dynamodbav:"SK"`
	GSI3PK string `dynamodbav:"GSI3PK,omitempty"`
	GSI3SK string `dynamodbav:"GSI3SK,omitempty"`
	GSI5PK string `dynamodbav:"GSI5PK,omitempty"`
	GSI5SK string `dynamodbav:"GSI5SK,omitempty"`
	GSI8PK string `dynamodbav:"GSI8PK,omitempty"`
	GSI8SK string `dynamodbav:"GSI8SK,omitempty"`

	ID        string    `dynamodbav:"id"`
	OwnerID   string    `dynamodbav:"owner_id"` // whose timeline it's on
	AuthorID  string    `dynamodbav:"author_id"`
	Author    string    `dynamodbav:"author"` // username shown on the molt
	Content   string    `dynamodbav:"content"`
	CreatedAt time.Time `dynamodbav:"created_at"`
	Deleted   bool      `dynamodbav:"deleted"`
	Removed   bool      `dynamodbav:"removed,omitempty"` // by a moderator; keeps its index keys so it can be restored

	Remolt     bool   `dynamodbav:"remolt"`
	RemoltOf   string `dynamodbav:"remolt_of,omitempty"`
	RemoltOfPK string `dynamodbav:"remolt_of_pk,omitempty"`
	RemoltOfSK string `dynamodbav:"remolt_of_sk,omitempty"`
	RemoltedBy string `dynamodbav:"remolted_by,omitempty"`

	ReplyTo         string `dynamodbav:"reply_to,omitempty"` // parent molt ID
	ReplyToPK       string `dynamodbav:"reply_to_pk,omitempty"`
	ReplyToSK       string `dynamodbav:"reply_to_sk,omitempty"`
	ReplyToAuthor   string `dynamodbav:"reply_to_author,omitempty"`
	ReplyToAuthorID string `dynamodbav:"reply_to_author_id,omitempty"`

	ReplyCount  int `dynamodbav:"reply_count"`
	LikeCount   int `dynamodbav:"like_count"`
	RemoltCount int `dynamodbav:"remolt_count"`

	// Not stored; filled in for display.
	EntryID        string `dynamodbav:"-"` // ID of the list entry (the remolt) when showing an original
	Liked          bool   `dynamodbav:"-"` // the viewer has liked it
	RemoltedByID   string `dynamodbav:"-"`
	AuthorName     string `dynamodbav:"-"` // display names, looked up by ID
	RemoltedByName string `dynamodbav:"-"`
	ReplyToName    string `dynamodbav:"-"`
}

// DOMID is unique per list entry, even when an original and its remolt are
// both on the page.
func (m Molt) DOMID() string { //nolint:gocritic // value receiver so templates can call it on list items
	if m.EntryID != "" {
		return "molt-" + m.EntryID
	}
	return "molt-" + m.ID
}

func (s *Store) newMolt(owner *Crab, authorID, author, content string) *Molt {
	id := newID()
	now := s.now()
	return &Molt{
		PK:        moltPK(owner.ID),
		SK:        moltSK(id),
		GSI3PK:    moltDayKey(now),
		GSI3SK:    moltSK(id),
		GSI5PK:    moltIDKey(id),
		GSI5SK:    moltIDKey(id),
		GSI8PK:    queueFanout,
		GSI8SK:    id,
		ID:        id,
		OwnerID:   owner.ID,
		AuthorID:  authorID,
		Author:    author,
		Content:   content,
		CreatedAt: now,
	}
}

// CreateMolt stores a new molt and marks it pending for trench fan-out.
func (s *Store) CreateMolt(ctx context.Context, author *Crab, content string) (*Molt, error) {
	m := s.newMolt(author, author.ID, author.UserName, content)
	item, err := marshal(m)
	if err != nil {
		return nil, err
	}
	err = s.transact(ctx,
		s.putNew(item),
		s.addCounter(author.PK, author.SK, "molt_count", 1),
	)
	if err != nil {
		return nil, fmt.Errorf("create molt: %w", err)
	}
	return m, nil
}

// Remolt shares an existing molt on the remolter's timeline. Each crab can
// remolt a given molt once.
func (s *Store) Remolt(ctx context.Context, by *Crab, original *Molt) (*Molt, error) {
	if original.Remolt || original.Deleted || original.Removed {
		return nil, ErrNotAllowed
	}
	m := s.newMolt(by, original.AuthorID, original.Author, "")
	m.Remolt = true
	m.RemoltOf = original.ID
	m.RemoltOfPK, m.RemoltOfSK = original.PK, original.SK
	m.RemoltedBy = by.UserName

	item, err := marshal(m)
	if err != nil {
		return nil, err
	}
	marker, err := marshal(map[string]string{
		"PK":      remoltMarkerPK(by.ID),
		"SK":      remoltMarkerSK(original.ID),
		"molt_id": m.ID,
	})
	if err != nil {
		return nil, err
	}

	err = s.transact(ctx,
		s.putNew(marker),
		s.putNew(item),
		s.addCounter(original.PK, original.SK, "remolt_count", 1),
		s.addCounter(by.PK, by.SK, "molt_count", 1),
	)
	switch {
	case cancelledAt(err, 0):
		return nil, ErrAlreadyExists
	case err != nil:
		return nil, fmt.Errorf("remolt: %w", err)
	}
	return m, nil
}

// DeleteMolt soft-deletes a molt, reply or remolt owned by crab c and erases
// its text. It leaves the feeds that point at it: every read skips deleted
// items, and trench entries expire on their own. Deleting a remolt frees the
// crab to remolt it again; deleting a reply takes it off its parent's thread.
func (s *Store) DeleteMolt(ctx context.Context, c *Crab, m *Molt) error {
	if m.OwnerID != c.ID {
		return ErrNotAllowed
	}
	var parentPK, parentSK, counter string
	var unlink types.Delete
	switch {
	case m.Remolt && m.RemoltOfPK != "":
		parentPK, parentSK, counter = m.RemoltOfPK, m.RemoltOfSK, "remolt_count"
		unlink = types.Delete{TableName: s.tableName(), Key: keyOf(remoltMarkerPK(c.ID), remoltMarkerSK(m.RemoltOf))}
	case m.ReplyTo != "":
		parentPK, parentSK, counter = m.ReplyToPK, m.ReplyToSK, "reply_count"
		unlink = types.Delete{TableName: s.tableName(), Key: keyOf(replyPointerPK(m.ReplyTo), replyPointerSK(m.ID))}
	}
	items := []types.TransactWriteItem{
		{Update: &types.Update{
			TableName:           s.tableName(),
			Key:                 keyOf(m.PK, m.SK),
			UpdateExpression:    aws.String("SET deleted = :t REMOVE content, GSI3PK, GSI3SK, GSI5PK, GSI5SK, GSI8PK, GSI8SK"),
			ConditionExpression: aws.String("attribute_exists(PK) AND deleted = :f AND owner_id = :me"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":t": boolean(true), ":f": boolean(false), ":me": str(c.ID),
			},
		}},
		s.addCounter(c.PK, c.SK, "molt_count", -1),
	}
	if counter != "" {
		items = append(items,
			types.TransactWriteItem{Delete: &unlink},
			s.addCounter(parentPK, parentSK, counter, -1),
		)
	}
	err := s.transact(ctx, items...)
	if counter != "" && cancelledAt(err, len(items)-1) {
		// The parent went with its author's account; there's no count to fix.
		err = s.transact(ctx, items[:len(items)-1]...)
	}
	switch {
	case cancelledAt(err, 0):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("delete molt: %w", err)
	}
	return nil
}

// MoltByID returns a non-deleted molt. The ID index only finds the key; the
// item is then read from the base table so like, reply and remolt counts are
// current (GSI projections of ADD updates lag, especially on DynamoDB Local).
func (s *Store) MoltByID(ctx context.Context, id string) (*Molt, error) {
	molts, err := queryAll[Molt](ctx, s.db, &dynamodb.QueryInput{
		TableName:                 s.tableName(),
		IndexName:                 aws.String(gsiMoltByID),
		KeyConditionExpression:    aws.String("GSI5PK = :id"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":id": str(moltIDKey(id))},
	}, 1)
	if err != nil {
		return nil, fmt.Errorf("molt by id: %w", err)
	}
	if len(molts) == 0 || molts[0].Deleted {
		return nil, ErrNotFound
	}
	return s.MoltByKey(ctx, molts[0].PK, molts[0].SK)
}

// MoltForModeration is MoltByID for moderators: it also returns molts a
// moderator removed.
func (s *Store) MoltForModeration(ctx context.Context, id string) (*Molt, error) {
	molts, err := queryAll[Molt](ctx, s.db, &dynamodb.QueryInput{
		TableName:                 s.tableName(),
		IndexName:                 aws.String(gsiMoltByID),
		KeyConditionExpression:    aws.String("GSI5PK = :id"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":id": str(moltIDKey(id))},
	}, 1)
	if err != nil {
		return nil, fmt.Errorf("molt for moderation: %w", err)
	}
	if len(molts) == 0 {
		return nil, ErrNotFound
	}
	var m Molt
	if err := s.getItem(ctx, molts[0].PK, molts[0].SK, &m); err != nil {
		return nil, err
	}
	if m.Deleted {
		return nil, ErrNotFound
	}
	return &m, nil
}

// SetMoltRemoved removes a molt from view, or restores it.
func (s *Store) SetMoltRemoved(ctx context.Context, m *Molt, removed bool) error {
	_, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 s.tableName(),
		Key:                       keyOf(m.PK, m.SK),
		UpdateExpression:          aws.String("SET removed = :r"),
		ConditionExpression:       aws.String("attribute_exists(PK) AND deleted = :f"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":r": boolean(removed), ":f": boolean(false)},
	})
	switch {
	case conditionFailed(err):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("set molt removed: %w", err)
	}
	return nil
}

// MoltByKey fetches a molt by table key with a strongly consistent read, so
// counters include a write that just happened.
func (s *Store) MoltByKey(ctx context.Context, pk, sk string) (*Molt, error) {
	var m Molt
	if err := s.getItem(ctx, pk, sk, &m); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("molt by key: %w", err)
	}
	if m.Deleted || m.Removed {
		return nil, ErrNotFound
	}
	return &m, nil
}

// ResolveRemolts replaces each remolt in a list with its original, so counts
// and actions belong to the original, keeping who remolted it for display.
func (s *Store) ResolveRemolts(ctx context.Context, molts []Molt) ([]Molt, error) {
	var keys [][2]string
	for _, m := range molts {
		if m.Remolt && m.RemoltOfPK != "" {
			keys = append(keys, [2]string{m.RemoltOfPK, m.RemoltOfSK})
		}
	}
	if len(keys) == 0 {
		return molts, nil
	}
	originals, err := s.MoltsByKeys(ctx, keys)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]Molt, len(originals))
	for _, o := range originals {
		byID[o.ID] = o
	}
	out := make([]Molt, 0, len(molts))
	for _, m := range molts {
		if !m.Remolt {
			out = append(out, m)
			continue
		}
		o, ok := byID[m.RemoltOf]
		if !ok {
			continue // original deleted
		}
		o.Remolt = true
		o.RemoltedBy = m.RemoltedBy
		o.RemoltedByID = m.OwnerID
		o.EntryID = m.ID
		o.CreatedAt = m.CreatedAt // when it was remolted
		out = append(out, o)
	}
	return out, nil
}

// MoltsByOwner returns a crab's own timeline (molts and remolts, no
// replies), newest first.
func (s *Store) MoltsByOwner(ctx context.Context, crabID string, limit int) ([]Molt, error) {
	return s.ownerMolts(ctx, crabID, moltSK(""), limit)
}

// RepliesByOwner returns the replies a crab wrote, newest first.
func (s *Store) RepliesByOwner(ctx context.Context, crabID string, limit int) ([]Molt, error) {
	return s.ownerMolts(ctx, crabID, replySK(""), limit)
}

func (s *Store) ownerMolts(ctx context.Context, crabID, prefix string, limit int) ([]Molt, error) {
	molts, err := queryAll[Molt](ctx, s.db, &dynamodb.QueryInput{
		TableName:                 s.tableName(),
		KeyConditionExpression:    aws.String("PK = :pk AND begins_with(SK, :m)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":pk": str(moltPK(crabID)), ":m": str(prefix)},
		ScanIndexForward:          aws.Bool(false),
		Limit:                     pageLimit(limit),
	}, limit)
	if err != nil {
		return nil, fmt.Errorf("molts by owner: %w", err)
	}
	return withoutDeleted(molts), nil
}

// LatestMolts returns the newest molts across the last week (UTC days), newest
// first. Each day is one query on the day index; the items are then loaded
// from the base table so counters match what the thread page shows.
func (s *Store) LatestMolts(ctx context.Context, limit int) ([]Molt, error) {
	now := s.now()
	var keys [][2]string
	seen := map[string]bool{}
	for d := 0; d < 7 && len(keys) < limit; d++ {
		day := now.AddDate(0, 0, -d)
		molts, err := queryAll[Molt](ctx, s.db, &dynamodb.QueryInput{
			TableName:                 s.tableName(),
			IndexName:                 aws.String(gsiMoltsByDay),
			KeyConditionExpression:    aws.String("GSI3PK = :day"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":day": str(moltDayKey(day))},
			ScanIndexForward:          aws.Bool(false),
		}, limit-len(keys))
		if err != nil {
			return nil, fmt.Errorf("latest molts: %w", err)
		}
		for _, m := range withoutDeleted(molts) {
			if m.PK == "" || m.SK == "" || seen[m.ID] {
				continue
			}
			seen[m.ID] = true
			keys = append(keys, [2]string{m.PK, m.SK})
			if len(keys) >= limit {
				break
			}
		}
	}
	return s.MoltsByKeys(ctx, keys)
}

// MoltsByKeys batch-fetches molts by table key, newest first, skipping any
// that are missing or deleted.
func (s *Store) MoltsByKeys(ctx context.Context, keys [][2]string) ([]Molt, error) {
	var out []Molt
	for start := 0; start < len(keys); start += 100 {
		end := min(start+100, len(keys))
		var ka types.KeysAndAttributes
		for _, k := range keys[start:end] {
			ka.Keys = append(ka.Keys, keyOf(k[0], k[1]))
		}
		req := map[string]types.KeysAndAttributes{s.table: ka}

		err := retryUnprocessed(ctx, 5, func() (int, error) {
			res, err := s.db.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{RequestItems: req})
			if err != nil {
				return 0, err
			}
			var molts []Molt
			if err := attributevalue.UnmarshalListOfMaps(res.Responses[s.table], &molts); err != nil {
				return 0, err
			}
			out = append(out, molts...)
			req = res.UnprocessedKeys
			return len(req[s.table].Keys), nil
		})
		if err != nil {
			return nil, fmt.Errorf("molts by keys: %w", err)
		}
	}
	out = withoutDeleted(out)
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// PendingFanouts returns molts still waiting for trench fan-out that were
// created before cutoff (so in-flight work isn't picked up twice).
func (s *Store) PendingFanouts(ctx context.Context, cutoff time.Time, limit int) ([]Molt, error) {
	molts, err := queryAll[Molt](ctx, s.db, &dynamodb.QueryInput{
		TableName:              s.tableName(),
		IndexName:              aws.String(gsiWorkQueue),
		KeyConditionExpression: aws.String("GSI8PK = :q AND GSI8SK < :cutoff"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":q":      str(queueFanout),
			":cutoff": str(ksuidFloor(cutoff)),
		},
		Limit: pageLimit(limit),
	}, limit)
	if err != nil {
		return nil, fmt.Errorf("pending fanouts: %w", err)
	}
	return molts, nil
}

// ClearFanout removes a molt from the fan-out queue.
func (s *Store) ClearFanout(ctx context.Context, m *Molt) error {
	_, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           s.tableName(),
		Key:                 keyOf(m.PK, m.SK),
		UpdateExpression:    aws.String("REMOVE GSI8PK, GSI8SK"),
		ConditionExpression: aws.String("attribute_exists(PK)"),
	})
	if err != nil && !conditionFailed(err) {
		return fmt.Errorf("clear fanout: %w", err)
	}
	return nil
}

func withoutDeleted(molts []Molt) []Molt {
	out := molts[:0]
	for _, m := range molts {
		if !m.Deleted && !m.Removed {
			out = append(out, m)
		}
	}
	return out
}
