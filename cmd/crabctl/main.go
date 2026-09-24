// Command crabctl runs account operations that must not be possible from the
// web, such as appointing admins. Point it at a table with TABLE_NAME (and
// DYNAMO_ENDPOINT for DynamoDB Local); prod uses the caller's AWS profile:
//
//	AWS_PROFILE=krabber-admin TABLE_NAME=krabber-prod go run ./cmd/crabctl role <username> admin
//	AWS_PROFILE=krabber-admin TABLE_NAME=krabber-prod go run ./cmd/crabctl verify <username> on
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/t0ul/krabber-net/internal/platform"
	"github.com/t0ul/krabber-net/internal/store"
)

const usage = `usage: crabctl role <username> <admin|moderator|none>
       crabctl verify <username> <on|off>`

func main() {
	if len(os.Args) != 4 {
		log.Fatal(usage)
	}
	cmd, name, value := os.Args[1], os.Args[2], os.Args[3]
	switch {
	case cmd == "role" && (value == store.RoleAdmin || value == store.RoleModerator || value == "none"):
	case cmd == "verify" && (value == "on" || value == "off"):
	default:
		log.Fatal(usage)
	}
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

	c, err := s.CrabByUsername(ctx, name)
	if err != nil {
		log.Fatalf("find %q in %q: %v", name, table, err) //nolint:gosec // the operator's own arguments, printed to their terminal
	}
	entry := store.ModAction{Moderator: "crabctl", CrabID: c.ID, Crab: c.UserName}
	switch cmd {
	case "role":
		role := value
		if role == "none" {
			role = ""
		}
		err = s.SetRole(ctx, c, role)
		entry.Action, entry.Note = "set_role", value
	case "verify":
		err = s.SetVerified(ctx, c, value == "on")
		entry.Action = "verify"
		if value == "off" {
			entry.Action = "unverify"
		}
	}
	if err != nil {
		log.Fatal(err)
	}
	if err := s.LogModAction(ctx, entry); err != nil {
		log.Printf("warning: done but not logged: %v", err)
	}
	fmt.Printf("@%s (%s) in %s: %s %s\n", c.UserName, c.ID, table, cmd, value)
}
