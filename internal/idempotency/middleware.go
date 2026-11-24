package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/edybackend/payments-idempotency/internal/problem"
)

// HeaderKey is the request header that carries the key; HeaderReplayed marks a replayed response.
const (
	HeaderKey      = "Idempotency-Key"
	HeaderReplayed = "Idempotent-Replayed"
	maxKeyLength   = 255
	maxBodyBytes   = 1 << 20
)

type keyContext struct{}

// KeyFrom returns the idempotency key of the request, so handlers can pass it downstream (to the acquirer).
func KeyFrom(ctx context.Context) string {
	key, _ := ctx.Value(keyContext{}).(string)
	return key
}

// Middleware wraps unsafe requests with the idempotency protocol.
type Middleware struct {
	Store       *Store
	Now         func() time.Time
	LockTimeout time.Duration
	Logger      *slog.Logger
}

// Wrap returns next guarded by an Idempotency-Key.
func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(HeaderKey)
		if key == "" || len(key) > maxKeyLength {
			problem.Write(w, http.StatusBadRequest, "Idempotency-Key required",
				"Send an Idempotency-Key header (1-255 characters) so the request can be retried safely.")
			return
		}

		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
		if err != nil {
			problem.Write(w, http.StatusRequestEntityTooLarge, "Body too large", "The request body is limited to 1 MiB.")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))

		outcome, rec, err := m.Store.Begin(r.Context(), key, fingerprint(r, body), m.Now(), m.LockTimeout)
		if err != nil {
			m.Logger.Error("idempotency begin", "error", err)
			problem.Write(w, http.StatusInternalServerError, "Internal error", "Could not check the idempotency key.")
			return
		}

		switch outcome {
		case InProgress:
			w.Header().Set("Retry-After", "1")
			problem.Write(w, http.StatusConflict, "Request in progress",
				"A request with this Idempotency-Key is still being processed. Retry in a moment to get its result.")
		case Mismatch:
			problem.Write(w, http.StatusUnprocessableEntity, "Idempotency-Key reused",
				"This Idempotency-Key was already used with a different request. Use a new key for a new request.")
		case Replay:
			w.Header().Set("Content-Type", rec.ContentType)
			w.Header().Set(HeaderReplayed, "true")
			w.WriteHeader(rec.StatusCode)
			_, _ = w.Write(rec.Body)
		case Execute:
			m.execute(w, r.WithContext(context.WithValue(r.Context(), keyContext{}, key)), key, next)
		}
	})
}

func (m *Middleware) execute(w http.ResponseWriter, r *http.Request, key string, next http.Handler) {
	recorder := &recorder{ResponseWriter: w, status: http.StatusOK}
	next.ServeHTTP(recorder, r)

	// Saving or releasing must happen even if the client went away: use a context that outlives the request.
	ctx := context.WithoutCancel(r.Context())
	if recorder.status >= http.StatusInternalServerError {
		// Our failure, not the client's: forget the key so a retry runs again (the acquirer call is idempotent too).
		if err := m.Store.Release(ctx, key); err != nil {
			m.Logger.Error("idempotency release", "key", key, "error", err)
		}
		return
	}
	rec := Record{StatusCode: recorder.status, ContentType: recorder.Header().Get("Content-Type"), Body: recorder.body.Bytes()}
	if err := m.Store.Complete(ctx, key, rec); err != nil {
		m.Logger.Error("idempotency complete", "key", key, "error", err)
	}
}

// fingerprint identifies the request a key was first used with.
func fingerprint(r *http.Request, body []byte) string {
	h := sha256.New()
	_, _ = io.WriteString(h, r.Method+" "+r.URL.Path+"\n")
	_, _ = h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// recorder passes the response through and keeps a copy of status and body.
type recorder struct {
	http.ResponseWriter
	status      int
	body        bytes.Buffer
	wroteHeader bool
}

func (r *recorder) WriteHeader(status int) {
	if !r.wroteHeader {
		r.status = status
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(p []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	r.body.Write(p)
	return r.ResponseWriter.Write(p)
}
