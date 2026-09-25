package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/t0ul/krabber-net/internal/avatar"
)

// Deleting an account takes two steps. DeleteAccount runs in the request: it
// swaps the account item for a tombstone with no email, password or profile.
// That ends every session (they point at the old item), frees the email for a
// new signup and keeps the username reserved, so nobody can take it over.
// The tombstone waits in the purge queue until PurgeCrab has removed the
// crab's molts, likes, follows, blocks, notifications and trench.

// DeleteAccount replaces the account with a tombstone and queues the purge.
func (s *Store) DeleteAccount(ctx context.Context, c *Crab) (*Crab, error) {
	tomb := &Crab{
		PK:             deletedCrabPK(c.ID),
		SK:             crabSK(),
		GSI2PK:         crabIDKey(c.ID),
		GSI2SK:         crabIDKey(c.ID),
		GSI8PK:         queuePurge,
		GSI8SK:         c.ID,
		ID:             c.ID,
		UserName:       c.UserName,
		CreatedAt:      c.CreatedAt,
		Activated:      c.Activated,
		Deleted:        true,
		DeletedAt:      s.now().Unix(),
		FollowerCount:  c.FollowerCount,
		FollowingCount: c.FollowingCount,
		MoltCount:      c.MoltCount,
		BlockLinks:     c.BlockLinks,
	}
	item, err := marshal(tomb)
	if err != nil {
		return nil, err
	}
	items := []types.TransactWriteItem{
		{Delete: &types.Delete{
			TableName:                 s.tableName(),
			Key:                       keyOf(c.PK, c.SK),
			ConditionExpression:       aws.String("attribute_exists(PK) AND deleted = :f"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":f": boolean(false)},
		}},
		s.putNew(item),
	}
	if avatar.Valid(c.Avatar) {
		items = append(items, types.TransactWriteItem{Delete: &types.Delete{
			TableName: s.tableName(),
			Key:       keyOf(avatarPK(c.Avatar), avatarSK()),
		}})
	}
	if c.InviteCode != "" {
		// The code points at the account item, which is going away.
		items = append(items, types.TransactWriteItem{Delete: &types.Delete{
			TableName: s.tableName(),
			Key:       keyOf(invitePK(c.InviteCode), inviteSK()),
		}})
	}
	err = s.transact(ctx, items...)
	switch {
	case cancelledAt(err, 0), cancelledAt(err, 1):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("delete account: %w", err)
	}
	return tomb, nil
}

// PendingPurges returns the IDs of deleted accounts not yet cleaned up.
func (s *Store) PendingPurges(ctx context.Context, limit int) ([]string, error) {
	crabs, err := queryAll[Crab](ctx, s.db, &dynamodb.QueryInput{
		TableName:                 s.tableName(),
		IndexName:                 aws.String(gsiWorkQueue),
		KeyConditionExpression:    aws.String("GSI8PK = :q"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":q": str(queuePurge)},
		ProjectionExpression:      aws.String("id"),
		Limit:                     pageLimit(limit),
	}, limit)
	if err != nil {
		return nil, fmt.Errorf("pending purges: %w", err)
	}
	ids := make([]string, 0, len(crabs))
	for _, c := range crabs {
		ids = append(ids, c.ID)
	}
	return ids, nil
}

// PurgeCrab removes what a deleted crab left in the table and fixes the
// counters of everyone they touched, then takes the tombstone off the queue.
// Every step is idempotent, so an interrupted purge is simply run again, and
// two instances purging the same crab at once don't double-count.
func (s *Store) PurgeCrab(ctx context.Context, crabID string) error {
	tomb, err := s.CrabByKey(ctx, deletedCrabPK(crabID), crabSK())
	if err != nil {
		return fmt.Errorf("purge %s: %w", crabID, err)
	}
	for _, step := range []func(context.Context, *Crab) error{
		s.purgeMolts, s.purgeLikes, s.purgeFollows, s.purgeBlocks,
	} {
		if err := step(ctx, tomb); err != nil {
			return fmt.Errorf("purge %s: %w", crabID, err)
		}
	}
	for _, pk := range []string{
		moltPK(crabID), remoltMarkerPK(crabID), likePK(crabID), followPK(crabID), blockPK(crabID),
		bookmarkPK(crabID), bookmarkListPK(crabID),
		trenchPK(crabID), notificationPK(crabID), notificationOncePK(crabID), notificationCounterPK(crabID),
		trophyPK(crabID),
	} {
		if err := s.deletePartition(ctx, pk); err != nil {
			return fmt.Errorf("purge %s: %w", crabID, err)
		}
	}
	_, err = s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           s.tableName(),
		Key:                 keyOf(tomb.PK, tomb.SK),
		UpdateExpression:    aws.String("REMOVE GSI8PK, GSI8SK SET follower_count = :z, following_count = :z, molt_count = :z, block_links = :z"),
		ConditionExpression: aws.String("attribute_exists(PK)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":z": num(0),
		},
	})
	if err != nil {
		return fmt.Errorf("purge %s: finish: %w", crabID, err)
	}
	return nil
}

// purgeMolts deletes the crab's molts, replies, quotes and remolts. Molts take
// their likes, reports and reply and quote listings with them (other crabs'
// replies and quotes stay, pointing at a missing molt); replies, quotes and
// remolts give back the parent's count.
func (s *Store) purgeMolts(ctx context.Context, tomb *Crab) error {
	molts, err := queryAll[Molt](ctx, s.db, &dynamodb.QueryInput{
		TableName:                 s.tableName(),
		KeyConditionExpression:    aws.String("PK = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":pk": str(moltPK(tomb.ID))},
	}, 0)
	if err != nil {
		return fmt.Errorf("molts: %w", err)
	}
	for _, m := range molts {
		if !m.Remolt {
			for _, pk := range []string{replyPointerPK(m.ID), quotePointerPK(m.ID), reportPK(m.ID)} {
				if err := s.deletePartition(ctx, pk); err != nil {
					return err
				}
			}
			likes, err := s.LikesOn(ctx, m.ID, 0)
			if err != nil {
				return err
			}
			keys := make([][2]string, 0, len(likes))
			for _, l := range likes {
				keys = append(keys, [2]string{l.PK, l.SK})
			}
			if err := s.batchDelete(ctx, append(keys, tagKeys(&m)...)); err != nil {
				return err
			}
		}
		switch {
		case m.Deleted:
			err = s.batchDelete(ctx, [][2]string{{m.PK, m.SK}})
		case m.Remolt && m.RemoltOfPK != "":
			err = s.deleteAndCount(ctx, m.PK, m.SK, m.RemoltOfPK, m.RemoltOfSK, "remolt_count")
		case m.ReplyTo != "":
			err = s.batchDelete(ctx, [][2]string{{replyPointerPK(m.ReplyTo), replyPointerSK(m.ID)}})
			if err == nil {
				err = s.deleteAndCount(ctx, m.PK, m.SK, m.ReplyToPK, m.ReplyToSK, "reply_count")
			}
		case m.QuoteOf != "":
			err = s.batchDelete(ctx, [][2]string{{quotePointerPK(m.QuoteOf), quotePointerSK(m.ID)}})
			if err == nil {
				err = s.deleteAndCount(ctx, m.PK, m.SK, m.QuoteOfPK, m.QuoteOfSK, "quote_count")
			}
		default:
			err = s.batchDelete(ctx, [][2]string{{m.PK, m.SK}})
		}
		if err != nil {
			return fmt.Errorf("molt %s: %w", m.ID, err)
		}
	}
	return nil
}

// purgeLikes removes the crab's likes from other crabs' molts.
func (s *Store) purgeLikes(ctx context.Context, tomb *Crab) error {
	likes, err := queryAll[Like](ctx, s.db, &dynamodb.QueryInput{
		TableName:                 s.tableName(),
		KeyConditionExpression:    aws.String("PK = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":pk": str(likePK(tomb.ID))},
	}, 0)
	if err != nil {
		return fmt.Errorf("likes: %w", err)
	}
	for _, l := range likes {
		pk, sk, err := s.moltKey(ctx, l.MoltID)
		switch {
		case errors.Is(err, ErrNotFound):
			err = s.batchDelete(ctx, [][2]string{{l.PK, l.SK}})
		case err == nil:
			err = s.deleteAndCount(ctx, l.PK, l.SK, pk, sk, "like_count")
		}
		if err != nil {
			return fmt.Errorf("like on %s: %w", l.MoltID, err)
		}
	}
	return nil
}

// purgeFollows unfollows in both directions, fixing the other crabs' counts.
func (s *Store) purgeFollows(ctx context.Context, tomb *Crab) error {
	following, err := s.Following(ctx, tomb.ID, 0)
	if err != nil {
		return err
	}
	for _, f := range following {
		if err := s.purgeFollow(ctx, tomb, f.FolloweeID, true); err != nil {
			return err
		}
	}
	followers, err := s.Followers(ctx, tomb.ID, 0)
	if err != nil {
		return err
	}
	for _, f := range followers {
		if err := s.purgeFollow(ctx, tomb, f.FollowerID, false); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) purgeFollow(ctx context.Context, tomb *Crab, otherID string, outgoing bool) error {
	other, err := s.CrabByID(ctx, otherID)
	if errors.Is(err, ErrNotFound) {
		if outgoing {
			return s.batchDelete(ctx, [][2]string{{followPK(tomb.ID), followSK(otherID)}})
		}
		return s.batchDelete(ctx, [][2]string{{followPK(otherID), followSK(tomb.ID)}})
	} else if err != nil {
		return err
	}
	if outgoing {
		err = s.Unfollow(ctx, tomb, other)
	} else {
		err = s.Unfollow(ctx, other, tomb)
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

// purgeBlocks lifts blocks in both directions, fixing the other crabs'
// block_links so their block query is skipped again once it reaches 0.
func (s *Store) purgeBlocks(ctx context.Context, tomb *Crab) error {
	b, err := s.BlocksOf(ctx, tomb.ID)
	if err != nil {
		return err
	}
	lift := func(otherID string, blocker bool) error {
		other, err := s.CrabByID(ctx, otherID)
		if errors.Is(err, ErrNotFound) {
			return nil // the tombstone's own rows go with its partition
		} else if err != nil {
			return err
		}
		if blocker {
			err = s.Unblock(ctx, tomb, other)
		} else {
			err = s.Unblock(ctx, other, tomb)
		}
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		return nil
	}
	for id := range b.Blocking {
		if err := lift(id, true); err != nil {
			return err
		}
	}
	for id := range b.BlockedBy {
		if err := lift(id, false); err != nil {
			return err
		}
	}
	return nil
}

// deleteAndCount deletes an item and lowers a counter on another item, once:
// if the item is already gone, nothing changes; if the counted item is gone,
// the item is deleted anyway.
func (s *Store) deleteAndCount(ctx context.Context, pk, sk, counterPK, counterSK, attr string) error {
	err := s.transact(ctx,
		types.TransactWriteItem{Delete: &types.Delete{
			TableName:           s.tableName(),
			Key:                 keyOf(pk, sk),
			ConditionExpression: aws.String("attribute_exists(PK)"),
		}},
		s.addCounter(counterPK, counterSK, attr, -1),
	)
	switch {
	case err == nil, cancelledAt(err, 0):
		return nil
	case cancelledAt(err, 1):
		return s.batchDelete(ctx, [][2]string{{pk, sk}})
	default:
		return err
	}
}

// moltKey finds a molt's table key by ID. Deleted molts aren't found.
func (s *Store) moltKey(ctx context.Context, id string) (string, string, error) {
	molts, err := queryAll[Molt](ctx, s.db, &dynamodb.QueryInput{
		TableName:                 s.tableName(),
		IndexName:                 aws.String(gsiMoltByID),
		KeyConditionExpression:    aws.String("GSI5PK = :id"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":id": str(moltIDKey(id))},
		ProjectionExpression:      aws.String("PK, SK"),
	}, 1)
	if err != nil {
		return "", "", fmt.Errorf("molt key: %w", err)
	}
	if len(molts) == 0 {
		return "", "", ErrNotFound
	}
	return molts[0].PK, molts[0].SK, nil
}

// deletePartition deletes every item with the given partition key.
func (s *Store) deletePartition(ctx context.Context, pk string) error {
	p := dynamodb.NewQueryPaginator(s.db, &dynamodb.QueryInput{
		TableName:                 s.tableName(),
		KeyConditionExpression:    aws.String("PK = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":pk": str(pk)},
		ProjectionExpression:      aws.String("PK, SK"),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("delete partition: %w", err)
		}
		keys := make([][2]string, 0, len(page.Items))
		for _, item := range page.Items {
			pkv, _ := item["PK"].(*types.AttributeValueMemberS)
			skv, _ := item["SK"].(*types.AttributeValueMemberS)
			if pkv != nil && skv != nil {
				keys = append(keys, [2]string{pkv.Value, skv.Value})
			}
		}
		if err := s.batchDelete(ctx, keys); err != nil {
			return err
		}
	}
	return nil
}

// batchDelete deletes items by key, 25 per request.
func (s *Store) batchDelete(ctx context.Context, keys [][2]string) error {
	for start := 0; start < len(keys); start += 25 {
		end := min(start+25, len(keys))
		writes := make([]types.WriteRequest, 0, end-start)
		for _, k := range keys[start:end] {
			writes = append(writes, types.WriteRequest{DeleteRequest: &types.DeleteRequest{Key: keyOf(k[0], k[1])}})
		}
		req := map[string][]types.WriteRequest{s.table: writes}
		err := retryUnprocessed(ctx, 8, func() (int, error) {
			res, err := s.db.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{RequestItems: req})
			if err != nil {
				return 0, err
			}
			req = res.UnprocessedItems
			return len(req[s.table]), nil
		})
		if err != nil {
			return fmt.Errorf("batch delete: %w", err)
		}
	}
	return nil
}
