// Command devseed creates the table in DynamoDB Local and adds a few activated
// sample crabs (password "crabcakes123") that follow each other. Dev only.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/t0ul/krabber-net/internal/auth"
	"github.com/t0ul/krabber-net/internal/platform"
	"github.com/t0ul/krabber-net/internal/store"
)

func main() {
	endpoint := os.Getenv("DYNAMO_ENDPOINT")
	table := os.Getenv("TABLE_NAME")
	if endpoint == "" || table == "" {
		log.Fatal("DYNAMO_ENDPOINT and TABLE_NAME are required (devseed only runs against DynamoDB Local)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cfg, err := platform.AWSConfig(ctx, "us-east-2")
	if err != nil {
		log.Fatal(err)
	}
	db := platform.DynamoDB(cfg, endpoint)
	if err := store.EnsureTable(ctx, db, table); err != nil {
		log.Fatal(err)
	}
	s := store.New(db, table)

	hash, err := auth.HashPassword("crabcakes123")
	if err != nil {
		log.Fatal(err)
	}

	crew := mustCrabs(ctx, s, hash, "mrkrabs", "spongebob", "plankton")
	// Extra crabs nobody follows automatically, so "who to follow" isn't empty
	// after the crew follow each other.
	extras := mustCrabs(ctx, s, hash, "sandy", "patrick", "squidward")

	for _, a := range crew {
		for _, b := range crew {
			if a.ID == b.ID {
				continue
			}
			a, _ = s.CrabByKey(ctx, a.PK, a.SK)
			b, _ = s.CrabByKey(ctx, b.PK, b.SK)
			if err := s.Follow(ctx, a, b); err != nil && !errors.Is(err, store.ErrAlreadyExists) {
				log.Fatal(err)
			}
		}
	}
	all := append(append([]*store.Crab{}, crew...), extras...)
	seedMolts(ctx, s, all)
	seedNotifications(ctx, s, all)
	fmt.Printf("table %s ready; sign in as any crab with password crabcakes123\n", table)
}

func mustCrabs(ctx context.Context, s *store.Store, hash []byte, names ...string) []*store.Crab {
	var crabs []*store.Crab
	for _, name := range names {
		c, err := s.CreateCrab(ctx, name, name+"@krabber.test", hash)
		switch {
		case errors.Is(err, store.ErrDuplicateEmail), errors.Is(err, store.ErrDuplicateUsername):
			c, err = s.CrabByEmail(ctx, name+"@krabber.test")
			if err != nil {
				log.Fatal(err)
			}
		case err != nil:
			log.Fatal(err)
		}
		if err := s.ActivateCrab(ctx, c.ID); err != nil {
			log.Fatal(err)
		}
		crabs = append(crabs, c)
		fmt.Printf("crab %-10s id=%s email=%s\n", c.UserName, c.ID, c.Email)
	}
	return crabs
}

// seedMolts adds a few molts, likes, a remolt and a comment the first time the
// table is seeded, so feeds, counts and trending have something to show.
func seedMolts(ctx context.Context, s *store.Store, crabs []*store.Crab) {
	lines := map[string]string{
		"mrkrabs":   "I like money.",
		"spongebob": "I'm ready! I'm ready! I'm ready!",
		"plankton":  "The Krabby Patty formula will be MINE.",
		"sandy":     "Karate island, here I come.",
		"patrick":   "The inner machinations of my mind are an enigma.",
		"squidward": "I hate all of you.",
	}
	byName := map[string]*store.Molt{}
	created := map[string]bool{}
	for _, c := range crabs {
		if existing, err := s.MoltsByOwner(ctx, c.ID, 1); err != nil {
			log.Fatal(err)
		} else if len(existing) > 0 {
			continue
		}
		line := lines[c.UserName]
		if line == "" {
			continue
		}
		m, err := s.CreateMolt(ctx, c, line)
		if err != nil {
			log.Fatal(err)
		}
		if err := s.AddToTrenches(ctx, m, followerIDs(ctx, s, c)); err != nil {
			log.Fatal(err)
		}
		if err := s.ClearFanout(ctx, m); err != nil {
			log.Fatal(err)
		}
		byName[c.UserName] = m
		created[c.UserName] = true
	}
	ready := byName["spongebob"]
	if ready == nil || !created["spongebob"] {
		return
	}
	for _, c := range crabs {
		if _, err := s.ToggleLike(ctx, c, ready); err != nil {
			log.Fatal(err)
		}
	}
	if _, err := s.Remolt(ctx, crabs[0], ready); err != nil && !errors.Is(err, store.ErrAlreadyExists) {
		log.Fatal(err)
	}
	if _, err := s.AddComment(ctx, crabs[2], ready, "Ugh. Barnacles."); err != nil {
		log.Fatal(err)
	}
}

// seedNotifications gives spongebob the notifications the seeded likes,
// follows and remolt would have sent. Likes, follows and remolts are recorded
// once per actor, so running the seed again adds nothing.
func seedNotifications(ctx context.Context, s *store.Store, crabs []*store.Crab) {
	sponge := crabs[1]
	molts, err := s.MoltsByOwner(ctx, sponge.ID, 100)
	if err != nil {
		log.Fatal(err)
	}
	var ready *store.Molt
	for i := range molts {
		if !molts[i].Remolt && molts[i].Content == "I'm ready! I'm ready! I'm ready!" {
			ready = &molts[i]
		}
	}
	add := func(n store.Notification) {
		n.RecipientID = sponge.ID
		if err := s.AddNotification(ctx, n); err != nil {
			log.Fatal(err)
		}
	}
	for _, c := range []*store.Crab{crabs[0], crabs[2]} {
		add(store.Notification{Type: store.NotifyFollow, ActorID: c.ID, Actor: c.UserName})
	}
	if ready == nil {
		return
	}
	for _, c := range crabs[2:4] {
		add(store.Notification{Type: store.NotifyLike, ActorID: c.ID, Actor: c.UserName, MoltID: ready.ID, Snippet: store.Snippet(ready.Content)})
	}
	add(store.Notification{Type: store.NotifyRemolt, ActorID: crabs[0].ID, Actor: crabs[0].UserName, MoltID: ready.ID, Snippet: store.Snippet(ready.Content)})
}

func followerIDs(ctx context.Context, s *store.Store, c *store.Crab) []string {
	ids := []string{c.ID}
	err := s.EachFollowerPage(ctx, c.ID, func(page []string) error {
		ids = append(ids, page...)
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}
	return ids
}
