package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/star/stargo/internal/api"
	"github.com/star/stargo/internal/auth"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	addr := os.Getenv("STARGO_HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	authCfg, err := loadAuthConfig(logger)
	if err != nil {
		logger.Error("invalid auth configuration", "error", err)
		os.Exit(1)
	}

	srv := api.NewServer(addr, logger, authCfg)

	// Graceful shutdown on SIGINT/SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("starting server", "addr", addr, "auth_enabled", authCfg.Enabled)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server listen error", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down server...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.HTTPServer().Shutdown(shutdownCtx); err != nil {
		logger.Error("server shutdown error", "error", err)
		os.Exit(1)
	}

	logger.Info("server stopped")
}

func loadAuthConfig(logger *slog.Logger) (auth.Config, error) {
	cfg := auth.Config{}

	enabledStr := os.Getenv("STARGO_AUTH_ENABLED")
	if enabledStr != "" {
		enabled, err := strconv.ParseBool(enabledStr)
		if err != nil {
			return cfg, errors.New("STARGO_AUTH_ENABLED must be a boolean value (true/false/1/0)")
		}
		cfg.Enabled = enabled
	}

	if cfg.Enabled {
		cfg.Token = os.Getenv("STARGO_AUTH_TOKEN")
		if cfg.Token == "" {
			return cfg, errors.New("STARGO_AUTH_TOKEN is required when auth is enabled")
		}
		logger.Info("auth enabled")
	}

	return cfg, nil
}
