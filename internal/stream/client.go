package stream

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/star/stargo/internal/metrics"
)

// client manages a single SSE connection's write operations.
type client struct {
	w       http.ResponseWriter
	flusher http.Flusher
	rc      *http.ResponseController
	ip      string
	logger  *slog.Logger

	messagesSent int64
	bytesSent    int64
}

// sendJSON marshals v as JSON and sends it as an SSE "data:" message.
// SSE format: "data: {json}\n\n"
func (c *client) sendJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("json marshal: %w", err)
	}

	// Extend write deadline before each write to prevent timeout on long-lived connections.
	if err := c.rc.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil {
		c.logger.Debug("could not set write deadline", "error", err)
	}

	msg := fmt.Sprintf("data: %s\n\n", data)
	n, err := fmt.Fprint(c.w, msg)
	if err != nil {
		return fmt.Errorf("write: %w", err)
	}

	c.flusher.Flush()
	c.messagesSent++
	c.bytesSent += int64(n)
	metrics.IncStreamMessages()
	metrics.AddStreamBytes(int64(n))

	return nil
}

// sendKeepalive sends an SSE comment line to keep the connection alive.
// SSE comment format: ":\n\n"
func (c *client) sendKeepalive() error {
	if err := c.rc.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil {
		c.logger.Debug("could not set write deadline", "error", err)
	}

	n, err := fmt.Fprint(c.w, ":\n\n")
	if err != nil {
		return fmt.Errorf("keepalive write: %w", err)
	}

	c.flusher.Flush()
	c.bytesSent += int64(n)
	metrics.AddStreamBytes(int64(n))

	return nil
}
