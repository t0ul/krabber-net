package jobs

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/segmentio/ksuid"

	"github.com/t0ul/krabber-net/internal/linkcard"
	"github.com/t0ul/krabber-net/internal/platform"
	"github.com/t0ul/krabber-net/internal/store"
)

func newStore(t *testing.T) *store.Store {
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
	table := "krabber-jobstest-" + ksuid.New().String()
	if err := store.EnsureTable(ctx, db, table); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(table)})
	})
	return store.New(db, table)
}

func crab(t *testing.T, s *store.Store, name string) *store.Crab {
	t.Helper()
	ctx := context.Background()
	c, err := s.CreateCrab(ctx, name, name+"@krabber.test", []byte("h"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ActivateCrab(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	c, err = s.CrabByKey(ctx, c.PK, c.SK)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestFanoutAndSweep(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	r := New(s, slog.New(slog.NewTextHandler(io.Discard, nil)))

	author := crab(t, s, "mrkrabs")
	var fans []*store.Crab
	for _, name := range []string{"spongebob", "patrick", "squidward"} {
		f := crab(t, s, name)
		if err := s.Follow(ctx, f, author); err != nil {
			t.Fatal(err)
		}
		fans = append(fans, f)
	}

	// Direct fan-out, as the in-process worker does it.
	m, err := s.CreateMolt(ctx, author, "fresh krabby patties")
	if err != nil {
		t.Fatal(err)
	}
	r.fanout(ctx, m)
	for _, f := range fans {
		trench, err := s.Trench(ctx, f.ID, 10)
		if err != nil || len(trench) != 1 || trench[0].ID != m.ID {
			t.Fatalf("%s trench: %+v, %v", f.UserName, trench, err)
		}
	}

	// A molt whose fan-out was lost (instance replaced mid-deploy) is picked
	// up by the sweep once it's old enough.
	lost, err := s.CreateMolt(ctx, author, "left behind")
	if err != nil {
		t.Fatal(err)
	}
	pending, _ := s.PendingFanouts(ctx, time.Now().Add(-fanoutSweepMinAge), 10)
	if len(pending) != 0 {
		t.Fatalf("fresh molt must be left to the worker, got %d pending", len(pending))
	}
	pending, _ = s.PendingFanouts(ctx, time.Now().Add(time.Minute), 10)
	if len(pending) != 1 || pending[0].ID != lost.ID {
		t.Fatalf("pending: %+v", pending)
	}
	r.fanout(ctx, &pending[0])
	trench, _ := s.Trench(ctx, fans[0].ID, 10)
	if len(trench) != 2 || trench[0].ID != lost.ID {
		t.Fatalf("after sweep, newest first: %+v", trench)
	}
	if pending, _ := s.PendingFanouts(ctx, time.Now().Add(time.Minute), 10); len(pending) != 0 {
		t.Fatalf("still pending: %+v", pending)
	}
}

type fakeFetcher struct{ calls map[string]int }

func (f *fakeFetcher) Fetch(_ context.Context, url string) (linkcard.Card, error) {
	f.calls[url]++
	if url == "https://good.test/" {
		return linkcard.Card{URL: url, Title: "Good", Description: "A page", Host: "good.test"}, nil
	}
	return linkcard.Card{}, linkcard.ErrNoCard
}

func TestAwardShow(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	r := New(s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	karen := crab(t, s, "karen")

	r.awardShow(ctx)
	if got, _ := s.Trophies(ctx, karen.ID); len(got) != 0 {
		t.Fatalf("new crab got %+v", got)
	}
	r.now = func() time.Time { return karen.CreatedAt.AddDate(1, 0, 1) }
	r.awardShow(ctx)
	r.awardShow(ctx)
	got, err := s.Trophies(ctx, karen.ID)
	if err != nil || len(got) != 1 || got[0].TrophyID != "one-year" {
		t.Fatalf("after a year: %+v %v", got, err)
	}
	if n := <-r.notes; n.Type != store.NotifyTrophy || n.RecipientID != karen.ID || len(r.notes) != 0 {
		t.Errorf("notifications: %+v, %d more", n, len(r.notes))
	}
}

func TestLinkCards(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	r := New(s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	f := &fakeFetcher{calls: map[string]int{}}
	r.fetcher = f

	r.fetchCard(ctx, "https://good.test/")
	r.fetchCard(ctx, "https://bad.test/")
	cards, err := s.LinkCards(ctx, []string{"https://good.test/", "https://bad.test/"})
	if err != nil {
		t.Fatal(err)
	}
	if c := cards["https://good.test/"]; c.Title != "Good" || c.Host != "good.test" || c.Failed {
		t.Errorf("good: %+v", c)
	}
	if c := cards["https://bad.test/"]; !c.Failed {
		t.Errorf("bad: %+v", c)
	}
	r.fetchCard(ctx, "https://good.test/")
	r.fetchCard(ctx, "https://bad.test/")
	if f.calls["https://good.test/"] != 1 || f.calls["https://bad.test/"] != 1 {
		t.Errorf("cached cards were fetched again: %v", f.calls)
	}

	// Queueing the same URL twice keeps one entry.
	r.EnqueueCard("https://queued.test/")
	r.EnqueueCard("https://queued.test/")
	if len(r.cards) != 1 {
		t.Errorf("queue length %d", len(r.cards))
	}
}
