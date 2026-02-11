package propagation

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/star/stargo/internal/tle"
	"github.com/star/stargo/internal/transform"
)

// propagateJob is a unit of work for the worker pool.
type propagateJob struct {
	entry      tle.TLEEntry
	prop       *SGP4Propagator // cached propagator (nil if init failed)
	targetTime time.Time
	gmst       float64 // precomputed GMST for targetTime
}

// propagateResult is the output of a single satellite propagation.
type propagateResult struct {
	position SatellitePosition
	err      error
	noradID  int
}

// WorkerPool manages a fixed number of goroutines for parallel SGP4 propagation.
type WorkerPool struct {
	workers int
	logger  *slog.Logger
}

// NewWorkerPool creates a worker pool with the given number of workers.
func NewWorkerPool(workers int, logger *slog.Logger) *WorkerPool {
	return &WorkerPool{
		workers: workers,
		logger:  logger,
	}
}

// PropagateBatch propagates all satellites to the target time using the worker pool.
// Returns results for all satellites that succeeded. Failed satellites are logged and skipped.
func (wp *WorkerPool) PropagateBatch(ctx context.Context, entries []tle.TLEEntry, targetTime time.Time) ([]SatellitePosition, int, int) {
	if len(entries) == 0 {
		return nil, 0, 0
	}

	// Precompute GMST once for the target time (same for all satellites).
	gmst := transform.GMST(targetTime)

	// Pre-build SGP4 propagators once per satellite to avoid re-parsing TLE
	// strings on every time step. Failed inits are logged and skipped.
	props := make(map[int]*SGP4Propagator, len(entries))
	for _, entry := range entries {
		if _, ok := props[entry.NORADID]; ok {
			continue
		}
		p, err := NewSGP4Propagator(entry.Line1, entry.Line2, entry.NORADID)
		if err != nil {
			wp.logger.Warn("sgp4 init failed (skipping)", "norad_id", entry.NORADID, "error", err)
			continue
		}
		props[entry.NORADID] = p
	}

	jobs := make(chan propagateJob, wp.workers*2)
	results := make(chan propagateResult, wp.workers*2)

	// Start workers.
	var wg sync.WaitGroup
	for i := 0; i < wp.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				result := propagateSingle(job)
				select {
				case results <- result:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	// Feed jobs in a goroutine, skipping satellites whose init failed.
	go func() {
		defer close(jobs)
		for _, entry := range entries {
			p := props[entry.NORADID]
			if p == nil {
				continue
			}
			job := propagateJob{
				entry:      entry,
				prop:       p,
				targetTime: targetTime,
				gmst:       gmst,
			}
			select {
			case jobs <- job:
			case <-ctx.Done():
				return
			}
		}
	}()

	// Close results when all workers are done.
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results.
	positions := make([]SatellitePosition, 0, len(entries))
	var successCount, errorCount int

	for result := range results {
		if result.err != nil {
			errorCount++
			wp.logger.Warn("propagation failed",
				"norad_id", result.noradID,
				"error", result.err,
			)
			continue
		}
		successCount++
		positions = append(positions, result.position)
	}

	return positions, successCount, errorCount
}

// propagateSingle performs SGP4 propagation and TEME→ECEF transform for one satellite.
func propagateSingle(job propagateJob) propagateResult {
	t := job.targetTime
	teme, err := job.prop.Propagate(t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second())
	if err != nil {
		return propagateResult{noradID: job.entry.NORADID, err: err}
	}

	ecef := transform.TEMEToECEFWithGMST(teme, job.gmst)

	return propagateResult{
		noradID: job.entry.NORADID,
		position: SatellitePosition{
			NORADID:      job.entry.NORADID,
			PositionECEF: [3]float64{ecef.X, ecef.Y, ecef.Z},
			VelocityECEF: [3]float64{ecef.VX, ecef.VY, ecef.VZ},
		},
	}
}
