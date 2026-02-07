package health

import "net/http"

// Healthz returns 200 "ok\n" unconditionally.
func Healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok\n"))
}

// Readyz returns 200 "ready\n" when the service is ready.
// Phase 0 readiness: config is valid (always true if we got this far).
func Readyz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ready\n"))
}
