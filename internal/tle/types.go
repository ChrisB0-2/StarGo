package tle

import "time"

// TLEEntry represents a single satellite's two-line element set.
type TLEEntry struct {
	NORADID int
	Name    string
	Epoch   time.Time
	Line1   string
	Line2   string
}

// EpochRange represents the minimum and maximum epoch times in a dataset.
type EpochRange struct {
	Min time.Time
	Max time.Time
}

// TLEDataset represents a complete set of TLE data from a source.
type TLEDataset struct {
	Source     string
	FetchedAt  time.Time
	EpochRange EpochRange
	Satellites []TLEEntry
}
