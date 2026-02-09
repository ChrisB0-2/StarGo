package propagation

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/star/stargo/internal/metrics"
	"github.com/star/stargo/internal/tle"
)

// Propagator orchestrates keyframe generation for TLE datasets.
type Propagator struct {
	store  *tle.Store
	pool   *WorkerPool
	config PropConfig
	logger *slog.Logger
}

// NewPropagator creates a new propagation orchestrator.
func NewPropagator(store *tle.Store, config PropConfig, logger *slog.Logger) *Propagator {
	pool := NewWorkerPool(config.Workers, logger)
	return &Propagator{
		store:  store,
		pool:   pool,
		config: config,
		logger: logger,
	}
}

// PropagateToTime generates a single keyframe at the given target time.
// Uses the current TLE dataset from the store.
func (p *Propagator) PropagateToTime(ctx context.Context, targetTime time.Time) (*Keyframe, error) {
	ds := p.store.Get()
	if ds == nil {
		return nil, fmt.Errorf("no TLE dataset loaded")
	}

	p.logger.Info("propagating",
		"satellite_count", len(ds.Satellites),
		"target_time", targetTime.UTC().Format(time.RFC3339),
		"workers", p.config.Workers,
	)

	start := time.Now()
	positions, successCount, errorCount := p.pool.PropagateBatch(ctx, ds.Satellites, targetTime)
	duration := time.Since(start)

	metrics.RecordPropagation(duration, successCount, errorCount)

	p.logger.Info("propagation complete",
		"success", successCount,
		"errors", errorCount,
		"duration_ms", duration.Milliseconds(),
	)

	return &Keyframe{
		Timestamp:  targetTime,
		Satellites: positions,
	}, nil
}

// GenerateKeyframes generates keyframes from startTime over the configured horizon
// at the configured step interval.
func (p *Propagator) GenerateKeyframes(ctx context.Context, startTime time.Time) ([]*Keyframe, error) {
	ds := p.store.Get()
	if ds == nil {
		return nil, fmt.Errorf("no TLE dataset loaded")
	}

	numFrames := int(p.config.Horizon/p.config.Step) + 1
	keyframes := make([]*Keyframe, 0, numFrames)

	for i := 0; i < numFrames; i++ {
		select {
		case <-ctx.Done():
			return keyframes, ctx.Err()
		default:
		}

		targetTime := startTime.Add(time.Duration(i) * p.config.Step)
		kf, err := p.PropagateToTime(ctx, targetTime)
		if err != nil {
			return keyframes, fmt.Errorf("keyframe %d at %s: %w", i, targetTime.Format(time.RFC3339), err)
		}
		keyframes = append(keyframes, kf)
	}

	return keyframes, nil
}
