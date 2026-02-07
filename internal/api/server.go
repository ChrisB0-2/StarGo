package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/star/stargo/internal/auth"
	"github.com/star/stargo/internal/health"
	"github.com/star/stargo/internal/metrics"
	"github.com/star/stargo/internal/tle"
)

// TLEConfig holds TLE-related configuration for the server.
type TLEConfig struct {
	EnableFetch bool
	SourceURL   string
	CacheDir    string
	MaxAge      time.Duration
	MaxFiles    int
}

// Server holds the HTTP server and its dependencies.
type Server struct {
	httpServer *http.Server
	logger     *slog.Logger
}

// NewServer creates a configured HTTP server.
func NewServer(addr string, logger *slog.Logger, authCfg auth.Config, store *tle.Store, tleCfg TLEConfig) *Server {
	mux := http.NewServeMux()

	checker := health.NewChecker(store, tleCfg.MaxAge)
	fetcher := tle.NewFetcher(tleCfg.SourceURL)
	cache := tle.NewCache(tleCfg.CacheDir, tleCfg.MaxFiles)
	rl := newRateLimiter()

	// Register routes.
	mux.HandleFunc("GET /healthz", health.Healthz)
	mux.HandleFunc("GET /readyz", checker.Readyz)
	mux.Handle("GET /metrics", metrics.Handler())
	mux.HandleFunc("GET /api/v1/test", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/v1/tle/metadata", tleMetadataHandler(store))
	mux.HandleFunc("POST /api/v1/tle/fetch", tleFetchHandler(logger, store, fetcher, cache, tleCfg.EnableFetch, rl))

	// Build middleware chain: metrics -> logging -> auth -> mux.
	var handler http.Handler = mux
	handler = auth.Middleware(authCfg)(handler)
	handler = loggingMiddleware(logger)(handler)
	handler = metrics.Middleware(handler)

	return &Server{
		httpServer: &http.Server{
			Addr:              addr,
			Handler:           handler,
			ReadTimeout:       10 * time.Second,
			ReadHeaderTimeout: 5 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       120 * time.Second,
		},
		logger: logger,
	}
}

// HTTPServer returns the underlying *http.Server for external control (e.g. shutdown).
func (s *Server) HTTPServer() *http.Server {
	return s.httpServer
}

// ListenAndServe starts the HTTP server.
func (s *Server) ListenAndServe() error {
	return s.httpServer.ListenAndServe()
}

func tleMetadataHandler(store *tle.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ds := store.Get()
		if ds == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "no TLE dataset loaded"})
			return
		}

		ageSeconds := time.Since(ds.FetchedAt).Seconds()

		resp := map[string]any{
			"source":      ds.Source,
			"fetched_at":  ds.FetchedAt.UTC().Format(time.RFC3339),
			"age_seconds": ageSeconds,
			"count":       len(ds.Satellites),
			"epoch_range": map[string]string{
				"min": ds.EpochRange.Min.UTC().Format(time.RFC3339),
				"max": ds.EpochRange.Max.UTC().Format(time.RFC3339),
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

func tleFetchHandler(logger *slog.Logger, store *tle.Store, fetcher *tle.Fetcher, cache *tle.Cache, enableFetch bool, rl *rateLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !enableFetch {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]string{"error": "TLE fetch is disabled"})
			return
		}

		// Rate limit by IP.
		ip := clientIP(r)
		if !rl.allow(ip) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]string{"error": "rate limit exceeded"})
			return
		}

		// Serialize fetch operations.
		store.Lock()
		defer store.Unlock()

		start := time.Now()
		raw, err := fetcher.Fetch(r.Context())
		duration := time.Since(start)

		if err != nil {
			metrics.RecordTLEFetch("error", duration)
			logger.Error("TLE fetch failed", "error", err, "duration_ms", duration.Milliseconds())
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			json.NewEncoder(w).Encode(map[string]string{"error": "failed to fetch TLE data"})
			return
		}

		entries, err := tle.Parse(bytes.NewReader(raw))
		if err != nil {
			metrics.RecordTLEFetch("parse_error", duration)
			metrics.IncTLEParseErrors()
			logger.Error("TLE parse failed", "error", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": "failed to parse TLE data"})
			return
		}

		if len(entries) == 0 {
			metrics.RecordTLEFetch("empty", duration)
			logger.Warn("TLE fetch returned no entries")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": "no TLE entries parsed"})
			return
		}

		now := time.Now()

		// Cache raw data.
		if err := cache.Write(raw, now); err != nil {
			logger.Error("failed to write TLE cache", "error", err)
		}

		// Compute epoch range.
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

		ds := &tle.TLEDataset{
			Source:    fetcher.SourceURL(),
			FetchedAt: now,
			EpochRange: tle.EpochRange{
				Min: minEpoch,
				Max: maxEpoch,
			},
			Satellites: entries,
		}

		store.Set(ds)
		metrics.RecordTLEFetch("success", duration)
		metrics.SetTLEDatasetCount(len(entries))
		metrics.SetTLEDatasetAge(0)

		logger.Info("TLE dataset updated", "count", len(entries), "duration_ms", duration.Milliseconds())

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"status":     "ok",
			"count":      len(entries),
			"fetched_at": now.UTC().Format(time.RFC3339),
		})
	}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// rateLimiter enforces 1 request per minute per IP.
type rateLimiter struct {
	mu    sync.Mutex
	times map[string]time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{
		times: make(map[string]time.Time),
	}
}

func (rl *rateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	last, ok := rl.times[ip]
	now := time.Now()

	if ok && now.Sub(last) < time.Minute {
		return false
	}

	rl.times[ip] = now
	return true
}

// probePath returns true for health/readiness probe paths that should not log at INFO.
func probePath(path string) bool {
	return path == "/healthz" || path == "/readyz"
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.statusCode = code
	sr.ResponseWriter.WriteHeader(code)
}

func loggingMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sr := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}

			next.ServeHTTP(sr, r)

			duration := time.Since(start)
			level := slog.LevelInfo
			if probePath(r.URL.Path) {
				level = slog.LevelDebug
			}

			logger.Log(r.Context(), level, "request",
				"component", "api",
				"method", r.Method,
				"path", r.URL.Path,
				"status", strconv.Itoa(sr.statusCode),
				"duration_ms", duration.Milliseconds(),
				"remote_ip", r.RemoteAddr,
			)
		})
	}
}
