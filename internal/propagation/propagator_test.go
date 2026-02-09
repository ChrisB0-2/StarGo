package propagation

import (
	"context"
	"log/slog"
	"math"
	"os"
	"testing"
	"time"

	"github.com/star/stargo/internal/tle"
	"github.com/star/stargo/internal/transform"
)

// ISS TLE (epoch 2024, will still propagate reasonably for near-future times).
// These are real ISS orbital elements used for testing.
const (
	issLine1 = "1 25544U 98067A   24100.50000000  .00016717  00000-0  10270-3 0  9005"
	issLine2 = "2 25544  51.6400 100.0000 0001000   0.0000   0.0000 15.50000000    09"
)

// Starlink TLE (typical LEO constellation satellite).
const (
	starlinkLine1 = "1 44713U 19074A   24100.50000000  .00001000  00000-0  10000-4 0  9995"
	starlinkLine2 = "2 44713  53.0000 200.0000 0001500  90.0000 270.0000 15.06000000    05"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// TestPropagateSingle verifies that a single satellite can be propagated
// and that the ECEF output is reasonable.
func TestPropagateSingle(t *testing.T) {
	prop, err := NewSGP4Propagator(issLine1, issLine2, 25544)
	if err != nil {
		t.Fatalf("NewSGP4Propagator failed: %v", err)
	}

	// Propagate to a time near the TLE epoch.
	target := time.Date(2024, 4, 10, 12, 0, 0, 0, time.UTC)
	teme, err := prop.Propagate(target.Year(), int(target.Month()), target.Day(), target.Hour(), target.Minute(), target.Second())
	if err != nil {
		t.Fatalf("Propagate failed: %v", err)
	}

	// Verify TEME position magnitude is reasonable for ISS (~420km altitude).
	// Expected: ~6371 + 420 ≈ 6791 km.
	mag := math.Sqrt(teme.X*teme.X + teme.Y*teme.Y + teme.Z*teme.Z)
	if mag < 6500 || mag > 7000 {
		t.Errorf("TEME position magnitude = %.1f km, expected ~6791 km (ISS orbit)", mag)
	}

	// Transform to ECEF and verify.
	ecef := transform.TEMEToECEF(teme, target)
	if !transform.ValidateECEF(ecef) {
		t.Errorf("ECEF position failed validation: [%.1f, %.1f, %.1f] m", ecef.X, ecef.Y, ecef.Z)
	}

	// ECEF magnitude should match TEME magnitude (just rotated + unit converted).
	ecefMag := math.Sqrt(ecef.X*ecef.X+ecef.Y*ecef.Y+ecef.Z*ecef.Z) / 1000.0
	if math.Abs(ecefMag-mag) > 0.01 {
		t.Errorf("ECEF magnitude = %.3f km, TEME magnitude = %.3f km (should match)", ecefMag, mag)
	}
}

// TestPropagateInvalidTLE verifies that an invalid TLE returns an error.
func TestPropagateInvalidTLE(t *testing.T) {
	_, err := NewSGP4Propagator("invalid line 1", "invalid line 2", 99999)
	if err == nil {
		t.Fatal("expected error for invalid TLE, got nil")
	}
	t.Logf("Expected error for invalid TLE: %v", err)
}

// TestWorkerPoolBatch verifies the worker pool processes multiple satellites correctly.
func TestWorkerPoolBatch(t *testing.T) {
	logger := testLogger()
	pool := NewWorkerPool(4, logger)

	entries := []tle.TLEEntry{
		{NORADID: 25544, Name: "ISS", Line1: issLine1, Line2: issLine2},
		{NORADID: 44713, Name: "STARLINK-1007", Line1: starlinkLine1, Line2: starlinkLine2},
	}

	target := time.Date(2024, 4, 10, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()

	positions, successCount, errorCount := pool.PropagateBatch(ctx, entries, target)
	if errorCount > 0 {
		t.Logf("errors: %d (may be expected for synthetic TLE)", errorCount)
	}
	if successCount == 0 {
		t.Fatal("expected at least one successful propagation")
	}

	// Verify each position is physically reasonable.
	for _, pos := range positions {
		ecef := transform.PositionECEF{X: pos.PositionECEF[0], Y: pos.PositionECEF[1], Z: pos.PositionECEF[2]}
		if !transform.ValidateECEF(ecef) {
			t.Errorf("NORAD %d: ECEF position failed validation: %v", pos.NORADID, pos.PositionECEF)
		}
	}
}

// TestWorkerPoolCancellation verifies the worker pool respects context cancellation.
func TestWorkerPoolCancellation(t *testing.T) {
	logger := testLogger()
	pool := NewWorkerPool(2, logger)

	// Create many entries to ensure some are still pending when we cancel.
	entries := make([]tle.TLEEntry, 100)
	for i := range entries {
		entries[i] = tle.TLEEntry{
			NORADID: 25544 + i,
			Name:    "TEST",
			Line1:   issLine1,
			Line2:   issLine2,
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	target := time.Date(2024, 4, 10, 12, 0, 0, 0, time.UTC)
	positions, _, _ := pool.PropagateBatch(ctx, entries, target)

	// With immediate cancellation, we should get fewer results than entries.
	// (Some may still complete before cancellation propagates.)
	if len(positions) >= len(entries) {
		t.Errorf("expected fewer results with cancelled context, got %d/%d", len(positions), len(entries))
	}
}

// TestPropagatorGenerateKeyframes verifies keyframe generation over a horizon.
func TestPropagatorGenerateKeyframes(t *testing.T) {
	logger := testLogger()
	store := tle.NewStore()

	store.Set(&tle.TLEDataset{
		Source:    "test",
		FetchedAt: time.Now(),
		Satellites: []tle.TLEEntry{
			{NORADID: 25544, Name: "ISS", Line1: issLine1, Line2: issLine2},
		},
	})

	cfg := PropConfig{
		Workers: 2,
		Step:    5 * time.Second,
		Horizon: 15 * time.Second, // Small horizon for test speed.
	}

	prop := NewPropagator(store, cfg, logger)
	ctx := context.Background()
	start := time.Date(2024, 4, 10, 12, 0, 0, 0, time.UTC)

	keyframes, err := prop.GenerateKeyframes(ctx, start)
	if err != nil {
		t.Fatalf("GenerateKeyframes failed: %v", err)
	}

	// With 15s horizon and 5s step: frames at 0s, 5s, 10s, 15s = 4 frames.
	expectedFrames := 4
	if len(keyframes) != expectedFrames {
		t.Errorf("got %d keyframes, want %d", len(keyframes), expectedFrames)
	}

	// Verify timestamps are spaced correctly.
	for i, kf := range keyframes {
		expectedTime := start.Add(time.Duration(i) * cfg.Step)
		if !kf.Timestamp.Equal(expectedTime) {
			t.Errorf("keyframe %d: time = %v, want %v", i, kf.Timestamp, expectedTime)
		}
		if len(kf.Satellites) == 0 {
			t.Errorf("keyframe %d: no satellites", i)
		}
	}
}

// TestPropagatorNoDataset verifies error when no TLE data is loaded.
func TestPropagatorNoDataset(t *testing.T) {
	logger := testLogger()
	store := tle.NewStore() // Empty store.

	cfg := PropConfig{Workers: 2, Step: 5 * time.Second, Horizon: 60 * time.Second}
	prop := NewPropagator(store, cfg, logger)

	_, err := prop.PropagateToTime(context.Background(), time.Now())
	if err == nil {
		t.Fatal("expected error when no dataset loaded")
	}
}

// BenchmarkPropagate1000 benchmarks propagating 1000 satellites.
func BenchmarkPropagate1000(b *testing.B) {
	logger := testLogger()

	// Create 1000 entries using the ISS TLE (same TLE, different NORAD IDs).
	entries := make([]tle.TLEEntry, 1000)
	for i := range entries {
		entries[i] = tle.TLEEntry{
			NORADID: 25544 + i,
			Name:    "TEST",
			Line1:   issLine1,
			Line2:   issLine2,
		}
	}

	store := tle.NewStore()
	store.Set(&tle.TLEDataset{
		Source:     "bench",
		FetchedAt:  time.Now(),
		Satellites: entries,
	})

	cfg := PropConfig{Workers: 4, Step: 5 * time.Second, Horizon: 5 * time.Second}
	prop := NewPropagator(store, cfg, logger)
	target := time.Date(2024, 4, 10, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := prop.PropagateToTime(ctx, target)
		if err != nil {
			b.Fatal(err)
		}
	}
}
