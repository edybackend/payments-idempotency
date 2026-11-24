// Command payments serves the payments API with idempotency keys, a simulated acquirer and the React panel.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/edybackend/payments-idempotency/internal/acquirer"
	"github.com/edybackend/payments-idempotency/internal/config"
	"github.com/edybackend/payments-idempotency/internal/httpapi"
	"github.com/edybackend/payments-idempotency/internal/idempotency"
	"github.com/edybackend/payments-idempotency/internal/payments"
	"github.com/edybackend/payments-idempotency/internal/storage"
	"github.com/edybackend/payments-idempotency/web"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("service stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := storage.Open(ctx, cfg.DBPath, idempotency.Schema+payments.Schema)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	acquirerDB, err := storage.Open(ctx, cfg.AcquirerDBPath, acquirer.Schema)
	if err != nil {
		return err
	}
	defer func() { _ = acquirerDB.Close() }()

	keys := idempotency.NewStore(db)
	acq := acquirer.NewSimulated(acquirerDB, time.Now, cfg.AcquirerLatency)
	server := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: httpapi.NewRouter(httpapi.Deps{
			Payments:    payments.NewService(db, acq, time.Now, cfg.AcquirerTimeout),
			Acquirer:    acq,
			Keys:        keys,
			Idempotency: &idempotency.Middleware{Store: keys, Now: time.Now, LockTimeout: cfg.LockTimeout, Logger: logger},
			Panel:       web.Panel(),
			Logger:      logger,
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		logger.Info("http listening", "addr", cfg.HTTPAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	g.Go(func() error { return janitor(ctx, keys, cfg, logger) })
	g.Go(func() error {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	})
	return g.Wait()
}

// janitor deletes completed keys older than the TTL.
func janitor(ctx context.Context, keys *idempotency.Store, cfg config.Config, logger *slog.Logger) error {
	ticker := time.NewTicker(cfg.JanitorFrequency)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			n, err := keys.DeleteExpired(ctx, time.Now().Add(-cfg.KeyTTL))
			if err != nil {
				logger.Error("delete expired keys", "error", err)
				continue
			}
			if n > 0 {
				logger.Info("deleted expired idempotency keys", "count", n)
			}
		}
	}
}
