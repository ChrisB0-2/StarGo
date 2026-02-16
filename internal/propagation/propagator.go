package propagation

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/star/stargo/internal/metrics"
	"github.com/star/stargo/internal/tle"
)

// sgp4Cache holds preinitialized SGP4 propagators for a specific TLE dataset.
// Immutable after construction; safe for concurrent reads.
type sgp4Cache struct {
	props     map[int]*SGP4Propagator
	fetchedAt time.Time
}

// Propagator orchestrates keyframe generation for TLE datasets.
type Propagator struct {
	store  *tle.Store
	pool   *WorkerPool
	config PropConfig
	logger *slog.Logger
	sgp4   atomic.Pointer[sgp4Cache]
	sgp4Mu sync.Mutex // serializes cache rebuilds
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

// cachedProps returns preinitialized SGP4 propagators for the given dataset.
// Rebuilds the cache if the dataset has changed (double-checked locking).
func (p *Propagator) cachedProps(ds *tle.TLEDataset) map[int]*SGP4Propagator {
	if c := p.sgp4.Load(); c != nil && c.fetchedAt.Equal(ds.FetchedAt) {
		return c.props
	}

	p.sgp4Mu.Lock()
	defer p.sgp4Mu.Unlock()

	if c := p.sgp4.Load(); c != nil && c.fetchedAt.Equal(ds.FetchedAt) {
		return c.props
	}

	props := make(map[int]*SGP4Propagator, len(ds.Satellites))
	var skipped int
	for _, entry := range ds.Satellites {
		if _, ok := props[entry.NORADID]; ok {
			continue
		}
		sp, err := NewSGP4Propagator(entry.Line1, entry.Line2, entry.NORADID)
		if err != nil {
			p.logger.Warn("sgp4 cache init failed", "norad_id", entry.NORADID, "error", err)
			skipped++
			continue
		}
		props[entry.NORADID] = sp
	}

	p.logger.Info("sgp4 propagator cache rebuilt",
		"cached", len(props),
		"skipped", skipped,
		"dataset_fetched_at", ds.FetchedAt.UTC().Format(time.RFC3339),
	)
	p.sgp4.Store(&sgp4Cache{props: props, fetchedAt: ds.FetchedAt})
	return props
}

// PropagateToTime generates a single keyframe at the given target time.
// Uses the current TLE dataset from the store.
func (p *Propagator) PropagateToTime(ctx context.Context, targetTime time.Time) (*Keyframe, error) {
	ds := p.store.Get()
	if ds == nil {
		return nil, fmt.Errorf("no TLE dataset loaded")
	}

	props := p.cachedProps(ds)

	p.logger.Debug("propagating",
		"satellite_count", len(ds.Satellites),
		"target_time", targetTime.UTC().Format(time.RFC3339),
		"workers", p.config.Workers,
	)

	start := time.Now()
	positions, successCount, errorCount := p.pool.PropagateBatch(ctx, ds.Satellites, targetTime, props)
	duration := time.Since(start)

	metrics.RecordPropagation(duration, successCount, errorCount)

	p.logger.Debug("propagation complete",
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
