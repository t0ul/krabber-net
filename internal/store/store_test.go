package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/segmentio/ksuid"

	"github.com/t0ul/krabber-net/internal/avatar"
	"github.com/t0ul/krabber-net/internal/platform"
)

// newTestStore creates a fresh table in DynamoDB Local. Tests are skipped when
// KRABBER_TEST_DYNAMO_ENDPOINT isn't set (run `make db` then `make test`).
func newTestStore(t *testing.T) *Store {
	t.Helper()
	endpoint := os.Getenv("KRABBER_TEST_DYNAMO_ENDPOINT")
	if endpoint == "" {
		t.Skip("KRABBER_TEST_DYNAMO_ENDPOINT not set; skipping DynamoDB Local test")
	}
	ctx := context.Background()
	cfg, err := platform.AWSConfig(ctx, "us-east-2")
	if err != nil {
		t.Fatal(err)
	}
	db := platform.DynamoDB(cfg, endpoint)
	table := "krabber-test-" + ksuid.New().String()
	if err := EnsureTable(ctx, db, table); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(table)})
	})
	return New(db, table)
}

func mustCrab(t *testing.T, s *Store, name string) *Crab {
	t.Helper()
	c, err := s.CreateCrab(context.Background(), name, name+"@krabber.test", []byte("hash"))
	if err != nil {
		t.Fatalf("create crab %s: %v", name, err)
	}
	if err := s.ActivateCrab(context.Background(), c.ID); err != nil {
		t.Fatal(err)
	}
	return reload(t, s, c)
}

func reload(t *testing.T, s *Store, c *Crab) *Crab {
	t.Helper()
	fresh, err := s.CrabByKey(context.Background(), c.PK, c.SK)
	if err != nil {
		t.Fatal(err)
	}
	return fresh
}

func TestCrabEmailAndUsernameAreUnique(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.CreateCrab(ctx, "Bob", "Bob@Example.com", []byte("h")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateCrab(ctx, "someoneelse", " bob@example.COM ", []byte("h")); !errors.Is(err, ErrDuplicateEmail) {
		t.Fatalf("same email in different case: got %v, want ErrDuplicateEmail", err)
	}
	if _, err := s.CreateCrab(ctx, "BOB", "other@example.com", []byte("h")); !errors.Is(err, ErrDuplicateUsername) {
		t.Fatalf("same username in different case: got %v, want ErrDuplicateUsername", err)
	}

	c, err := s.CrabByEmail(ctx, "BOB@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if c.UserName != "Bob" || c.Activated || !avatar.Valid(c.Avatar) {
		t.Fatalf("unexpected crab: %+v", c)
	}
	byID, err := s.CrabByID(ctx, c.ID)
	if err != nil || byID.Email != "bob@example.com" {
		t.Fatalf("CrabByID: %+v, %v", byID, err)
	}
	if _, err := s.CrabByID(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing crab: got %v, want ErrNotFound", err)
	}
}

func TestTokensAreSingleUseAndExpire(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	tok, err := s.NewToken(ctx, "crab-1", ScopeActivation, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != TokenLength {
		t.Fatalf("token length %d, want %d", len(tok), TokenLength)
	}
	if _, err := s.ConsumeToken(ctx, ScopePasswordReset, tok); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("wrong scope: got %v", err)
	}
	got, err := s.ConsumeToken(ctx, ScopeActivation, tok)
	if err != nil || got.CrabID != "crab-1" {
		t.Fatalf("consume: %+v, %v", got, err)
	}
	if _, err := s.ConsumeToken(ctx, ScopeActivation, tok); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("second use: got %v, want ErrInvalidToken", err)
	}

	expired, err := s.NewToken(ctx, "crab-1", ScopeActivation, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Now().UTC().Add(2 * time.Minute) }
	if _, err := s.ConsumeToken(ctx, ScopeActivation, expired); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expired token: got %v, want ErrInvalidToken", err)
	}
}

func TestSessions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	ss := s.Sessions()

	if err := ss.CommitCtx(ctx, "tok-a", []byte("data-a"), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	// A new Store (a restarted or replacement instance) sees the session.
	restarted := New(s.db, s.table).Sessions()
	b, found, err := restarted.FindCtx(ctx, "tok-a")
	if err != nil || !found || string(b) != "data-a" {
		t.Fatalf("after restart: %q %v %v", b, found, err)
	}

	// Expired items are rejected even though TTL hasn't deleted them yet.
	if err := ss.CommitCtx(ctx, "tok-b", []byte("data-b"), time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, found, err := ss.FindCtx(ctx, "tok-b"); err != nil || found {
		t.Fatalf("expired session found=%v err=%v", found, err)
	}

	if err := ss.DeleteCtx(ctx, "tok-a"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := ss.FindCtx(ctx, "tok-a"); found {
		t.Fatal("deleted session still found")
	}
	if _, found, err := ss.FindCtx(ctx, "never-existed"); err != nil || found {
		t.Fatalf("unknown token found=%v err=%v", found, err)
	}
}

func TestMoltFanoutTrenchAndSea(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	author := mustCrab(t, s, "mrkrabs")
	fan := mustCrab(t, s, "spongebob")

	if err := s.Follow(ctx, fan, author); err != nil {
		t.Fatal(err)
	}
	if err := s.Follow(ctx, fan, author); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("second follow: got %v", err)
	}
	if err := s.Follow(ctx, fan, fan); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("self follow: got %v", err)
	}

	m, err := s.CreateMolt(ctx, author, "money money money")
	if err != nil {
		t.Fatal(err)
	}

	pending, err := s.PendingFanouts(ctx, time.Now().Add(time.Minute), 10)
	if err != nil || len(pending) != 1 || pending[0].ID != m.ID {
		t.Fatalf("pending fanouts: %+v, %v", pending, err)
	}

	var followers []string
	err = s.EachFollowerPage(ctx, author.ID, func(ids []string) error {
		followers = append(followers, ids...)
		return nil
	})
	if err != nil || len(followers) != 1 || followers[0] != fan.ID {
		t.Fatalf("followers: %v, %v", followers, err)
	}
	if err := s.AddToTrenches(ctx, m, followers); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearFanout(ctx, m); err != nil {
		t.Fatal(err)
	}
	if pending, _ := s.PendingFanouts(ctx, time.Now().Add(time.Minute), 10); len(pending) != 0 {
		t.Fatalf("still pending after clear: %+v", pending)
	}

	trench, err := s.Trench(ctx, fan.ID, 25)
	if err != nil || len(trench) != 1 || trench[0].Content != "money money money" {
		t.Fatalf("trench: %+v, %v", trench, err)
	}

	fan = reload(t, s, fan)
	re, err := s.Remolt(ctx, fan, m)
	if err != nil {
		t.Fatal(err)
	}
	if !re.Remolt || re.Author != "mrkrabs" || re.RemoltedBy != "spongebob" {
		t.Fatalf("remolt: %+v", re)
	}
	if _, err := s.Remolt(ctx, fan, m); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("second remolt: got %v", err)
	}
	if _, err := s.Remolt(ctx, fan, re); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("remolt of a remolt: got %v", err)
	}

	// Remolts land in the day index too, so they reach the sea, shown as the
	// original (live counts) with who remolted it.
	sea, err := s.Sea(ctx, 25)
	if err != nil || len(sea) != 2 {
		t.Fatalf("sea: %+v, %v", sea, err)
	}
	if sea[0].EntryID != re.ID || sea[0].ID != m.ID || sea[0].RemoltedBy != "spongebob" || sea[0].RemoltCount != 1 {
		t.Fatalf("resolved remolt (newest first): %+v", sea[0])
	}
	if sea[1].ID != m.ID || sea[1].DOMID() == sea[0].DOMID() {
		t.Fatalf("original entry: %+v", sea[1])
	}

	got, err := s.MoltByID(ctx, m.ID)
	if err != nil || got.RemoltCount != 1 {
		t.Fatalf("original after remolt: %+v, %v", got, err)
	}
	if author = reload(t, s, author); author.MoltCount != 1 || author.FollowerCount != 1 {
		t.Fatalf("author counters: %+v", author)
	}
}

func TestDeleteMoltAndRemolt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	author := mustCrab(t, s, "pearl")
	fan := mustCrab(t, s, "larry")

	m, err := s.CreateMolt(ctx, author, "whale hello there")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddToTrenches(ctx, m, []string{fan.ID}); err != nil {
		t.Fatal(err)
	}
	re, err := s.Remolt(ctx, reload(t, s, fan), m)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.MoltByKey(ctx, re.PK, re.SK); got.Content != "" {
		t.Fatalf("remolt copied the text: %q", got.Content)
	}

	if err := s.DeleteMolt(ctx, fan, m); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("deleting someone else's molt: got %v", err)
	}

	// Undoing the remolt fixes both counters and allows remolting again.
	if err := s.DeleteMolt(ctx, reload(t, s, fan), re); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteMolt(ctx, reload(t, s, fan), re); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: got %v", err)
	}
	if got, _ := s.MoltByKey(ctx, m.PK, m.SK); got.RemoltCount != 0 {
		t.Fatalf("remolt count after undo: %d", got.RemoltCount)
	}
	if fan = reload(t, s, fan); fan.MoltCount != 0 {
		t.Fatalf("fan molt count after undo: %d", fan.MoltCount)
	}
	if _, err := s.Remolt(ctx, fan, m); err != nil {
		t.Fatalf("remolt after undo: %v", err)
	}

	// Deleting the original hides it and its remolts everywhere.
	if err := s.DeleteMolt(ctx, reload(t, s, author), m); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MoltByID(ctx, m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted molt by id: got %v", err)
	}
	var raw Molt
	if err := s.getItem(ctx, m.PK, m.SK, &raw); err != nil || !raw.Deleted || raw.Content != "" {
		t.Fatalf("deleted molt keeps its text: %+v, %v", raw, err)
	}
	if sea, _ := s.Sea(ctx, 25); len(sea) != 0 {
		t.Fatalf("sea after delete: %+v", sea)
	}
	if trench, _ := s.Trench(ctx, fan.ID, 25); len(trench) != 0 {
		t.Fatalf("trench after delete: %+v", trench)
	}
	if author = reload(t, s, author); author.MoltCount != 0 {
		t.Fatalf("author molt count: %d", author.MoltCount)
	}
}

func TestBlocks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a := mustCrab(t, s, "bubblebass")
	b := mustCrab(t, s, "spongebob")
	for _, pair := range [][2]*Crab{{a, b}, {b, a}} {
		if err := s.Follow(ctx, pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.Block(ctx, a, b); err != nil {
		t.Fatal(err)
	}
	if err := s.Block(ctx, a, b); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("second block: %v", err)
	}
	a, b = reload(t, s, a), reload(t, s, b)
	if a.FollowerCount+a.FollowingCount+b.FollowerCount+b.FollowingCount != 0 || a.BlockLinks != 1 || b.BlockLinks != 1 {
		t.Fatalf("after block: a=%+v b=%+v", a, b)
	}
	if err := s.Follow(ctx, b, a); !errors.Is(err, ErrBlocked) {
		t.Fatalf("follow blocker: %v", err)
	}
	if err := s.Follow(ctx, a, b); !errors.Is(err, ErrBlocked) {
		t.Fatalf("follow blocked: %v", err)
	}
	ab, _ := s.BlocksOf(ctx, a.ID)
	bb, _ := s.BlocksOf(ctx, b.ID)
	if ab.Blocking[b.ID] != "spongebob" || !bb.BlockedBy[a.ID] || !ab.Hides(b.ID) || !bb.Hides(a.ID) || bb.Hides("someone") {
		t.Fatalf("blocks: a=%+v b=%+v", ab, bb)
	}

	// Blocking back is its own block; undoing one leaves the other.
	if err := s.Block(ctx, b, a); err != nil {
		t.Fatal(err)
	}
	if err := s.Unblock(ctx, a, b); err != nil {
		t.Fatal(err)
	}
	if err := s.Unblock(ctx, a, b); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second unblock: %v", err)
	}
	ab, _ = s.BlocksOf(ctx, a.ID)
	if len(ab.Blocking) != 0 || !ab.BlockedBy[b.ID] {
		t.Fatalf("after unblock: %+v", ab)
	}
	if err := s.Unblock(ctx, b, a); err != nil {
		t.Fatal(err)
	}
	if a, b = reload(t, s, a), reload(t, s, b); a.BlockLinks != 0 || b.BlockLinks != 0 {
		t.Fatalf("block links: %d %d", a.BlockLinks, b.BlockLinks)
	}
	if err := s.Follow(ctx, a, b); err != nil {
		t.Fatalf("follow after unblock: %v", err)
	}
}

func TestDeleteAccount(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	gone := mustCrab(t, s, "flyingdutchman")
	friend := mustCrab(t, s, "spongebob")
	fan := mustCrab(t, s, "patrick")
	rival := mustCrab(t, s, "plankton")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Follow(ctx, gone, friend))
	must(s.Follow(ctx, fan, gone))
	must(s.Block(ctx, rival, gone))
	bye, err := s.CreateMolt(ctx, gone, "Ahoy, I'm off")
	must(err)
	hi, err := s.CreateMolt(ctx, friend, "I'm ready!")
	must(err)
	must(s.LikeMolt(ctx, friend, bye))
	_, err = s.Reply(ctx, friend, bye, "Bye!")
	must(err)
	_, err = s.Reply(ctx, gone, hi, "Ready for what?")
	must(err)
	_, err = s.Remolt(ctx, fan, bye)
	must(err)
	must(s.LikeMolt(ctx, gone, hi))
	_, err = s.Remolt(ctx, gone, hi)
	must(err)
	must(s.AddToTrenches(ctx, hi, []string{gone.ID}))
	must(s.AddNotification(ctx, Notification{RecipientID: gone.ID, Type: NotifyFollow, ActorID: fan.ID, Actor: "patrick"}))

	gone = reload(t, s, gone)
	tomb, err := s.DeleteAccount(ctx, gone)
	must(err)
	if _, err := s.DeleteAccount(ctx, gone); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if _, err := s.CrabByEmail(ctx, "flyingdutchman@krabber.test"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("email still stored: %v", err)
	}
	byName, err := s.CrabByUsername(ctx, "flyingdutchman")
	if err != nil || !byName.Deleted || byName.CanSignIn() || byName.Email != "" || len(byName.PasswordHash) != 0 {
		t.Fatalf("tombstone: %+v, %v", byName, err)
	}
	if _, err := s.CreateCrab(ctx, "flyingdutchman", "someone@krabber.test", []byte("h")); !errors.Is(err, ErrDuplicateUsername) {
		t.Fatalf("username not reserved: %v", err)
	}
	if _, err := s.CreateCrab(ctx, "dutchman2", "flyingdutchman@krabber.test", []byte("h")); err != nil {
		t.Fatalf("email not freed: %v", err)
	}

	queued, err := s.PendingPurges(ctx, 10)
	if err != nil || len(queued) != 1 || queued[0] != tomb.ID {
		t.Fatalf("purge queue: %v, %v", queued, err)
	}
	must(s.PurgeCrab(ctx, tomb.ID))
	must(s.PurgeCrab(ctx, tomb.ID)) // idempotent

	if queued, _ := s.PendingPurges(ctx, 10); len(queued) != 0 {
		t.Fatalf("still queued: %v", queued)
	}
	friend, fan, rival = reload(t, s, friend), reload(t, s, fan), reload(t, s, rival)
	if friend.FollowerCount != 0 || fan.FollowingCount != 0 || rival.BlockLinks != 0 {
		t.Fatalf("counters: friend=%+v fan=%+v rival=%+v", friend, fan, rival)
	}
	if m, err := s.MoltByKey(ctx, hi.PK, hi.SK); err != nil || m.LikeCount != 0 || m.RemoltCount != 0 || m.ReplyCount != 0 {
		t.Fatalf("friend's molt: %+v, %v", m, err)
	}
	if marks, _ := s.MarksOn(ctx, friend.ID, []string{bye.ID}); marks[bye.ID].Liked {
		t.Fatal("like on the deleted molt survived")
	}
	if _, err := s.MoltByID(ctx, bye.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted crab's molt: %v", err)
	}
	for _, pk := range []string{moltPK(tomb.ID), replyPointerPK(bye.ID), replyPointerPK(hi.ID), likePK(tomb.ID), followPK(tomb.ID), blockPK(tomb.ID),
		blockPK(rival.ID), remoltMarkerPK(tomb.ID), trenchPK(tomb.ID), notificationPK(tomb.ID), notificationCounterPK(tomb.ID)} {
		left, err := queryAll[map[string]any](ctx, s.db, &dynamodb.QueryInput{
			TableName:                 s.tableName(),
			KeyConditionExpression:    aws.String("PK = :pk"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":pk": str(pk)},
		}, 0)
		if err != nil || len(left) != 0 {
			t.Fatalf("%s: %d items left, %v", pk, len(left), err)
		}
	}
	if tomb = reload(t, s, tomb); tomb.FollowerCount+tomb.FollowingCount+tomb.MoltCount+tomb.BlockLinks != 0 || tomb.GSI8PK != "" {
		t.Fatalf("tombstone after purge: %+v", tomb)
	}
}

func TestReports(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	troll := mustCrab(t, s, "plankton")
	a, b, c := mustCrab(t, s, "sandy"), mustCrab(t, s, "gary"), mustCrab(t, s, "larry")
	m, err := s.CreateMolt(ctx, troll, "Buy my chum, it's the best")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.ReportMolt(ctx, troll, m, "spam", ""); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("reporting your own molt: %v", err)
	}
	if err := s.ReportMolt(ctx, a, m, "spam", "Chum again"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReportMolt(ctx, a, m, "hate", ""); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("second report by the same crab: %v", err)
	}
	if err := s.ReportMolt(ctx, b, m, "other", ""); err != nil {
		t.Fatal(err)
	}
	open, err := s.OpenReports(ctx, 10)
	if err != nil || len(open) != 1 || open[0].MoltID != m.ID || open[0].OpenReports != 2 || len(open[0].Reasons) != 2 || open[0].Author != "plankton" {
		t.Fatalf("queue: %+v, %v", open, err)
	}
	summary, rows, err := s.ReportsOn(ctx, m.ID)
	if err != nil || summary == nil || len(rows) != 2 || rows[0].Note+rows[1].Note != "Chum again" {
		t.Fatalf("reports on molt: %+v %+v %v", summary, rows, err)
	}

	if err := s.ResolveReports(ctx, m.ID, "mrkrabs", "dismissed"); err != nil {
		t.Fatal(err)
	}
	if open, _ := s.OpenReports(ctx, 10); len(open) != 0 {
		t.Fatalf("still open: %+v", open)
	}
	if summary, _, _ := s.ReportsOn(ctx, m.ID); summary.Resolution != "dismissed" || summary.ResolvedBy != "mrkrabs" || summary.OpenReports != 0 {
		t.Fatalf("resolved: %+v", summary)
	}

	// A new report after a decision reopens the molt with a fresh count.
	if err := s.ReportMolt(ctx, c, m, "sexual", ""); err != nil {
		t.Fatal(err)
	}
	open, _ = s.OpenReports(ctx, 10)
	if len(open) != 1 || open[0].OpenReports != 1 || len(open[0].Reasons) != 1 || open[0].Resolution != "" {
		t.Fatalf("reopened: %+v", open)
	}
	if err := s.ResolveReports(ctx, "never-reported", "mrkrabs", "dismissed"); err != nil {
		t.Fatalf("resolving an unreported molt: %v", err)
	}
}

func TestEmptySea(t *testing.T) {
	s := newTestStore(t)
	molts, err := s.Sea(context.Background(), 25)
	if err != nil || len(molts) != 0 {
		t.Fatalf("empty sea: %v, %v", molts, err)
	}
}

func TestProfilesFollowListsAndLikeToggle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a := mustCrab(t, s, "Sandy")
	b := mustCrab(t, s, "gary")

	got, err := s.CrabByUsername(ctx, "sandy")
	if err != nil || got.ID != a.ID {
		t.Fatalf("by username (case-insensitive): %+v, %v", got, err)
	}
	if _, err := s.CrabByUsername(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing username: %v", err)
	}

	if err := s.Follow(ctx, b, a); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.IsFollowing(ctx, b.ID, a.ID); !ok {
		t.Fatal("IsFollowing b->a = false")
	}
	if ok, _ := s.IsFollowing(ctx, a.ID, b.ID); ok {
		t.Fatal("IsFollowing a->b = true")
	}
	followers, err := s.Followers(ctx, a.ID, 10)
	if err != nil || len(followers) != 1 || followers[0].FollowerName != "gary" {
		t.Fatalf("followers: %+v, %v", followers, err)
	}
	following, err := s.Following(ctx, b.ID, 10)
	if err != nil || len(following) != 1 || following[0].FolloweeName != "Sandy" {
		t.Fatalf("following: %+v, %v", following, err)
	}
	if ids, _ := s.FollowingIDs(ctx, b.ID); !ids[a.ID] {
		t.Fatalf("following ids: %v", ids)
	}

	m, err := s.CreateMolt(ctx, a, "hi-yah!")
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []bool{true, false, true} {
		liked, err := s.ToggleLike(ctx, b, m)
		if err != nil || liked != want {
			t.Fatalf("toggle %d: liked=%v err=%v", i, liked, err)
		}
	}
	fresh, err := s.MoltByKey(ctx, m.PK, m.SK)
	if err != nil || fresh.LikeCount != 1 {
		t.Fatalf("like count after like/unlike/like: %+v, %v", fresh, err)
	}
	if marks, _ := s.MarksOn(ctx, b.ID, []string{m.ID, "other"}); !marks[m.ID].Liked || marks["other"].Liked {
		t.Fatalf("likes: %v", marks)
	}
}

func TestLikes(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	author := mustCrab(t, s, "plankton")
	liker := mustCrab(t, s, "karen")
	m, err := s.CreateMolt(ctx, author, "the formula will be mine")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.LikeMolt(ctx, liker, m); err != nil {
		t.Fatal(err)
	}
	if err := s.LikeMolt(ctx, liker, m); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("second like: got %v", err)
	}
	likes, err := s.LikesOn(ctx, m.ID, 10)
	if err != nil || len(likes) != 1 || likes[0].CrabName != "karen" {
		t.Fatalf("likes: %+v, %v", likes, err)
	}

	got, err := s.MoltByID(ctx, m.ID)
	if err != nil || got.LikeCount != 1 {
		t.Fatalf("counters: %+v, %v", got, err)
	}

	// Likes from before likes stored the molt's key are found by molt ID.
	older, err := s.CreateMolt(ctx, author, "phase one")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LikeMolt(ctx, liker, older); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: s.tableName(), Key: keyOf(likePK(liker.ID), likeSK(older.ID)),
		UpdateExpression: aws.String("REMOVE molt_pk, molt_sk"),
	}); err != nil {
		t.Fatal(err)
	}
	liked, err := s.LikedMolts(ctx, liker.ID, 10)
	if err != nil || len(liked) != 2 || liked[0].ID != older.ID || liked[1].ID != m.ID {
		t.Fatalf("liked molts: %+v, %v", liked, err)
	}
}

func TestReplies(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	author := mustCrab(t, s, "plankton")
	karen := mustCrab(t, s, "karen")
	m, err := s.CreateMolt(ctx, author, "the formula will be mine")
	if err != nil {
		t.Fatal(err)
	}

	// Two replies in the same second don't collide, and come back oldest first.
	var replies []*Molt
	for i := range 2 {
		r, err := s.Reply(ctx, karen, m, fmt.Sprintf("reply %d", i))
		if err != nil {
			t.Fatal(err)
		}
		replies = append(replies, r)
	}
	thread, err := s.Replies(ctx, m.ID, 10)
	if err != nil || len(thread) != 2 || thread[0].Content != "reply 0" || thread[0].ReplyToAuthor != "plankton" {
		t.Fatalf("thread: %+v, %v", thread, err)
	}
	nested, err := s.Reply(ctx, author, replies[0], "quiet, computer wife")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reply(ctx, karen, &Molt{ID: "x", Remolt: true}, "no"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("reply to a remolt: %v", err)
	}
	if got, _ := s.MoltByID(ctx, m.ID); got.ReplyCount != 2 {
		t.Fatalf("reply count: %d", got.ReplyCount)
	}
	if got, _ := s.MoltByID(ctx, nested.ID); got == nil || got.ReplyTo != replies[0].ID {
		t.Fatalf("nested reply by id: %+v", got)
	}

	// Replies stay off the timeline, the Sea and the trench queue, and have
	// their own list on the profile.
	if own, _ := s.MoltsByOwner(ctx, karen.ID, 10); len(own) != 0 {
		t.Fatalf("replies on the molts timeline: %+v", own)
	}
	if own, _ := s.RepliesByOwner(ctx, karen.ID, 10); len(own) != 2 || own[0].ID != replies[1].ID {
		t.Fatalf("replies by owner: %+v", own)
	}
	if sea, _ := s.Sea(ctx, 25); len(sea) != 1 {
		t.Fatalf("sea has %d molts, want just the original", len(sea))
	}
	if pending, _ := s.PendingFanouts(ctx, time.Now().Add(time.Hour), 10); len(pending) != 1 {
		t.Fatalf("fan-out queue has %d molts, want just the original", len(pending))
	}
	if karen = reload(t, s, karen); karen.MoltCount != 2 {
		t.Fatalf("karen's molt count: %d", karen.MoltCount)
	}

	// Deleting a reply takes it off the thread and fixes the counts.
	if err := s.DeleteMolt(ctx, karen, replies[1]); err != nil {
		t.Fatal(err)
	}
	if thread, _ := s.Replies(ctx, m.ID, 10); len(thread) != 1 {
		t.Fatalf("thread after delete: %+v", thread)
	}
	if got, _ := s.MoltByID(ctx, m.ID); got.ReplyCount != 1 {
		t.Fatalf("reply count after delete: %d", got.ReplyCount)
	}

	// A reply whose parent was purged with its author's account can still be deleted.
	if err := s.batchDelete(ctx, [][2]string{{m.PK, m.SK}}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteMolt(ctx, reload(t, s, karen), replies[0]); err != nil {
		t.Fatalf("delete reply to a purged molt: %v", err)
	}
	if karen = reload(t, s, karen); karen.MoltCount != 0 {
		t.Fatalf("karen's molt count after deletes: %d", karen.MoltCount)
	}
}

func TestQuotes(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	author := mustCrab(t, s, "plankton")
	karen := mustCrab(t, s, "karen")
	m, err := s.CreateMolt(ctx, author, "the formula will be mine")
	if err != nil {
		t.Fatal(err)
	}
	var quotes []*Molt
	for i := range 2 {
		q, err := s.Quote(ctx, karen, m, fmt.Sprintf("quote %d", i))
		if err != nil {
			t.Fatal(err)
		}
		quotes = append(quotes, q)
	}
	if _, err := s.Quote(ctx, karen, &Molt{ID: "x", Remolt: true}, "no"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("quote a remolt: %v", err)
	}
	if got, _ := s.MoltByID(ctx, m.ID); got.QuoteCount != 2 {
		t.Fatalf("quote count: %d", got.QuoteCount)
	}
	list, err := s.Quotes(ctx, m.ID, 10)
	if err != nil || len(list) != 2 || list[0].ID != quotes[1].ID || list[0].QuoteOf != m.ID {
		t.Fatalf("quotes, newest first: %+v, %v", list, err)
	}

	// Quotes are ordinary molts: on the timeline, in the Sea and queued for trenches.
	if own, _ := s.MoltsByOwner(ctx, karen.ID, 10); len(own) != 2 {
		t.Fatalf("quotes on the timeline: %d", len(own))
	}
	if sea, _ := s.Sea(ctx, 25); len(sea) != 3 {
		t.Fatalf("sea has %d molts, want the original and both quotes", len(sea))
	}
	if pending, _ := s.PendingFanouts(ctx, time.Now().Add(time.Hour), 10); len(pending) != 3 {
		t.Fatalf("fan-out queue has %d molts", len(pending))
	}

	// Deleting a quote takes it off the list and fixes the count.
	if err := s.DeleteMolt(ctx, karen, quotes[1]); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.Quotes(ctx, m.ID, 10); len(list) != 1 {
		t.Fatalf("quotes after delete: %+v", list)
	}
	if got, _ := s.MoltByID(ctx, m.ID); got.QuoteCount != 1 {
		t.Fatalf("quote count after delete: %d", got.QuoteCount)
	}

	// Purging the quoter gives the count back.
	tomb, err := s.DeleteAccount(ctx, reload(t, s, karen))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeCrab(ctx, tomb.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.MoltByID(ctx, m.ID); got.QuoteCount != 0 {
		t.Fatalf("quote count after purge: %d", got.QuoteCount)
	}
	if list, _ := s.Quotes(ctx, m.ID, 10); len(list) != 0 {
		t.Fatalf("quotes after purge: %+v", list)
	}
}

func TestCrabtags(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	author := mustCrab(t, s, "plankton")
	karen := mustCrab(t, s, "karen")
	m, err := s.CreateMolt(ctx, author, "Phase one of %TheFormula, with @Karen. %plan %theformula")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(m.Tags, []string{"theformula", "plan"}) || !slices.Equal(m.Mentions, []string{"karen"}) {
		t.Fatalf("tags %q, mentions %q", m.Tags, m.Mentions)
	}
	reply, err := s.Reply(ctx, karen, m, "%theformula again?")
	if err != nil {
		t.Fatal(err)
	}
	quote, err := s.Quote(ctx, karen, m, "It never works. %TheFormula")
	if err != nil {
		t.Fatal(err)
	}
	tagged, err := s.MoltsWithTag(ctx, "theformula", 10)
	if err != nil || len(tagged) != 3 || tagged[0].ID != quote.ID {
		t.Fatalf("tagged: %+v, %v", tagged, err)
	}

	// Deleting a molt takes it off its tags' pages and erases its tags.
	if err := s.DeleteMolt(ctx, reload(t, s, karen), reply); err != nil {
		t.Fatal(err)
	}
	var p moltPointer
	if err := s.getItem(ctx, tagPK("theformula"), tagSK(reply.ID), &p); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tag pointer after delete: %v", err)
	}
	var deleted Molt
	if err := s.getItem(ctx, reply.PK, reply.SK, &deleted); err != nil || len(deleted.Tags) != 0 {
		t.Fatalf("deleted reply kept its tags: %+v, %v", deleted.Tags, err)
	}

	// Purging the author removes their tag pointers.
	tomb, err := s.DeleteAccount(ctx, reload(t, s, author))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeCrab(ctx, tomb.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.getItem(ctx, tagPK("plan"), tagSK(m.ID), &p); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tag pointer after purge: %v", err)
	}
	if tagged, _ := s.MoltsWithTag(ctx, "theformula", 10); len(tagged) != 1 || tagged[0].ID != quote.ID {
		t.Fatalf("tagged after purge: %+v", tagged)
	}
}

func TestBookmarks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	author := mustCrab(t, s, "plankton")
	karen := mustCrab(t, s, "karen")
	older, _ := s.CreateMolt(ctx, author, "older")
	newer, _ := s.CreateMolt(ctx, author, "newer")
	gone, _ := s.CreateMolt(ctx, author, "gone")
	for _, m := range []*Molt{newer, older, gone} {
		if err := s.Bookmark(ctx, karen, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Bookmark(ctx, karen, older); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("bookmark twice: %v", err)
	}
	re, _ := s.Remolt(ctx, karen, newer)
	if err := s.Bookmark(ctx, karen, re); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("bookmark a remolt: %v", err)
	}
	if err := s.DeleteMolt(ctx, reload(t, s, author), gone); err != nil {
		t.Fatal(err)
	}

	// Most recently bookmarked first, whatever the molts' age; deleted skipped.
	got, err := s.Bookmarks(ctx, karen.ID, 10)
	if err != nil || len(got) != 2 || got[0].ID != older.ID || got[1].ID != newer.ID {
		t.Fatalf("bookmarks: %+v, %v", got, err)
	}
	if on, err := s.ToggleBookmark(ctx, karen, older); err != nil || on {
		t.Fatalf("toggle off: %v, %v", on, err)
	}
	if err := s.Unbookmark(ctx, karen, older.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unbookmark twice: %v", err)
	}
	if got, _ := s.Bookmarks(ctx, karen.ID, 10); len(got) != 1 || got[0].ID != newer.ID {
		t.Fatalf("bookmarks after removing one: %+v", got)
	}

	tomb, err := s.DeleteAccount(ctx, reload(t, s, karen))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeCrab(ctx, tomb.ID); err != nil {
		t.Fatal(err)
	}
	for _, pk := range []string{bookmarkPK(karen.ID), bookmarkListPK(karen.ID)} {
		items, err := queryAll[moltPointer](ctx, s.db, &dynamodb.QueryInput{
			TableName:                 s.tableName(),
			KeyConditionExpression:    aws.String("PK = :pk"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":pk": str(pk)},
		}, 0)
		if err != nil || len(items) != 0 {
			t.Fatalf("%s after purge: %d items, %v", pk, len(items), err)
		}
	}
}

func TestPins(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	karen := mustCrab(t, s, "karen")
	plankton := mustCrab(t, s, "plankton")
	first, _ := s.CreateMolt(ctx, karen, "first")
	second, _ := s.CreateMolt(ctx, karen, "second")
	theirs, _ := s.CreateMolt(ctx, plankton, "theirs")
	re, _ := s.Remolt(ctx, karen, theirs)

	if err := s.Pin(ctx, karen, theirs); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("pin someone else's molt: %v", err)
	}
	if err := s.Pin(ctx, karen, re); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("pin a remolt: %v", err)
	}
	if err := s.Pin(ctx, karen, first); err != nil {
		t.Fatal(err)
	}
	if err := s.Pin(ctx, karen, second); err != nil {
		t.Fatal(err)
	}
	fresh := reload(t, s, karen)
	if got, err := s.PinnedMolt(ctx, fresh); err != nil || got == nil || got.ID != second.ID {
		t.Fatalf("pinned: %+v, %v", got, err)
	}
	// An unpin of the old pin doesn't clear the new one.
	if err := s.Unpin(ctx, fresh, first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unpin a stale pin: %v", err)
	}
	if err := s.DeleteMolt(ctx, fresh, second); err != nil {
		t.Fatal(err)
	}
	if got, err := s.PinnedMolt(ctx, reload(t, s, karen)); err != nil || got != nil {
		t.Fatalf("pinned after delete: %+v, %v", got, err)
	}
	if err := s.Unpin(ctx, reload(t, s, karen), second.ID); err != nil {
		t.Fatalf("unpin: %v", err)
	}
	if fresh := reload(t, s, karen); fresh.PinnedMoltID != "" {
		t.Fatalf("still pinned: %q", fresh.PinnedMoltID)
	}
}

func TestMarksAndUndoRemolt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	author := mustCrab(t, s, "plankton")
	karen := mustCrab(t, s, "karen")
	a, _ := s.CreateMolt(ctx, author, "one")
	b, _ := s.CreateMolt(ctx, author, "two")
	c, _ := s.CreateMolt(ctx, author, "three")
	re, err := s.Remolt(ctx, karen, a)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LikeMolt(ctx, karen, a); err != nil {
		t.Fatal(err)
	}
	if err := s.Bookmark(ctx, karen, c); err != nil {
		t.Fatal(err)
	}
	got, err := s.MarksOn(ctx, karen.ID, []string{a.ID, b.ID, a.ID, c.ID})
	want := map[string]Marks{a.ID: {Liked: true, RemoltedAs: re.ID}, c.ID: {Bookmarked: true}}
	if err != nil || len(got) != 2 || got[a.ID] != want[a.ID] || got[c.ID] != want[c.ID] {
		t.Fatalf("marks: %+v, %v", got, err)
	}
	if _, err := s.UndoRemolt(ctx, karen, b); !errors.Is(err, ErrNotFound) {
		t.Fatalf("undo a remolt that isn't there: %v", err)
	}
	id, err := s.UndoRemolt(ctx, reload(t, s, karen), a)
	if err != nil || id != re.ID {
		t.Fatalf("undo: %q, %v", id, err)
	}
	if got, _ := s.MoltByID(ctx, a.ID); got.RemoltCount != 0 {
		t.Fatalf("remolt count after undo: %d", got.RemoltCount)
	}
	if got, _ := s.MarksOn(ctx, karen.ID, []string{a.ID}); got[a.ID].RemoltedAs != "" {
		t.Fatalf("still remolted: %v", got)
	}
	if _, err := s.Remolt(ctx, reload(t, s, karen), a); err != nil {
		t.Fatalf("remolt again after undo: %v", err)
	}
}

func TestRateLimitWindows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for want := 1; want <= 3; want++ {
		n, err := s.Hit(ctx, "login-ip", "203.0.113.7", 15*time.Minute)
		if err != nil || n != want {
			t.Fatalf("hit %d: got %d, %v", want, n, err)
		}
	}
	if n, _ := s.Count(ctx, "login-ip", "203.0.113.7", 15*time.Minute); n != 3 {
		t.Fatalf("count = %d, want 3", n)
	}
	s.now = func() time.Time { return time.Now().UTC().Add(16 * time.Minute) }
	if n, _ := s.Count(ctx, "login-ip", "203.0.113.7", 15*time.Minute); n != 0 {
		t.Fatalf("next window count = %d, want 0", n)
	}
}

func TestEditMolt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	karen := mustCrab(t, s, "karen")
	plankton := mustCrab(t, s, "plankton")
	m, err := s.CreateMolt(ctx, plankton, "Step one %plan %formula")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EditMolt(ctx, karen, m, "mine now"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("edit someone else's molt: %v", err)
	}

	edited, err := s.EditMolt(ctx, plankton, m, "Step two %formula %chumbucket with @karen")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.MoltByID(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Edited || got.Content != edited.Content || !slices.Equal(got.Tags, []string{"formula", "chumbucket"}) ||
		!slices.Equal(got.Mentions, []string{"karen"}) {
		t.Fatalf("after edit: %+v", got)
	}
	for tag, want := range map[string]int{"plan": 0, "formula": 1, "chumbucket": 1} {
		if tagged, err := s.MoltsWithTag(ctx, tag, 10); err != nil || len(tagged) != want {
			t.Fatalf("%%%s: %d molts, %v", tag, len(tagged), err)
		}
	}

	// An edit based on text that has since changed loses.
	if _, err := s.EditMolt(ctx, plankton, m, "stale"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale edit: %v", err)
	}
	// Dropping every tag and mention clears them.
	if _, err := s.EditMolt(ctx, plankton, got, "Nothing to see"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.MoltByID(ctx, m.ID); len(got.Tags) != 0 || len(got.Mentions) != 0 {
		t.Fatalf("tags %q, mentions %q", got.Tags, got.Mentions)
	}
	if tagged, _ := s.MoltsWithTag(ctx, "formula", 10); len(tagged) != 0 {
		t.Fatalf("%%formula still lists %d molts", len(tagged))
	}

	s.now = func() time.Time { return time.Now().UTC().Add(EditWindow) }
	latest, _ := s.MoltByID(ctx, m.ID)
	if _, err := s.EditMolt(ctx, plankton, latest, "too late"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("edit after the window: %v", err)
	}
}

func TestAvatars(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	karen := mustCrab(t, s, "karen")
	if !avatar.Valid(karen.Avatar) {
		t.Fatalf("signup avatar: %q", karen.Avatar)
	}
	old := karen.Avatar
	code, err := s.RerollAvatar(ctx, karen)
	if err != nil || code == old || !avatar.Valid(code) {
		t.Fatalf("reroll: %s %v (was %s)", code, err, old)
	}
	fresh, err := s.CrabByID(ctx, karen.ID)
	if err != nil || fresh.Avatar != code {
		t.Fatalf("stored: %+v %v", fresh, err)
	}
	again, err := s.EnsureAvatar(ctx, fresh)
	if err != nil || again != code {
		t.Fatalf("ensure keeps it: %s %v", again, err)
	}
}

func TestNSFW(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	karen := mustCrab(t, s, "karen")
	m, err := s.CreateMolt(ctx, karen, "spicy", WithNSFW(true))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.MoltByID(ctx, m.ID); got == nil || !got.NSFW {
		t.Fatalf("label not stored: %+v", got)
	}
	reply, err := s.Reply(ctx, karen, m, "also spicy", WithNSFW(true))
	if err != nil || !reply.NSFW {
		t.Fatalf("reply: %+v %v", reply, err)
	}
	quote, err := s.Quote(ctx, karen, m, "mild")
	if err != nil || quote.NSFW {
		t.Fatalf("quote: %+v %v", quote, err)
	}

	if err := s.SetMoltNSFW(ctx, m, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.MoltByID(ctx, m.ID); got.NSFW {
		t.Fatal("label not removed")
	}
	if err := s.SetMoltNSFW(ctx, m, true); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteMolt(ctx, karen, m); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMoltNSFW(ctx, m, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("labelling a deleted molt: %v", err)
	}

	want := ContentFilters{ShowNSFW: true, MutedWords: []string{"chum", "plankton"}}
	if err := s.SetContentFilters(ctx, karen, want); err != nil {
		t.Fatal(err)
	}
	if fresh, _ := s.CrabByID(ctx, karen.ID); !fresh.ShowNSFW || !slices.Equal(fresh.MutedWords, want.MutedWords) {
		t.Fatalf("filters not stored: %+v", fresh.ContentFilters)
	}
	if err := s.SetContentFilters(ctx, karen, ContentFilters{}); err != nil {
		t.Fatal(err)
	}
	if fresh, _ := s.CrabByID(ctx, karen.ID); fresh.ShowNSFW || len(fresh.MutedWords) != 0 {
		t.Fatalf("filters not cleared: %+v", fresh.ContentFilters)
	}
}

func TestVerified(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	karen := mustCrab(t, s, "karen")
	if err := s.SetVerified(ctx, karen, true); err != nil {
		t.Fatal(err)
	}
	crabs, _, err := s.ListCrabs(ctx, 10)
	if err != nil || len(crabs) != 1 || !crabs[0].Verified {
		t.Fatalf("listed: %+v %v", crabs, err)
	}
	if err := s.SetVerified(ctx, karen, false); err != nil {
		t.Fatal(err)
	}
	if fresh, _ := s.CrabByID(ctx, karen.ID); fresh.Verified {
		t.Fatal("still verified")
	}
}

func TestLinkCards(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	good := LinkCard{URL: "https://krabber.net/", Title: "Krabber", Description: "Molts", Host: "krabber.net"}
	if err := s.PutLinkCard(ctx, good); err != nil {
		t.Fatal(err)
	}
	if err := s.PutLinkCard(ctx, LinkCard{URL: "https://broken.test/", Title: "kept?", Failed: true}); err != nil {
		t.Fatal(err)
	}
	cards, err := s.LinkCards(ctx, []string{"https://krabber.net/", "https://broken.test/", "https://krabber.net/", "https://new.test/", ""})
	if err != nil {
		t.Fatal(err)
	}
	if c := cards["https://krabber.net/"]; c.Title != "Krabber" || c.Host != "krabber.net" || c.Failed {
		t.Errorf("good card: %+v", c)
	}
	if c, ok := cards["https://broken.test/"]; !ok || !c.Failed || c.Title != "" {
		t.Errorf("failed card: %+v %v", c, ok)
	}
	if _, ok := cards["https://new.test/"]; ok || len(cards) != 2 {
		t.Errorf("cards: %+v", cards)
	}

	s.now = func() time.Time { return time.Now().Add(31 * 24 * time.Hour) }
	if cards, _ := s.LinkCards(ctx, []string{"https://krabber.net/"}); len(cards) != 0 {
		t.Error("an expired card was returned")
	}
}

func TestPages(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	karen := mustCrab(t, s, "karen")
	for _, text := range []string{"one", "two", "three"} {
		if _, err := s.CreateMolt(ctx, karen, text); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.MoltsByOwnerPage(ctx, karen.ID, "", 2)
	if err != nil || len(first.Molts) != 2 || first.Molts[0].Content != "three" || first.Next == "" {
		t.Fatalf("first page: %+v, %v", first, err)
	}
	second, err := s.MoltsByOwnerPage(ctx, karen.ID, first.Next, 2)
	if err != nil || len(second.Molts) != 1 || second.Molts[0].Content != "one" || second.Next != "" {
		t.Fatalf("second page: %+v, %v", second, err)
	}
	sea, err := s.SeaPage(ctx, "", 2)
	if err != nil || len(sea.Molts) != 2 || sea.Next == "" {
		t.Fatalf("sea first page: %+v, %v", sea, err)
	}
	rest, err := s.SeaPage(ctx, sea.Next, 2)
	if err != nil || len(rest.Molts) != 1 || rest.Next != "" {
		t.Fatalf("sea second page: %+v, %v", rest, err)
	}
	if _, err := s.MoltsByOwnerPage(ctx, karen.ID, "not-a-cursor", 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bad cursor: %v", err)
	}
}

func TestNewer(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	karen := mustCrab(t, s, "karen")
	var ids []string
	for _, text := range []string{"oldest", "middle", "newest"} {
		m, err := s.CreateMolt(ctx, karen, text)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.AddToTrenches(ctx, m, []string{karen.ID}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
	}
	oldest, newest := ids[0], ids[2]
	n, err := s.SeaNewer(ctx, newest, 20)
	if err != nil || n != 0 {
		t.Fatalf("sea at top: %d, %v", n, err)
	}
	n, err = s.SeaNewer(ctx, oldest, 20)
	if err != nil || n != 2 {
		t.Fatalf("sea since oldest: %d, %v", n, err)
	}
	n, err = s.TrenchNewer(ctx, karen.ID, newest, 20)
	if err != nil || n != 0 {
		t.Fatalf("trench at top: %d, %v", n, err)
	}
	n, err = s.TrenchNewer(ctx, karen.ID, oldest, 20)
	if err != nil || n != 2 {
		t.Fatalf("trench since oldest: %d, %v", n, err)
	}
}
