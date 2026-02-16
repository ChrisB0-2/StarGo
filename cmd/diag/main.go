package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/star/stargo/internal/passes"
	"github.com/star/stargo/internal/tle"
	"github.com/star/stargo/internal/transform"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	data, err := os.ReadFile("/tmp/stargo/tle/tle_1770838763.txt")
	if err != nil {
		fmt.Println("ERROR reading TLE cache:", err)
		os.Exit(1)
	}

	entries, err := tle.Parse(bytes.NewReader(data), logger)
	if err != nil {
		fmt.Println("ERROR parsing TLE:", err)
		os.Exit(1)
	}
	fmt.Printf("Loaded %d TLE entries\n", len(entries))
	if len(entries) > 0 {
		fmt.Printf("First entry: %s (NORAD %d) epoch %v\n", entries[0].Name, entries[0].NORADID, entries[0].Epoch)
	}

	subset := entries[:5]
	obs := transform.NewObserverPosition(39.7392, -104.9903, 1609)

	now := time.Now().UTC()
	fmt.Printf("Prediction start: %v\n", now)

	req := passes.Request{
		Observer:     obs,
		Entries:      subset,
		Start:        now,
		HorizonHours: 72,
		MinElevation: 1,
		MaxPasses:    10,
	}

	results := passes.Predict(context.Background(), req)

	totalPasses := 0
	for _, sat := range results {
		if sat.Error != "" {
			fmt.Printf("  NORAD %d: ERROR %s\n", sat.NORADID, sat.Error)
		} else {
			fmt.Printf("  NORAD %d: %d passes\n", sat.NORADID, len(sat.Passes))
			totalPasses += len(sat.Passes)
			for j, p := range sat.Passes {
				fmt.Printf("    pass %d: start=%v maxEl=%.1f° dur=%.0fs\n",
					j, p.StartTime.Format(time.RFC3339), p.MaxElevation, p.DurationSeconds)
			}
		}
	}
	fmt.Printf("\nTotal passes found: %d\n", totalPasses)
}
