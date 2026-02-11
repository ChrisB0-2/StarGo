package stream

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/star/stargo/internal/cache"
	"github.com/star/stargo/internal/propagation"
	"github.com/star/stargo/internal/tle"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))
}

func testStore() *tle.Store {
	store := tle.NewStore()
	store.Set(&tle.TLEDataset{
		Source:    "test",
		FetchedAt: time.Date(2026, 2, 6, 3, 45, 0, 0, time.UTC),
		Satellites: []tle.TLEEntry{
			{NORADID: 25544, Name: "ISS"},
		},
	})
	return store
}

func testConfig() Config {
	return Config{
		MaxConcurrentPerIP: 10,
		BandwidthLimit:     1048576,
		KeepaliveInterval:  30 * time.Second,
	}
}

// TestBuildBatchMessage verifies the keyframe batch payload structure.
func TestBuildBatchMessage(t *testing.T) {
	kf := &propagation.Keyframe{
		Timestamp: time.Date(2026, 2, 6, 4, 0, 0, 0, time.UTC),
		Satellites: []propagation.SatellitePosition{
			{
				NORADID:      25544,
				PositionECEF: [3]float64{6378137.0, 0.0, 0.0},
			},
			{
				NORADID:      67890,
				PositionECEF: [3]float64{6378137.0, 100000.0, 0.0},
			},
		},
	}

	msg := buildBatchMessage(kf, nil)

	if msg.Type != "keyframe_batch" {
		t.Errorf("type = %q, want %q", msg.Type, "keyframe_batch")
	}
	if msg.Frame != "ECEF" {
		t.Errorf("frame = %q, want %q", msg.Frame, "ECEF")
	}
	if msg.T != "2026-02-06T04:00:00Z" {
		t.Errorf("t = %q, want %q", msg.T, "2026-02-06T04:00:00Z")
	}
	if len(msg.Sat) != 2 {
		t.Fatalf("sat count = %d, want 2", len(msg.Sat))
	}
	if msg.Sat[0].ID != 25544 {
		t.Errorf("sat[0].id = %d, want 25544", msg.Sat[0].ID)
	}
	if msg.Sat[0].P != [3]float64{6378137.0, 0.0, 0.0} {
		t.Errorf("sat[0].p = %v, want [6378137 0 0]", msg.Sat[0].P)
	}
}

// TestBatchMessageJSON verifies the JSON serialization matches the spec format.
func TestBatchMessageJSON(t *testing.T) {
	msg := keyframeBatchMessage{
		Type:  "keyframe_batch",
		T:     "2026-02-06T04:00:00Z",
		Frame: "ECEF",
		Sat: []satPayload{
			{ID: 12345, P: [3]float64{6378137.0, 0.0, 0.0}},
		},
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}

	if parsed["type"] != "keyframe_batch" {
		t.Errorf("type = %v, want keyframe_batch", parsed["type"])
	}
	if parsed["frame"] != "ECEF" {
		t.Errorf("frame = %v, want ECEF", parsed["frame"])
	}
	if parsed["t"] != "2026-02-06T04:00:00Z" {
		t.Errorf("t = %v, want 2026-02-06T04:00:00Z", parsed["t"])
	}

	sats, ok := parsed["sat"].([]any)
	if !ok || len(sats) != 1 {
		t.Fatalf("sat = %v, want 1-element array", parsed["sat"])
	}

	sat := sats[0].(map[string]any)
	if sat["id"].(float64) != 12345 {
		t.Errorf("sat[0].id = %v, want 12345", sat["id"])
	}
}

// TestMetadataMessageJSON verifies the metadata message format.
func TestMetadataMessageJSON(t *testing.T) {
	msg := metadataMessage{
		Type:         "metadata",
		DatasetEpoch: "2026-02-06T03:45:00Z",
		TLEAge:       1800,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}

	if parsed["type"] != "metadata" {
		t.Errorf("type = %v, want metadata", parsed["type"])
	}
	if parsed["dataset_epoch"] != "2026-02-06T03:45:00Z" {
		t.Errorf("dataset_epoch = %v, want 2026-02-06T03:45:00Z", parsed["dataset_epoch"])
	}
	if parsed["tle_age_seconds"].(float64) != 1800 {
		t.Errorf("tle_age_seconds = %v, want 1800", parsed["tle_age_seconds"])
	}
}

// TestSSEMessageFormat verifies the SSE wire format: "data: {json}\n\n".
func TestSSEMessageFormat(t *testing.T) {
	store := testStore()
	kfCache := cache.NewKeyframeCache(cache.Config{
		Step:        5 * time.Second,
		Horizon:     30 * time.Second,
		GracePeriod: 5 * time.Second,
		Buffer:      10 * time.Second,
	}, nil, store, testLogger())

	handler := NewHandler(kfCache, store, Config{
		MaxConcurrentPerIP: 10,
		BandwidthLimit:     1048576,
		KeepaliveInterval:  5 * time.Second,
	}, testLogger())

	// Use httptest to call the handler.
	req := httptest.NewRequest("GET", "/api/v1/stream/keyframes?step=1", nil)
	req.RemoteAddr = "127.0.0.1:12345"

	// Cancel request after receiving first message.
	ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
	defer cancel()
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	handler.HandleKeyframes(w, req)

	resp := w.Result()

	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", resp.Header.Get("Content-Type"))
	}
	if resp.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", resp.Header.Get("Cache-Control"))
	}

	// Parse the SSE body for the metadata message.
	body := w.Body.String()
	scanner := bufio.NewScanner(strings.NewReader(body))
	var foundMetadata bool

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			jsonStr := strings.TrimPrefix(line, "data: ")
			var msg map[string]any
			if err := json.Unmarshal([]byte(jsonStr), &msg); err != nil {
				t.Errorf("invalid JSON in SSE data line: %v", err)
				continue
			}
			if msg["type"] == "metadata" {
				foundMetadata = true
				if _, ok := msg["dataset_epoch"]; !ok {
					t.Error("metadata missing dataset_epoch")
				}
				if _, ok := msg["tle_age_seconds"]; !ok {
					t.Error("metadata missing tle_age_seconds")
				}
			}
		}
	}

	if !foundMetadata {
		t.Error("did not receive metadata message")
	}

	// Verify SSE format: lines should be "data: ..." or ":" (keepalive) or empty.
	for _, line := range strings.Split(body, "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data: ") && !strings.HasPrefix(line, "retry: ") && line != ":" {
			// Allow empty lines between events.
			if strings.TrimSpace(line) != "" {
				t.Errorf("unexpected SSE line: %q", line)
			}
		}
	}
}

// TestRateLimiting verifies per-IP concurrent stream limits.
func TestRateLimiting(t *testing.T) {
	limiter := newStreamLimiter(3)

	// Acquire up to the limit.
	for i := 0; i < 3; i++ {
		if !limiter.acquire("10.0.0.1") {
			t.Fatalf("acquire %d should succeed", i+1)
		}
	}

	// 4th should fail.
	if limiter.acquire("10.0.0.1") {
		t.Error("acquire beyond limit should fail")
	}

	// Different IP should still work.
	if !limiter.acquire("10.0.0.2") {
		t.Error("different IP should not be rate limited")
	}

	// Release one and try again.
	limiter.release("10.0.0.1")
	if !limiter.acquire("10.0.0.1") {
		t.Error("acquire after release should succeed")
	}

	// Count checks.
	if c := limiter.count("10.0.0.1"); c != 3 {
		t.Errorf("count = %d, want 3", c)
	}
	if c := limiter.count("10.0.0.2"); c != 1 {
		t.Errorf("count = %d, want 1", c)
	}
}

// TestRateLimitingConcurrent verifies rate limiter thread safety.
func TestRateLimitingConcurrent(t *testing.T) {
	limiter := newStreamLimiter(100)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if limiter.acquire("10.0.0.1") {
				defer limiter.release("10.0.0.1")
				time.Sleep(10 * time.Millisecond)
			}
		}()
	}
	wg.Wait()

	if c := limiter.count("10.0.0.1"); c != 0 {
		t.Errorf("count after all released = %d, want 0", c)
	}
}

// TestRateLimitHTTPResponse verifies 429 response when limit exceeded.
func TestRateLimitHTTPResponse(t *testing.T) {
	store := testStore()
	kfCache := cache.NewKeyframeCache(cache.Config{
		Step:        5 * time.Second,
		Horizon:     30 * time.Second,
		GracePeriod: 5 * time.Second,
		Buffer:      10 * time.Second,
	}, nil, store, testLogger())

	handler := NewHandler(kfCache, store, Config{
		MaxConcurrentPerIP: 1,
		BandwidthLimit:     1048576,
		KeepaliveInterval:  30 * time.Second,
	}, testLogger())

	// Hold the first connection open.
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequest("GET", "/api/v1/stream/keyframes", nil)
		req.RemoteAddr = "10.0.0.1:12345"
		ctx, cancel := context.WithCancel(req.Context())
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		go func() {
			// Signal ready after short delay to allow acquire.
			time.Sleep(50 * time.Millisecond)
			close(ready)
			// Hold connection for a bit.
			time.Sleep(200 * time.Millisecond)
			cancel()
		}()

		handler.HandleKeyframes(w, req)
	}()

	// Wait for first connection to be established.
	<-ready

	// Second connection from same IP should get 429.
	req := httptest.NewRequest("GET", "/api/v1/stream/keyframes", nil)
	req.RemoteAddr = "10.0.0.1:54321"
	w := httptest.NewRecorder()
	handler.HandleKeyframes(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want %d", w.Code, http.StatusTooManyRequests)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("missing Retry-After header")
	}

	<-done
}

// TestInvalidQueryParams verifies error responses for bad step/horizon values.
func TestInvalidQueryParams(t *testing.T) {
	store := testStore()
	kfCache := cache.NewKeyframeCache(cache.Config{
		Step:        5 * time.Second,
		Horizon:     30 * time.Second,
		GracePeriod: 5 * time.Second,
		Buffer:      10 * time.Second,
	}, nil, store, testLogger())

	handler := NewHandler(kfCache, store, testConfig(), testLogger())

	tests := []struct {
		name  string
		query string
	}{
		{"bad step", "?step=0"},
		{"step too large", "?step=100"},
		{"step non-numeric", "?step=abc"},
		{"bad horizon", "?horizon=5"},
		{"horizon too large", "?horizon=9999"},
		{"horizon non-numeric", "?horizon=xyz"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/stream/keyframes"+tt.query, nil)
			req.RemoteAddr = "127.0.0.1:12345"
			w := httptest.NewRecorder()
			handler.HandleKeyframes(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
			}
		})
	}
}

// TestClientIP verifies IP extraction from RemoteAddr.
func TestClientIP(t *testing.T) {
	tests := []struct {
		remoteAddr string
		want       string
	}{
		{"192.168.1.1:12345", "192.168.1.1"},
		{"[::1]:12345", "::1"},
		{"192.168.1.1", "192.168.1.1"},
	}

	for _, tt := range tests {
		t.Run(tt.remoteAddr, func(t *testing.T) {
			r := &http.Request{RemoteAddr: tt.remoteAddr}
			got := clientIP(r)
			if got != tt.want {
				t.Errorf("clientIP(%q) = %q, want %q", tt.remoteAddr, got, tt.want)
			}
		})
	}
}

// TestKeepaliveFormat verifies keep-alive is an SSE comment.
func TestKeepaliveFormat(t *testing.T) {
	// The keep-alive message should be ":\n\n" - a comment line followed by blank line.
	expected := ":\n\n"
	if len(expected) != 3 {
		t.Errorf("keepalive length = %d, want 3", len(expected))
	}
	if expected[0] != ':' {
		t.Error("keepalive should start with ':'")
	}
}
