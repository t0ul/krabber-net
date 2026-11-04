// Command devseed creates the table in DynamoDB Local and adds a few activated
// sample crabs (password "crabcakes123") that follow each other. Every seeded
// molt takes a writing form, so the seeds also stand when the site requires
// one (MOLT_FORMS=required). Dev only.
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
	"github.com/t0ul/krabber-net/internal/writing"
)

// A seeded molt: its text and the writing form it takes.
type seed struct{ text, form string }

// ready is SpongeBob's molt, an envelope; the seeded likes and replies hang
// off it.
var ready = seed{"I'm ready!\nI'm ready! I'm ready!\nI'm ready! I'm ready! I'm ready!\nI'm ready!", "envelope"}

// molts are the first molt of each crab.
var molts = map[string]seed{
	"mrkrabs":   {"I like money, I like it a lot,\nmore than anything else I've got.", "couplet"},
	"spongebob": ready,
	"plankton":  {"The Krabby Patty formula will be MINE,\nand the Chum Bucket will finally have a line.", "couplet"},
	"sandy":     {"Karate Island\nhere I come, claws up, hi-yah\nTexas can wait, y'all", "haiku"},
	"patrick":   {"The inner machinations of my mind are an enigma.", "metaphor"},
	"squidward": {"the reef at sunrise\nall of you, still here, still loud\nI hate all of you", "haiku"},
}

// The thread under SpongeBob's molt: Plankton's reply, SpongeBob's answer,
// and Squidward's quote.
var (
	barnacles = seed{"Ugh.\n\nBarnacles, every single morning.\n\nSix a.m.", "three-panel"}
	rocks     = seed{"Barnacles are just krabs who love their rocks!", "metaphor"}
	sinceSix  = seed{"He has been saying this since six in the morning, like a foghorn with a day job.", "simile"}
)

// mustFit stops the seed if a molt breaks its form's exact rules, so a
// change to the catalogue shows up here and not as a refused edit later.
func mustFit(sd seed) seed {
	f, ok := writing.ByID(sd.form)
	if !ok {
		log.Fatalf("seed %q: no form %q", sd.text, sd.form)
	}
	if broken := writing.Broken(f.Check(sd.text)); len(broken) > 0 {
		log.Fatalf("seed %q is not a %s: %s (%s)", sd.text, f.Name, broken[0].Rule, broken[0].Note)
	}
	return sd
}

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
	// SpongeBob runs Crabmin in dev; Mr. Krabs moderates, so the
	// moderator-vs-admin rules have someone to try.
	for c, role := range map[*store.Crab]string{crew[1]: store.RoleAdmin, crew[0]: store.RoleModerator} {
		if err := s.SetRole(ctx, c, role); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("crab %-10s role=%s\n", c.UserName, role)
	}
	fmt.Printf("table %s ready; sign in as any krab with password crabcakes123\n", table)
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

// seedMolts adds a few molts, likes, a remolt and a short reply thread the first time the
// table is seeded, so feeds, counts and trending have something to show.
func seedMolts(ctx context.Context, s *store.Store, crabs []*store.Crab) {
	byName := map[string]*store.Molt{}
	created := map[string]bool{}
	for _, c := range crabs {
		if existing, err := s.MoltsByOwner(ctx, c.ID, 1); err != nil {
			log.Fatal(err)
		} else if len(existing) > 0 {
			continue
		}
		sd, ok := molts[c.UserName]
		if !ok {
			continue
		}
		sd = mustFit(sd)
		m, err := s.CreateMolt(ctx, c, sd.text, store.WithForm(sd.form))
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
	readyMolt := byName["spongebob"]
	if readyMolt == nil || !created["spongebob"] {
		return
	}
	for _, c := range crabs {
		if _, err := s.ToggleLike(ctx, c, readyMolt); err != nil {
			log.Fatal(err)
		}
	}
	if _, err := s.Remolt(ctx, crabs[0], readyMolt); err != nil && !errors.Is(err, store.ErrAlreadyExists) {
		log.Fatal(err)
	}
	readyMolt, err := s.MoltByKey(ctx, readyMolt.PK, readyMolt.SK)
	if err != nil {
		log.Fatal(err)
	}
	ugh, err := s.Reply(ctx, crabs[2], readyMolt, mustFit(barnacles).text, store.WithForm(barnacles.form))
	if err != nil {
		log.Fatal(err)
	}
	if _, err := s.Reply(ctx, crabs[1], ugh, mustFit(rocks).text, store.WithForm(rocks.form)); err != nil {
		log.Fatal(err)
	}
	quote, err := s.Quote(ctx, crabs[2], readyMolt, mustFit(sinceSix).text, store.WithForm(sinceSix.form))
	if err != nil {
		log.Fatal(err)
	}
	if err := s.AddToTrenches(ctx, quote, followerIDs(ctx, s, crabs[2])); err != nil {
		log.Fatal(err)
	}
	if err := s.ClearFanout(ctx, quote); err != nil {
		log.Fatal(err)
	}
}

// seedNotifications gives spongebob the notifications the seeded likes,
// follows and remolt would have sent. Likes, follows and remolts are recorded
// once per actor, so running the seed again adds nothing.
func seedNotifications(ctx context.Context, s *store.Store, crabs []*store.Crab) {
	sponge := crabs[1]
	own, err := s.MoltsByOwner(ctx, sponge.ID, 100)
	if err != nil {
		log.Fatal(err)
	}
	var readyMolt *store.Molt
	for i := range own {
		if !own[i].Remolt && own[i].Content == ready.text {
			readyMolt = &own[i]
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
	if readyMolt == nil {
		return
	}
	for _, c := range crabs[2:4] {
		add(store.Notification{Type: store.NotifyLike, ActorID: c.ID, Actor: c.UserName, MoltID: readyMolt.ID, Snippet: store.Snippet(readyMolt.Content)})
	}
	add(store.Notification{Type: store.NotifyRemolt, ActorID: crabs[0].ID, Actor: crabs[0].UserName, MoltID: readyMolt.ID, Snippet: store.Snippet(readyMolt.Content)})
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
