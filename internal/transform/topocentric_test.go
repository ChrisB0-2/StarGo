package transform

import (
	"math"
	"testing"
)

func TestNewObserverPosition_ECEFMagnitude(t *testing.T) {
	// Observer at sea level should have ECEF magnitude close to Earth radius (~6.371e6 m).
	obs := NewObserverPosition(0, 0, 0) // equator, prime meridian
	mag := math.Sqrt(obs.ECEFx*obs.ECEFx + obs.ECEFy*obs.ECEFy + obs.ECEFz*obs.ECEFz)

	// WGS-84 equatorial radius is 6378137 m.
	if math.Abs(mag-6378137.0) > 1.0 {
		t.Errorf("equatorial observer ECEF magnitude = %.1f m, want ~6378137 m", mag)
	}

	// Observer at north pole: magnitude should be ~6356752 m (polar radius).
	obs2 := NewObserverPosition(90, 0, 0)
	mag2 := math.Sqrt(obs2.ECEFx*obs2.ECEFx + obs2.ECEFy*obs2.ECEFy + obs2.ECEFz*obs2.ECEFz)
	if math.Abs(mag2-6356752.3) > 1.0 {
		t.Errorf("polar observer ECEF magnitude = %.1f m, want ~6356752 m", mag2)
	}
}

func TestNewObserverPosition_Altitude(t *testing.T) {
	obs0 := NewObserverPosition(0, 0, 0)
	obs100 := NewObserverPosition(0, 0, 100)

	mag0 := math.Sqrt(obs0.ECEFx*obs0.ECEFx + obs0.ECEFy*obs0.ECEFy + obs0.ECEFz*obs0.ECEFz)
	mag100 := math.Sqrt(obs100.ECEFx*obs100.ECEFx + obs100.ECEFy*obs100.ECEFy + obs100.ECEFz*obs100.ECEFz)

	diff := mag100 - mag0
	if math.Abs(diff-100.0) > 0.01 {
		t.Errorf("altitude difference = %.3f m, want 100 m", diff)
	}
}

func TestECEFToLookAngles_DirectlyOverhead(t *testing.T) {
	// Observer at equator, prime meridian. Satellite directly above at 400km altitude.
	obs := NewObserverPosition(0, 0, 0)

	// Satellite directly overhead: same lat/lon, higher altitude.
	satAlt := 400000.0 // 400 km in meters
	satX := obs.ECEFx + satAlt // straight up from equator/prime meridian
	satY := obs.ECEFy
	satZ := obs.ECEFz

	la := ECEFToLookAngles(obs, satX, satY, satZ)

	// Elevation should be ~90 degrees.
	if math.Abs(la.ElevationDeg-90.0) > 0.1 {
		t.Errorf("overhead elevation = %.2f deg, want ~90", la.ElevationDeg)
	}

	// Range should be ~400 km.
	if math.Abs(la.RangeKm-400.0) > 1.0 {
		t.Errorf("overhead range = %.2f km, want ~400", la.RangeKm)
	}
}

func TestECEFToLookAngles_HorizonElevation(t *testing.T) {
	// Observer at equator, prime meridian. Satellite far east on the horizon.
	obs := NewObserverPosition(0, 0, 0)

	// Satellite at same altitude but displaced 90 degrees east in longitude.
	// At 400 km altitude, 90 degrees away should be well below the horizon.
	// Use a satellite at ~10 degrees east and 400km alt to get near the horizon.
	satObs := NewObserverPosition(0, 20, 400000) // rough approximation
	la := ECEFToLookAngles(obs, satObs.ECEFx, satObs.ECEFy, satObs.ECEFz)

	// Should have low-ish elevation (positive but well under 90).
	if la.ElevationDeg < -5 || la.ElevationDeg > 45 {
		t.Errorf("near-horizon elevation = %.2f deg, expected between -5 and 45", la.ElevationDeg)
	}
}

func TestECEFToLookAngles_AzimuthDirections(t *testing.T) {
	// Observer at equator, prime meridian.
	obs := NewObserverPosition(0, 0, 0)

	// Satellite to the north (higher latitude, same longitude).
	satN := NewObserverPosition(10, 0, 400000)
	laN := ECEFToLookAngles(obs, satN.ECEFx, satN.ECEFy, satN.ECEFz)

	// Azimuth should be close to 0 (North) or 360.
	if laN.AzimuthDeg > 30 && laN.AzimuthDeg < 330 {
		t.Errorf("northward azimuth = %.2f deg, want near 0/360", laN.AzimuthDeg)
	}

	// Satellite to the east (same latitude, higher longitude).
	satE := NewObserverPosition(0, 10, 400000)
	laE := ECEFToLookAngles(obs, satE.ECEFx, satE.ECEFy, satE.ECEFz)

	// Azimuth should be close to 90 (East).
	if math.Abs(laE.AzimuthDeg-90.0) > 30 {
		t.Errorf("eastward azimuth = %.2f deg, want near 90", laE.AzimuthDeg)
	}

	// Satellite to the south (lower latitude, same longitude).
	satS := NewObserverPosition(-10, 0, 400000)
	laS := ECEFToLookAngles(obs, satS.ECEFx, satS.ECEFy, satS.ECEFz)

	// Azimuth should be close to 180 (South).
	if math.Abs(laS.AzimuthDeg-180.0) > 30 {
		t.Errorf("southward azimuth = %.2f deg, want near 180", laS.AzimuthDeg)
	}
}

func TestECEFToLookAngles_RangePositive(t *testing.T) {
	obs := NewObserverPosition(40.7128, -74.006, 10) // NYC
	// ISS-like position: ~6778km from center
	satX := 6778000.0
	satY := 0.0
	satZ := 0.0

	la := ECEFToLookAngles(obs, satX, satY, satZ)
	if la.RangeKm <= 0 {
		t.Errorf("range should be positive, got %.2f km", la.RangeKm)
	}
}
