// Package stream implements Server-Sent Events (SSE) streaming for satellite
// keyframe batches. Clients connect via GET /api/v1/stream/keyframes and receive
// a continuous stream of ECEF positions from the keyframe cache.
//
// SSE message format:
//
//	data: {"type":"keyframe_batch","t":"2026-02-06T04:00:00Z","frame":"ECEF","sat":[...]}\n\n
//
// First message is always metadata:
//
//	data: {"type":"metadata","dataset_epoch":"...","tle_age_seconds":1800}\n\n
//
// Keep-alive comments (:\n\n) are sent every KeepaliveInterval to prevent timeout.
// Reconnecting clients receive a fresh metadata message on each connection.
package stream

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"strconv"
	"time"

	"github.com/star/stargo/internal/cache"
	"github.com/star/stargo/internal/metrics"
	"github.com/star/stargo/internal/propagation"
	"github.com/star/stargo/internal/tle"
)

// Config holds streaming configuration loaded from environment variables.
type Config struct {
	MaxConcurrentPerIP int           // Max concurrent streams per IP (default: 10).
	BandwidthLimit     int           // Bytes per second per stream (default: 1048576).
	KeepaliveInterval  time.Duration // Keep-alive ping interval (default: 30s).
}

// Handler manages SSE streaming connections.
type Handler struct {
	cache   *cache.KeyframeCache
	store   *tle.Store
	config  Config
	limiter *streamLimiter
	logger  *slog.Logger
}

// NewHandler creates a new streaming handler.
func NewHandler(kfCache *cache.KeyframeCache, store *tle.Store, config Config, logger *slog.Logger) *Handler {
	return &Handler{
		cache:   kfCache,
		store:   store,
		config:  config,
		limiter: newStreamLimiter(config.MaxConcurrentPerIP),
		logger:  logger,
	}
}

// HandleKeyframes serves the SSE keyframe stream.
// GET /api/v1/stream/keyframes?step=5&horizon=600
func (h *Handler) HandleKeyframes(w http.ResponseWriter, r *http.Request) {
	// Parse query parameters.
	step := 5
	if v := r.URL.Query().Get("step"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 60 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid step parameter, must be 1-60"})
			return
		}
		step = n
	}

	horizon := 600
	if v := r.URL.Query().Get("horizon"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 10 || n > 3600 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid horizon parameter, must be 10-3600"})
			return
		}
		horizon = n
	}

	trail := 20
	if v := r.URL.Query().Get("trail"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 120 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid trail parameter, must be 0-120"})
			return
		}
		trail = n
	}

	// Rate limiting: enforce concurrent stream limit per IP.
	ip := clientIP(r)
	if !h.limiter.acquire(ip) {
		metrics.IncStreamErrors("rate_limit")
		h.logger.Warn("stream rate limit exceeded",
			"remote_ip", ip,
			"current_count", h.limiter.count(ip),
		)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]string{"error": "too many concurrent streams"})
		return
	}

	// Track connection metrics.
	metrics.IncStreamConnections("connect")
	metrics.IncStreamsActive()

	startTime := time.Now()
	h.logger.Info("stream connected",
		"remote_ip", ip,
		"user_agent", r.Header.Get("User-Agent"),
		"step", step,
		"horizon", horizon,
	)

	// Cleanup on disconnect: release rate limit slot and update metrics.
	defer func() {
		h.limiter.release(ip)
		metrics.IncStreamConnections("disconnect")
		metrics.DecStreamsActive()
		h.logger.Info("stream disconnected",
			"remote_ip", ip,
			"duration_seconds", int(time.Since(startTime).Seconds()),
		)
	}()

	// Verify flusher support (required for SSE).
	flusher, ok := w.(http.Flusher)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "streaming not supported"})
		return
	}

	// Set SSE response headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering.
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Use ResponseController to manage write deadlines for long-lived SSE.
	// Clear the server's default WriteTimeout for this connection.
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		h.logger.Debug("could not clear write deadline", "error", err)
	}

	c := &client{
		w:       w,
		flusher: flusher,
		rc:      rc,
		ip:      ip,
		logger:  h.logger,
	}

	// Send jittered retry interval (3-7s) to prevent thundering-herd
	// reconnection storms when the server restarts.
	retryMs := 3000 + rand.Intn(4000)
	fmt.Fprintf(w, "retry: %d\n\n", retryMs)
	flusher.Flush()

	// Send metadata message (first message on every connection).
	if ds := h.store.Get(); ds != nil {
		meta := metadataMessage{
			Type:         "metadata",
			DatasetEpoch: ds.FetchedAt.UTC().Format(time.RFC3339),
			TLEAge:       int(time.Since(ds.FetchedAt).Seconds()),
		}
		if err := c.sendJSON(meta); err != nil {
			metrics.IncStreamErrors("send_error")
			h.logger.Warn("stream send error (metadata)", "remote_ip", ip, "error", err)
			return
		}
	}

	// Stream keyframes at the requested step interval.
	stepDuration := time.Duration(step) * time.Second
	ticker := time.NewTicker(stepDuration)
	defer ticker.Stop()

	keepaliveTicker := time.NewTicker(h.config.KeepaliveInterval)
	defer keepaliveTicker.Stop()

	ctx := r.Context()
	_ = horizon // Accepted for client hint; stream runs until disconnect.

	for {
		select {
		case <-ctx.Done():
			return

		case t := <-ticker.C:
			kf := h.cache.Get(t)
			if kf == nil {
				metrics.IncStreamErrors("cache_miss")
				h.logger.Debug("stream cache miss",
					"timestamp", h.cache.RoundToStep(t).UTC().Format(time.RFC3339),
					"remote_ip", ip,
				)
				continue
			}

			var trailKFs []*propagation.Keyframe
			if trail > 0 {
				trailKFs = h.cache.GetRecent(t, trail)
			}

			batch := buildBatchMessage(kf, trailKFs)
			data, err := json.Marshal(batch)
			if err != nil {
				metrics.IncStreamErrors("marshal_error")
				h.logger.Warn("stream marshal error", "remote_ip", ip, "error", err)
				continue
			}
			if err := c.sendRaw(data); err != nil {
				metrics.IncStreamErrors("send_error")
				h.logger.Warn("stream send error", "remote_ip", ip, "error", err)
				return
			}

			// Reset keepalive since we just sent data.
			keepaliveTicker.Reset(h.config.KeepaliveInterval)

		case <-keepaliveTicker.C:
			if err := c.sendKeepalive(); err != nil {
				metrics.IncStreamErrors("send_error")
				h.logger.Warn("stream keepalive error", "remote_ip", ip, "error", err)
				return
			}
		}
	}
}

// buildBatchMessage formats a keyframe into the SSE batch payload.
// If trailKFs is non-empty, each satellite includes past positions (oldest first).
func buildBatchMessage(kf *propagation.Keyframe, trailKFs []*propagation.Keyframe) keyframeBatchMessage {
	// Build index: NORAD ID → trail positions (oldest first).
	var trailIndex map[int][][3]float64
	if len(trailKFs) > 0 {
		trailIndex = make(map[int][][3]float64, len(kf.Satellites))
		for _, tkf := range trailKFs {
			for _, s := range tkf.Satellites {
				trailIndex[s.NORADID] = append(trailIndex[s.NORADID], s.PositionECEF)
			}
		}
	}

	sats := make([]satPayload, len(kf.Satellites))
	for i, s := range kf.Satellites {
		sats[i] = satPayload{
			ID: s.NORADID,
			P:  s.PositionECEF,
		}
		if trailIndex != nil {
			if tr, ok := trailIndex[s.NORADID]; ok {
				sats[i].Tr = tr
			}
		}
	}
	return keyframeBatchMessage{
		Type:  "keyframe_batch",
		T:     kf.Timestamp.UTC().Format(time.RFC3339),
		Frame: "ECEF",
		Sat:   sats,
	}
}

// SSE message payload types.

type metadataMessage struct {
	Type         string `json:"type"`
	DatasetEpoch string `json:"dataset_epoch"`
	TLEAge       int    `json:"tle_age_seconds"`
}

type keyframeBatchMessage struct {
	Type  string       `json:"type"`
	T     string       `json:"t"`
	Frame string       `json:"frame"`
	Sat   []satPayload `json:"sat"`
}

type satPayload struct {
	ID int          `json:"id"`
	P  [3]float64   `json:"p"`
	Tr [][3]float64 `json:"tr,omitempty"`
}
