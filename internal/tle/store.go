package tle

import (
	"sync"
	"sync/atomic"
	"time"
)

// Store provides thread-safe access to the current TLE dataset.
type Store struct {
	dataset atomic.Pointer[TLEDataset]
	mu      sync.Mutex // serializes fetch operations
}

// NewStore creates a new empty Store.
func NewStore() *Store {
	return &Store{}
}

// Get returns the current dataset, or nil if none has been loaded.
func (s *Store) Get() *TLEDataset {
	return s.dataset.Load()
}

// Set atomically replaces the current dataset.
func (s *Store) Set(ds *TLEDataset) {
	s.dataset.Store(ds)
}

// AgeSeconds returns the age of the current dataset in seconds.
// Returns -1 if no dataset is loaded.
func (s *Store) AgeSeconds() float64 {
	ds := s.dataset.Load()
	if ds == nil {
		return -1
	}
	return time.Since(ds.FetchedAt).Seconds()
}

// Lock acquires the fetch mutex for serializing fetch operations.
func (s *Store) Lock() {
	s.mu.Lock()
}

// Unlock releases the fetch mutex.
func (s *Store) Unlock() {
	s.mu.Unlock()
}
