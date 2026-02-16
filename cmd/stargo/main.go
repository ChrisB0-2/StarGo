package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/star/stargo/internal/api"
	"github.com/star/stargo/internal/auth"
	"github.com/star/stargo/internal/cache"
	"github.com/star/stargo/internal/metrics"
	"github.com/star/stargo/internal/propagation"
	"github.com/star/stargo/internal/stream"
	"github.com/star/stargo/internal/tle"
	"github.com/star/stargo/web"
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

	tleCfg := loadTLEConfig(logger)
	store := tle.NewStore()
	tleCache := tle.NewCache(tleCfg.CacheDir, tleCfg.MaxFiles)

	// Attempt to load cached TLE data on startup.
	data, ts, err := tleCache.LoadLatest()
	if err != nil {
		logger.Info("no TLE cache found, starting without TLE data", "error", err)
	} else {
		entries, err := tle.Parse(bytes.NewReader(data), logger)
		if err != nil {
			logger.Warn("failed to parse cached TLE data", "error", err)
		} else if len(entries) > 0 {
			minEpoch := entries[0].Epoch
			maxEpoch := entries[0].Epoch
			for _, e := range entries[1:] {
				if e.Epoch.Before(minEpoch) {
					minEpoch = e.Epoch
				}
				if e.Epoch.After(maxEpoch) {
					maxEpoch = e.Epoch
				}
			}

			store.Set(&tle.TLEDataset{
				Source:    "cache",
				FetchedAt: ts,
				EpochRange: tle.EpochRange{
					Min: minEpoch,
					Max: maxEpoch,
				},
				Satellites: entries,
			})
			metrics.SetTLEDatasetCount(len(entries))
			logger.Info("loaded TLE data from cache", "count", len(entries), "cached_at", ts.Format(time.RFC3339))
		}
	}

	propCfg := loadPropConfig(logger)
	prop := propagation.NewPropagator(store, propCfg, logger)
	metrics.SetPropagationWorkersActive(propCfg.Workers)

	cacheCfg := loadCacheConfig(logger, propCfg)
	kfCache := cache.NewKeyframeCache(cacheCfg, prop, store, logger)

	streamCfg := loadStreamConfig(logger)
	streamHandler := stream.NewHandler(kfCache, store, streamCfg, logger)

	srv := api.NewServer(addr, logger, authCfg, store, tleCfg, prop, kfCache, streamHandler, web.Content)

	// Graceful shutdown on SIGINT/SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Start cache background worker.
	go kfCache.Start(ctx)

	// Background goroutine to update TLE dataset age gauge.
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				age := store.AgeSeconds()
				if age >= 0 {
					metrics.SetTLEDatasetAge(age)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		logger.Info("starting server", "addr", addr, "auth_enabled", authCfg.Enabled, "tle_fetch_enabled", tleCfg.EnableFetch)
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

func loadCacheConfig(logger *slog.Logger, propCfg propagation.PropConfig) cache.Config {
	cfg := cache.Config{
		Step:        propCfg.Step,
		Horizon:     propCfg.Horizon,
		GracePeriod: 30 * time.Second,
		Buffer:      60 * time.Second,
	}

	if v := os.Getenv("STARGO_CACHE_STEP"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			logger.Warn("invalid STARGO_CACHE_STEP value, using propagation step", "value", v)
		} else {
			cfg.Step = time.Duration(n) * time.Second
		}
	}

	if v := os.Getenv("STARGO_CACHE_HORIZON"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			logger.Warn("invalid STARGO_CACHE_HORIZON value, using propagation horizon", "value", v)
		} else {
			cfg.Horizon = time.Duration(n) * time.Second
		}
	}

	if v := os.Getenv("STARGO_CACHE_GRACE_PERIOD"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			logger.Warn("invalid STARGO_CACHE_GRACE_PERIOD value, using default", "value", v, "default", 30)
		} else {
			cfg.GracePeriod = time.Duration(n) * time.Second
		}
	}

	if v := os.Getenv("STARGO_CACHE_BUFFER"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			logger.Warn("invalid STARGO_CACHE_BUFFER value, using default", "value", v, "default", 60)
		} else {
			cfg.Buffer = time.Duration(n) * time.Second
		}
	}

	logger.Info("cache config",
		"step_seconds", cfg.Step.Seconds(),
		"horizon_seconds", cfg.Horizon.Seconds(),
		"grace_period_seconds", cfg.GracePeriod.Seconds(),
		"buffer_seconds", cfg.Buffer.Seconds(),
	)

	return cfg
}

func loadPropConfig(logger *slog.Logger) propagation.PropConfig {
	cfg := propagation.PropConfig{
		Workers: runtime.NumCPU(),
		Step:    5 * time.Second,
		Horizon: 600 * time.Second,
	}

	if v := os.Getenv("STARGO_PROP_WORKERS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			logger.Warn("invalid STARGO_PROP_WORKERS value, using default", "value", v, "default", cfg.Workers)
		} else {
			cfg.Workers = n
		}
	}

	if v := os.Getenv("STARGO_KEYFRAME_STEP"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			logger.Warn("invalid STARGO_KEYFRAME_STEP value, using default", "value", v, "default", 5)
		} else {
			cfg.Step = time.Duration(n) * time.Second
		}
	}

	if v := os.Getenv("STARGO_KEYFRAME_HORIZON"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			logger.Warn("invalid STARGO_KEYFRAME_HORIZON value, using default", "value", v, "default", 600)
		} else {
			cfg.Horizon = time.Duration(n) * time.Second
		}
	}

	logger.Info("propagation config",
		"workers", cfg.Workers,
		"step_seconds", cfg.Step.Seconds(),
		"horizon_seconds", cfg.Horizon.Seconds(),
	)

	return cfg
}

func loadStreamConfig(logger *slog.Logger) stream.Config {
	cfg := stream.Config{
		MaxConcurrentPerIP: 10,
		BandwidthLimit:     1048576,
		KeepaliveInterval:  30 * time.Second,
	}

	if v := os.Getenv("STARGO_STREAM_MAX_CONCURRENT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			logger.Warn("invalid STARGO_STREAM_MAX_CONCURRENT value, using default", "value", v, "default", 10)
		} else {
			cfg.MaxConcurrentPerIP = n
		}
	}

	if v := os.Getenv("STARGO_STREAM_BANDWIDTH_LIMIT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			logger.Warn("invalid STARGO_STREAM_BANDWIDTH_LIMIT value, using default", "value", v, "default", 1048576)
		} else {
			cfg.BandwidthLimit = n
		}
	}

	if v := os.Getenv("STARGO_STREAM_KEEPALIVE_INTERVAL"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			logger.Warn("invalid STARGO_STREAM_KEEPALIVE_INTERVAL value, using default", "value", v, "default", 30)
		} else {
			cfg.KeepaliveInterval = time.Duration(n) * time.Second
		}
	}

	logger.Info("stream config",
		"max_concurrent_per_ip", cfg.MaxConcurrentPerIP,
		"bandwidth_limit", cfg.BandwidthLimit,
		"keepalive_interval_seconds", cfg.KeepaliveInterval.Seconds(),
	)

	return cfg
}

func loadTLEConfig(logger *slog.Logger) api.TLEConfig {
	cfg := api.TLEConfig{
		EnableFetch: true,
		CacheDir:    "/tmp/stargo/tle",
		MaxFiles:    5,
		MaxAge:      24 * time.Hour,
		ExtraSourceURLs: []string{
			// ISS (NORAD 25544) — well-documented reference satellite for validation.
			"https://celestrak.org/NORAD/elements/gp.php?CATNR=25544&FORMAT=tle",
		},
	}

	if v := os.Getenv("STARGO_ENABLE_TLE_FETCH"); v != "" {
		enabled, err := strconv.ParseBool(v)
		if err != nil {
			logger.Warn("invalid STARGO_ENABLE_TLE_FETCH value, defaulting to false", "value", v)
		} else {
			cfg.EnableFetch = enabled
		}
	}

	if v := os.Getenv("STARGO_TLE_SOURCE_URL"); v != "" {
		cfg.SourceURL = v
	}

	if v := os.Getenv("STARGO_TLE_EXTRA_URLS"); v != "" {
		var urls []string
		for _, u := range strings.Split(v, ",") {
			u = strings.TrimSpace(u)
			if u != "" {
				urls = append(urls, u)
			}
		}
		cfg.ExtraSourceURLs = urls
	}

	if v := os.Getenv("STARGO_TLE_CACHE_DIR"); v != "" {
		cfg.CacheDir = v
	}

	if v := os.Getenv("STARGO_TLE_MAX_AGE"); v != "" {
		seconds, err := strconv.Atoi(v)
		if err != nil {
			logger.Warn("invalid STARGO_TLE_MAX_AGE value, defaulting to 86400", "value", v)
		} else {
			cfg.MaxAge = time.Duration(seconds) * time.Second
		}
	}

	logger.Info("TLE config",
		"source_url", cfg.SourceURL,
		"extra_urls", cfg.ExtraSourceURLs,
		"cache_dir", cfg.CacheDir,
	)

	return cfg
}
