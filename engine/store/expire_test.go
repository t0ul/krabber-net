package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestExpireUnactivated(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	active := mustCrab(t, s, "sandy")
	squat, err := s.CreateCrab(ctx, "patrick", "patrick@krabber.test", []byte("hash"))
	if err != nil {
		t.Fatal(err)
	}

	if gone, err := s.ExpireUnactivated(ctx, time.Now().Add(-time.Hour)); err != nil || len(gone) != 0 {
		t.Fatalf("a fresh signup was expired: %v %v", gone, err)
	}
	gone, err := s.ExpireUnactivated(ctx, time.Now().Add(time.Hour))
	if err != nil || len(gone) != 1 || gone[0] != squat.ID {
		t.Fatalf("expired %v, %v; want only patrick", gone, err)
	}

	if _, err := s.CrabByKey(ctx, squat.PK, squat.SK); !errors.Is(err, ErrNotFound) {
		t.Errorf("the unactivated account is still there: %v", err)
	}
	if _, err := s.CreateCrab(ctx, "patrick", "patrick@krabber.test", []byte("hash")); err != nil {
		t.Errorf("the name and email should be free again: %v", err)
	}
	if _, err := s.CrabByKey(ctx, active.PK, active.SK); err != nil {
		t.Errorf("an activated account was removed: %v", err)
	}
}
