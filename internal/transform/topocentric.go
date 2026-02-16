package transform

import "math"

// WGS-84 ellipsoid parameters.
const (
	wgs84A  = 6378137.0             // semi-major axis (meters)
	wgs84F  = 1.0 / 298.257223563   // flattening
	wgs84E2 = wgs84F * (2 - wgs84F) // first eccentricity squared
)

// ObserverPosition holds a ground observer's location in both geodetic and ECEF frames.
// ECEF coordinates are precomputed once so they can be reused across many satellite lookups.
type ObserverPosition struct {
	LatRad, LonRad, AltM float64 // geodetic (radians, meters above ellipsoid)
	ECEFx, ECEFy, ECEFz  float64 // precomputed ECEF (meters)
}

// LookAngles holds azimuth, elevation, and range from observer to satellite.
type LookAngles struct {
	AzimuthDeg   float64 // 0 = North, clockwise
	ElevationDeg float64 // 0 = horizon, 90 = zenith
	RangeKm      float64
}

// NewObserverPosition creates an ObserverPosition from geodetic coordinates.
// Latitude and longitude are in degrees, altitude in meters above the WGS-84 ellipsoid.
func NewObserverPosition(latDeg, lonDeg, altM float64) ObserverPosition {
	lat := latDeg * math.Pi / 180.0
	lon := lonDeg * math.Pi / 180.0

	sinLat := math.Sin(lat)
	cosLat := math.Cos(lat)
	sinLon := math.Sin(lon)
	cosLon := math.Cos(lon)

	// Radius of curvature in the prime vertical.
	N := wgs84A / math.Sqrt(1-wgs84E2*sinLat*sinLat)

	x := (N + altM) * cosLat * cosLon
	y := (N + altM) * cosLat * sinLon
	z := (N*(1-wgs84E2) + altM) * sinLat

	return ObserverPosition{
		LatRad: lat,
		LonRad: lon,
		AltM:   altM,
		ECEFx:  x,
		ECEFy:  y,
		ECEFz:  z,
	}
}

// GeodeticPoint holds a geodetic position (latitude/longitude in degrees, altitude in meters).
type GeodeticPoint struct {
	LatDeg, LonDeg, AltM float64
}

// ECEFToGeodetic converts ECEF coordinates (meters) to geodetic coordinates
// using the iterative Bowring method. Converges in 2-3 iterations for Earth orbits.
func ECEFToGeodetic(x, y, z float64) GeodeticPoint {
	lon := math.Atan2(y, x)

	p := math.Sqrt(x*x + y*y)

	// Initial estimate using Bowring's method.
	lat := math.Atan2(z, p*(1-wgs84E2))

	for i := 0; i < 5; i++ {
		sinLat := math.Sin(lat)
		N := wgs84A / math.Sqrt(1-wgs84E2*sinLat*sinLat)
		lat = math.Atan2(z+wgs84E2*N*sinLat, p)
	}

	sinLat := math.Sin(lat)
	cosLat := math.Cos(lat)
	N := wgs84A / math.Sqrt(1-wgs84E2*sinLat*sinLat)

	var alt float64
	if math.Abs(cosLat) > 1e-10 {
		alt = p/cosLat - N
	} else {
		alt = math.Abs(z)/math.Abs(sinLat) - N*(1-wgs84E2)
	}

	return GeodeticPoint{
		LatDeg: lat * 180.0 / math.Pi,
		LonDeg: lon * 180.0 / math.Pi,
		AltM:   alt,
	}
}

// ECEFToLookAngles computes azimuth, elevation, and range from an observer
// to a satellite given in ECEF meters.
//
// Uses the SEZ (South-East-Zenith) topocentric rotation per Vallado Section 4.4.
// Azimuth: 0 = North, measured clockwise. Elevation: 0 = horizon, 90 = zenith.
func ECEFToLookAngles(obs ObserverPosition, satX, satY, satZ float64) LookAngles {
	// Range vector in ECEF.
	rx := satX - obs.ECEFx
	ry := satY - obs.ECEFy
	rz := satZ - obs.ECEFz

	sinLat := math.Sin(obs.LatRad)
	cosLat := math.Cos(obs.LatRad)
	sinLon := math.Sin(obs.LonRad)
	cosLon := math.Cos(obs.LonRad)

	// Rotate ECEF range vector to SEZ (South, East, Zenith).
	south := sinLat*cosLon*rx + sinLat*sinLon*ry - cosLat*rz
	east := -sinLon*rx + cosLon*ry
	zenith := cosLat*cosLon*rx + cosLat*sinLon*ry + sinLat*rz

	rangeMag := math.Sqrt(south*south + east*east + zenith*zenith)

	// Elevation: angle above horizon.
	el := math.Asin(zenith / rangeMag)

	// Azimuth: measured clockwise from North.
	// In SEZ, North = -South direction, so az = atan2(east, -south).
	az := math.Atan2(east, -south)
	if az < 0 {
		az += 2 * math.Pi
	}

	return LookAngles{
		AzimuthDeg:   az * 180.0 / math.Pi,
		ElevationDeg: el * 180.0 / math.Pi,
		RangeKm:      rangeMag / 1000.0,
	}
}
