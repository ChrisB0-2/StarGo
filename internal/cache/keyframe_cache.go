// Package cache provides an in-memory keyframe cache with a rolling window.
//
// The cache maintains keyframes for [now, now+horizon] continuously. A background
// worker generates new keyframes at the leading edge and evicts expired entries
// from the trailing edge. When the TLE dataset changes, the cache is rebuilt
// gracefully without interrupting reads.
package cache

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/star/stargo/internal/metrics"
	"github.com/star/stargo/internal/propagation"
	"github.com/star/stargo/internal/tle"
)

// Config holds cache configuration loaded from environment variables.
type Config struct {
	Step        time.Duration // Keyframe interval (default: 5s)
	Horizon     time.Duration // How far ahead to cache (default: 600s)
	GracePeriod time.Duration // TLE cutover grace period (default: 30s)
	Buffer      time.Duration // Keep entries this long past expiration (default: 60s)
}

// CacheEntry wraps a keyframe with generation metadata.
type CacheEntry struct {
	Keyframe    *propagation.Keyframe
	GeneratedAt time.Time
}

// KeyframeCache is an in-memory cache of keyframes with a rolling window.
// Safe for concurrent use by multiple goroutines.
type KeyframeCache struct {
	mu      sync.RWMutex
	entries map[time.Time]*CacheEntry

	config Config
	prop   *propagation.Propagator
	store  *tle.Store
	logger *slog.Logger

	// Track current TLE dataset for change detection.
	currentFetchedAt time.Time

	// Counters (lock-free).
	hits      atomic.Int64
	misses    atomic.Int64
	evictions atomic.Int64

	// Cutover state.
	inGracePeriod atomic.Bool
}

// NewKeyframeCache creates a new keyframe cache.
func NewKeyframeCache(config Config, prop *propagation.Propagator, store *tle.Store, logger *slog.Logger) *KeyframeCache {
	logger.Info("cache initialized",
		"step_seconds", config.Step.Seconds(),
		"horizon_seconds", config.Horizon.Seconds(),
		"buffer_seconds", config.Buffer.Seconds(),
		"grace_period_seconds", config.GracePeriod.Seconds(),
	)

	return &KeyframeCache{
		entries: make(map[time.Time]*CacheEntry),
		config:  config,
		prop:    prop,
		store:   store,
		logger:  logger,
	}
}

// RoundToStep rounds a timestamp down to the nearest step boundary.
// This normalizes timestamps so cache lookups hit consistently.
// Always converts to UTC first — SGP4 and GMST expect UTC components.
func (c *KeyframeCache) RoundToStep(t time.Time) time.Time {
	return t.UTC().Truncate(c.config.Step)
}

// Get returns the keyframe for the given timestamp, or nil if not cached.
// The timestamp is rounded to the step boundary.
func (c *KeyframeCache) Get(t time.Time) *propagation.Keyframe {
	key := c.RoundToStep(t)

	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()

	if ok {
		c.hits.Add(1)
		metrics.IncCacheHits()
		return entry.Keyframe
	}

	c.misses.Add(1)
	metrics.IncCacheMisses()
	return nil
}

// GetRecent returns up to count keyframes before (and including) time t,
// ordered oldest-first. Used to build orbital trails.
func (c *KeyframeCache) GetRecent(t time.Time, count int) []*propagation.Keyframe {
	if count <= 0 {
		return nil
	}

	key := c.RoundToStep(t)

	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make([]*propagation.Keyframe, 0, count)
	for i := count - 1; i >= 0; i-- {
		ts := key.Add(-time.Duration(i) * c.config.Step)
		if entry, ok := c.entries[ts]; ok {
			result = append(result, entry.Keyframe)
		}
	}
	return result
}

// GetLatest returns the keyframe closest to (but not after) the current time.
func (c *KeyframeCache) GetLatest() *propagation.Keyframe {
	now := c.RoundToStep(time.Now())

	c.mu.RLock()
	defer c.mu.RUnlock()

	// Walk backwards from now to find the most recent entry.
	for i := 0; i < 10; i++ {
		key := now.Add(-time.Duration(i) * c.config.Step)
		if entry, ok := c.entries[key]; ok {
			c.hits.Add(1)
			metrics.IncCacheHits()
			return entry.Keyframe
		}
	}

	c.misses.Add(1)
	metrics.IncCacheMisses()
	return nil
}

// put stores a keyframe in the cache. Caller must not hold mu.
func (c *KeyframeCache) put(kf *propagation.Keyframe) {
	key := c.RoundToStep(kf.Timestamp)
	entry := &CacheEntry{
		Keyframe:    kf,
		GeneratedAt: time.Now(),
	}

	c.mu.Lock()
	c.entries[key] = entry
	c.mu.Unlock()

	c.updateMetrics()
}

// evictExpired removes entries older than now - buffer.
func (c *KeyframeCache) evictExpired() int {
	cutoff := time.Now().Add(-c.config.Buffer)
	var removed int

	c.mu.Lock()
	for ts := range c.entries {
		if ts.Before(cutoff) {
			delete(c.entries, ts)
			removed++
		}
	}
	c.mu.Unlock()

	if removed > 0 {
		c.evictions.Add(int64(removed))
		metrics.AddCacheEvictions(removed)
		c.updateMetrics()
		c.logger.Debug("cache eviction", "entries_removed", removed)
	}

	return removed
}

// replaceAll atomically replaces all cache entries (used during TLE cutover).
func (c *KeyframeCache) replaceAll(newEntries map[time.Time]*CacheEntry) {
	c.mu.Lock()
	c.entries = newEntries
	c.mu.Unlock()
	c.updateMetrics()
}

// Stats returns current cache statistics.
func (c *KeyframeCache) Stats() CacheStats {
	c.mu.RLock()
	count := len(c.entries)

	var oldest, newest time.Time
	for ts := range c.entries {
		if oldest.IsZero() || ts.Before(oldest) {
			oldest = ts
		}
		if newest.IsZero() || ts.After(newest) {
			newest = ts
		}
	}
	c.mu.RUnlock()

	return CacheStats{
		Entries:         count,
		SizeBytes:       c.estimateSizeBytes(),
		OldestTimestamp: oldest,
		NewestTimestamp: newest,
		Hits:            c.hits.Load(),
		Misses:          c.misses.Load(),
		Evictions:       c.evictions.Load(),
		InGracePeriod:   c.inGracePeriod.Load(),
	}
}

// CacheStats holds cache statistics for the stats endpoint.
type CacheStats struct {
	Entries         int
	SizeBytes       int64
	OldestTimestamp time.Time
	NewestTimestamp time.Time
	Hits            int64
	Misses          int64
	Evictions       int64
	InGracePeriod   bool
}

// estimateSizeBytes returns a rough estimate of the cache memory footprint.
func (c *KeyframeCache) estimateSizeBytes() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var total int64
	for _, entry := range c.entries {
		if entry.Keyframe == nil {
			continue
		}
		// Per SatellitePosition: NORADID(8) + PositionECEF(24) + VelocityECEF(24) = 56 bytes.
		satSize := int64(len(entry.Keyframe.Satellites)) * int64(unsafe.Sizeof(propagation.SatellitePosition{}))
		// Keyframe overhead: Timestamp(24) + slice header(24).
		kfOverhead := int64(48)
		// CacheEntry overhead: pointer(8) + GeneratedAt(24).
		entryOverhead := int64(32)
		total += satSize + kfOverhead + entryOverhead
	}

	// Map overhead (rough: 8 bytes per bucket).
	total += int64(len(c.entries)) * 8

	return total
}

// updateMetrics publishes current cache size to Prometheus.
func (c *KeyframeCache) updateMetrics() {
	c.mu.RLock()
	count := len(c.entries)
	c.mu.RUnlock()

	metrics.SetCacheEntries(count)
	metrics.SetCacheSizeBytes(c.estimateSizeBytes())
}
