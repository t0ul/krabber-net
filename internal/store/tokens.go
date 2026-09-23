package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// Token scopes.
const (
	ScopeActivation    = "activation"
	ScopePasswordReset = "password-reset"
)

// Token is a one-time emailed token. Only its SHA-256 hash is stored; the
// plaintext exists only in the email.
type Token struct {
	PK        string `dynamodbav:"PK"`
	SK        string `dynamodbav:"SK"`
	Scope     string `dynamodbav:"scope"`
	CrabID    string `dynamodbav:"crab_id"`
	ExpiresAt int64  `dynamodbav:"expires_at"`
}

// TokenLength is the length of a plaintext token.
const TokenLength = 26

// NewToken creates a token for a crab and returns its plaintext.
func (s *Store) NewToken(ctx context.Context, crabID, scope string, ttl time.Duration) (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	plaintext := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)

	item, err := marshal(Token{
		PK:        tokenPK(hashToken(plaintext)),
		SK:        tokenSK(scope),
		Scope:     scope,
		CrabID:    crabID,
		ExpiresAt: s.now().Add(ttl).Unix(),
	})
	if err != nil {
		return "", err
	}
	_, err = s.db.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           s.tableName(),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(PK)"),
	})
	if err != nil {
		return "", fmt.Errorf("store token: %w", err)
	}
	return plaintext, nil
}

// ConsumeToken atomically deletes an unexpired token and returns it, so each
// token works exactly once.
func (s *Store) ConsumeToken(ctx context.Context, scope, plaintext string) (*Token, error) {
	if len(plaintext) != TokenLength {
		return nil, ErrInvalidToken
	}
	res, err := s.db.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName:                 s.tableName(),
		Key:                       keyOf(tokenPK(hashToken(plaintext)), tokenSK(scope)),
		ConditionExpression:       aws.String("attribute_exists(PK) AND expires_at > :now"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":now": num(s.now().Unix())},
		ReturnValues:              types.ReturnValueAllOld,
	})
	if err != nil {
		if conditionFailed(err) {
			return nil, ErrInvalidToken
		}
		return nil, fmt.Errorf("consume token: %w", err)
	}
	var t Token
	if err := attributevalue.UnmarshalMap(res.Attributes, &t); err != nil {
		return nil, fmt.Errorf("consume token: %w", err)
	}
	if t.CrabID == "" {
		return nil, errors.New("consume token: token has no crab")
	}
	return &t, nil
}

func hashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}
