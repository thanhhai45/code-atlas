// Command api serves the Code Atlas HTTP API.
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

	"github.com/gin-gonic/gin"

	"github.com/thanhhai45/code-atlas/internal/api"
	"github.com/thanhhai45/code-atlas/internal/cache"
	"github.com/thanhhai45/code-atlas/internal/config"
	"github.com/thanhhai45/code-atlas/internal/search"
	"github.com/thanhhai45/code-atlas/internal/store"
)

func main() {
	if err := run(); err != nil {
		slog.Error("api exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := retry(ctx, "postgres migrate", func() error { return db.Migrate(ctx) }); err != nil {
		return err
	}

	es := search.NewClient(cfg.ElasticsearchURL, cfg.SearchAlias)
	if err := retry(ctx, "elasticsearch ensure index", func() error { return es.EnsureIndex(ctx) }); err != nil {
		return err
	}

	srv := &api.Server{Repos: db, Search: es}
	if c, err := cache.New(cfg.RedisURL, cfg.SearchCacheTTL); err != nil {
		slog.Warn("redis disabled", "err", err)
	} else {
		defer c.Close()
		srv.Cache = c
	}

	if os.Getenv("GIN_MODE") == "" {
		gin.SetMode(gin.ReleaseMode)
	}
	httpSrv := &http.Server{Addr: cfg.HTTPAddr, Handler: srv.Router(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()
	slog.Info("api listening", "addr", cfg.HTTPAddr)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// retry waits for a dependency that may still be starting (e.g. under docker compose).
func retry(ctx context.Context, what string, fn func() error) error {
	var err error
	for attempt := 1; attempt <= 30; attempt++ {
		if err = fn(); err == nil {
			return nil
		}
		slog.Warn("dependency not ready", "step", what, "attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return err
}
