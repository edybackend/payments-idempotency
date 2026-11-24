package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edybackend/payments-idempotency/internal/acquirer"
	"github.com/edybackend/payments-idempotency/internal/httpapi"
	"github.com/edybackend/payments-idempotency/internal/idempotency"
	"github.com/edybackend/payments-idempotency/internal/payments"
	"github.com/edybackend/payments-idempotency/internal/storage"
)

type api struct {
	server   *httptest.Server
	acquirer *acquirer.Simulated
}

func newAPI(t *testing.T, latency time.Duration) *api {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := storage.Open(ctx, filepath.Join(dir, "payments.db"), idempotency.Schema+payments.Schema)
	if err != nil {
		t.Fatal(err)
	}
	acqDB, err := storage.Open(ctx, filepath.Join(dir, "acquirer.db"), acquirer.Schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close(); _ = acqDB.Close() })

	logger := slog.New(slog.DiscardHandler)
	keys := idempotency.NewStore(db)
	acq := acquirer.NewSimulated(acqDB, time.Now, latency)
	server := httptest.NewServer(httpapi.NewRouter(httpapi.Deps{
		Payments:    payments.NewService(db, acq, time.Now, 2*time.Second),
		Acquirer:    acq,
		Keys:        keys,
		Idempotency: &idempotency.Middleware{Store: keys, Now: time.Now, LockTimeout: 30 * time.Second, Logger: logger},
		Logger:      logger,
	}))
	t.Cleanup(server.Close)
	return &api{server: server, acquirer: acq}
}

func (a *api) pay(t *testing.T, key, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, a.server.URL+"/v1/payments", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Error(err)
		return 0, nil
	}
	defer func() { _ = res.Body.Close() }()
	var out map[string]any
	b, _ := io.ReadAll(res.Body)
	_ = json.Unmarshal(b, &out)
	return res.StatusCode, out
}

func (a *api) stats(t *testing.T) httpapi.Stats {
	t.Helper()
	res, err := http.Get(a.server.URL + "/v1/stats")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var s httpapi.Stats
	if err := json.NewDecoder(res.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

const approved = `{"amount":1990,"currency":"BRL","card":"4242 4242 4242 4242","description":"Book"}`

func TestPaymentOutcomes(t *testing.T) {
	tests := []struct {
		name, body, status, code string
	}{
		{"approved", approved, "approved", "AUTH-"},
		{"declined card", `{"amount":1990,"currency":"USD","card":"4000000000000002"}`, "declined", "card_declined"},
		{"amount too large", `{"amount":100000001,"currency":"EUR","card":"4242424242424242"}`, "declined", "amount_too_large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newAPI(t, 0)
			status, p := a.pay(t, "key-"+tt.name, tt.body)
			if status != http.StatusCreated || p["status"] != tt.status || !strings.HasPrefix(p["code"].(string), tt.code) {
				t.Fatalf("got %d %v; want 201 %s %s", status, p, tt.status, tt.code)
			}
			if strings.Contains(p["cardLast4"].(string)+p["id"].(string), "42424242") {
				t.Fatalf("the full card number leaked: %v", p)
			}
		})
	}
}

func TestInvalidPaymentsAre400WithTheField(t *testing.T) {
	tests := map[string]string{
		"amount":   `{"amount":0,"currency":"BRL","card":"4242424242424242"}`,
		"currency": `{"amount":100,"currency":"XYZ","card":"4242424242424242"}`,
		"card":     `{"amount":100,"currency":"BRL","card":"4242424242424241"}`,
	}
	for field, body := range tests {
		t.Run(field, func(t *testing.T) {
			a := newAPI(t, 0)
			status, p := a.pay(t, "k", body)
			errs, _ := p["errors"].(map[string]any)
			if status != http.StatusBadRequest || errs[field] == nil {
				t.Fatalf("got %d %v; want 400 with errors.%s", status, p, field)
			}
			if a.stats(t).AcquirerCharges != 0 {
				t.Fatal("an invalid payment must not reach the acquirer")
			}
		})
	}
}

func TestADoubleClickChargesOnce(t *testing.T) {
	a := newAPI(t, 150*time.Millisecond)
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() { a.pay(t, "double-click", approved) })
	}
	wg.Wait()

	if s := a.stats(t); s.Payments != 1 || s.AcquirerCharges != 1 {
		t.Fatalf("stats = %+v; want 1 payment and 1 charge", s)
	}
}

func TestChargedButTheAnswerWasLost(t *testing.T) {
	a := newAPI(t, 0)
	a.acquirer.SetChaos(acquirer.Chaos{CrashAfterCharge: 1})

	status, _ := a.pay(t, "lost-answer", approved)
	if status != http.StatusBadGateway {
		t.Fatalf("first attempt: %d; want 502", status)
	}
	if s := a.stats(t); s.AcquirerCharges != 1 || s.Payments != 0 {
		t.Fatalf("after the lost answer: %+v; want the money moved (1 charge) and no payment yet", s)
	}

	status, p := a.pay(t, "lost-answer", approved) // the client retries with the same key

	if status != http.StatusCreated || p["status"] != "approved" {
		t.Fatalf("retry: %d %v; want 201 approved", status, p)
	}
	if s := a.stats(t); s.AcquirerCharges != 1 || s.Payments != 1 {
		t.Fatalf("after the retry: %+v; want still 1 charge and now 1 payment", s)
	}
}

func TestAnUnreachableAcquirerIs502AndTheRetryWorks(t *testing.T) {
	a := newAPI(t, 0)
	a.acquirer.SetChaos(acquirer.Chaos{FailNextCalls: 2})

	first, _ := a.pay(t, "k", approved)
	second, _ := a.pay(t, "k", approved)
	third, _ := a.pay(t, "k", approved)

	if first != http.StatusBadGateway || second != http.StatusBadGateway || third != http.StatusCreated {
		t.Fatalf("statuses = %d, %d, %d; want 502, 502, 201", first, second, third)
	}
	if s := a.stats(t); s.AcquirerCharges != 1 || s.Payments != 1 {
		t.Fatalf("stats = %+v; want 1 charge, 1 payment", s)
	}
}

func TestMalformedJSONAndUnknownRoutes(t *testing.T) {
	a := newAPI(t, 0)
	if status, _ := a.pay(t, "k", `{bad`); status != http.StatusBadRequest {
		t.Fatalf("malformed JSON: %d; want 400", status)
	}
	if status, _ := a.pay(t, "k2", `{"amount":1,"currency":"BRL","card":"4242424242424242","cvv":"123"}`); status != http.StatusBadRequest {
		t.Fatalf("unknown field: %d; want 400", status)
	}
	for _, path := range []string{"/v1/payments/pay_nope", "/v1/nope"} {
		res, err := http.Get(a.server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusNotFound || res.Header.Get("Content-Type") != "application/problem+json" {
			t.Fatalf("%s: %d %s; want 404 problem", path, res.StatusCode, res.Header.Get("Content-Type"))
		}
	}
}
