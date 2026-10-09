package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// ScopeWriteMolts lets a key post molts as its crab. It's the only scope a
// posting bot needs; more can be added as the API grows.
const ScopeWriteMolts = "write:molts"

// apiKeyPrefix marks a Krabber API key; the rest is random hex. apiKeyBytes is
// how many random bytes the key carries before hex-encoding.
const (
	apiKeyPrefix = "kb_"
	apiKeyBytes  = 32
)

// APIKey is a personal access key that acts as the crab who made it, with no
// third-party app flow. Only the SHA-256 hash of the key is stored, so reading
// the table (or a backup) never reveals a usable key; the plaintext is shown
// once, at creation. The lookup item carries the owner's table key so a request
// authenticates with one consistent GetItem, exactly like a session.
type APIKey struct {
	PK string `dynamodbav:"PK"`
	SK string `dynamodbav:"SK"`

	ID         string   `dynamodbav:"id"`
	CrabID     string   `dynamodbav:"crab_id"`
	CrabPK     string   `dynamodbav:"crab_pk"`
	CrabSK     string   `dynamodbav:"crab_sk"`
	Name       string   `dynamodbav:"name"`
	Scopes     []string `dynamodbav:"scopes"`
	CreatedAt  int64    `dynamodbav:"created_at"` // Unix seconds
	LastUsedAt int64    `dynamodbav:"last_used_at,omitempty"`
	Revoked    bool     `dynamodbav:"revoked,omitempty"`
}

// HasScope reports whether the key carries scope.
func (k *APIKey) HasScope(scope string) bool {
	for _, s := range k.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// apiKeyListItem is the per-owner copy used to list and revoke keys. It keeps
// the hash so a revoke can delete the lookup item, but never the plaintext.
type apiKeyListItem struct {
	PK string `dynamodbav:"PK"`
	SK string `dynamodbav:"SK"`

	ID        string   `dynamodbav:"id"`
	Name      string   `dynamodbav:"name"`
	Scopes    []string `dynamodbav:"scopes"`
	Hash      string   `dynamodbav:"hash"`
	CreatedAt int64    `dynamodbav:"created_at"`
	Revoked   bool     `dynamodbav:"revoked,omitempty"`
}

// CreateAPIKey mints a key for a crab and returns its plaintext (shown once).
// It writes the hashed lookup item and the owner's list item together.
func (s *Store) CreateAPIKey(ctx context.Context, c *Crab, name string, scopes []string) (string, *APIKey, error) {
	raw := make([]byte, apiKeyBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("generate api key: %w", err)
	}
	plaintext := apiKeyPrefix + hex.EncodeToString(raw)
	hash := hashToken(plaintext)
	id := newID()
	now := s.now().Unix()

	key := &APIKey{
		PK: apiKeyPK(hash), SK: apiKeySK(),
		ID: id, CrabID: c.ID, CrabPK: c.PK, CrabSK: c.SK,
		Name: name, Scopes: scopes, CreatedAt: now,
	}
	keyItem, err := marshal(key)
	if err != nil {
		return "", nil, err
	}
	listItem, err := marshal(apiKeyListItem{
		PK: apiKeyListPK(c.ID), SK: apiKeyListSK(id),
		ID: id, Name: name, Scopes: scopes, Hash: hash, CreatedAt: now,
	})
	if err != nil {
		return "", nil, err
	}
	if err := s.transact(ctx, s.putNew(keyItem), s.putNew(listItem)); err != nil {
		return "", nil, fmt.Errorf("create api key: %w", err)
	}
	return plaintext, key, nil
}

// APIKeyByPlaintext looks a key up by its plaintext, returning ErrNotFound for
// a key that's malformed, unknown or revoked.
func (s *Store) APIKeyByPlaintext(ctx context.Context, plaintext string) (*APIKey, error) {
	if !strings.HasPrefix(plaintext, apiKeyPrefix) || len(plaintext) != len(apiKeyPrefix)+2*apiKeyBytes {
		return nil, ErrNotFound
	}
	var k APIKey
	if err := s.getItem(ctx, apiKeyPK(hashToken(plaintext)), apiKeySK(), &k); err != nil {
		return nil, err
	}
	if k.Revoked {
		return nil, ErrNotFound
	}
	return &k, nil
}

// TouchAPIKey records that a key was just used. It's best-effort: callers log
// and carry on if it fails, since the request itself has already authenticated.
func (s *Store) TouchAPIKey(ctx context.Context, k *APIKey) error {
	_, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 s.tableName(),
		Key:                       keyOf(k.PK, k.SK),
		UpdateExpression:          aws.String("SET last_used_at = :t"),
		ConditionExpression:       aws.String("attribute_exists(PK)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":t": num(s.now().Unix())},
	})
	if err != nil && !conditionFailed(err) {
		return fmt.Errorf("touch api key: %w", err)
	}
	return nil
}

// ListAPIKeys returns a crab's keys (without hashes), newest first.
func (s *Store) ListAPIKeys(ctx context.Context, crabID string) ([]APIKey, error) {
	items, err := queryAll[apiKeyListItem](ctx, s.db, &dynamodb.QueryInput{
		TableName:              s.tableName(),
		KeyConditionExpression: aws.String("PK = :pk AND begins_with(SK, :sk)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": str(apiKeyListPK(crabID)), ":sk": str("AKL#"),
		},
		ScanIndexForward: aws.Bool(false),
	}, 0)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	out := make([]APIKey, 0, len(items))
	for _, it := range items {
		out = append(out, APIKey{
			ID: it.ID, CrabID: crabID, Name: it.Name,
			Scopes: it.Scopes, CreatedAt: it.CreatedAt, Revoked: it.Revoked,
		})
	}
	return out, nil
}

// purgeAPIKeys deletes a deleted crab's API keys: the hashed lookup items and
// the owner's list items. Called from PurgeCrab, after the account is a
// tombstone (so the keys are already dead to apiAuthenticate).
func (s *Store) purgeAPIKeys(ctx context.Context, tomb *Crab) error {
	items, err := queryAll[apiKeyListItem](ctx, s.db, &dynamodb.QueryInput{
		TableName:                 s.tableName(),
		KeyConditionExpression:    aws.String("PK = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":pk": str(apiKeyListPK(tomb.ID))},
	}, 0)
	if err != nil {
		return fmt.Errorf("api keys: %w", err)
	}
	keys := make([][2]string, 0, 2*len(items))
	for _, it := range items {
		keys = append(keys, [2]string{apiKeyPK(it.Hash), apiKeySK()}, [2]string{it.PK, it.SK})
	}
	if err := s.batchDelete(ctx, keys); err != nil {
		return fmt.Errorf("api keys: %w", err)
	}
	return nil
}

// RevokeAPIKey stops a key working: it deletes the lookup item and marks the
// owner's list item revoked. Revoking an already-revoked or missing key is not
// an error.
func (s *Store) RevokeAPIKey(ctx context.Context, crabID, keyID string) error {
	var li apiKeyListItem
	if err := s.getItem(ctx, apiKeyListPK(crabID), apiKeyListSK(keyID), &li); err != nil {
		return err
	}
	err := s.transact(ctx,
		types.TransactWriteItem{Delete: &types.Delete{
			TableName: s.tableName(),
			Key:       keyOf(apiKeyPK(li.Hash), apiKeySK()),
		}},
		types.TransactWriteItem{Update: &types.Update{
			TableName:                 s.tableName(),
			Key:                       keyOf(apiKeyListPK(crabID), apiKeyListSK(keyID)),
			UpdateExpression:          aws.String("SET revoked = :t"),
			ConditionExpression:       aws.String("attribute_exists(PK)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":t": boolean(true)},
		}},
	)
	if err != nil {
		return fmt.Errorf("revoke api key: %w", err)
	}
	return nil
}
