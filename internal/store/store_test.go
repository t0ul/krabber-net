package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/segmentio/ksuid"

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
	if c.UserName != "Bob" || c.Activated {
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
	if ids, _ := s.LikedIDs(ctx, b.ID, []string{m.ID, "other"}); !ids[m.ID] || ids["other"] {
		t.Fatalf("liked ids: %v", ids)
	}
}

func TestLikesAndComments(t *testing.T) {
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

	// Two comments in the same second no longer collide.
	for i := range 2 {
		if _, err := s.AddComment(ctx, liker, m, fmt.Sprintf("comment %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	comments, err := s.CommentsOn(ctx, m.ID, 10)
	if err != nil || len(comments) != 2 || comments[0].Content != "comment 0" {
		t.Fatalf("comments: %+v, %v", comments, err)
	}
	got, err := s.MoltByID(ctx, m.ID)
	if err != nil || got.LikeCount != 1 || got.CommentCount != 2 {
		t.Fatalf("counters: %+v, %v", got, err)
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
