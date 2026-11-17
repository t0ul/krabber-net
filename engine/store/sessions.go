package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

// SessionStore implements scs.Store and scs.CtxStore on the table, so sessions
// survive deploys and instance replacement.
//
// The session token is hashed before use as a key: reading the table (or a
// backup) doesn't reveal usable session tokens. Expiry is checked on every
// read because DynamoDB's TTL deletion can lag by up to about two days.
type SessionStore struct {
	s *Store
}

// Sessions returns the scs store backed by this table.
func (s *Store) Sessions() *SessionStore { return &SessionStore{s: s} }

type sessionItem struct {
	PK        string `dynamodbav:"PK"`
	SK        string `dynamodbav:"SK"`
	Data      []byte `dynamodbav:"data"`
	ExpiresAt int64  `dynamodbav:"expires_at"`
}

// FindCtx returns the session data for a token, or found=false if the session
// doesn't exist or has expired.
func (ss *SessionStore) FindCtx(ctx context.Context, token string) ([]byte, bool, error) {
	var it sessionItem
	err := ss.s.getItem(ctx, sessionPK(hashToken(token)), sessionSK(), &it)
	switch {
	case errors.Is(err, ErrNotFound):
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("find session: %w", err)
	}
	if it.ExpiresAt <= ss.s.now().Unix() {
		return nil, false, nil
	}
	return it.Data, true, nil
}

// CommitCtx saves session data until expiry.
func (ss *SessionStore) CommitCtx(ctx context.Context, token string, b []byte, expiry time.Time) error {
	item, err := marshal(sessionItem{
		PK:        sessionPK(hashToken(token)),
		SK:        sessionSK(),
		Data:      b,
		ExpiresAt: expiry.Unix(),
	})
	if err != nil {
		return err
	}
	if _, err := ss.s.db.PutItem(ctx, &dynamodb.PutItemInput{TableName: ss.s.tableName(), Item: item}); err != nil {
		return fmt.Errorf("commit session: %w", err)
	}
	return nil
}

// DeleteCtx removes a session. scs calls it when a token is renewed (login,
// logout) or destroyed.
func (ss *SessionStore) DeleteCtx(ctx context.Context, token string) error {
	_, err := ss.s.db.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: ss.s.tableName(),
		Key:       keyOf(sessionPK(hashToken(token)), sessionSK()),
	})
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// Find, Commit and Delete satisfy scs.Store; scs prefers the Ctx variants.

// Find implements scs.Store.
func (ss *SessionStore) Find(token string) ([]byte, bool, error) {
	return ss.FindCtx(context.Background(), token)
}

// Commit implements scs.Store.
func (ss *SessionStore) Commit(token string, b []byte, expiry time.Time) error {
	return ss.CommitCtx(context.Background(), token, b, expiry)
}

// Delete implements scs.Store.
func (ss *SessionStore) Delete(token string) error {
	return ss.DeleteCtx(context.Background(), token)
}
