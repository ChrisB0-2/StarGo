package propagation

import "time"

// Keyframe holds the positions of all satellites at a single point in time.
type Keyframe struct {
	Timestamp  time.Time
	Satellites []SatellitePosition
}

// SatellitePosition holds a single satellite's ECEF position at a keyframe time.
type SatellitePosition struct {
	NORADID      int
	PositionECEF [3]float64 // meters (X, Y, Z in ECEF)
	VelocityECEF [3]float64 // m/s (X, Y, Z in ECEF)
}

// PropConfig holds propagation configuration loaded from environment variables.
type PropConfig struct {
	Workers int           // Worker pool size (default: runtime.NumCPU())
	Step    time.Duration // Keyframe interval (default: 5s)
	Horizon time.Duration // Propagation horizon (default: 600s)
}
