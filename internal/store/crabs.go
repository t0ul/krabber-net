package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// Crab is a user account.
type Crab struct {
	PK     string `dynamodbav:"PK"`
	SK     string `dynamodbav:"SK"`
	GSI2PK string `dynamodbav:"GSI2PK,omitempty"`
	GSI2SK string `dynamodbav:"GSI2SK,omitempty"`

	ID           string    `dynamodbav:"id"`
	UserName     string    `dynamodbav:"user_name"`
	Email        string    `dynamodbav:"email"`
	PasswordHash []byte    `dynamodbav:"password_hash"`
	CreatedAt    time.Time `dynamodbav:"created_at"`

	Activated bool `dynamodbav:"activated"`
	Banned    bool `dynamodbav:"banned"`
	Deleted   bool `dynamodbav:"deleted"`

	Profile

	FollowerCount  int `dynamodbav:"follower_count"`
	FollowingCount int `dynamodbav:"following_count"`
	MoltCount      int `dynamodbav:"molt_count"`
	// BlockLinks counts blocks this crab made or received; at 0 nothing
	// needs filtering and the block query is skipped.
	BlockLinks int `dynamodbav:"block_links"`

	// SessionsValidAfter (Unix seconds) invalidates every session created
	// before it: set on password change, reset and ban.
	SessionsValidAfter int64 `dynamodbav:"sessions_valid_after"`
}

// Profile is what a crab tells others about themselves.
type Profile struct {
	DisplayName string `dynamodbav:"display_name,omitempty"`
	Bio         string `dynamodbav:"bio,omitempty"`
	Location    string `dynamodbav:"location,omitempty"`
	Website     string `dynamodbav:"website,omitempty"`
}

// Profile field limits, in characters (the same as Crabber's).
const (
	MaxDisplayName = 64
	MaxBio         = 512
	MaxLocation    = 128
	MaxWebsite     = 512
)

// Name is the display name, or the username when none is set.
func (c Crab) Name() string {
	if c.DisplayName != "" {
		return c.DisplayName
	}
	return c.UserName
}

type usernameMarker struct {
	PK     string `dynamodbav:"PK"`
	SK     string `dynamodbav:"SK"`
	CrabID string `dynamodbav:"crab_id"`
}

// CanSignIn reports whether the account may hold a session.
func (c *Crab) CanSignIn() bool { return c.Activated && !c.Banned && !c.Deleted }

// CreateCrab registers a new, not yet activated account. Email and username
// are each unique; the username marker item enforces the latter.
func (s *Store) CreateCrab(ctx context.Context, username, email string, passwordHash []byte) (*Crab, error) {
	id := newID()
	c := &Crab{
		PK:           crabPK(email),
		SK:           crabSK(),
		GSI2PK:       crabIDKey(id),
		GSI2SK:       crabIDKey(id),
		ID:           id,
		UserName:     username,
		Email:        normalizeEmail(email),
		PasswordHash: passwordHash,
		CreatedAt:    s.now(),
	}
	crabItem, err := marshal(c)
	if err != nil {
		return nil, err
	}
	markerItem, err := marshal(usernameMarker{PK: usernamePK(username), SK: usernameSK(), CrabID: id})
	if err != nil {
		return nil, err
	}

	err = s.transact(ctx, s.putNew(crabItem), s.putNew(markerItem))
	switch {
	case cancelledAt(err, 0):
		return nil, ErrDuplicateEmail
	case cancelledAt(err, 1):
		return nil, ErrDuplicateUsername
	case err != nil:
		return nil, fmt.Errorf("create crab: %w", err)
	}
	return c, nil
}

// CrabByEmail returns the account for an email address.
func (s *Store) CrabByEmail(ctx context.Context, email string) (*Crab, error) {
	return s.CrabByKey(ctx, crabPK(email), crabSK())
}

// CrabByKey fetches a crab by its table key with a strongly consistent read.
func (s *Store) CrabByKey(ctx context.Context, pk, sk string) (*Crab, error) {
	var c Crab
	if err := s.getItem(ctx, pk, sk, &c); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get crab: %w", err)
	}
	return &c, nil
}

// CrabByUsername looks a crab up by username (any letter case).
func (s *Store) CrabByUsername(ctx context.Context, name string) (*Crab, error) {
	var m usernameMarker
	if err := s.getItem(ctx, usernamePK(name), usernameSK(), &m); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("crab by username: %w", err)
	}
	return s.CrabByID(ctx, m.CrabID)
}

// CrabByID looks a crab up by ID through GSI2.
func (s *Store) CrabByID(ctx context.Context, id string) (*Crab, error) {
	crabs, err := queryAll[Crab](ctx, s.db, &dynamodb.QueryInput{
		TableName:                 s.tableName(),
		IndexName:                 aws.String(gsiCrabByID),
		KeyConditionExpression:    aws.String("GSI2PK = :id"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":id": str(crabIDKey(id))},
	}, 1)
	if err != nil {
		return nil, fmt.Errorf("crab by id: %w", err)
	}
	if len(crabs) == 0 {
		return nil, ErrNotFound
	}
	return &crabs[0], nil
}

// ListCrabs returns up to limit accounts for the "who to follow" page, without
// password hashes or emails.
func (s *Store) ListCrabs(ctx context.Context, limit int) ([]Crab, error) {
	var out []Crab
	p := dynamodb.NewScanPaginator(s.db, &dynamodb.ScanInput{
		TableName:            s.tableName(),
		IndexName:            aws.String(gsiCrabByID),
		ProjectionExpression: aws.String("#id, #un, #frc, #fgc, #mc, #act, #ban, #del, #ca, #dn, #bio"),
		ExpressionAttributeNames: map[string]string{
			"#dn":  "display_name",
			"#bio": "bio",
			"#ca":  "created_at",
			"#id":  "id",
			"#un":  "user_name",
			"#frc": "follower_count",
			"#fgc": "following_count",
			"#mc":  "molt_count",
			"#act": "activated",
			"#ban": "banned",
			"#del": "deleted",
		},
	})
	for p.HasMorePages() && len(out) < limit {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list crabs: %w", err)
		}
		var crabs []Crab
		if err := attributevalue.UnmarshalListOfMaps(page.Items, &crabs); err != nil {
			return nil, fmt.Errorf("list crabs: %w", err)
		}
		for _, c := range crabs {
			if c.CanSignIn() {
				out = append(out, c)
			}
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ActivateCrab marks an account as activated.
func (s *Store) ActivateCrab(ctx context.Context, crabID string) error {
	c, err := s.CrabByID(ctx, crabID)
	if err != nil {
		return err
	}
	_, err = s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 s.tableName(),
		Key:                       keyOf(c.PK, c.SK),
		UpdateExpression:          aws.String("SET activated = :t"),
		ConditionExpression:       aws.String("attribute_exists(PK)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":t": boolean(true)},
	})
	if err != nil {
		return fmt.Errorf("activate crab: %w", err)
	}
	return nil
}

// UpdateProfile replaces the crab's profile fields; empty fields are removed.
func (s *Store) UpdateProfile(ctx context.Context, c *Crab, p Profile) error {
	fields := []struct{ attr, value string }{
		{"display_name", p.DisplayName},
		{"bio", p.Bio},
		{"location", p.Location},
		{"website", p.Website},
	}
	var set, remove []string
	names := map[string]string{}
	values := map[string]types.AttributeValue{}
	for i, f := range fields {
		name := fmt.Sprintf("#f%d", i)
		names[name] = f.attr
		if f.value == "" {
			remove = append(remove, name)
			continue
		}
		value := fmt.Sprintf(":v%d", i)
		values[value] = str(f.value)
		set = append(set, name+" = "+value)
	}
	expr := ""
	if len(set) > 0 {
		expr = "SET " + strings.Join(set, ", ")
	}
	if len(remove) > 0 {
		expr += " REMOVE " + strings.Join(remove, ", ")
	}
	in := &dynamodb.UpdateItemInput{
		TableName:                s.tableName(),
		Key:                      keyOf(c.PK, c.SK),
		UpdateExpression:         aws.String(strings.TrimSpace(expr)),
		ConditionExpression:      aws.String("attribute_exists(PK)"),
		ExpressionAttributeNames: names,
	}
	if len(values) > 0 {
		in.ExpressionAttributeValues = values
	}
	if _, err := s.db.UpdateItem(ctx, in); err != nil {
		return fmt.Errorf("update profile: %w", err)
	}
	return nil
}

// SetPassword stores a new password hash and ends every existing session. It
// returns the cut-off so the caller can keep its own session. The cut-off is
// a second ahead because sessions are stamped in whole seconds.
func (s *Store) SetPassword(ctx context.Context, c *Crab, hash []byte) (int64, error) {
	now := s.now().Unix() + 1
	_, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           s.tableName(),
		Key:                 keyOf(c.PK, c.SK),
		UpdateExpression:    aws.String("SET password_hash = :h, sessions_valid_after = :now"),
		ConditionExpression: aws.String("attribute_exists(PK)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":h":   &types.AttributeValueMemberB{Value: hash},
			":now": num(now),
		},
	})
	if err != nil {
		return 0, fmt.Errorf("set password: %w", err)
	}
	return now, nil
}

// SetBanned bans or unbans an account and ends all of its sessions.
func (s *Store) SetBanned(ctx context.Context, c *Crab, banned bool) error {
	_, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           s.tableName(),
		Key:                 keyOf(c.PK, c.SK),
		UpdateExpression:    aws.String("SET banned = :b, sessions_valid_after = :now"),
		ConditionExpression: aws.String("attribute_exists(PK)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":b":   boolean(banned),
			":now": num(s.now().Unix()),
		},
	})
	if err != nil {
		return fmt.Errorf("set banned: %w", err)
	}
	return nil
}
