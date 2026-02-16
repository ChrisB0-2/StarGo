package passes

import (
	"context"
	"testing"
	"time"

	"github.com/star/stargo/internal/tle"
	"github.com/star/stargo/internal/transform"
)

// Real ISS TLE (epoch Feb 2025, valid for testing pass geometry).
var issTLE = tle.TLEEntry{
	NORADID: 25544,
	Name:    "ISS (ZARYA)",
	Line1:   "1 25544U 98067A   25045.18032407  .00016717  00000+0  30099-3 0  9993",
	Line2:   "2 25544  51.6412 193.5765 0003457 126.2851 233.8519 15.49874301495058",
	Epoch:   time.Date(2025, 2, 14, 4, 19, 40, 0, time.UTC),
}

// NYC observer.
var nycObserver = transform.NewObserverPosition(40.7128, -74.006, 10)

func TestPredictISS(t *testing.T) {
	req := Request{
		Observer:     nycObserver,
		Entries:      []tle.TLEEntry{issTLE},
		Start:        time.Date(2025, 2, 14, 12, 0, 0, 0, time.UTC),
		HorizonHours: 24,
		MinElevation: 0,
		MaxPasses:    10,
	}

	results := Predict(context.Background(), req)

	if len(results) != 1 {
		t.Fatalf("expected 1 satellite result, got %d", len(results))
	}

	sat := results[0]
	if sat.NORADID != 25544 {
		t.Errorf("NORAD ID = %d, want 25544", sat.NORADID)
	}
	if sat.Error != "" {
		t.Fatalf("unexpected error: %s", sat.Error)
	}

	// ISS in LEO should have multiple passes over 24h from NYC.
	if len(sat.Passes) == 0 {
		t.Fatal("expected at least 1 ISS pass over NYC in 24h")
	}

	for i, p := range sat.Passes {
		// Validate pass structure.
		if p.DurationSeconds < 10 {
			t.Errorf("pass %d: duration %.1fs too short", i, p.DurationSeconds)
		}
		if p.MaxElevation <= 0 {
			t.Errorf("pass %d: max elevation %.2f should be positive", i, p.MaxElevation)
		}
		if p.MaxElevation > 90 {
			t.Errorf("pass %d: max elevation %.2f exceeds 90 degrees", i, p.MaxElevation)
		}
		if p.AzimuthAtMax < 0 || p.AzimuthAtMax >= 360 {
			t.Errorf("pass %d: azimuth at max %.2f out of range", i, p.AzimuthAtMax)
		}
		if p.StartAzimuth < 0 || p.StartAzimuth >= 360 {
			t.Errorf("pass %d: start azimuth %.2f out of range", i, p.StartAzimuth)
		}
		if p.EndAzimuth < 0 || p.EndAzimuth >= 360 {
			t.Errorf("pass %d: end azimuth %.2f out of range", i, p.EndAzimuth)
		}
		if !p.StartTime.Before(p.MaxElevationTime) || !p.MaxElevationTime.Before(p.EndTime) {
			t.Errorf("pass %d: time ordering violated: start=%v max=%v end=%v", i, p.StartTime, p.MaxElevationTime, p.EndTime)
		}

		// Validate ground track.
		if len(p.GroundTrack) == 0 {
			t.Errorf("pass %d: expected ground track points, got none", i)
		}
		for j, gt := range p.GroundTrack {
			if gt.Latitude < -90 || gt.Latitude > 90 {
				t.Errorf("pass %d gt %d: latitude %.2f out of range", i, j, gt.Latitude)
			}
			if gt.Longitude < -180 || gt.Longitude > 180 {
				t.Errorf("pass %d gt %d: longitude %.2f out of range", i, j, gt.Longitude)
			}
			if gt.Altitude < 100000 || gt.Altitude > 1000000 {
				t.Errorf("pass %d gt %d: altitude %.0f m out of LEO range", i, j, gt.Altitude)
			}
		}

		t.Logf("pass %d: start=%v maxEl=%.1f° az=%.1f° dur=%.0fs groundTrack=%d pts",
			i, p.StartTime.Format(time.RFC3339), p.MaxElevation, p.AzimuthAtMax, p.DurationSeconds, len(p.GroundTrack))
	}
}

func TestPredictMinElevationFilter(t *testing.T) {
	// Predict with min_elevation=0 and min_elevation=45 — the latter should find fewer passes.
	reqLow := Request{
		Observer:     nycObserver,
		Entries:      []tle.TLEEntry{issTLE},
		Start:        time.Date(2025, 2, 14, 12, 0, 0, 0, time.UTC),
		HorizonHours: 48,
		MinElevation: 0,
		MaxPasses:    20,
	}
	reqHigh := Request{
		Observer:     nycObserver,
		Entries:      []tle.TLEEntry{issTLE},
		Start:        time.Date(2025, 2, 14, 12, 0, 0, 0, time.UTC),
		HorizonHours: 48,
		MinElevation: 45,
		MaxPasses:    20,
	}

	resultsLow := Predict(context.Background(), reqLow)
	resultsHigh := Predict(context.Background(), reqHigh)

	nLow := len(resultsLow[0].Passes)
	nHigh := len(resultsHigh[0].Passes)

	if nLow == 0 {
		t.Fatal("expected passes with min_elevation=0")
	}
	if nHigh >= nLow {
		t.Errorf("min_elevation=45 passes (%d) should be fewer than min_elevation=0 passes (%d)", nHigh, nLow)
	}
}

func TestPredictCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	req := Request{
		Observer:     nycObserver,
		Entries:      []tle.TLEEntry{issTLE},
		Start:        time.Now().UTC(),
		HorizonHours: 24,
		MinElevation: 0,
		MaxPasses:    10,
	}

	// Should not panic and should return quickly.
	results := Predict(ctx, req)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestPredictInvalidTLE(t *testing.T) {
	badEntry := tle.TLEEntry{
		NORADID: 99999,
		Name:    "BAD SAT",
		Line1:   "1 99999U 00000A   25045.00000000  .00000000  00000+0  00000+0 0  0000",
		Line2:   "2 99999   0.0000   0.0000 0000000   0.0000   0.0000  0.00000000 0000",
	}

	req := Request{
		Observer:     nycObserver,
		Entries:      []tle.TLEEntry{issTLE, badEntry},
		Start:        time.Date(2025, 2, 14, 12, 0, 0, 0, time.UTC),
		HorizonHours: 24,
		MinElevation: 0,
		MaxPasses:    10,
	}

	results := Predict(context.Background(), req)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// ISS should succeed.
	if results[0].Error != "" {
		t.Errorf("ISS should succeed, got error: %s", results[0].Error)
	}
	// Bad satellite should report per-satellite error.
	if results[1].Error == "" {
		t.Error("bad TLE should report error")
	}
}

func BenchmarkPredict100Sats24h(b *testing.B) {
	// Create 100 copies of ISS TLE with different NORAD IDs.
	entries := make([]tle.TLEEntry, 100)
	for i := range entries {
		entries[i] = issTLE
		entries[i].NORADID = 25544 + i
	}

	req := Request{
		Observer:     nycObserver,
		Entries:      entries,
		Start:        time.Date(2025, 2, 14, 12, 0, 0, 0, time.UTC),
		HorizonHours: 24,
		MinElevation: 10,
		MaxPasses:    10,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Predict(context.Background(), req)
	}
}
