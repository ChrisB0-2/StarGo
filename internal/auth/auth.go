package auth

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
)

// Config holds authentication configuration.
type Config struct {
	Enabled bool
	Token   string
}

// exemptPaths are always public regardless of auth configuration.
var exemptPaths = map[string]bool{
	"/healthz":             true,
	"/readyz":              true,
	"/metrics":             true,
	"/api/v1/tle/metadata": true,
}

// exemptPrefixes are path prefixes that are always public.
var exemptPrefixes = []string{
	"/api/v1/propagate/",
}

// isExempt returns true if the path is exempt from auth.
func isExempt(path string) bool {
	if exemptPaths[path] {
		return true
	}
	for _, prefix := range exemptPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// Middleware returns an HTTP middleware that enforces Bearer token auth
// on non-exempt paths when auth is enabled.
func Middleware(cfg Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !cfg.Enabled || isExempt(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			header := r.Header.Get("Authorization")
			token := strings.TrimPrefix(header, "Bearer ")

			if header == "" || token == header || subtle.ConstantTimeCompare([]byte(token), []byte(cfg.Token)) != 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
