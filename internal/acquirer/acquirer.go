// Package acquirer simulates the card acquirer: a separate system, reached over an unreliable network,
// that deduplicates charges by idempotency key like real payment networks do.
package acquirer

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Schema creates the acquirer's own table (it lives in its own database file).
const Schema = `
CREATE TABLE IF NOT EXISTS charges (
	idempotency_key TEXT PRIMARY KEY,
	amount          INTEGER NOT NULL,
	currency        TEXT NOT NULL,
	card_last4      TEXT NOT NULL,
	approved        INTEGER NOT NULL,
	code            TEXT NOT NULL,   -- authorization code, or decline code
	created_at      TEXT NOT NULL
);`

// ErrUnavailable means the call failed and the outcome is unknown; retry with the same key.
var ErrUnavailable = errors.New("acquirer unavailable")

// MaxAmount is the largest amount (minor units) the acquirer approves.
const MaxAmount = 100_000_000

// Charge is what the acquirer is asked to do.
type Charge struct {
	Amount   int64
	Currency string
	Card     string
}

// Result is the acquirer's answer.
type Result struct {
	Approved bool
	Code     string
}

// Chaos injects failures, to see what idempotency protects against.
type Chaos struct {
	LatencyMs        int `json:"latencyMs"`
	FailNextCalls    int `json:"failNextCalls"`
	CrashAfterCharge int `json:"crashAfterCharge"`
}

// Simulated is an in-process acquirer backed by SQLite.
type Simulated struct {
	db  *sql.DB
	now func() time.Time

	mu    sync.Mutex
	chaos Chaos
}

// NewSimulated returns an acquirer with the given base latency.
func NewSimulated(db *sql.DB, now func() time.Time, latency time.Duration) *Simulated {
	return &Simulated{db: db, now: now, chaos: Chaos{LatencyMs: int(latency.Milliseconds())}}
}

// SetChaos replaces the failure injection settings.
func (s *Simulated) SetChaos(c Chaos) Chaos {
	s.mu.Lock()
	defer s.mu.Unlock()
	c.LatencyMs = clamp(c.LatencyMs, 0, 10_000)
	c.FailNextCalls = clamp(c.FailNextCalls, 0, 100)
	c.CrashAfterCharge = clamp(c.CrashAfterCharge, 0, 100)
	s.chaos = c
	return c
}

// CurrentChaos returns the failure injection settings.
func (s *Simulated) CurrentChaos() Chaos {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.chaos
}

// Charge charges once per key. A repeated key returns the first result without charging again.
func (s *Simulated) Charge(ctx context.Context, key string, c Charge) (Result, error) {
	failBefore, crashAfter, latency := s.nextCall()
	select {
	case <-time.After(latency):
	case <-ctx.Done():
		return Result{}, fmt.Errorf("%w: %w", ErrUnavailable, ctx.Err())
	}
	if failBefore {
		return Result{}, fmt.Errorf("%w: connection refused (injected)", ErrUnavailable)
	}

	var approved bool
	var code string
	err := s.db.QueryRowContext(ctx, `SELECT approved, code FROM charges WHERE idempotency_key = ?`, key).Scan(&approved, &code)
	switch {
	case err == nil:
		return Result{Approved: approved, Code: code}, nil // a retry: same answer, no new charge
	case !errors.Is(err, sql.ErrNoRows):
		return Result{}, fmt.Errorf("read charge: %w", err)
	}

	result := decide(c)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO charges (idempotency_key, amount, currency, card_last4, approved, code, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(idempotency_key) DO NOTHING`,
		key, c.Amount, c.Currency, last4(c.Card), result.Approved, result.Code, s.now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return Result{}, fmt.Errorf("insert charge: %w", err)
	}
	if crashAfter {
		// The money moved, but the answer is lost on the way back.
		return Result{}, fmt.Errorf("%w: connection reset after charging (injected)", ErrUnavailable)
	}
	return result, nil
}

// Charges counts the charges the acquirer made.
func (s *Simulated) Charges(ctx context.Context) (int64, error) {
	var n int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM charges`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count charges: %w", err)
	}
	return n, nil
}

func (s *Simulated) nextCall() (failBefore, crashAfter bool, latency time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	latency = time.Duration(s.chaos.LatencyMs) * time.Millisecond
	switch {
	case s.chaos.FailNextCalls > 0:
		s.chaos.FailNextCalls--
		return true, false, latency
	case s.chaos.CrashAfterCharge > 0:
		s.chaos.CrashAfterCharge--
		return false, true, latency
	}
	return false, false, latency
}

func decide(c Charge) Result {
	switch {
	case c.Card == "4000000000000002":
		return Result{Approved: false, Code: "card_declined"}
	case c.Amount > MaxAmount:
		return Result{Approved: false, Code: "amount_too_large"}
	}
	return Result{Approved: true, Code: "AUTH-" + strings.ToUpper(rand.Text()[:8])}
}

func last4(card string) string {
	if len(card) < 4 {
		return card
	}
	return card[len(card)-4:]
}

func clamp(v, low, high int) int { return max(low, min(v, high)) }
