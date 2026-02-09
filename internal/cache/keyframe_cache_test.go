package cache

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/star/stargo/internal/propagation"
	"github.com/star/stargo/internal/tle"
)

// Test TLE lines (ISS, from Phase 2).
const (
	issLine1 = "1 25544U 98067A   24100.50000000  .00016717  00000-0  10270-3 0  9005"
	issLine2 = "2 25544  51.6400 100.0000 0001000   0.0000   0.0000 15.50000000    09"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func testStore() *tle.Store {
	store := tle.NewStore()
	store.Set(&tle.TLEDataset{
		Source:    "test",
		FetchedAt: time.Now(),
		Satellites: []tle.TLEEntry{
			{NORADID: 25544, Name: "ISS", Line1: issLine1, Line2: issLine2},
		},
	})
	return store
}

func testPropagator(store *tle.Store) *propagation.Propagator {
	cfg := propagation.PropConfig{Workers: 2, Step: 5 * time.Second, Horizon: 30 * time.Second}
	return propagation.NewPropagator(store, cfg, testLogger())
}

func testConfig() Config {
	return Config{
		Step:        5 * time.Second,
		Horizon:     30 * time.Second,
		GracePeriod: 5 * time.Second,
		Buffer:      10 * time.Second,
	}
}

// TestKeyframeCache tests basic cache operations: put, get, evict.
func TestKeyframeCache(t *testing.T) {
	store := testStore()
	prop := testPropagator(store)
	c := NewKeyframeCache(testConfig(), prop, store, testLogger())

	// Generate a keyframe and put it in the cache.
	ctx := context.Background()
	target := time.Now().Truncate(5 * time.Second)
	kf, err := prop.PropagateToTime(ctx, target)
	if err != nil {
		t.Fatalf("PropagateToTime failed: %v", err)
	}

	c.put(kf)

	// Get should return it.
	got := c.Get(target)
	if got == nil {
		t.Fatal("expected cache hit, got nil")
	}
	if !got.Timestamp.Equal(target) {
		t.Errorf("timestamp mismatch: got %v, want %v", got.Timestamp, target)
	}

	// Stats should reflect one entry.
	stats := c.Stats()
	if stats.Entries != 1 {
		t.Errorf("entries: got %d, want 1", stats.Entries)
	}
	if stats.Hits < 1 {
		t.Errorf("hits: got %d, want >= 1", stats.Hits)
	}
}

// TestRoundToStep verifies timestamp rounding.
func TestRoundToStep(t *testing.T) {
	store := testStore()
	prop := testPropagator(store)
	c := NewKeyframeCache(testConfig(), prop, store, testLogger())

	tests := []struct {
		input    time.Time
		expected time.Time
	}{
		{
			input:    time.Date(2026, 2, 6, 12, 0, 3, 0, time.UTC),
			expected: time.Date(2026, 2, 6, 12, 0, 0, 0, time.UTC),
		},
		{
			input:    time.Date(2026, 2, 6, 12, 0, 7, 0, time.UTC),
			expected: time.Date(2026, 2, 6, 12, 0, 5, 0, time.UTC),
		},
		{
			input:    time.Date(2026, 2, 6, 12, 0, 10, 0, time.UTC),
			expected: time.Date(2026, 2, 6, 12, 0, 10, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		got := c.RoundToStep(tt.input)
		if !got.Equal(tt.expected) {
			t.Errorf("RoundToStep(%v) = %v, want %v", tt.input, got, tt.expected)
		}
	}
}

// TestCacheMiss verifies that a miss returns nil and increments miss counter.
func TestCacheMiss(t *testing.T) {
	store := testStore()
	prop := testPropagator(store)
	c := NewKeyframeCache(testConfig(), prop, store, testLogger())

	got := c.Get(time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC))
	if got != nil {
		t.Fatal("expected nil for cache miss")
	}

	stats := c.Stats()
	if stats.Misses < 1 {
		t.Errorf("misses: got %d, want >= 1", stats.Misses)
	}
}

// TestEvictExpired verifies that expired entries are removed.
func TestEvictExpired(t *testing.T) {
	store := testStore()
	prop := testPropagator(store)
	cfg := testConfig()
	cfg.Buffer = 0 // No buffer — evict immediately if in the past.
	c := NewKeyframeCache(cfg, prop, store, testLogger())

	ctx := context.Background()

	// Put a keyframe in the past.
	pastTime := time.Now().Add(-2 * time.Minute).Truncate(5 * time.Second)
	kf, err := prop.PropagateToTime(ctx, pastTime)
	if err != nil {
		t.Fatalf("PropagateToTime failed: %v", err)
	}
	c.put(kf)

	// Put a keyframe in the future.
	futureTime := time.Now().Add(1 * time.Minute).Truncate(5 * time.Second)
	kf2, err := prop.PropagateToTime(ctx, futureTime)
	if err != nil {
		t.Fatalf("PropagateToTime failed: %v", err)
	}
	c.put(kf2)

	if c.Stats().Entries != 2 {
		t.Fatalf("expected 2 entries, got %d", c.Stats().Entries)
	}

	// Evict.
	removed := c.evictExpired()
	if removed != 1 {
		t.Errorf("expected 1 eviction, got %d", removed)
	}

	// Past entry should be gone, future entry should remain.
	if c.Get(pastTime) != nil {
		t.Error("expected past entry to be evicted")
	}
	if c.Get(futureTime) == nil {
		t.Error("expected future entry to remain")
	}
}

// TestIncrementalGeneration verifies the background warmup fills the cache.
func TestIncrementalGeneration(t *testing.T) {
	store := testStore()
	prop := testPropagator(store)
	cfg := testConfig()
	cfg.Horizon = 15 * time.Second // Small horizon: 4 keyframes (0, 5, 10, 15).
	c := NewKeyframeCache(cfg, prop, store, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Run warmup only (not the full Start loop).
	c.warmup(ctx)

	stats := c.Stats()
	expectedFrames := int(cfg.Horizon/cfg.Step) + 1
	if stats.Entries < expectedFrames {
		t.Errorf("warmup generated %d entries, expected >= %d", stats.Entries, expectedFrames)
	}

	// GetLatest should return something.
	kf := c.GetLatest()
	if kf == nil {
		t.Fatal("GetLatest returned nil after warmup")
	}
}

// TestTLECutover verifies graceful TLE dataset cutover.
func TestTLECutover(t *testing.T) {
	store := testStore()
	prop := testPropagator(store)
	cfg := testConfig()
	cfg.Horizon = 10 * time.Second // 3 keyframes: 0, 5, 10.
	c := NewKeyframeCache(cfg, prop, store, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Warmup with original TLE data.
	c.warmup(ctx)

	oldStats := c.Stats()
	if oldStats.Entries == 0 {
		t.Fatal("no entries after warmup")
	}

	// Simulate TLE update by setting a new dataset with different FetchedAt.
	store.Set(&tle.TLEDataset{
		Source:    "updated",
		FetchedAt: time.Now().Add(1 * time.Second), // Different timestamp.
		Satellites: []tle.TLEEntry{
			{NORADID: 25544, Name: "ISS", Line1: issLine1, Line2: issLine2},
		},
	})

	// Should detect change.
	if !c.tleChanged() {
		t.Fatal("expected tleChanged() to return true after dataset update")
	}

	// Perform cutover.
	c.performCutover(ctx)

	// Grace period should be cleared.
	if c.inGracePeriod.Load() {
		t.Error("grace period should be false after cutover")
	}

	// Cache should have entries (regenerated with new TLE).
	newStats := c.Stats()
	if newStats.Entries == 0 {
		t.Fatal("no entries after cutover")
	}

	// Should no longer detect change.
	if c.tleChanged() {
		t.Error("expected tleChanged() to return false after cutover")
	}
}

// TestGetLatestEmpty verifies GetLatest with empty cache returns nil.
func TestGetLatestEmpty(t *testing.T) {
	store := testStore()
	prop := testPropagator(store)
	c := NewKeyframeCache(testConfig(), prop, store, testLogger())

	got := c.GetLatest()
	if got != nil {
		t.Fatal("expected nil from empty cache")
	}
}

// TestConcurrentAccess verifies cache is safe for concurrent reads and writes.
func TestConcurrentAccess(t *testing.T) {
	store := testStore()
	prop := testPropagator(store)
	c := NewKeyframeCache(testConfig(), prop, store, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start cache in background.
	go c.Start(ctx)

	// Give warmup time to complete.
	time.Sleep(3 * time.Second)

	// Concurrent reads should not panic.
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				c.GetLatest()
				c.Get(time.Now())
				c.Stats()
			}
			done <- struct{}{}
		}()
	}

	for i := 0; i < 10; i++ {
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("timeout waiting for concurrent reads")
		}
	}
}

// TestSizeEstimation verifies the size estimation is reasonable.
func TestSizeEstimation(t *testing.T) {
	store := testStore()
	prop := testPropagator(store)
	cfg := testConfig()
	cfg.Horizon = 10 * time.Second
	c := NewKeyframeCache(cfg, prop, store, testLogger())

	ctx := context.Background()
	c.warmup(ctx)

	stats := c.Stats()
	if stats.SizeBytes <= 0 {
		t.Errorf("expected positive size estimate, got %d", stats.SizeBytes)
	}

	// With 1 satellite and 3 entries, size should be small (< 1KB).
	if stats.SizeBytes > 10000 {
		t.Errorf("size estimate seems too large for 1 satellite: %d bytes", stats.SizeBytes)
	}
}
