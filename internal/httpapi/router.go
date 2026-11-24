// Package httpapi exposes the payments API, the chaos switch and the embedded panel.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/edybackend/payments-idempotency/internal/acquirer"
	"github.com/edybackend/payments-idempotency/internal/idempotency"
	"github.com/edybackend/payments-idempotency/internal/payments"
	"github.com/edybackend/payments-idempotency/internal/problem"
)

// Deps are what the router needs.
type Deps struct {
	Payments    *payments.Service
	Acquirer    *acquirer.Simulated
	Keys        *idempotency.Store
	Idempotency *idempotency.Middleware
	Panel       fs.FS // the built React app; may be empty in tests
	Logger      *slog.Logger
}

// Stats is the body of GET /v1/stats.
type Stats struct {
	Payments        int64 `json:"payments"`
	AcquirerCharges int64 `json:"acquirerCharges"`
	Keys            int64 `json:"keys"`
	Replays         int64 `json:"replays"`
}

// NewRouter wires the routes.
func NewRouter(d Deps) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /v1/payments", d.Idempotency.Wrap(http.HandlerFunc(d.createPayment)))
	mux.HandleFunc("GET /v1/payments", d.listPayments)
	mux.HandleFunc("GET /v1/payments/{id}", d.getPayment)
	mux.HandleFunc("GET /v1/stats", d.stats)
	mux.HandleFunc("GET /v1/chaos", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, d.Acquirer.CurrentChaos()) })
	mux.HandleFunc("PUT /v1/chaos", d.setChaos)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, _ *http.Request) {
		problem.Write(w, http.StatusNotFound, "Not found", "No such API route.")
	})
	mux.Handle("/", panel(d.Panel))
	return mux
}

// panel serves the built React app, or a short note when it was not built (go run without npm run build).
func panel(files fs.FS) http.Handler {
	if files != nil {
		if _, err := fs.Stat(files, "index.html"); err == nil {
			return http.FileServerFS(files)
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "The panel is not built: run `npm ci && npm run build` in web/, or `docker compose up --build`.\nThe API is at /v1/payments.\n")
	})
}

func (d Deps) createPayment(w http.ResponseWriter, r *http.Request) {
	var req payments.Request
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		problem.Write(w, http.StatusBadRequest, "Malformed JSON", err.Error())
		return
	}

	p, err := d.Payments.Create(r.Context(), idempotency.KeyFrom(r.Context()), req)
	var invalid payments.ValidationError
	switch {
	case errors.As(err, &invalid):
		problem.WriteDetails(w, problem.Details{Type: "about:blank", Title: "Invalid payment", Status: http.StatusBadRequest, Errors: invalid})
	case errors.Is(err, acquirer.ErrUnavailable):
		// 5xx: the idempotency key is released, so retrying with the same key is safe and expected.
		problem.Write(w, http.StatusBadGateway, "Acquirer unavailable", err.Error()+" Retry with the same Idempotency-Key.")
	case err != nil:
		d.Logger.Error("create payment", "error", err)
		problem.Write(w, http.StatusInternalServerError, "Internal error", "The payment could not be created.")
	default:
		w.Header().Set("Location", "/v1/payments/"+p.ID)
		writeJSON(w, http.StatusCreated, p)
	}
}

func (d Deps) listPayments(w http.ResponseWriter, r *http.Request) {
	list, err := d.Payments.List(r.Context(), 50)
	if err != nil {
		d.internal(w, "list payments", err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (d Deps) getPayment(w http.ResponseWriter, r *http.Request) {
	p, found, err := d.Payments.Get(r.Context(), r.PathValue("id"))
	switch {
	case err != nil:
		d.internal(w, "get payment", err)
	case !found:
		problem.Write(w, http.StatusNotFound, "Payment not found", "No payment with id "+r.PathValue("id")+".")
	default:
		writeJSON(w, http.StatusOK, p)
	}
}

func (d Deps) stats(w http.ResponseWriter, r *http.Request) {
	var s Stats
	var err error
	if s.Payments, err = d.Payments.Count(r.Context()); err == nil {
		if s.AcquirerCharges, err = d.Acquirer.Charges(r.Context()); err == nil {
			s.Keys, s.Replays, err = d.Keys.Stats(r.Context())
		}
	}
	if err != nil {
		d.internal(w, "stats", err)
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (d Deps) setChaos(w http.ResponseWriter, r *http.Request) {
	var c acquirer.Chaos
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		problem.Write(w, http.StatusBadRequest, "Malformed JSON", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, d.Acquirer.SetChaos(c))
}

func (d Deps) internal(w http.ResponseWriter, what string, err error) {
	d.Logger.Error(what, "error", err)
	problem.Write(w, http.StatusInternalServerError, "Internal error", "Something went wrong.")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
