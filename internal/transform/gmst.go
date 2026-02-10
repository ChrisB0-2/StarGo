package transform

import (
	"math"
	"time"
)

// j2000 is the Julian Date of the J2000.0 epoch (January 1, 2000, 12:00:00 TT).
const j2000 = 2451545.0

// OmegaEarth is Earth's rotation rate in rad/s (IAU value).
const OmegaEarth = 7.292115146706979e-5

// JulianDate converts a time.Time (UTC) to Julian Date.
// Uses the standard astronomical algorithm valid for dates after March 1, 4801 BC.
func JulianDate(t time.Time) float64 {
	y := float64(t.Year())
	m := float64(t.Month())
	d := float64(t.Day())
	h := float64(t.Hour())
	min := float64(t.Minute())
	s := float64(t.Second()) + float64(t.Nanosecond())/1e9

	// Adjust year/month for Jan/Feb (treat as months 13/14 of previous year).
	if m <= 2 {
		y -= 1
		m += 12
	}

	A := math.Floor(y / 100)
	B := 2 - A + math.Floor(A/4)

	jd := math.Floor(365.25*(y+4716)) + math.Floor(30.6001*(m+1)) + d + B - 1524.5
	jd += (h + min/60.0 + s/3600.0) / 24.0

	return jd
}

// GMST calculates Greenwich Mean Sidereal Time in radians for a given UTC time.
// Uses the IAU-82 model as described in Vallado "Fundamentals of Astrodynamics".
//
// Formula (Vallado Eq 3-47):
//
//	θ_GMST = 67310.54841 + (876600h + 8640184.812866)*T + 0.093104*T² - 6.2e-6*T³
//
// where T is Julian centuries of UT1 from J2000.0, result is in seconds of time.
func GMST(t time.Time) float64 {
	t = t.UTC()
	jd := JulianDate(t)
	tUT1 := (jd - j2000) / 36525.0

	// GMST in seconds of time.
	// 876600h = 876600 * 3600 = 3155760000 seconds.
	gmstSec := 67310.54841 +
		(3155760000.0+8640184.812866)*tUT1 +
		0.093104*tUT1*tUT1 -
		6.2e-6*tUT1*tUT1*tUT1

	// Normalize to [0, 86400) seconds, then convert to radians.
	gmstSec = math.Mod(gmstSec, 86400.0)
	if gmstSec < 0 {
		gmstSec += 86400.0
	}
	gmstRad := gmstSec / 86400.0 * 2.0 * math.Pi

	return gmstRad
}
