package web

import (
	"context"
	"testing"
	"time"

	"github.com/segmentio/ksuid"

	"github.com/t0ul/krabber-net/internal/store"
)

func TestDirectoryKeepsChangesMadeDuringAReload(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, name := range []string{"karen", "plankton"} {
		c, err := h.store.CreateCrab(ctx, name, name+"@krabber.test", []byte("h"))
		if err != nil {
			t.Fatal(err)
		}
		if err := h.store.ActivateCrab(ctx, c.ID); err != nil {
			t.Fatal(err)
		}
	}
	d := &directory{}
	logErr := func(err error) { t.Error(err) }
	if got := d.snapshot(ctx, h.store, logErr); len(got.crabs) != 2 || got.names["karen"] != "karen" {
		t.Fatalf("first load: %+v", got.crabs)
	}

	at := func(ago time.Duration) string {
		id, err := ksuid.FromParts(time.Now().Add(-ago), make([]byte, 16))
		if err != nil {
			t.Fatal(err)
		}
		return id.String()
	}
	// A krab activating and a molt arriving while a reload is running (which
	// read the table before they existed) survive the swap.
	d.mu.Lock()
	d.loading = true
	d.mu.Unlock()
	d.putCrab(store.Crab{ID: "newbie-id", UserName: "Newbie", Activated: true, Email: "secret@krabber.test"})
	d.addMolt(store.Molt{ID: at(time.Minute), Content: "fresh"})
	d.reload(ctx, h.store, logErr)
	got := d.snapshot(ctx, h.store, logErr)
	if c, ok := got.byID["newbie-id"]; !ok || got.names["newbie"] != "Newbie" || len(got.crabs) != 3 {
		t.Fatalf("change lost in the reload: %+v", got.crabs)
	} else if c.Email != "" {
		t.Error("the directory kept a krab's email")
	}
	if len(got.recent) != 1 || got.recent[0].Content != "fresh" {
		t.Fatalf("molt lost in the reload: %+v", got.recent)
	}

	// Molts stay newest first, and adding one twice keeps one.
	d.addMolt(store.Molt{ID: at(time.Hour), Content: "older"})
	d.addMolt(store.Molt{ID: at(0), Content: "newest"})
	d.addMolt(store.Molt{ID: at(0), Content: "newest"})
	if got := d.snapshot(ctx, h.store, logErr).recent; len(got) != 3 || got[0].Content != "newest" || got[2].Content != "older" {
		t.Errorf("recent order: %+v", got)
	}

	// A rename drops the old name; a ban drops the krab.
	d.putCrab(store.Crab{ID: "newbie-id", UserName: "Oldtimer", Activated: true})
	if got := d.snapshot(ctx, h.store, logErr); got.names["newbie"] != "" || got.names["oldtimer"] != "Oldtimer" {
		t.Errorf("names after rename: %v", got.names)
	}
	d.setGone("newbie-id", true)
	if got := d.snapshot(ctx, h.store, logErr); !got.gone["newbie-id"] || len(got.crabs) != 2 {
		t.Errorf("after ban: %d krabs, gone %v", len(got.crabs), got.gone)
	}
}
