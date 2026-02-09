// Package transform provides coordinate frame transformations for satellite positions.
//
// The primary transform is TEME (True Equator Mean Equinox) to ECEF (Earth-Centered
// Earth-Fixed), which is needed because SGP4 outputs positions in TEME and Cesium
// expects positions in ECEF (ITRF).
//
// Method: Simplified Vallado-style rotation using GMST only (TEME → PEF ≈ ECEF).
// This ignores polar motion and equation of equinoxes, which introduces ~50m error
// at most — acceptable for satellite visualization.
//
// Reference: Vallado, "Fundamentals of Astrodynamics and Applications", Ch. 3.
package transform

import (
	"math"
	"time"
)

// PositionTEME represents a satellite position and velocity in the TEME frame.
type PositionTEME struct {
	X, Y, Z    float64 // km
	VX, VY, VZ float64 // km/s
}

// PositionECEF represents a satellite position and velocity in the ECEF frame.
type PositionECEF struct {
	X, Y, Z    float64 // meters
	VX, VY, VZ float64 // m/s
}

// TEMEToECEF transforms a TEME position/velocity to ECEF at the given UTC time.
// Input: TEME in km and km/s.
// Output: ECEF in meters and m/s.
func TEMEToECEF(teme PositionTEME, t time.Time) PositionECEF {
	gmst := GMST(t)
	return TEMEToECEFWithGMST(teme, gmst)
}

// TEMEToECEFWithGMST transforms TEME to ECEF using a precomputed GMST angle (radians).
// Useful when propagating multiple satellites to the same time (compute GMST once).
//
// Position transform: r_ECEF = R3(θ) * r_TEME
// Velocity transform: v_ECEF = R3(θ) * v_TEME - ω × r_ECEF
//
// where R3(θ) is a rotation about the Z-axis by angle θ (GMST),
// and ω = [0, 0, ω_earth] is Earth's angular velocity vector.
func TEMEToECEFWithGMST(teme PositionTEME, gmst float64) PositionECEF {
	cosG := math.Cos(gmst)
	sinG := math.Sin(gmst)

	// Position: R3(GMST) rotation.
	xECEF := teme.X*cosG + teme.Y*sinG
	yECEF := -teme.X*sinG + teme.Y*cosG
	zECEF := teme.Z

	// Velocity: R3(GMST) rotation, then subtract Earth rotation effect.
	// v_ECEF = R3(θ) * v_TEME - ω × r_ECEF
	// ω × r_ECEF = [-ω*y_ECEF, ω*x_ECEF, 0]
	vxRot := teme.VX*cosG + teme.VY*sinG
	vyRot := -teme.VX*sinG + teme.VY*cosG
	vzRot := teme.VZ

	vxECEF := vxRot + OmegaEarth*yECEF // -(-ω*y) = +ω*y
	vyECEF := vyRot - OmegaEarth*xECEF // -(ω*x)
	vzECEF := vzRot

	// Convert km → meters, km/s → m/s.
	return PositionECEF{
		X:  xECEF * 1000.0,
		Y:  yECEF * 1000.0,
		Z:  zECEF * 1000.0,
		VX: vxECEF * 1000.0,
		VY: vyECEF * 1000.0,
		VZ: vzECEF * 1000.0,
	}
}

// ValidateECEF checks that an ECEF position is physically reasonable for an
// Earth-orbiting satellite. Returns true if valid.
// Expected: magnitude between Earth radius (~6371km) and ~50000km (high orbit).
func ValidateECEF(pos PositionECEF) bool {
	// Check for NaN/Inf.
	if math.IsNaN(pos.X) || math.IsNaN(pos.Y) || math.IsNaN(pos.Z) {
		return false
	}
	if math.IsInf(pos.X, 0) || math.IsInf(pos.Y, 0) || math.IsInf(pos.Z, 0) {
		return false
	}

	// Position magnitude in meters.
	mag := math.Sqrt(pos.X*pos.X + pos.Y*pos.Y + pos.Z*pos.Z)

	// Earth radius is ~6371km. LEO is ~6571-6971km. GEO is ~42164km.
	// Allow generous range: 6200km to 50000km (in meters).
	const minRadius = 6200.0 * 1000.0  // 6200 km in meters
	const maxRadius = 50000.0 * 1000.0 // 50000 km in meters

	return mag >= minRadius && mag <= maxRadius
}
