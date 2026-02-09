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
	"github.com/star/stargo/internal/cache"
	"github.com/star/stargo/internal/health"
	"github.com/star/stargo/internal/metrics"
	"github.com/star/stargo/internal/propagation"
	"github.com/star/stargo/internal/stream"
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
func NewServer(addr string, logger *slog.Logger, authCfg auth.Config, store *tle.Store, tleCfg TLEConfig, prop *propagation.Propagator, kfCache *cache.KeyframeCache, streamHandler *stream.Handler) *Server {
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
	if prop != nil {
		mux.HandleFunc("GET /api/v1/propagate/test", propagateTestHandler(logger, prop))
	}
	if kfCache != nil {
		mux.HandleFunc("GET /api/v1/cache/keyframes/latest", cacheLatestHandler(kfCache))
		mux.HandleFunc("GET /api/v1/cache/keyframes/at", cacheAtHandler(kfCache))
		mux.HandleFunc("GET /api/v1/cache/stats", cacheStatsHandler(kfCache))
	}
	if streamHandler != nil {
		mux.HandleFunc("GET /api/v1/stream/keyframes", streamHandler.HandleKeyframes)
	}

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

// propagateTestHandler returns a handler for the propagation test endpoint.
// GET /api/v1/propagate/test?time=<RFC3339>
func propagateTestHandler(logger *slog.Logger, prop *propagation.Propagator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Parse target time (default: now + 60s).
		targetTime := time.Now().Add(60 * time.Second)
		if t := r.URL.Query().Get("time"); t != "" {
			parsed, err := time.Parse(time.RFC3339, t)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": "invalid time format, use RFC3339"})
				return
			}
			targetTime = parsed
		}

		kf, err := prop.PropagateToTime(r.Context(), targetTime)
		if err != nil {
			logger.Error("propagation test failed", "error", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(keyframeResponse(kf))
	}
}

// cacheLatestHandler returns the most recent cached keyframe.
func cacheLatestHandler(kfCache *cache.KeyframeCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kf := kfCache.GetLatest()
		if kf == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "no cached keyframes available"})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(keyframeResponse(kf))
	}
}

// cacheAtHandler returns the cached keyframe at a specific time.
func cacheAtHandler(kfCache *cache.KeyframeCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t := r.URL.Query().Get("time")
		if t == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "time parameter required (RFC3339)"})
			return
		}

		parsed, err := time.Parse(time.RFC3339, t)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid time format, use RFC3339"})
			return
		}

		kf := kfCache.Get(parsed)
		if kf == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "no keyframe cached for this time"})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(keyframeResponse(kf))
	}
}

// cacheStatsHandler returns cache statistics.
func cacheStatsHandler(kfCache *cache.KeyframeCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stats := kfCache.Stats()

		resp := map[string]any{
			"entries":         stats.Entries,
			"size_bytes":      stats.SizeBytes,
			"hits":            stats.Hits,
			"misses":          stats.Misses,
			"evictions":       stats.Evictions,
			"in_grace_period": stats.InGracePeriod,
		}

		if !stats.OldestTimestamp.IsZero() {
			resp["oldest_timestamp"] = stats.OldestTimestamp.UTC().Format(time.RFC3339)
		}
		if !stats.NewestTimestamp.IsZero() {
			resp["newest_timestamp"] = stats.NewestTimestamp.UTC().Format(time.RFC3339)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

// keyframeResponse builds the compact JSON response for a keyframe.
func keyframeResponse(kf *propagation.Keyframe) any {
	type satResponse struct {
		ID int        `json:"id"`
		P  [3]float64 `json:"p"`
	}
	sats := make([]satResponse, len(kf.Satellites))
	for i, s := range kf.Satellites {
		sats[i] = satResponse{ID: s.NORADID, P: s.PositionECEF}
	}
	return struct {
		Timestamp  string        `json:"timestamp"`
		Satellites []satResponse `json:"satellites"`
	}{
		Timestamp:  kf.Timestamp.UTC().Format(time.RFC3339),
		Satellites: sats,
	}
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
