package health

import (
	"net/http"
	"time"

	"github.com/star/stargo/internal/tle"
)

// Checker evaluates service health based on TLE data freshness.
type Checker struct {
	store  *tle.Store
	maxAge time.Duration
}

// NewChecker creates a Checker that gates readiness on TLE freshness.
func NewChecker(store *tle.Store, maxAge time.Duration) *Checker {
	return &Checker{
		store:  store,
		maxAge: maxAge,
	}
}

// Readyz returns 200 when the service is ready.
// If no dataset exists, returns 200 (TLE not required yet).
// If dataset exists and age exceeds maxAge, returns 503.
func (c *Checker) Readyz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")

	ds := c.store.Get()
	if ds == nil {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ready\n"))
		return
	}

	age := time.Since(ds.FetchedAt)
	if c.maxAge > 0 && age > c.maxAge {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("not ready\n"))
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ready\n"))
}

// Healthz returns 200 "ok\n" unconditionally.
func Healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok\n"))
}
