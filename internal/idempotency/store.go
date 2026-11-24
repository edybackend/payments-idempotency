// Package idempotency makes POST requests safe to retry: one execution per Idempotency-Key, replayed afterwards.
package idempotency

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Schema creates the keys table.
const Schema = `
CREATE TABLE IF NOT EXISTS idempotency_keys (
	key           TEXT PRIMARY KEY,
	fingerprint   TEXT NOT NULL,
	state         TEXT NOT NULL CHECK (state IN ('processing', 'completed')),
	locked_at     INTEGER NOT NULL,  -- unix ms; a processing key older than the lock timeout can be taken over
	status_code   INTEGER NULL,
	content_type  TEXT NULL,
	body          BLOB NULL,
	replays       INTEGER NOT NULL DEFAULT 0,
	created_at    INTEGER NOT NULL
);`

// Outcome says what the middleware must do with a request.
type Outcome int

const (
	// Execute means the key was claimed by this request (new, or taken over from a crashed one).
	Execute Outcome = iota
	// InProgress means another request with this key is running right now.
	InProgress
	// Mismatch means the key was used before with a different request.
	Mismatch
	// Replay means the request already finished; Record holds its response.
	Replay
)

// Record is a stored response.
type Record struct {
	StatusCode  int
	ContentType string
	Body        []byte
}

// Store keeps idempotency keys in SQLite.
type Store struct {
	db *sql.DB
}

// NewStore returns a Store on db (the schema must be applied).
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Begin claims key for a request with fingerprint, or reports why it cannot run.
// Every branch is a single statement, so two concurrent requests cannot both get Execute.
func (s *Store) Begin(ctx context.Context, key, fingerprint string, now time.Time, lockTimeout time.Duration) (Outcome, Record, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO idempotency_keys (key, fingerprint, state, locked_at, created_at)
		 VALUES (?, ?, 'processing', ?, ?) ON CONFLICT(key) DO NOTHING`,
		key, fingerprint, now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return 0, Record{}, fmt.Errorf("claim key: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return Execute, Record{}, nil
	}

	var (
		storedFingerprint, state string
		lockedAt                 int64
		status                   sql.NullInt64
		contentType              sql.NullString
		body                     []byte
	)
	err = s.db.QueryRowContext(ctx,
		`SELECT fingerprint, state, locked_at, status_code, content_type, body FROM idempotency_keys WHERE key = ?`, key).
		Scan(&storedFingerprint, &state, &lockedAt, &status, &contentType, &body)
	if errors.Is(err, sql.ErrNoRows) {
		// Released (5xx) or expired between the two statements: claim it again.
		return s.Begin(ctx, key, fingerprint, now, lockTimeout)
	}
	if err != nil {
		return 0, Record{}, fmt.Errorf("read key: %w", err)
	}

	switch {
	case storedFingerprint != fingerprint:
		return Mismatch, Record{}, nil
	case state == "completed":
		if _, err := s.db.ExecContext(ctx, `UPDATE idempotency_keys SET replays = replays + 1 WHERE key = ?`, key); err != nil {
			return 0, Record{}, fmt.Errorf("count replay: %w", err)
		}
		return Replay, Record{StatusCode: int(status.Int64), ContentType: contentType.String, Body: body}, nil
	case now.Sub(time.UnixMilli(lockedAt)) < lockTimeout:
		return InProgress, Record{}, nil
	}

	// The request that claimed the key died. Take the claim over, but only if nobody else just did (compare-and-set).
	res, err = s.db.ExecContext(ctx,
		`UPDATE idempotency_keys SET locked_at = ? WHERE key = ? AND state = 'processing' AND locked_at = ?`,
		now.UnixMilli(), key, lockedAt)
	if err != nil {
		return 0, Record{}, fmt.Errorf("take over stale key: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return Execute, Record{}, nil
	}
	return InProgress, Record{}, nil
}

// Complete stores the response of the request that holds key.
func (s *Store) Complete(ctx context.Context, key string, rec Record) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE idempotency_keys SET state = 'completed', status_code = ?, content_type = ?, body = ? WHERE key = ? AND state = 'processing'`,
		rec.StatusCode, rec.ContentType, rec.Body, key)
	if err != nil {
		return fmt.Errorf("complete key: %w", err)
	}
	return nil
}

// Release forgets key so the request can be retried (used when the server failed with a 5xx).
func (s *Store) Release(ctx context.Context, key string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE key = ? AND state = 'processing'`, key); err != nil {
		return fmt.Errorf("release key: %w", err)
	}
	return nil
}

// DeleteExpired removes keys created before cutoff and returns how many.
func (s *Store) DeleteExpired(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE created_at < ? AND state = 'completed'`, cutoff.UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("delete expired keys: %w", err)
	}
	return res.RowsAffected()
}

// Stats counts keys and replays.
func (s *Store) Stats(ctx context.Context) (keys, replays int64, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(replays), 0) FROM idempotency_keys`).Scan(&keys, &replays)
	if err != nil {
		return 0, 0, fmt.Errorf("key stats: %w", err)
	}
	return keys, replays, nil
}
