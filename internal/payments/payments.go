// Package payments creates card payments through the acquirer and stores them.
package payments

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/edybackend/payments-idempotency/internal/acquirer"
)

// Schema creates the payments table.
const Schema = `
CREATE TABLE IF NOT EXISTS payments (
	id              TEXT PRIMARY KEY,
	idempotency_key TEXT NOT NULL UNIQUE,  -- second guard: one payment per key, whatever happens upstream
	amount          INTEGER NOT NULL,
	currency        TEXT NOT NULL,
	card_last4      TEXT NOT NULL,
	description     TEXT NOT NULL,
	status          TEXT NOT NULL CHECK (status IN ('approved', 'declined')),
	code            TEXT NOT NULL,
	created_at      TEXT NOT NULL
);`

// Request is the body of POST /v1/payments.
type Request struct {
	Amount      int64  `json:"amount"`
	Currency    string `json:"currency"`
	Card        string `json:"card"`
	Description string `json:"description"`
}

// Payment is what the API returns. The card number is never stored or returned, only its last 4 digits.
type Payment struct {
	ID          string    `json:"id"`
	Amount      int64     `json:"amount"`
	Currency    string    `json:"currency"`
	CardLast4   string    `json:"cardLast4"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	Code        string    `json:"code"`
	CreatedAt   time.Time `json:"createdAt"`
}

// ValidationError lists invalid fields.
type ValidationError map[string]string

func (v ValidationError) Error() string { return fmt.Sprintf("invalid payment: %d field(s)", len(v)) }

// Acquirer charges cards. Implementations must deduplicate by key.
type Acquirer interface {
	Charge(ctx context.Context, key string, c acquirer.Charge) (acquirer.Result, error)
}

// Service creates payments.
type Service struct {
	db       *sql.DB
	acquirer Acquirer
	now      func() time.Time
	timeout  time.Duration
}

// NewService returns a payment service.
func NewService(db *sql.DB, a Acquirer, now func() time.Time, acquirerTimeout time.Duration) *Service {
	return &Service{db: db, acquirer: a, now: now, timeout: acquirerTimeout}
}

// Create charges the card once for key and stores the payment.
func (s *Service) Create(ctx context.Context, key string, req Request) (Payment, error) {
	req.Card = strings.ReplaceAll(req.Card, " ", "")
	req.Currency = strings.ToUpper(strings.TrimSpace(req.Currency))
	req.Description = strings.TrimSpace(req.Description)
	if err := validate(req); err != nil {
		return Payment{}, err
	}

	callCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	result, err := s.acquirer.Charge(callCtx, key, acquirer.Charge{Amount: req.Amount, Currency: req.Currency, Card: req.Card})
	if err != nil {
		return Payment{}, err
	}

	status := "declined"
	if result.Approved {
		status = "approved"
	}
	p := Payment{
		ID:          "pay_" + strings.ToLower(rand.Text()[:16]),
		Amount:      req.Amount,
		Currency:    req.Currency,
		CardLast4:   req.Card[len(req.Card)-4:],
		Description: req.Description,
		Status:      status,
		Code:        result.Code,
		CreatedAt:   s.now().UTC(),
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO payments (id, idempotency_key, amount, currency, card_last4, description, status, code, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(idempotency_key) DO NOTHING`,
		p.ID, key, p.Amount, p.Currency, p.CardLast4, p.Description, p.Status, p.Code, p.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return Payment{}, fmt.Errorf("insert payment: %w", err)
	}
	// If a taken-over request already stored the payment for this key, return that one.
	return s.byKey(ctx, key)
}

// Get returns a payment by id.
func (s *Service) Get(ctx context.Context, id string) (Payment, bool, error) {
	p, err := s.scanOne(s.db.QueryRowContext(ctx, selectPayment+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Payment{}, false, nil
	}
	return p, err == nil, err
}

// List returns the latest payments, newest first.
func (s *Service) List(ctx context.Context, limit int) ([]Payment, error) {
	rows, err := s.db.QueryContext(ctx, selectPayment+` ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list payments: %w", err)
	}
	defer func() { _ = rows.Close() }()
	list := []Payment{}
	for rows.Next() {
		p, err := s.scanOne(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, p)
	}
	return list, rows.Err()
}

// Count returns how many payments exist.
func (s *Service) Count(ctx context.Context) (int64, error) {
	var n int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM payments`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count payments: %w", err)
	}
	return n, nil
}

const selectPayment = `SELECT id, amount, currency, card_last4, description, status, code, created_at FROM payments`

func (s *Service) byKey(ctx context.Context, key string) (Payment, error) {
	return s.scanOne(s.db.QueryRowContext(ctx, selectPayment+` WHERE idempotency_key = ?`, key))
}

type scanner interface{ Scan(dest ...any) error }

func (s *Service) scanOne(row scanner) (Payment, error) {
	var p Payment
	var created string
	if err := row.Scan(&p.ID, &p.Amount, &p.Currency, &p.CardLast4, &p.Description, &p.Status, &p.Code, &created); err != nil {
		return Payment{}, fmt.Errorf("scan payment: %w", err)
	}
	t, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Payment{}, fmt.Errorf("parse created_at: %w", err)
	}
	p.CreatedAt = t
	return p, nil
}

var currencies = map[string]bool{"BRL": true, "USD": true, "EUR": true}

func validate(r Request) error {
	errs := ValidationError{}
	if r.Amount <= 0 {
		errs["amount"] = "must be a positive integer in minor units (1990 = 19.90)"
	}
	if !currencies[r.Currency] {
		errs["currency"] = "must be BRL, USD or EUR"
	}
	if !validCard(r.Card) {
		errs["card"] = "must be a 16-digit card number that passes the Luhn check (try 4242 4242 4242 4242)"
	}
	if len(r.Description) > 200 {
		errs["description"] = "must be at most 200 characters"
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}

// validCard checks length, digits and the Luhn checksum.
func validCard(card string) bool {
	if len(card) != 16 {
		return false
	}
	sum := 0
	for i := len(card) - 1; i >= 0; i-- {
		d := int(card[i] - '0')
		if d < 0 || d > 9 {
			return false
		}
		if (len(card)-i)%2 == 0 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return sum%10 == 0
}
