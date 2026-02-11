package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
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
	"github.com/star/stargo/internal/transform"
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
// webFS provides the frontend static files. If nil, the frontend is not served.
func NewServer(addr string, logger *slog.Logger, authCfg auth.Config, store *tle.Store, tleCfg TLEConfig, prop *propagation.Propagator, kfCache *cache.KeyframeCache, streamHandler *stream.Handler, webFS fs.FS) *Server {
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
	mux.HandleFunc("POST /api/v1/refresh-tles", tleRefreshHandler(logger, store, fetcher, cache, tleCfg.EnableFetch, rl))
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
	mux.HandleFunc("GET /api/v1/propagate/{norad_id}", propagateSingleHandler(logger, store))

	// Serve web frontend (fallback for unmatched GET requests).
	if webFS != nil {
		mux.Handle("GET /", http.FileServer(http.FS(webFS)))
	}

	// Build middleware chain: metrics -> logging -> cors -> auth -> mux.
	var handler http.Handler = mux
	handler = auth.Middleware(authCfg)(handler)
	handler = corsMiddleware(handler)
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

// tleRefreshResult holds the result of a TLE fetch-parse-store operation.
type tleRefreshResult struct {
	Dataset  *tle.TLEDataset
	Duration time.Duration
}

// doTLERefresh downloads, parses, caches, and stores a fresh TLE dataset.
// Caller must hold store.Lock() to serialize concurrent refreshes.
func doTLERefresh(ctx context.Context, logger *slog.Logger, store *tle.Store, fetcher *tle.Fetcher, diskCache *tle.Cache) (*tleRefreshResult, error) {
	logger.Info("Refreshing TLE data from Celestrak...")

	start := time.Now()
	raw, err := fetcher.Fetch(ctx)
	duration := time.Since(start)

	if err != nil {
		metrics.RecordTLEFetch("error", duration)
		logger.Error("TLE refresh failed", "error", err, "duration_ms", duration.Milliseconds())
		return nil, fmt.Errorf("fetch: %w", err)
	}

	entries, err := tle.Parse(bytes.NewReader(raw), logger)
	if err != nil {
		metrics.RecordTLEFetch("parse_error", duration)
		metrics.IncTLEParseErrors()
		logger.Error("TLE parse failed", "error", err)
		return nil, fmt.Errorf("parse: %w", err)
	}

	if len(entries) == 0 {
		metrics.RecordTLEFetch("empty", duration)
		logger.Warn("TLE fetch returned no entries")
		return nil, fmt.Errorf("no TLE entries parsed")
	}

	now := time.Now()

	// Cache raw data to disk.
	if err := diskCache.Write(raw, now); err != nil {
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

	logger.Info("TLE refresh complete",
		"satellites_loaded", len(entries),
		"age_seconds", 0,
		"duration_ms", duration.Milliseconds(),
	)

	return &tleRefreshResult{Dataset: ds, Duration: duration}, nil
}

func tleFetchHandler(logger *slog.Logger, store *tle.Store, fetcher *tle.Fetcher, cache *tle.Cache, enableFetch bool, rl *rateLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !enableFetch {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]string{"error": "TLE fetch is disabled"})
			return
		}

		ip := clientIP(r)
		if !rl.allow(ip) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]string{"error": "rate limit exceeded"})
			return
		}

		store.Lock()
		defer store.Unlock()

		result, err := doTLERefresh(r.Context(), logger, store, fetcher, cache)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		json.NewEncoder(w).Encode(map[string]any{
			"status":     "ok",
			"count":      len(result.Dataset.Satellites),
			"fetched_at": result.Dataset.FetchedAt.UTC().Format(time.RFC3339),
		})
	}
}

func tleRefreshHandler(logger *slog.Logger, store *tle.Store, fetcher *tle.Fetcher, cache *tle.Cache, enableFetch bool, rl *rateLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !enableFetch {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]string{"error": "TLE fetch is disabled"})
			return
		}

		ip := clientIP(r)
		if !rl.allow(ip) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]string{"error": "rate limit exceeded"})
			return
		}

		store.Lock()
		defer store.Unlock()

		result, err := doTLERefresh(r.Context(), logger, store, fetcher, cache)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{
				"status":  "error",
				"message": "TLE refresh failed: " + err.Error(),
			})
			return
		}

		ds := result.Dataset
		json.NewEncoder(w).Encode(map[string]any{
			"status":            "success",
			"satellites_loaded": len(ds.Satellites),
			"tle_age_seconds":   int(time.Since(ds.FetchedAt).Seconds()),
			"dataset_epoch":     ds.FetchedAt.UTC().Format(time.RFC3339),
			"message":           "TLE data refreshed successfully",
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

// rateLimiter enforces 1 request per minute per IP with periodic pruning.
type rateLimiter struct {
	mu    sync.Mutex
	times map[string]time.Time
}

func newRateLimiter() *rateLimiter {
	rl := &rateLimiter{
		times: make(map[string]time.Time),
	}
	go rl.pruneLoop()
	return rl
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

// pruneLoop removes stale entries every 5 minutes to prevent unbounded map growth.
func (rl *rateLimiter) pruneLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		rl.mu.Lock()
		now := time.Now()
		for ip, last := range rl.times {
			if now.Sub(last) >= 2*time.Minute {
				delete(rl.times, ip)
			}
		}
		rl.mu.Unlock()
	}
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

func (sr *statusRecorder) Flush() {
	if f, ok := sr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap returns the underlying ResponseWriter (required by http.ResponseController).
func (sr *statusRecorder) Unwrap() http.ResponseWriter {
	return sr.ResponseWriter
}

// propagateTestHandler returns a handler for the propagation test endpoint.
// GET /api/v1/propagate/test?time=<RFC3339>
func propagateTestHandler(logger *slog.Logger, prop *propagation.Propagator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Parse target time (default: now + 60s).
		targetTime := time.Now().UTC().Add(60 * time.Second)
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

// propagateSingleHandler returns a handler for on-demand single-satellite propagation.
// GET /api/v1/propagate/{norad_id}?horizon=5400&step=5
func propagateSingleHandler(logger *slog.Logger, store *tle.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		w.Header().Set("Content-Type", "application/json")

		// Parse NORAD ID from path.
		noradIDStr := r.PathValue("norad_id")
		noradID, err := strconv.Atoi(noradIDStr)
		if err != nil || noradID <= 0 {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid norad_id"})
			return
		}

		// Parse horizon (seconds), default 5400 (90 min).
		horizon := 5400
		if v := r.URL.Query().Get("horizon"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 86400 {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": "invalid horizon: must be <= 86400 seconds"})
				return
			}
			horizon = n
		}

		// Parse step (seconds), default 5.
		step := 5
		if v := r.URL.Query().Get("step"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 60 {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": "invalid step: must be between 1 and 60 seconds"})
				return
			}
			step = n
		}

		// Get current TLE dataset.
		ds := store.Get()
		if ds == nil {
			metrics.RecordPropagateRequest("503", time.Since(start))
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"error": "no TLE dataset available"})
			return
		}

		// Find the TLE entry for this NORAD ID.
		var entry *tle.TLEEntry
		for i := range ds.Satellites {
			if ds.Satellites[i].NORADID == noradID {
				entry = &ds.Satellites[i]
				break
			}
		}
		if entry == nil {
			metrics.RecordPropagateRequest("404", time.Since(start))
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]any{"error": "satellite not found", "norad_id": noradID})
			return
		}

		// Create SGP4 propagator for this satellite.
		sgp4, err := propagation.NewSGP4Propagator(entry.Line1, entry.Line2, noradID)
		if err != nil {
			metrics.RecordPropagateRequest("500", time.Since(start))
			logger.Warn("sgp4 init failed", "norad_id", noradID, "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": "propagation init failed"})
			return
		}

		// Cap computation budget: max 10,800 positions per request
		// (e.g., 90 min at 0.5s step, or 15h at 5s step).
		const maxPositions = 10800
		numPositions := horizon/step + 1
		if numPositions > maxPositions {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{
				"error":         "too many positions requested",
				"max_positions": maxPositions,
				"requested":     numPositions,
			})
			return
		}

		// Propagate from now to now+horizon at step intervals.
		now := time.Now().UTC()
		stepDur := time.Duration(step) * time.Second

		type posEntry struct {
			T string     `json:"t"`
			P [3]float64 `json:"p"`
		}
		positions := make([]posEntry, 0, numPositions)

		for i := 0; i < numPositions; i++ {
			t := now.Add(time.Duration(i) * stepDur)
			teme, err := sgp4.Propagate(t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second())
			if err != nil {
				continue // Skip failed time steps.
			}
			ecef := transform.TEMEToECEF(teme, t)
			positions = append(positions, posEntry{
				T: t.Format(time.RFC3339),
				P: [3]float64{ecef.X, ecef.Y, ecef.Z},
			})
		}

		duration := time.Since(start)
		metrics.RecordPropagateRequest("200", duration)

		logger.Debug("propagate single",
			"norad_id", noradID,
			"horizon", horizon,
			"step", step,
			"duration_ms", duration.Milliseconds(),
			"position_count", len(positions),
			"remote_ip", r.RemoteAddr,
		)

		json.NewEncoder(w).Encode(map[string]any{
			"norad_id":  noradID,
			"horizon":   horizon,
			"step":      step,
			"count":     len(positions),
			"positions": positions,
		})
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

// corsMiddleware adds CORS headers for cross-origin frontend access (e.g., dev server on a different port).
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
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
