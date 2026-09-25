package store

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// Every crab gets one invite code, like Crabber's referral codes. A new crab
// who signs up with it records who invited them, and the inviter's count
// goes up. Moderators can disable a code.

const (
	inviteCodeLen      = 8
	inviteCodeAlphabet = "abcdefghjkmnpqrstuvwxyz23456789" // no 0/o, 1/i/l
	inviteAttempts     = 8
)

// ErrInvalidInvite is returned for an invite code that doesn't exist or was
// disabled.
var ErrInvalidInvite = errors.New("store: invalid invite code")

type inviteCode struct {
	PK       string `dynamodbav:"PK"`
	SK       string `dynamodbav:"SK"`
	CrabID   string `dynamodbav:"crab_id"`
	CrabPK   string `dynamodbav:"crab_pk"`
	CrabSK   string `dynamodbav:"crab_sk"`
	Disabled bool   `dynamodbav:"disabled,omitempty"`
}

// NormalizeInviteCode lowercases and trims a typed or linked code.
func NormalizeInviteCode(code string) string { return strings.ToLower(strings.TrimSpace(code)) }

func newInviteCode() (string, error) {
	b := make([]byte, inviteCodeLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = inviteCodeAlphabet[int(b[i])%len(inviteCodeAlphabet)]
	}
	return string(b), nil
}

// EnsureInviteCode gives c an invite code if they don't have one yet.
func (s *Store) EnsureInviteCode(ctx context.Context, c *Crab) (string, error) {
	if c.InviteCode != "" {
		return c.InviteCode, nil
	}
	for range inviteAttempts {
		code, err := newInviteCode()
		if err != nil {
			return "", err
		}
		item, err := marshal(inviteCode{PK: invitePK(code), SK: inviteSK(), CrabID: c.ID, CrabPK: c.PK, CrabSK: c.SK})
		if err != nil {
			return "", err
		}
		err = s.transact(ctx, s.putNew(item), types.TransactWriteItem{Update: &types.Update{
			TableName:                 s.tableName(),
			Key:                       keyOf(c.PK, c.SK),
			UpdateExpression:          aws.String("SET invite_code = :c"),
			ConditionExpression:       aws.String("attribute_exists(PK) AND attribute_not_exists(invite_code)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":c": str(code)},
		}})
		switch {
		case cancelledAt(err, 0):
			continue
		case cancelledAt(err, 1):
			// Another request gave them one first.
			fresh, err := s.CrabByKey(ctx, c.PK, c.SK)
			if err != nil {
				return "", err
			}
			c.InviteCode = fresh.InviteCode
			return c.InviteCode, nil
		case err != nil:
			return "", fmt.Errorf("invite code: %w", err)
		}
		c.InviteCode = code
		return code, nil
	}
	return "", errors.New("invite code: no free code")
}

// InviteCodeDisabled reports whether c's code was disabled by a moderator.
func (s *Store) InviteCodeDisabled(ctx context.Context, c *Crab) (bool, error) {
	if c.InviteCode == "" {
		return false, nil
	}
	var ic inviteCode
	if err := s.getItem(ctx, invitePK(c.InviteCode), inviteSK(), &ic); err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("invite code: %w", err)
	}
	return ic.Disabled, nil
}

// SetInviteCodeDisabled turns c's code off or back on.
func (s *Store) SetInviteCodeDisabled(ctx context.Context, c *Crab, disabled bool) error {
	if c.InviteCode == "" {
		return ErrNotFound
	}
	_, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 s.tableName(),
		Key:                       keyOf(invitePK(c.InviteCode), inviteSK()),
		UpdateExpression:          aws.String("SET disabled = :d"),
		ConditionExpression:       aws.String("attribute_exists(PK)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":d": boolean(disabled)},
	})
	switch {
	case conditionFailed(err):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("disable invite code: %w", err)
	}
	return nil
}

// inviteWrites checks an invite code and returns the transaction items that
// use it (a condition that it's still enabled, and the inviter's count), and
// the inviter's ID.
func (s *Store) inviteWrites(ctx context.Context, code string) ([]types.TransactWriteItem, string, error) {
	var ic inviteCode
	if err := s.getItem(ctx, invitePK(code), inviteSK(), &ic); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, "", ErrInvalidInvite
		}
		return nil, "", fmt.Errorf("invite code: %w", err)
	}
	if ic.Disabled {
		return nil, "", ErrInvalidInvite
	}
	// A banned or deleted krab's code stops working.
	if inviter, err := s.CrabByKey(ctx, ic.CrabPK, ic.CrabSK); err != nil || !inviter.CanSignIn() {
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, "", fmt.Errorf("invite code: %w", err)
		}
		return nil, "", ErrInvalidInvite
	}
	return []types.TransactWriteItem{
		{ConditionCheck: &types.ConditionCheck{
			TableName:                 s.tableName(),
			Key:                       keyOf(ic.PK, ic.SK),
			ConditionExpression:       aws.String("attribute_exists(PK) AND (attribute_not_exists(disabled) OR disabled = :f)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":f": boolean(false)},
		}},
		s.addCounter(ic.CrabPK, ic.CrabSK, "invites", 1),
	}, ic.CrabID, nil
}
