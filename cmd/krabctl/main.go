// Command krabctl runs account operations that must not be possible from the
// web, such as appointing admins and minting API keys. Point it at a table with
// TABLE_NAME (and DYNAMO_ENDPOINT for DynamoDB Local); prod uses the caller's
// AWS profile:
//
//	AWS_PROFILE=krabber-admin TABLE_NAME=krabber-prod go run ./cmd/krabctl role <username> admin
//	AWS_PROFILE=krabber-admin TABLE_NAME=krabber-prod go run ./cmd/krabctl verify <username> on
//	AWS_PROFILE=krabber-admin TABLE_NAME=krabber-prod go run ./cmd/krabctl trophy <username> contributor
//	AWS_PROFILE=krabber-admin TABLE_NAME=krabber-prod go run ./cmd/krabctl bot scuttle scuttle@krabber.net
//	AWS_PROFILE=krabber-admin TABLE_NAME=krabber-prod go run ./cmd/krabctl apikey scuttle "scuttle bot"
//	AWS_PROFILE=krabber-admin TABLE_NAME=krabber-prod go run ./cmd/krabctl apikeys scuttle
//	AWS_PROFILE=krabber-admin TABLE_NAME=krabber-prod go run ./cmd/krabctl apikey-revoke scuttle <key-id>
//	AWS_PROFILE=krabber-admin TABLE_NAME=krabber-prod go run ./cmd/krabctl form scuttle couplet
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/t0ul/krabber-net/internal/auth"
	"github.com/t0ul/krabber-net/internal/platform"
	"github.com/t0ul/krabber-net/internal/store"
	"github.com/t0ul/krabber-net/internal/trophies"
	"github.com/t0ul/krabber-net/internal/writing"
)

const usage = `usage: krabctl role <username> <admin|moderator|none>
       krabctl verify <username> <on|off>
       krabctl trophy <username> <trophy-id>
       krabctl bot <username> <email> [display] [bio]  create an activated bot account
       krabctl profile <username> <display> [bio]       set a crab's display name (and bio)
       krabctl apikey <username> <name> [scope]  mint an API key (shown once); default scope write:molts
       krabctl apikeys <username>                list a crab's API keys
       krabctl apikey-revoke <username> <key-id> stop a key working
       krabctl form <username> <form-id|note>    the form the crab's molts start as (a haiku unless set)`

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		log.Fatal(usage)
	}
	cmd := args[0]

	table := os.Getenv("TABLE_NAME")
	if table == "" {
		log.Fatal("TABLE_NAME is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg, err := platform.AWSConfig(ctx, "us-east-2")
	if err != nil {
		log.Fatal(err)
	}
	s := store.New(platform.DynamoDB(cfg, os.Getenv("DYNAMO_ENDPOINT")), table)

	switch cmd {
	case "role", "verify", "trophy":
		runModeration(ctx, s, table, args)
	case "bot":
		runBot(ctx, s, table, args)
	case "profile":
		runProfile(ctx, s, args)
	case "apikey":
		runAPIKey(ctx, s, args)
	case "apikeys":
		runAPIKeys(ctx, s, args)
	case "apikey-revoke":
		runAPIKeyRevoke(ctx, s, args)
	case "form":
		runForm(ctx, s, args)
	default:
		log.Fatal(usage)
	}
}

// runModeration handles the role, verify and trophy commands.
func runModeration(ctx context.Context, s *store.Store, table string, args []string) {
	if len(args) != 3 {
		log.Fatal(usage)
	}
	cmd, name, value := args[0], args[1], args[2]
	_, isTrophy := trophies.Get(value)
	switch {
	case cmd == "role" && (value == store.RoleAdmin || value == store.RoleModerator || value == "none"):
	case cmd == "verify" && (value == "on" || value == "off"):
	case cmd == "trophy" && isTrophy:
	default:
		log.Fatal(usage)
	}

	c, err := s.CrabByUsername(ctx, name)
	if err != nil {
		log.Fatalf("find %q in %q: %v", name, table, err) //nolint:gosec // the operator's own arguments, printed to their terminal
	}
	entry := store.ModAction{Moderator: "krabctl", CrabID: c.ID, Crab: c.UserName}
	grant := ""
	switch cmd {
	case "role":
		role := value
		if role == "none" {
			role = ""
		}
		err = s.SetRole(ctx, c, role)
		entry.Action, entry.Note = "set_role", value
		if role != "" {
			grant = "unlimited-power"
		}
	case "verify":
		err = s.SetVerified(ctx, c, value == "on")
		entry.Action = "verify"
		if value == "off" {
			entry.Action = "unverify"
		}
	case "trophy":
		grant = value
		entry.Action, entry.Note = "award_trophy", value
	}
	if err != nil {
		log.Fatal(err)
	}
	if grant != "" {
		ok, err := s.AwardTrophy(ctx, c, grant)
		switch {
		case err != nil:
			log.Fatal(err)
		case ok:
			if err := s.AddNotification(ctx, store.Notification{RecipientID: c.ID, Type: store.NotifyTrophy, Actor: "Krabber", Snippet: grant}); err != nil {
				log.Printf("warning: awarded but not notified: %v", err)
			}
		case cmd == "trophy":
			log.Fatalf("@%s already has %s", c.UserName, grant) //nolint:gosec // the operator's own arguments, printed to their terminal
		}
	}
	if err := s.LogModAction(ctx, entry); err != nil {
		log.Printf("warning: done but not logged: %v", err)
	}
	fmt.Printf("@%s (%s) in %s: %s %s\n", c.UserName, c.ID, table, cmd, value)
}

// runBot creates an activated account for a bot. The bot never signs in through
// the web (its password is random and discarded); it posts through the API with
// a key from `krabctl apikey`.
func runBot(ctx context.Context, s *store.Store, table string, args []string) {
	if len(args) < 3 || len(args) > 5 {
		log.Fatal(usage)
	}
	name, email := args[1], args[2]
	display, bio := "", ""
	if len(args) >= 4 {
		display = args[3]
	}
	if len(args) == 5 {
		bio = args[4]
	}

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		log.Fatal(err)
	}
	hash, err := auth.HashPassword(hex.EncodeToString(secret))
	if err != nil {
		log.Fatal(err)
	}

	c, err := s.CreateCrab(ctx, name, email, hash)
	switch {
	case errors.Is(err, store.ErrDuplicateEmail), errors.Is(err, store.ErrDuplicateUsername):
		log.Fatalf("@%s or %s already exists", name, email) //nolint:gosec // the operator's own arguments
	case err != nil:
		log.Fatal(err)
	}
	if err := s.ActivateCrab(ctx, c.ID); err != nil {
		log.Fatal(err)
	}
	// Set the display name / bio only if given; otherwise the crab shows as
	// @username. (No hardcoded default, so bots aren't all named alike.)
	if display != "" || bio != "" {
		if err := s.UpdateProfile(ctx, c, store.Profile{DisplayName: display, Bio: bio}); err != nil {
			log.Printf("warning: created but profile not set: %v", err)
		}
	}
	fmt.Printf("@%s (%s) in %s: bot account created and activated\n", c.UserName, c.ID, table)
	fmt.Printf("next: krabctl apikey %s \"%s bot\"\n", name, name)
}

// runProfile sets a crab's display name (and optionally bio), preserving the
// rest of the profile.
func runProfile(ctx context.Context, s *store.Store, args []string) {
	if len(args) != 3 && len(args) != 4 {
		log.Fatal(usage)
	}
	name, display := args[1], args[2]
	c, err := s.CrabByUsername(ctx, name)
	if err != nil {
		log.Fatalf("find %q: %v", name, err) //nolint:gosec // the operator's own arguments
	}
	p := c.Profile // keep existing bio/location/website/fun facts
	p.DisplayName = display
	if len(args) == 4 {
		p.Bio = args[3]
	}
	if err := s.UpdateProfile(ctx, c, p); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("@%s: display name set to %q\n", c.UserName, display)
}

// runAPIKey mints a key and prints it once.
func runAPIKey(ctx context.Context, s *store.Store, args []string) {
	if len(args) != 3 && len(args) != 4 {
		log.Fatal(usage)
	}
	name, keyName := args[1], args[2]
	scope := store.ScopeWriteMolts
	if len(args) == 4 {
		scope = args[3]
	}
	c, err := s.CrabByUsername(ctx, name)
	if err != nil {
		log.Fatalf("find %q: %v", name, err) //nolint:gosec // the operator's own arguments
	}
	plaintext, key, err := s.CreateAPIKey(ctx, c, keyName, []string{scope})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("@%s key %s (%s), scope %s\n", c.UserName, key.ID, keyName, scope)
	fmt.Printf("\n  %s\n\n", plaintext)
	fmt.Println("Copy it now: it is shown once and only the hash is stored.")
}

// runAPIKeys lists a crab's keys.
func runAPIKeys(ctx context.Context, s *store.Store, args []string) {
	if len(args) != 2 {
		log.Fatal(usage)
	}
	c, err := s.CrabByUsername(ctx, args[1])
	if err != nil {
		log.Fatalf("find %q: %v", args[1], err) //nolint:gosec // the operator's own arguments
	}
	keys, err := s.ListAPIKeys(ctx, c.ID)
	if err != nil {
		log.Fatal(err)
	}
	if len(keys) == 0 {
		fmt.Printf("@%s has no API keys\n", c.UserName)
		return
	}
	for _, k := range keys {
		state := "active"
		if k.Revoked {
			state = "revoked"
		}
		used := "never"
		if k.LastUsedAt != 0 {
			used = time.Unix(k.LastUsedAt, 0).UTC().Format(time.RFC3339)
		}
		fmt.Printf("%s  %-20s %-8s scopes=%v created=%s last-used=%s\n",
			k.ID, k.Name, state, k.Scopes, time.Unix(k.CreatedAt, 0).UTC().Format(time.DateOnly), used)
	}
}

// runAPIKeyRevoke stops a key working.
func runAPIKeyRevoke(ctx context.Context, s *store.Store, args []string) {
	if len(args) != 3 {
		log.Fatal(usage)
	}
	c, err := s.CrabByUsername(ctx, args[1])
	if err != nil {
		log.Fatalf("find %q: %v", args[1], err) //nolint:gosec // the operator's own arguments
	}
	if err := s.RevokeAPIKey(ctx, c.ID, args[2]); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			log.Fatalf("@%s has no key %s", c.UserName, args[2]) //nolint:gosec // the operator's own arguments
		}
		log.Fatal(err)
	}
	fmt.Printf("@%s key %s revoked\n", c.UserName, args[2])
}

// runForm sets the writing form a crab's molts start as: for bot accounts,
// which never see the settings page. An API post that names no form takes
// it; "note" means a plain note.
func runForm(ctx context.Context, s *store.Store, args []string) {
	if len(args) != 3 {
		log.Fatal(usage)
	}
	name, id := args[1], args[2]
	lookup := id
	if id == store.FormNote {
		lookup = ""
	}
	if _, ok := writing.ByID(lookup); !ok || id == "" {
		log.Fatalf("%q is not a writing form (see /forms) or note", id) //nolint:gosec // the operator's own argument
	}
	c, err := s.CrabByUsername(ctx, name)
	if err != nil {
		log.Fatalf("find %q: %v", name, err) //nolint:gosec // the operator's own argument
	}
	if err := s.SetDefaultForm(ctx, c, id); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s's molts now start as %s\n", c.UserName, id)
}
