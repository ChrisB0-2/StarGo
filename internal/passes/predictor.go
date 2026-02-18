package passes

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/star/stargo/internal/propagation"
	"github.com/star/stargo/internal/tle"
	"github.com/star/stargo/internal/transform"
)

// GroundTrackPoint is a sub-satellite position at a specific time during a pass.
type GroundTrackPoint struct {
	Time      time.Time `json:"time"`
	Latitude  float64   `json:"latitude"`
	Longitude float64   `json:"longitude"`
	Altitude  float64   `json:"altitude"`
	Elevation float64   `json:"elevation"` // degrees above observer's horizon (0-90)
}

// PassEvent describes a single satellite pass over an observer location.
type PassEvent struct {
	StartTime        time.Time          `json:"start_time"`
	MaxElevationTime time.Time          `json:"max_elevation_time"`
	EndTime          time.Time          `json:"end_time"`
	DurationSeconds  float64            `json:"duration_seconds"`
	MaxElevation     float64            `json:"max_elevation"`
	AzimuthAtMax     float64            `json:"azimuth_at_max"`
	StartAzimuth     float64            `json:"start_azimuth"`
	EndAzimuth       float64            `json:"end_azimuth"`
	GroundTrack      []GroundTrackPoint `json:"ground_track"`
}

// SatellitePasses holds the predicted passes for one satellite.
type SatellitePasses struct {
	NORADID int         `json:"norad_id"`
	Passes  []PassEvent `json:"passes"`
	Error   string      `json:"error,omitempty"`
}

// Request holds the parameters for a pass prediction request.
type Request struct {
	Observer     transform.ObserverPosition
	Entries      []tle.TLEEntry
	Start        time.Time
	HorizonHours float64
	MinElevation float64 // degrees
	MaxPasses    int
}

const (
	coarseStepSec      = 30 // seconds between coarse scan steps
	fineStepSec        = 1  // seconds between fine scan steps
	groundTrackStepSec = 10 // seconds between ground track samples
	minPassDur         = 10 * time.Second
)

// Predict computes satellite passes for the given request.
// Each satellite is processed in its own goroutine, bounded by a semaphore.
func Predict(ctx context.Context, req Request) []SatellitePasses {
	results := make([]SatellitePasses, len(req.Entries))
	sem := make(chan struct{}, runtime.NumCPU())
	var wg sync.WaitGroup

	for i, entry := range req.Entries {
		wg.Add(1)
		go func(idx int, e tle.TLEEntry) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[idx] = SatellitePasses{
					NORADID: e.NORADID,
					Error:   "cancelled",
				}
				return
			}

			passes, err := predictSatellite(ctx, req, e)
			if err != nil {
				results[idx] = SatellitePasses{
					NORADID: e.NORADID,
					Error:   err.Error(),
				}
				return
			}
			results[idx] = SatellitePasses{
				NORADID: e.NORADID,
				Passes:  passes,
			}
		}(i, entry)
	}

	wg.Wait()
	return results
}

// predictSatellite finds all passes for a single satellite.
func predictSatellite(ctx context.Context, req Request, entry tle.TLEEntry) ([]PassEvent, error) {
	prop, err := propagation.NewSGP4Propagator(entry.Line1, entry.Line2, entry.NORADID)
	if err != nil {
		return nil, fmt.Errorf("sgp4 init: %w", err)
	}

	end := req.Start.Add(time.Duration(req.HorizonHours * float64(time.Hour)))
	var passes []PassEvent

	// Coarse scan: step through the time range looking for elevation > 0.
	t := req.Start
	for t.Before(end) && len(passes) < req.MaxPasses {
		if ctx.Err() != nil {
			return passes, nil
		}

		el, _, _, err := elevationAt(prop, req.Observer, t)
		if err != nil {
			t = t.Add(coarseStepSec * time.Second)
			continue
		}

		if el > 0 {
			// Found a candidate window — fine scan to find the full pass.
			pass, windowEnd := refinePas(ctx, prop, req.Observer, t, req.Start, end, req.MinElevation)
			if pass != nil && pass.EndTime.Sub(pass.StartTime) >= minPassDur {
				passes = append(passes, *pass)
			}
			// Jump past the end of this window.
			t = windowEnd.Add(coarseStepSec * time.Second)
		} else {
			t = t.Add(coarseStepSec * time.Second)
		}
	}

	return passes, nil
}

// refinePas does a fine-grained scan around a coarse-detected above-horizon region.
// It backs up to find the actual rise, then scans forward to find set.
// Returns the pass event and the time the window ends.
func refinePas(ctx context.Context, prop *propagation.SGP4Propagator, obs transform.ObserverPosition, coarseHit, windowStart, windowEnd time.Time, minElev float64) (*PassEvent, time.Time) {
	// Back up to find where elevation first crossed 0.
	searchStart := coarseHit.Add(-coarseStepSec * time.Second)
	if searchStart.Before(windowStart) {
		searchStart = windowStart
	}

	// Fine scan from searchStart.
	var (
		riseTime    time.Time
		setTime     time.Time
		riseAz      float64
		setAz       float64
		maxEl       float64
		maxElTime   time.Time
		maxElAz     float64
		wasAbove    bool
		foundRise   bool
		groundTrack []GroundTrackPoint
	)

	t := searchStart
	for t.Before(windowEnd) {
		if ctx.Err() != nil {
			break
		}

		el, la, ecef, err := elevationAt(prop, obs, t)
		if err != nil {
			t = t.Add(fineStepSec * time.Second)
			continue
		}

		above := el >= minElev

		if above && !wasAbove {
			// Rising.
			riseTime = t
			riseAz = la.AzimuthDeg
			foundRise = true
			maxEl = el
			maxElTime = t
			maxElAz = la.AzimuthDeg
		}

		if above && foundRise {
			if el > maxEl {
				maxEl = el
				maxElTime = t
				maxElAz = la.AzimuthDeg
			}
			// Sample ground track point every groundTrackStepSec seconds.
			secSinceRise := int(t.Sub(riseTime).Seconds())
			if secSinceRise%groundTrackStepSec == 0 {
				geo := transform.ECEFToGeodetic(ecef.X, ecef.Y, ecef.Z)
				groundTrack = append(groundTrack, GroundTrackPoint{
					Time:      t,
					Latitude:  geo.LatDeg,
					Longitude: geo.LonDeg,
					Altitude:  geo.AltM,
					Elevation: el,
				})
			}
		}

		if !above && wasAbove && foundRise {
			// Setting.
			setTime = t
			setAz = la.AzimuthDeg
			break
		}

		wasAbove = above
		t = t.Add(fineStepSec * time.Second)
	}

	// If satellite was still above at windowEnd, close the pass there.
	if foundRise && setTime.IsZero() && wasAbove {
		el, la, _, err := elevationAt(prop, obs, t)
		if err == nil {
			setTime = t
			setAz = la.AzimuthDeg
			if el > maxEl {
				maxEl = el
				maxElTime = t
				maxElAz = la.AzimuthDeg
			}
		} else {
			setTime = t
		}
	}

	if !foundRise || setTime.IsZero() {
		return nil, t
	}

	return &PassEvent{
		StartTime:        riseTime,
		MaxElevationTime: maxElTime,
		EndTime:          setTime,
		DurationSeconds:  setTime.Sub(riseTime).Seconds(),
		MaxElevation:     maxEl,
		AzimuthAtMax:     maxElAz,
		StartAzimuth:     riseAz,
		EndAzimuth:       setAz,
		GroundTrack:      groundTrack,
	}, setTime
}

// elevationAt computes the look angles and satellite ECEF position from observer to satellite at time t.
func elevationAt(prop *propagation.SGP4Propagator, obs transform.ObserverPosition, t time.Time) (float64, transform.LookAngles, transform.PositionECEF, error) {
	teme, err := prop.Propagate(t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second())
	if err != nil {
		return 0, transform.LookAngles{}, transform.PositionECEF{}, err
	}
	ecef := transform.TEMEToECEF(teme, t)
	la := transform.ECEFToLookAngles(obs, ecef.X, ecef.Y, ecef.Z)
	return la.ElevationDeg, la, ecef, nil
}
