// Command crabctl runs account operations that must not be possible from the
// web, such as appointing admins. Point it at a table with TABLE_NAME (and
// DYNAMO_ENDPOINT for DynamoDB Local); prod uses the caller's AWS profile:
//
//	AWS_PROFILE=krabber-admin TABLE_NAME=krabber-prod go run ./cmd/crabctl role <username> admin
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

const usage = `usage: crabctl role <username> <admin|moderator|none>`

func main() {
	if len(os.Args) != 4 || os.Args[1] != "role" {
		log.Fatal(usage)
	}
	name, role := os.Args[2], os.Args[3]
	switch role {
	case store.RoleAdmin, store.RoleModerator:
	case "none":
		role = ""
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
	if err := s.SetRole(ctx, c, role); err != nil {
		log.Fatal(err)
	}
	if err := s.LogModAction(ctx, store.ModAction{Moderator: "crabctl", Action: "set_role", CrabID: c.ID, Crab: c.UserName, Note: valueOr(role, "none")}); err != nil {
		log.Printf("warning: role set but not logged: %v", err)
	}
	fmt.Printf("@%s (%s) in %s: role %s\n", c.UserName, c.ID, table, valueOr(role, "none"))
}

func valueOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
