package transform

import (
	"math"
	"testing"
	"time"

	satellite "github.com/joshuaferrara/go-satellite"
)

// TestJulianDate verifies our Julian Date calculation against known values.
func TestJulianDate(t *testing.T) {
	tests := []struct {
		name     string
		time     time.Time
		expected float64
	}{
		{
			name:     "J2000.0 epoch",
			time:     time.Date(2000, 1, 1, 12, 0, 0, 0, time.UTC),
			expected: 2451545.0,
		},
		{
			name:     "Unix epoch",
			time:     time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC),
			expected: 2440587.5,
		},
		{
			// Vallado Example 3-15: April 6, 2004, 07:51:28.386 UTC
			name:     "Vallado example date",
			time:     time.Date(2004, 4, 6, 7, 51, 28, 386009000, time.UTC),
			expected: 2453101.827411875,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := JulianDate(tt.time)
			diff := math.Abs(got - tt.expected)
			if diff > 1e-6 {
				t.Errorf("JulianDate(%v) = %.10f, want %.10f (diff=%.2e)", tt.time, got, tt.expected, diff)
			}
		})
	}
}

// TestGMST validates our GMST calculation against the go-satellite library's
// GSTimeFromDate function, which uses the same IAU-82 model.
func TestGMST(t *testing.T) {
	tests := []struct {
		name string
		time time.Time
	}{
		{
			name: "J2000.0 epoch",
			time: time.Date(2000, 1, 1, 12, 0, 0, 0, time.UTC),
		},
		{
			name: "Vallado example date",
			time: time.Date(2004, 4, 6, 7, 51, 28, 0, time.UTC), // integer seconds for library compat
		},
		{
			name: "recent date 2026",
			time: time.Date(2026, 2, 6, 4, 1, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			our := GMST(tt.time)
			// go-satellite's GSTimeFromDate returns GMST in radians.
			ref := satellite.GSTimeFromDate(
				tt.time.Year(), int(tt.time.Month()), tt.time.Day(),
				tt.time.Hour(), tt.time.Minute(), tt.time.Second(),
			)

			diff := math.Abs(our - ref)
			// Allow small difference for float precision; 1e-8 radians ≈ 0.06 arcsec.
			if diff > 1e-8 {
				t.Errorf("GMST(%v) = %.12f rad, go-satellite = %.12f rad (diff=%.2e)", tt.time, our, ref, diff)
			}
		})
	}
}

// TestTEMEToECEF validates our TEME→ECEF transform against the go-satellite
// library's ECIToECEF function using the same GMST. Both use simplified
// GMST-only rotation (no nutation or polar motion), so they should agree
// to floating point precision.
//
// We test with the Vallado Example 3-15 TEME position and also with
// typical LEO positions.
func TestTEMEToECEF(t *testing.T) {
	tests := []struct {
		name string
		teme PositionTEME
		time time.Time
	}{
		{
			// Vallado "Fundamentals of Astrodynamics" Example 3-15
			name: "Vallado example 3-15",
			teme: PositionTEME{
				X: 5094.18016, Y: 6127.64465, Z: 6380.34453,
				VX: -4.746131487, VY: 0.786598499, VZ: 5.531931288,
			},
			time: time.Date(2004, 4, 6, 7, 51, 28, 0, time.UTC),
		},
		{
			// Typical LEO satellite (roughly ISS-like orbit)
			name: "LEO equatorial",
			teme: PositionTEME{
				X: 6778.0, Y: 0.0, Z: 0.0,
				VX: 0.0, VY: 7.5, VZ: 0.0,
			},
			time: time.Date(2026, 2, 6, 12, 0, 0, 0, time.UTC),
		},
		{
			// Polar orbit
			name: "LEO polar",
			teme: PositionTEME{
				X: 0.0, Y: 0.0, Z: 6978.0,
				VX: 7.4, VY: 0.0, VZ: 0.0,
			},
			time: time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Compute GMST using go-satellite as reference.
			gmst := satellite.GSTimeFromDate(
				tt.time.Year(), int(tt.time.Month()), tt.time.Day(),
				tt.time.Hour(), tt.time.Minute(), tt.time.Second(),
			)

			// Our transform (uses meters output).
			ourECEF := TEMEToECEFWithGMST(tt.teme, gmst)

			// Reference: go-satellite's ECIToECEF (uses km).
			refVec := satellite.ECIToECEF(
				satellite.Vector3{X: tt.teme.X, Y: tt.teme.Y, Z: tt.teme.Z},
				gmst,
			)

			// Compare positions (our output is meters, reference is km).
			diffX := math.Abs(ourECEF.X - refVec.X*1000.0)
			diffY := math.Abs(ourECEF.Y - refVec.Y*1000.0)
			diffZ := math.Abs(ourECEF.Z - refVec.Z*1000.0)

			// Tolerance: 1 meter (as specified).
			const tolerance = 1.0 // meter
			if diffX > tolerance || diffY > tolerance || diffZ > tolerance {
				t.Errorf("position mismatch (tolerance=%.0fm):\n  ours:  [%.3f, %.3f, %.3f] m\n  ref:   [%.3f, %.3f, %.3f] m\n  diff:  [%.6f, %.6f, %.6f] m",
					tolerance,
					ourECEF.X, ourECEF.Y, ourECEF.Z,
					refVec.X*1000, refVec.Y*1000, refVec.Z*1000,
					diffX, diffY, diffZ)
			}

			// Also verify position is physically reasonable.
			if !ValidateECEF(ourECEF) {
				t.Errorf("ECEF position failed validation: [%.1f, %.1f, %.1f] m", ourECEF.X, ourECEF.Y, ourECEF.Z)
			}
		})
	}
}

// TestTEMEToECEFVelocity verifies the velocity transform includes Earth rotation correction.
func TestTEMEToECEFVelocity(t *testing.T) {
	// Prograde equatorial satellite at longitude 0°.
	teme := PositionTEME{
		X: 6778.0, Y: 0.0, Z: 0.0,
		VX: 0.0, VY: 7.5, VZ: 0.0,
	}
	gmst := 0.0 // GMST = 0 means TEME X-axis aligns with ECEF X-axis.

	ecef := TEMEToECEFWithGMST(teme, gmst)

	// Position should be identical (just km→m conversion).
	if math.Abs(ecef.X-6778000.0) > 0.1 {
		t.Errorf("X position: got %.1f, want 6778000.0", ecef.X)
	}

	// Earth rotation velocity at this radius: ω*R = 7.292115e-5 * 6778 = 0.4943 km/s.
	// ECEF Y-velocity should be: 7.5 - 0.4943 = 7.0057 km/s = 7005.7 m/s.
	expectedVY := (7.5 - OmegaEarth*6778.0) * 1000.0
	if math.Abs(ecef.VY-expectedVY) > 0.1 {
		t.Errorf("VY: got %.1f m/s, want %.1f m/s", ecef.VY, expectedVY)
	}
}

// TestValidateECEF tests the ECEF position validation function.
func TestValidateECEF(t *testing.T) {
	tests := []struct {
		name  string
		pos   PositionECEF
		valid bool
	}{
		{"LEO", PositionECEF{X: 6778000, Y: 0, Z: 0}, true},
		{"GEO", PositionECEF{X: 42164000, Y: 0, Z: 0}, true},
		{"too low", PositionECEF{X: 5000000, Y: 0, Z: 0}, false},
		{"too high", PositionECEF{X: 60000000, Y: 0, Z: 0}, false},
		{"NaN", PositionECEF{X: math.NaN(), Y: 0, Z: 0}, false},
		{"Inf", PositionECEF{X: math.Inf(1), Y: 0, Z: 0}, false},
		{"zero", PositionECEF{X: 0, Y: 0, Z: 0}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidateECEF(tt.pos); got != tt.valid {
				t.Errorf("ValidateECEF(%v) = %v, want %v", tt.pos, got, tt.valid)
			}
		})
	}
}
