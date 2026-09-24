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

	"github.com/t0ul/krabber-net/internal/avatar"
)

// Crab is a user account.
type Crab struct {
	PK     string `dynamodbav:"PK"`
	SK     string `dynamodbav:"SK"`
	GSI2PK string `dynamodbav:"GSI2PK,omitempty"`
	GSI2SK string `dynamodbav:"GSI2SK,omitempty"`
	GSI8PK string `dynamodbav:"GSI8PK,omitempty"` // purge queue, while a deleted account is cleaned up
	GSI8SK string `dynamodbav:"GSI8SK,omitempty"`

	ID           string    `dynamodbav:"id"`
	UserName     string    `dynamodbav:"user_name"`
	Email        string    `dynamodbav:"email"`
	PasswordHash []byte    `dynamodbav:"password_hash"`
	CreatedAt    time.Time `dynamodbav:"created_at"`

	Activated bool   `dynamodbav:"activated"`
	Banned    bool   `dynamodbav:"banned"`
	BanReason string `dynamodbav:"ban_reason,omitempty"`
	Deleted   bool   `dynamodbav:"deleted"`
	DeletedAt int64  `dynamodbav:"deleted_at,omitempty"` // Unix seconds
	Role      string `dynamodbav:"role,omitempty"`       // RoleAdmin, RoleModerator or empty
	Verified  bool   `dynamodbav:"verified,omitempty"`   // set by an admin; shows the badge

	Profile

	Avatar string `dynamodbav:"avatar,omitempty"` // seven-digit generated-crab code

	ContentFilters

	UsernameChangedAt int64 `dynamodbav:"username_changed_at,omitempty"` // Unix seconds of the last rename

	// The molt shown at the top of the crab's profile, if any.
	PinnedMoltID string `dynamodbav:"pinned_molt_id,omitempty"`
	PinnedMoltPK string `dynamodbav:"pinned_molt_pk,omitempty"`
	PinnedMoltSK string `dynamodbav:"pinned_molt_sk,omitempty"`

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
	FunFacts
}

// FunFacts are the extra fields of Crabber's "Full bio".
type FunFacts struct {
	Age       string `dynamodbav:"fun_age,omitempty"`
	Pronouns  string `dynamodbav:"fun_pronouns,omitempty"`
	Quote     string `dynamodbav:"fun_quote,omitempty"`
	Jam       string `dynamodbav:"fun_jam,omitempty"`
	Obsession string `dynamodbav:"fun_obsession,omitempty"`
	Remember  string `dynamodbav:"fun_remember,omitempty"`
	Emoji     string `dynamodbav:"fun_emoji,omitempty"`
}

// Empty reports whether no fun fact is filled in.
func (f FunFacts) Empty() bool { return f == FunFacts{} }

// ContentFilters are what a crab chose to see less of.
type ContentFilters struct {
	ShowNSFW   bool     `dynamodbav:"show_nsfw,omitempty"`   // show NSFW molts without a click
	MutedWords []string `dynamodbav:"muted_words,omitempty"` // lowercase; molts containing any are left out of lists
}

// Muted word limits, in characters for the lengths.
const (
	MaxMutedWords    = 100
	MaxMutedWordLen  = 64
	MaxMutedWordsLen = 2048
)

// Profile field limits, in characters (the same as Crabber's).
const (
	MaxDisplayName = 64
	MaxBio         = 512
	MaxLocation    = 128
	MaxWebsite     = 512
	MaxAge         = 32
	MaxPronouns    = 64
	MaxFunFact     = 256 // quote, jam, obsession, remember
	MaxEmoji       = 32
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
	// ExpiresAt is set on a name its crab changed away from: it keeps pointing
	// at them, and stays taken, until then.
	ExpiresAt int64 `dynamodbav:"expires_at,omitempty"`
}

// Username change limits.
const (
	UsernameChangeEvery = 30 * 24 * time.Hour // how often a crab can rename
	usernameHold        = 30 * 24 * time.Hour // how long an old name keeps redirecting
)

// ErrTooSoon is returned for a rename within UsernameChangeEvery of the last.
var ErrTooSoon = errors.New("store: too soon")

// putUsername claims a username marker. A marker whose hold has run out
// counts as free even before TTL deletes it; reclaimMine also lets crabID
// take back its own held name.
func (s *Store) putUsername(item map[string]types.AttributeValue, crabID string, reclaimMine bool) types.TransactWriteItem {
	cond := "attribute_not_exists(PK) OR (attribute_exists(expires_at) AND expires_at < :now)"
	values := map[string]types.AttributeValue{":now": num(s.now().Unix())}
	if reclaimMine {
		cond += " OR crab_id = :me"
		values[":me"] = str(crabID)
	}
	return types.TransactWriteItem{Put: &types.Put{
		TableName:                 s.tableName(),
		Item:                      item,
		ConditionExpression:       aws.String(cond),
		ExpressionAttributeValues: values,
	}}
}

// NextUsernameChange is when c may rename again; zero if they may now.
func (s *Store) NextUsernameChange(c *Crab) time.Time {
	if c.UsernameChangedAt == 0 {
		return time.Time{}
	}
	next := time.Unix(c.UsernameChangedAt, 0).Add(UsernameChangeEvery)
	if !next.After(s.now()) {
		return time.Time{}
	}
	return next
}

// ChangeUsername renames c in one transaction: the crab item, a marker for
// the new name, and a hold on the old marker so /krabs/<old> keeps finding
// c (and nobody else can take it) for usernameHold. Molts show names by
// crab ID, so nothing else changes. A change of case keeps the same marker.
func (s *Store) ChangeUsername(ctx context.Context, c *Crab, newName string) error {
	if !s.NextUsernameChange(c).IsZero() {
		return ErrTooSoon
	}
	now := s.now()
	items := []types.TransactWriteItem{{Update: &types.Update{
		TableName:           s.tableName(),
		Key:                 keyOf(c.PK, c.SK),
		UpdateExpression:    aws.String("SET user_name = :n, username_changed_at = :t"),
		ConditionExpression: aws.String("attribute_exists(PK) AND user_name = :old"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":n": str(newName), ":t": num(now.Unix()), ":old": str(c.UserName),
		},
	}}}
	if usernamePK(newName) != usernamePK(c.UserName) {
		item, err := marshal(usernameMarker{PK: usernamePK(newName), SK: usernameSK(), CrabID: c.ID})
		if err != nil {
			return err
		}
		items = append(items, s.putUsername(item, c.ID, true), types.TransactWriteItem{Update: &types.Update{
			TableName:                 s.tableName(),
			Key:                       keyOf(usernamePK(c.UserName), usernameSK()),
			UpdateExpression:          aws.String("SET expires_at = :hold"),
			ConditionExpression:       aws.String("crab_id = :me"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":hold": num(now.Add(usernameHold).Unix()), ":me": str(c.ID)},
		}})
	}
	err := s.transact(ctx, items...)
	switch {
	case cancelledAt(err, 0):
		return ErrNotFound
	case cancelledAt(err, 1):
		return ErrDuplicateUsername
	case err != nil:
		return fmt.Errorf("change username: %w", err)
	}
	c.UserName, c.UsernameChangedAt = newName, now.Unix()
	return nil
}

// Roles. Admins can do everything moderators can, plus act on moderators and
// appoint them.
const (
	RoleAdmin     = "admin"
	RoleModerator = "moderator"
)

// IsAdmin reports whether the crab is an admin.
func (c Crab) IsAdmin() bool { return c.Role == RoleAdmin }

// IsModerator reports whether the crab can moderate (admins included).
func (c Crab) IsModerator() bool { return c.Role == RoleAdmin || c.Role == RoleModerator }

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
	nameItem, err := marshal(usernameMarker{PK: usernamePK(username), SK: usernameSK(), CrabID: id})
	if err != nil {
		return nil, err
	}
	for range avatarAttempts {
		code, err := avatar.Random()
		if err != nil {
			return nil, err
		}
		c.Avatar = code
		crabItem, err := marshal(c)
		if err != nil {
			return nil, err
		}
		avItem, err := s.avatarItem(code, id)
		if err != nil {
			return nil, err
		}
		err = s.transact(ctx, s.putNew(crabItem), s.putUsername(nameItem, id, false), s.putNew(avItem))
		switch {
		case cancelledAt(err, 0):
			return nil, ErrDuplicateEmail
		case cancelledAt(err, 1):
			return nil, ErrDuplicateUsername
		case cancelledAt(err, 2):
			continue
		case err != nil:
			return nil, fmt.Errorf("create crab: %w", err)
		}
		return c, nil
	}
	return nil, fmt.Errorf("create crab: no free avatar")
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
	if m.ExpiresAt != 0 && m.ExpiresAt < s.now().Unix() {
		return nil, ErrNotFound
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

// ListCrabs returns up to limit accounts that can sign in, without password
// hashes or emails, and the IDs of banned and deleted accounts (whose molts
// stay hidden).
func (s *Store) ListCrabs(ctx context.Context, limit int) ([]Crab, map[string]bool, error) {
	var out []Crab
	gone := map[string]bool{}
	p := dynamodb.NewScanPaginator(s.db, &dynamodb.ScanInput{
		TableName:            s.tableName(),
		IndexName:            aws.String(gsiCrabByID),
		ProjectionExpression: aws.String("#id, #un, #frc, #fgc, #mc, #act, #ban, #del, #ca, #dn, #bio, #av, #ver"),
		ExpressionAttributeNames: map[string]string{
			"#ver": "verified",
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
			"#av":  "avatar",
		},
	})
	for p.HasMorePages() && len(out) < limit {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("list crabs: %w", err)
		}
		var crabs []Crab
		if err := attributevalue.UnmarshalListOfMaps(page.Items, &crabs); err != nil {
			return nil, nil, fmt.Errorf("list crabs: %w", err)
		}
		for _, c := range crabs {
			switch {
			case c.Banned || c.Deleted:
				gone[c.ID] = true
			case c.CanSignIn():
				out = append(out, c)
			}
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, gone, nil
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
		{"fun_age", p.Age},
		{"fun_pronouns", p.Pronouns},
		{"fun_quote", p.Quote},
		{"fun_jam", p.Jam},
		{"fun_obsession", p.Obsession},
		{"fun_remember", p.Remember},
		{"fun_emoji", p.Emoji},
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

// SetBanned bans (with a reason) or unbans an account and ends all of its
// sessions.
func (s *Store) SetBanned(ctx context.Context, c *Crab, banned bool, reason string) error {
	expr := "SET banned = :b, sessions_valid_after = :now, ban_reason = :r"
	values := map[string]types.AttributeValue{
		":b":   boolean(banned),
		":now": num(s.now().Unix() + 1),
		":r":   str(reason),
	}
	if !banned || reason == "" {
		expr = "SET banned = :b, sessions_valid_after = :now REMOVE ban_reason"
		delete(values, ":r")
	}
	_, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 s.tableName(),
		Key:                       keyOf(c.PK, c.SK),
		UpdateExpression:          aws.String(expr),
		ConditionExpression:       aws.String("attribute_exists(PK)"),
		ExpressionAttributeValues: values,
	})
	if err != nil {
		return fmt.Errorf("set banned: %w", err)
	}
	return nil
}

// SetVerified gives a crab the verified badge, or takes it away.
func (s *Store) SetVerified(ctx context.Context, c *Crab, on bool) error {
	in := &dynamodb.UpdateItemInput{
		TableName:           s.tableName(),
		Key:                 keyOf(c.PK, c.SK),
		UpdateExpression:    aws.String("REMOVE verified"),
		ConditionExpression: aws.String("attribute_exists(PK)"),
	}
	if on {
		in.UpdateExpression = aws.String("SET verified = :t")
		in.ExpressionAttributeValues = map[string]types.AttributeValue{":t": boolean(true)}
	}
	if _, err := s.db.UpdateItem(ctx, in); err != nil {
		return fmt.Errorf("set verified: %w", err)
	}
	c.Verified = on
	return nil
}

// SetContentFilters saves the crab's NSFW preference and muted words.
func (s *Store) SetContentFilters(ctx context.Context, c *Crab, f ContentFilters) error {
	var set, remove []string
	values := map[string]types.AttributeValue{}
	if f.ShowNSFW {
		set, values[":t"] = append(set, "show_nsfw = :t"), boolean(true)
	} else {
		remove = append(remove, "show_nsfw")
	}
	if len(f.MutedWords) > 0 {
		words, err := attributevalue.Marshal(f.MutedWords)
		if err != nil {
			return err
		}
		set, values[":w"] = append(set, "muted_words = :w"), words
	} else {
		remove = append(remove, "muted_words")
	}
	expr := ""
	if len(set) > 0 {
		expr = "SET " + strings.Join(set, ", ")
	}
	if len(remove) > 0 {
		expr += " REMOVE " + strings.Join(remove, ", ")
	}
	in := &dynamodb.UpdateItemInput{
		TableName:           s.tableName(),
		Key:                 keyOf(c.PK, c.SK),
		UpdateExpression:    aws.String(strings.TrimSpace(expr)),
		ConditionExpression: aws.String("attribute_exists(PK)"),
	}
	if len(values) > 0 {
		in.ExpressionAttributeValues = values
	}
	if _, err := s.db.UpdateItem(ctx, in); err != nil {
		return fmt.Errorf("set content filters: %w", err)
	}
	c.ContentFilters = f
	return nil
}

// SetRole gives a crab a role, or removes it when role is empty.
func (s *Store) SetRole(ctx context.Context, c *Crab, role string) error {
	in := &dynamodb.UpdateItemInput{
		TableName:                s.tableName(),
		Key:                      keyOf(c.PK, c.SK),
		UpdateExpression:         aws.String("REMOVE #role"),
		ConditionExpression:      aws.String("attribute_exists(PK)"),
		ExpressionAttributeNames: map[string]string{"#role": "role"},
	}
	switch role {
	case "":
	case RoleAdmin, RoleModerator:
		in.UpdateExpression = aws.String("SET #role = :r")
		in.ExpressionAttributeValues = map[string]types.AttributeValue{":r": str(role)}
	default:
		return fmt.Errorf("set role: unknown role %q", role)
	}
	if _, err := s.db.UpdateItem(ctx, in); err != nil {
		return fmt.Errorf("set role: %w", err)
	}
	return nil
}
