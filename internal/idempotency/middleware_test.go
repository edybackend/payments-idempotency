package idempotency_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/edybackend/payments-idempotency/internal/idempotency"
	"github.com/edybackend/payments-idempotency/internal/storage"
)

// clock is a time source the test moves by hand.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

func (c *clock) Advance(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.now = c.now.Add(d) }

type fixture struct {
	server *httptest.Server
	calls  atomic.Int32
	clock  *clock
	// status is what the wrapped handler answers; release closes to let the first (slow) execution finish.
	status  atomic.Int32
	release chan struct{}
}

func newFixture(t *testing.T, slow bool) *fixture {
	t.Helper()
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "keys.db"), idempotency.Schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	f := &fixture{clock: &clock{now: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)}, release: make(chan struct{})}
	f.status.Store(http.StatusCreated)
	if !slow {
		close(f.release)
	}
	mw := &idempotency.Middleware{
		Store: idempotency.NewStore(db), Now: f.clock.Now, LockTimeout: 30 * time.Second,
		Logger: slog.New(slog.DiscardHandler),
	}
	f.server = httptest.NewServer(mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := f.calls.Add(1)
		if n == 1 {
			<-f.release // only the first execution can be held back
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(int(f.status.Load()))
		_, _ = io.WriteString(w, `{"execution":`+strconv.Itoa(int(n))+`,"key":"`+idempotency.KeyFrom(r.Context())+`","echo":`+string(body)+`}`)
	})))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fixture) post(t *testing.T, key, body string) (int, string, http.Header) {
	t.Helper()
	status, b, h, err := f.send(key, body)
	if err != nil {
		t.Error(err) // Error, not Fatal: post is also called from goroutines
	}
	return status, b, h
}

func (f *fixture) send(key, body string) (int, string, http.Header, error) {
	req, err := http.NewRequest(http.MethodPost, f.server.URL+"/v1/payments", strings.NewReader(body))
	if err != nil {
		return 0, "", nil, err
	}
	if key != "" {
		req.Header.Set(idempotency.HeaderKey, key)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", nil, err
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	return res.StatusCode, string(b), res.Header, err
}

func TestFirstRequestRunsAndTheRetryIsReplayed(t *testing.T) {
	f := newFixture(t, false)

	status1, body1, h1 := f.post(t, "k1", `{"amount":1}`)
	status2, body2, h2 := f.post(t, "k1", `{"amount":1}`)

	if status1 != http.StatusCreated || status2 != http.StatusCreated {
		t.Fatalf("statuses = %d, %d; want 201, 201", status1, status2)
	}
	if body1 != body2 {
		t.Fatalf("replayed body differs:\n%s\n%s", body1, body2)
	}
	if h1.Get(idempotency.HeaderReplayed) != "" || h2.Get(idempotency.HeaderReplayed) != "true" {
		t.Fatalf("Idempotent-Replayed = %q, %q; want \"\", \"true\"", h1.Get(idempotency.HeaderReplayed), h2.Get(idempotency.HeaderReplayed))
	}
	if got := f.calls.Load(); got != 1 {
		t.Fatalf("handler ran %d times; want 1", got)
	}
	if !strings.Contains(body1, `"key":"k1"`) {
		t.Fatalf("handler did not see the key in the context: %s", body1)
	}
}

func TestRejectedRequests(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want int
	}{
		{"missing key", "", http.StatusBadRequest},
		{"key too long", strings.Repeat("k", 256), http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, false)
			if status, _, _ := f.post(t, tt.key, `{}`); status != tt.want {
				t.Fatalf("status = %d; want %d", status, tt.want)
			}
			if f.calls.Load() != 0 {
				t.Fatal("the handler must not run")
			}
		})
	}
}

func TestSameKeyWithADifferentBodyIs422(t *testing.T) {
	f := newFixture(t, false)
	f.post(t, "k1", `{"amount":1}`)

	status, _, _ := f.post(t, "k1", `{"amount":2}`)

	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; want 422", status)
	}
	if f.calls.Load() != 1 {
		t.Fatal("the handler must not run for a reused key")
	}
}

func TestConcurrentRequestsWithOneKeyRunTheHandlerOnce(t *testing.T) {
	f := newFixture(t, true)
	const n = 20
	statuses := make(chan int, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			status, _, h := f.post(t, "double-click", `{"amount":1}`)
			if status == http.StatusConflict && h.Get("Retry-After") != "1" {
				t.Errorf("409 without Retry-After")
			}
			statuses <- status
		})
	}
	// Let every request reach the key check, then let the single winner finish.
	for f.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	close(f.release)
	wg.Wait()
	close(statuses)

	count := map[int]int{}
	for s := range statuses {
		count[s]++
	}
	if f.calls.Load() != 1 {
		t.Fatalf("handler ran %d times; want 1", f.calls.Load())
	}
	if count[http.StatusCreated]+count[http.StatusConflict] != n || count[http.StatusCreated] < 1 {
		t.Fatalf("statuses = %v; want one or more 201 (winner + replays) and the rest 409", count)
	}
}

func TestA5xxReleasesTheKeySoTheRetryRunsAgain(t *testing.T) {
	f := newFixture(t, false)
	f.status.Store(http.StatusBadGateway)
	if status, _, _ := f.post(t, "k1", `{"amount":1}`); status != http.StatusBadGateway {
		t.Fatalf("status = %d; want 502", status)
	}

	f.status.Store(http.StatusCreated)
	status, _, h := f.post(t, "k1", `{"amount":1}`)

	if status != http.StatusCreated || h.Get(idempotency.HeaderReplayed) != "" {
		t.Fatalf("retry after 5xx: status %d replayed %q; want a fresh 201", status, h.Get(idempotency.HeaderReplayed))
	}
	if f.calls.Load() != 2 {
		t.Fatalf("handler ran %d times; want 2", f.calls.Load())
	}
}

func TestAKeyStuckInProcessingIsTakenOverAfterTheLockTimeout(t *testing.T) {
	f := newFixture(t, true)
	// The first request claims the key and never finishes (the process "died").
	go func() { _, _, _, _ = f.send("k1", `{"amount":1}`) }()
	for f.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}

	if status, _, _ := f.post(t, "k1", `{"amount":1}`); status != http.StatusConflict {
		t.Fatalf("before the timeout: status %d; want 409", status)
	}

	f.clock.Advance(31 * time.Second)
	status, _, _ := f.post(t, "k1", `{"amount":1}`)
	close(f.release) // the zombie finishes late; its Complete is a no-op because the key is already completed

	if status != http.StatusCreated {
		t.Fatalf("after the timeout: status %d; want 201", status)
	}
	if f.calls.Load() != 2 {
		t.Fatalf("handler ran %d times; want 2 (the dead one and the takeover)", f.calls.Load())
	}
}
