package cache

import (
	"context"
	"time"

	"github.com/star/stargo/internal/metrics"
)

// Start begins the background cache maintenance loop. It performs an initial
// warmup (filling the full [now, now+horizon] window), then continuously:
//   - Generates new keyframes at the leading edge
//   - Evicts expired entries from the trailing edge
//   - Detects TLE dataset changes and triggers cutover
//
// Blocks until ctx is cancelled.
func (c *KeyframeCache) Start(ctx context.Context) {
	// Wait for TLE data to be available before warmup.
	if !c.waitForTLEData(ctx) {
		return
	}

	c.warmup(ctx)

	ticker := time.NewTicker(c.config.Step)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.logger.Info("cache generator stopped")
			return
		case <-ticker.C:
			c.tick(ctx)
		}
	}
}

// waitForTLEData blocks until a TLE dataset is available in the store,
// checking every second. Returns false if ctx is cancelled.
func (c *KeyframeCache) waitForTLEData(ctx context.Context) bool {
	ds := c.store.Get()
	if ds != nil {
		return true
	}

	c.logger.Info("cache waiting for TLE data...")
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			if c.store.Get() != nil {
				c.logger.Info("TLE data available, starting cache warmup")
				return true
			}
		}
	}
}

// warmup fills the cache with keyframes for [now, now+horizon].
func (c *KeyframeCache) warmup(ctx context.Context) {
	ds := c.store.Get()
	if ds == nil {
		return
	}
	c.currentFetchedAt = ds.FetchedAt

	now := c.RoundToStep(time.Now())
	numFrames := int(c.config.Horizon/c.config.Step) + 1

	c.logger.Info("cache warmup starting",
		"frames", numFrames,
		"from", now.UTC().Format(time.RFC3339),
		"to", now.Add(c.config.Horizon).UTC().Format(time.RFC3339),
	)

	start := time.Now()
	generated := 0

	for i := 0; i < numFrames; i++ {
		select {
		case <-ctx.Done():
			return
		default:
		}

		targetTime := now.Add(time.Duration(i) * c.config.Step)
		kf, err := c.prop.PropagateToTime(ctx, targetTime)
		if err != nil {
			c.logger.Warn("warmup propagation failed", "timestamp", targetTime, "error", err)
			metrics.IncCacheRegenerationErrors()
			continue
		}

		c.put(kf)
		generated++
	}

	duration := time.Since(start)
	c.logger.Info("cache warmup complete",
		"generated", generated,
		"duration_ms", duration.Milliseconds(),
	)
}

// tick runs one iteration of the maintenance loop.
func (c *KeyframeCache) tick(ctx context.Context) {
	// Check for TLE dataset change.
	if c.tleChanged() {
		c.performCutover(ctx)
		return
	}

	// Generate leading edge keyframe.
	c.generateLeadingEdge(ctx)

	// Evict expired entries.
	c.evictExpired()
}

// generateLeadingEdge generates the keyframe at the leading edge of the window.
func (c *KeyframeCache) generateLeadingEdge(ctx context.Context) {
	target := c.RoundToStep(time.Now().Add(c.config.Horizon))

	// Skip if already cached.
	if c.Get(target) != nil {
		return
	}

	start := time.Now()
	kf, err := c.prop.PropagateToTime(ctx, target)
	duration := time.Since(start)

	if err != nil {
		c.logger.Warn("leading edge generation failed",
			"timestamp", target.UTC().Format(time.RFC3339),
			"error", err,
		)
		metrics.IncCacheRegenerationErrors()
		return
	}

	c.put(kf)
	metrics.ObserveCacheRegenerationDuration(duration)

	c.logger.Debug("leading edge generated",
		"timestamp", target.UTC().Format(time.RFC3339),
		"duration_ms", duration.Milliseconds(),
	)
}
