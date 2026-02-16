package stream

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/star/stargo/internal/metrics"
)

// Pre-allocated SSE framing bytes to avoid per-write allocations.
var (
	sseDataPrefix = []byte("data: ")
	sseDataSuffix = []byte("\n\n")
	sseComment    = []byte(":\n\n")
)

// client manages a single SSE connection's write operations and bandwidth budget.
// All methods are called from a single goroutine (the request handler), so no
// locking is needed on client state.
type client struct {
	w       http.ResponseWriter
	flusher http.Flusher
	rc      *http.ResponseController
	gzw     *gzip.Writer // nil if gzip disabled
	ip      string
	logger  *slog.Logger

	messagesSent int64
	bytesSent    int64

	// Token bucket for per-client bandwidth limiting.
	// Rate and budget are measured in uncompressed payload bytes.
	tokens     float64
	maxTokens  float64
	rate       float64 // bytes per second
	lastRefill time.Time
}

// initBandwidth configures the per-client token bucket.
// bytesPerSec <= 0 disables bandwidth limiting.
func (c *client) initBandwidth(bytesPerSec int) {
	c.rate = float64(bytesPerSec)
	c.maxTokens = c.rate * 2 // 2 seconds of burst
	c.tokens = c.maxTokens
	c.lastRefill = time.Now()
}

// refill adds tokens based on elapsed time since last refill.
func (c *client) refill() {
	now := time.Now()
	elapsed := now.Sub(c.lastRefill).Seconds()
	c.lastRefill = now
	c.tokens += elapsed * c.rate
	if c.tokens > c.maxTokens {
		c.tokens = c.maxTokens
	}
}

// hasBudget returns true if n uncompressed bytes fit within the current bandwidth budget.
// Returns true unconditionally if bandwidth limiting is disabled (rate <= 0).
func (c *client) hasBudget(n int) bool {
	if c.rate <= 0 {
		return true
	}
	c.refill()
	return c.tokens >= float64(n)
}

// activeWriter returns the gzip writer if enabled, otherwise the raw ResponseWriter.
func (c *client) activeWriter() io.Writer {
	if c.gzw != nil {
		return c.gzw
	}
	return c.w
}

// flush flushes gzip (if enabled) and HTTP transport layers.
func (c *client) flush() {
	if c.gzw != nil {
		c.gzw.Flush()
	}
	c.flusher.Flush()
}

// sendJSON marshals v as JSON and sends it as an SSE "data:" message.
// SSE format: "data: {json}\n\n"
func (c *client) sendJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("json marshal: %w", err)
	}
	return c.sendRaw(data)
}

// sendRaw writes pre-serialized JSON bytes as an SSE "data:" message.
// Consumes len(data) tokens from the bandwidth budget.
// Uses direct Write calls instead of fmt.Sprintf to avoid copying the payload.
func (c *client) sendRaw(data []byte) error {
	if err := c.rc.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil {
		c.logger.Debug("could not set write deadline", "error", err)
	}

	w := c.activeWriter()
	if _, err := w.Write(sseDataPrefix); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if _, err := w.Write(sseDataSuffix); err != nil {
		return fmt.Errorf("write: %w", err)
	}

	c.flush()
	c.tokens -= float64(len(data))

	n := int64(len(sseDataPrefix) + len(data) + len(sseDataSuffix))
	c.messagesSent++
	c.bytesSent += n
	metrics.IncStreamMessages()
	metrics.AddStreamBytes(n)

	return nil
}

// frameSkipMessage notifies the client that a frame was intentionally dropped.
type frameSkipMessage struct {
	Type   string `json:"type"`
	T      string `json:"t"`
	Reason string `json:"reason"`
}

// sendFrameSkip sends a small notification that a keyframe was dropped
// due to bandwidth constraints. The message is small (~80 bytes) and
// always sent regardless of remaining budget.
func (c *client) sendFrameSkip(ts time.Time, reason string) error {
	data, _ := json.Marshal(frameSkipMessage{
		Type:   "frame_skip",
		T:      ts.UTC().Format(time.RFC3339),
		Reason: reason,
	})
	return c.sendRaw(data)
}

// sendKeepalive sends an SSE comment line to keep the connection alive.
// SSE comment format: ":\n\n"
func (c *client) sendKeepalive() error {
	if err := c.rc.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil {
		c.logger.Debug("could not set write deadline", "error", err)
	}

	w := c.activeWriter()
	if _, err := w.Write(sseComment); err != nil {
		return fmt.Errorf("keepalive write: %w", err)
	}

	c.flush()
	c.bytesSent += int64(len(sseComment))
	metrics.AddStreamBytes(int64(len(sseComment)))

	return nil
}
