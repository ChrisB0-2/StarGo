package cache

import (
	"context"
	"time"

	"github.com/star/stargo/internal/metrics"
)

// tleChanged checks if the TLE dataset has been updated since the cache was last built.
func (c *KeyframeCache) tleChanged() bool {
	ds := c.store.Get()
	if ds == nil {
		return false
	}
	return !ds.FetchedAt.Equal(c.currentFetchedAt)
}

// performCutover rebuilds the entire cache using the new TLE dataset.
//
// Strategy:
//  1. Set grace period flag (old cache continues serving reads)
//  2. Build new entries map in the background
//  3. Atomic swap: replace old entries with new
//  4. Clear grace period flag
//
// During the rebuild, reads against the old cache continue uninterrupted.
func (c *KeyframeCache) performCutover(ctx context.Context) {
	ds := c.store.Get()
	if ds == nil {
		return
	}

	c.logger.Info("TLE cutover starting",
		"old_dataset_fetched_at", c.currentFetchedAt.UTC().Format(time.RFC3339),
		"new_dataset_fetched_at", ds.FetchedAt.UTC().Format(time.RFC3339),
	)

	c.inGracePeriod.Store(true)
	metrics.SetCacheGracePeriodActive(true)

	start := time.Now()
	now := c.RoundToStep(time.Now())
	numFrames := int(c.config.Horizon/c.config.Step) + 1

	newEntries := make(map[time.Time]*CacheEntry, numFrames)
	generated := 0

	for i := 0; i < numFrames; i++ {
		select {
		case <-ctx.Done():
			c.inGracePeriod.Store(false)
			metrics.SetCacheGracePeriodActive(false)
			c.logger.Warn("cutover cancelled by context")
			return
		default:
		}

		targetTime := now.Add(time.Duration(i) * c.config.Step)
		kf, err := c.prop.PropagateToTime(ctx, targetTime)
		if err != nil {
			c.logger.Warn("cutover propagation failed",
				"timestamp", targetTime.UTC().Format(time.RFC3339),
				"error", err,
			)
			metrics.IncCacheRegenerationErrors()
			continue
		}

		key := c.RoundToStep(kf.Timestamp)
		newEntries[key] = &CacheEntry{
			Keyframe:    kf,
			GeneratedAt: time.Now(),
		}
		generated++
	}

	// Atomic swap.
	c.replaceAll(newEntries)
	c.currentFetchedAt = ds.FetchedAt

	c.inGracePeriod.Store(false)
	metrics.SetCacheGracePeriodActive(false)

	duration := time.Since(start)
	c.logger.Info("TLE cutover complete",
		"duration_ms", duration.Milliseconds(),
		"entries_replaced", generated,
	)
	metrics.ObserveCacheRegenerationDuration(duration)
}
