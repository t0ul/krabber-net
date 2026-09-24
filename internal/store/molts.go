package store

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/t0ul/krabber-net/internal/richtext"
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
	Edited    bool      `dynamodbav:"edited,omitempty"`
	NSFW      bool      `dynamodbav:"nsfw,omitempty"` // set by the author or a moderator; hidden until the viewer reveals it or opts in

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

	Tags     []string `dynamodbav:"tags,omitempty"`     // lowercase crabtags, each with a TG# pointer
	Mentions []string `dynamodbav:"mentions,omitempty"` // lowercase usernames

	QuoteOf   string `dynamodbav:"quote_of,omitempty"` // quoted molt ID
	QuoteOfPK string `dynamodbav:"quote_of_pk,omitempty"`
	QuoteOfSK string `dynamodbav:"quote_of_sk,omitempty"`

	ReplyCount  int `dynamodbav:"reply_count"`
	LikeCount   int `dynamodbav:"like_count"`
	RemoltCount int `dynamodbav:"remolt_count"`
	QuoteCount  int `dynamodbav:"quote_count"`

	// Not stored; filled in for display.
	EntryID        string        `dynamodbav:"-"` // ID of the list entry (the remolt) when showing an original
	Liked          bool          `dynamodbav:"-"` // the viewer has liked it
	RemoltedAs     string        `dynamodbav:"-"` // ID of the viewer's remolt of it, if any
	Bookmarked     bool          `dynamodbav:"-"` // the viewer has bookmarked it
	Quoted         *Molt         `dynamodbav:"-"` // the quoted molt, or nil when it's gone
	ContentHTML    template.HTML `dynamodbav:"-"` // Content with mentions and crabtags linked
	RemoltedByID   string        `dynamodbav:"-"`
	AuthorName     string        `dynamodbav:"-"` // display names, looked up by ID
	AuthorAvatar   string        `dynamodbav:"-"` // generated-crab code, from the directory
	AuthorVerified bool          `dynamodbav:"-"`
	RemoltedByName string        `dynamodbav:"-"`
	ReplyToName    string        `dynamodbav:"-"`
	Veiled         bool          `dynamodbav:"-"` // NSFW and the viewer hasn't opted in, so the text waits behind a click
	Card           *LinkCard     `dynamodbav:"-"` // the first link's card, once fetched
	YouTube        string        `dynamodbav:"-"` // video ID of the first YouTube link, for the click-to-load player
}

// MoltOption sets something extra on a molt, reply or quote as it's written.
type MoltOption func(*Molt)

// WithNSFW labels the new molt NSFW.
func WithNSFW(on bool) MoltOption { return func(m *Molt) { m.NSFW = on } }

func applyOptions(m *Molt, opts []MoltOption) {
	for _, o := range opts {
		o(m)
	}
}

// DOMID is unique per list entry, even when an original and its remolt are
// both on the page.
func (m Molt) DOMID() string { //nolint:gocritic // value receiver so templates can call it on list items
	if m.EntryID != "" {
		return "molt-" + m.EntryID
	}
	return "molt-" + m.ID
}

// FeedID is the list entry's ID: the remolt's when showing an original, so
// "newer than this" uses the remolt's place in the Sea and Trench.
func (m Molt) FeedID() string { //nolint:gocritic // value receiver so templates can call it on list items
	if m.EntryID != "" {
		return m.EntryID
	}
	return m.ID
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
		Tags:      richtext.Tags(content),
		Mentions:  richtext.Mentions(content),
	}
}

// CreateMolt stores a new molt and marks it pending for trench fan-out.
func (s *Store) CreateMolt(ctx context.Context, author *Crab, content string, opts ...MoltOption) (*Molt, error) {
	m := s.newMolt(author, author.ID, author.UserName, content)
	applyOptions(m, opts)
	item, err := marshal(m)
	if err != nil {
		return nil, err
	}
	tags, err := s.tagPointers(m)
	if err != nil {
		return nil, err
	}
	err = s.transact(ctx, append([]types.TransactWriteItem{
		s.putNew(item),
		s.addCounter(author.PK, author.SK, "molt_count", 1),
	}, tags...)...)
	if err != nil {
		return nil, fmt.Errorf("create molt: %w", err)
	}
	return m, nil
}

// tagPointers lists m on each of its crabtags' pages.
func (s *Store) tagPointers(m *Molt) ([]types.TransactWriteItem, error) {
	items := make([]types.TransactWriteItem, 0, len(m.Tags))
	for _, tag := range m.Tags {
		item, err := marshal(moltPointer{PK: tagPK(tag), SK: tagSK(m.ID), MoltPK: m.PK, MoltSK: m.SK})
		if err != nil {
			return nil, err
		}
		items = append(items, types.TransactWriteItem{Put: &types.Put{TableName: s.tableName(), Item: item}})
	}
	return items, nil
}

// tagKeys are the keys of m's crabtag pointers.
func tagKeys(m *Molt) [][2]string {
	keys := make([][2]string, 0, len(m.Tags))
	for _, tag := range m.Tags {
		keys = append(keys, [2]string{tagPK(tag), tagSK(m.ID)})
	}
	return keys
}

// MoltsWithTag returns the molts using a crabtag, newest first.
func (s *Store) MoltsWithTag(ctx context.Context, tag string, limit int) ([]Molt, error) {
	p, err := s.MoltsWithTagPage(ctx, tag, "", limit)
	return p.Molts, err
}

// MoltsWithTagPage is MoltsWithTag starting after the cursor from a previous page.
func (s *Store) MoltsWithTagPage(ctx context.Context, tag, after string, limit int) (Page, error) {
	p, err := s.pointedPage(ctx, tagPK(tag), false, after, limit)
	if err != nil {
		return Page{}, fmt.Errorf("molts with tag: %w", err)
	}
	return p, nil
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

// DeleteMolt soft-deletes a molt, reply, quote or remolt owned by crab c and
// erases its text. It leaves the feeds that point at it: every read skips
// deleted items, and trench entries expire on their own. Deleting a remolt
// frees the crab to remolt it again; deleting a reply or quote takes it off
// its parent's list.
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
	case m.QuoteOf != "":
		parentPK, parentSK, counter = m.QuoteOfPK, m.QuoteOfSK, "quote_count"
		unlink = types.Delete{TableName: s.tableName(), Key: keyOf(quotePointerPK(m.QuoteOf), quotePointerSK(m.ID))}
	}
	items := []types.TransactWriteItem{
		{Update: &types.Update{
			TableName:           s.tableName(),
			Key:                 keyOf(m.PK, m.SK),
			UpdateExpression:    aws.String("SET deleted = :t REMOVE content, tags, mentions, GSI3PK, GSI3SK, GSI5PK, GSI5SK, GSI8PK, GSI8SK"),
			ConditionExpression: aws.String("attribute_exists(PK) AND deleted = :f AND owner_id = :me"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":t": boolean(true), ":f": boolean(false), ":me": str(c.ID),
			},
		}},
		s.addCounter(c.PK, c.SK, "molt_count", -1),
	}
	for _, k := range tagKeys(m) {
		items = append(items, types.TransactWriteItem{Delete: &types.Delete{TableName: s.tableName(), Key: keyOf(k[0], k[1])}})
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

// SetMoltNSFW adds or removes a molt's NSFW label.
func (s *Store) SetMoltNSFW(ctx context.Context, m *Molt, on bool) error {
	in := &dynamodb.UpdateItemInput{
		TableName:                 s.tableName(),
		Key:                       keyOf(m.PK, m.SK),
		UpdateExpression:          aws.String("SET nsfw = :t"),
		ConditionExpression:       aws.String("attribute_exists(PK) AND deleted = :f"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":t": boolean(true), ":f": boolean(false)},
	}
	if !on {
		in.UpdateExpression = aws.String("REMOVE nsfw")
		delete(in.ExpressionAttributeValues, ":t")
	}
	_, err := s.db.UpdateItem(ctx, in)
	switch {
	case conditionFailed(err):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("set molt nsfw: %w", err)
	}
	m.NSFW = on
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
	p, err := s.MoltsByOwnerPage(ctx, crabID, "", limit)
	return p.Molts, err
}

// MoltsByOwnerPage is MoltsByOwner starting after the cursor from a previous page.
func (s *Store) MoltsByOwnerPage(ctx context.Context, crabID, after string, limit int) (Page, error) {
	return s.ownerMolts(ctx, crabID, moltSK(""), after, limit)
}

// RepliesByOwner returns the replies a crab wrote, newest first.
func (s *Store) RepliesByOwner(ctx context.Context, crabID string, limit int) ([]Molt, error) {
	p, err := s.RepliesByOwnerPage(ctx, crabID, "", limit)
	return p.Molts, err
}

// RepliesByOwnerPage is RepliesByOwner starting after the cursor from a previous page.
func (s *Store) RepliesByOwnerPage(ctx context.Context, crabID, after string, limit int) (Page, error) {
	return s.ownerMolts(ctx, crabID, replySK(""), after, limit)
}

func (s *Store) ownerMolts(ctx context.Context, crabID, prefix, after string, limit int) (Page, error) {
	p, err := s.queryMolts(ctx, &dynamodb.QueryInput{
		TableName:                 s.tableName(),
		KeyConditionExpression:    aws.String("PK = :pk AND begins_with(SK, :m)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":pk": str(moltPK(crabID)), ":m": str(prefix)},
		ScanIndexForward:          aws.Bool(false),
	}, after, limit)
	if err != nil {
		return Page{}, fmt.Errorf("molts by owner: %w", err)
	}
	return p, nil
}

// LatestMolts returns the newest molts across the last week (UTC days), newest
// first. Each day is one query on the day index; the items are then loaded
// from the base table so counters match what the thread page shows.
func (s *Store) LatestMolts(ctx context.Context, limit int) ([]Molt, error) {
	p, err := s.LatestMoltsPage(ctx, "", limit)
	return p.Molts, err
}

// LatestMoltsPage is LatestMolts starting after the last molt of a previous
// page. The cursor is that molt's day key and SK, so the next page picks up
// on the same day and walks older days if needed.
func (s *Store) LatestMoltsPage(ctx context.Context, after string, limit int) (Page, error) {
	now := s.now()
	afterDay, afterSK, err := parseSeaCursor(after)
	if err != nil {
		return Page{}, err
	}
	var keys [][2]string
	seen := map[string]bool{}
	var lastDay, lastSK string
	for d := 0; d < 7 && len(keys) < limit; d++ {
		day := now.AddDate(0, 0, -d)
		dayKey := moltDayKey(day)
		if afterDay != "" && dayKey > afterDay {
			continue
		}
		in := &dynamodb.QueryInput{
			TableName:              s.tableName(),
			IndexName:              aws.String(gsiMoltsByDay),
			KeyConditionExpression: aws.String("GSI3PK = :day"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":day": str(dayKey),
			},
			ScanIndexForward: aws.Bool(false),
		}
		if afterDay == dayKey && afterSK != "" {
			in.KeyConditionExpression = aws.String("GSI3PK = :day AND GSI3SK < :sk")
			in.ExpressionAttributeValues[":sk"] = str(afterSK)
		}
		molts, err := queryAll[Molt](ctx, s.db, in, limit-len(keys)+1)
		if err != nil {
			return Page{}, fmt.Errorf("latest molts: %w", err)
		}
		afterDay, afterSK = "", ""
		for _, m := range withoutDeleted(molts) {
			if m.PK == "" || m.SK == "" || seen[m.ID] {
				continue
			}
			seen[m.ID] = true
			if len(keys) >= limit {
				break
			}
			keys = append(keys, [2]string{m.PK, m.SK})
			lastDay, lastSK = dayKey, m.SK
		}
	}
	molts, err := s.MoltsByKeys(ctx, keys)
	if err != nil {
		return Page{}, err
	}
	next := ""
	if len(keys) == limit && lastSK != "" {
		next = encodeSeaCursor(lastDay, lastSK)
	}
	return Page{Molts: molts, Next: next}, nil
}

func encodeSeaCursor(day, sk string) string {
	return encodeCursor(map[string]types.AttributeValue{"d": str(day), "s": str(sk)})
}

func parseSeaCursor(after string) (day, sk string, err error) {
	if after == "" {
		return "", "", nil
	}
	key, err := decodeCursor(after)
	if err != nil {
		return "", "", err
	}
	if d, ok := key["d"].(*types.AttributeValueMemberS); ok {
		day = d.Value
	}
	if s, ok := key["s"].(*types.AttributeValueMemberS); ok {
		sk = s.Value
	}
	if day == "" || sk == "" {
		return "", "", fmt.Errorf("%w: cursor", ErrNotFound)
	}
	return day, sk, nil
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
