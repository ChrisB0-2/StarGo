package auth

import (
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
	"/healthz": true,
	"/readyz":  true,
	"/metrics": true,
}

// Middleware returns an HTTP middleware that enforces Bearer token auth
// on non-exempt paths when auth is enabled.
func Middleware(cfg Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !cfg.Enabled || exemptPaths[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}

			header := r.Header.Get("Authorization")
			token := strings.TrimPrefix(header, "Bearer ")

			if header == "" || token == header || token != cfg.Token {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
