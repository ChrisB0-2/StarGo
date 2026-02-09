package propagation

import (
	"fmt"
	"math"
	"strings"

	satellite "github.com/joshuaferrara/go-satellite"
	"github.com/star/stargo/internal/transform"
)

// SGP4 library choice: github.com/joshuaferrara/go-satellite
//
// Selected for: most community adoption (92+ stars), pure Go (no CGO), battle-tested
// since 2016, explicit TEME output, includes ECIToECEF for cross-validation.
//
// Note: Propagate() takes Satellite by value so SGP4 error codes are not visible
// to the caller. We detect propagation failures by checking output for NaN/Inf
// and unreasonable position magnitudes.

// SGP4Propagator wraps the go-satellite library for a single satellite.
type SGP4Propagator struct {
	sat     satellite.Satellite
	noradID int
}

// NewSGP4Propagator creates an SGP4 propagator from TLE lines.
// Returns an error if the TLE cannot be parsed or the SGP4 model fails to initialize.
//
// Pre-validates TLE format before passing to the library, because go-satellite
// calls log.Fatal on malformed input (which would kill the process).
func NewSGP4Propagator(line1, line2 string, noradID int) (*SGP4Propagator, error) {
	if err := validateTLELines(line1, line2); err != nil {
		return nil, fmt.Errorf("invalid TLE for NORAD %d: %w", noradID, err)
	}

	sat := satellite.TLEToSat(line1, line2, satellite.GravityWGS84)
	if sat.Error != 0 {
		return nil, fmt.Errorf("sgp4 init failed for NORAD %d: code=%d %s", noradID, sat.Error, sat.ErrorStr)
	}
	return &SGP4Propagator{sat: sat, noradID: noradID}, nil
}

// validateTLELines performs basic format validation on TLE lines.
// This prevents passing garbage to go-satellite which calls log.Fatal on parse errors.
func validateTLELines(line1, line2 string) error {
	line1 = strings.TrimSpace(line1)
	line2 = strings.TrimSpace(line2)

	if len(line1) != 69 {
		return fmt.Errorf("line1 length %d, expected 69", len(line1))
	}
	if len(line2) != 69 {
		return fmt.Errorf("line2 length %d, expected 69", len(line2))
	}
	if line1[0] != '1' {
		return fmt.Errorf("line1 must start with '1', got '%c'", line1[0])
	}
	if line2[0] != '2' {
		return fmt.Errorf("line2 must start with '2', got '%c'", line2[0])
	}
	return nil
}

// Propagate computes the satellite position at the given time.
// Returns position and velocity in TEME frame (km, km/s).
func (p *SGP4Propagator) Propagate(year, month, day, hour, min, sec int) (transform.PositionTEME, error) {
	pos, vel := satellite.Propagate(p.sat, year, month, day, hour, min, sec)

	// Detect propagation failures via NaN/Inf check.
	if math.IsNaN(pos.X) || math.IsNaN(pos.Y) || math.IsNaN(pos.Z) ||
		math.IsInf(pos.X, 0) || math.IsInf(pos.Y, 0) || math.IsInf(pos.Z, 0) {
		return transform.PositionTEME{}, fmt.Errorf("sgp4 propagation failed for NORAD %d: output is NaN/Inf", p.noradID)
	}

	// Sanity check: position magnitude should be between ~6200km and ~50000km.
	mag := math.Sqrt(pos.X*pos.X + pos.Y*pos.Y + pos.Z*pos.Z)
	if mag < 6200.0 || mag > 50000.0 {
		return transform.PositionTEME{}, fmt.Errorf("sgp4 propagation failed for NORAD %d: unreasonable position magnitude %.1f km", p.noradID, mag)
	}

	return transform.PositionTEME{
		X:  pos.X,
		Y:  pos.Y,
		Z:  pos.Z,
		VX: vel.X,
		VY: vel.Y,
		VZ: vel.Z,
	}, nil
}
