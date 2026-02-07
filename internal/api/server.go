package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/star/stargo/internal/auth"
	"github.com/star/stargo/internal/health"
	"github.com/star/stargo/internal/metrics"
)

// Server holds the HTTP server and its dependencies.
type Server struct {
	httpServer *http.Server
	logger     *slog.Logger
}

// NewServer creates a configured HTTP server.
func NewServer(addr string, logger *slog.Logger, authCfg auth.Config) *Server {
	mux := http.NewServeMux()

	// Register routes.
	mux.HandleFunc("GET /healthz", health.Healthz)
	mux.HandleFunc("GET /readyz", health.Readyz)
	mux.Handle("GET /metrics", metrics.Handler())
	mux.HandleFunc("GET /api/v1/test", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})

	// Build middleware chain: metrics -> logging -> auth -> mux.
	var handler http.Handler = mux
	handler = auth.Middleware(authCfg)(handler)
	handler = loggingMiddleware(logger)(handler)
	handler = metrics.Middleware(handler)

	return &Server{
		httpServer: &http.Server{
			Addr:              addr,
			Handler:           handler,
			ReadTimeout:       10 * time.Second,
			ReadHeaderTimeout: 5 * time.Second,
			WriteTimeout:      10 * time.Second,
			IdleTimeout:       120 * time.Second,
		},
		logger: logger,
	}
}

// HTTPServer returns the underlying *http.Server for external control (e.g. shutdown).
func (s *Server) HTTPServer() *http.Server {
	return s.httpServer
}

// ListenAndServe starts the HTTP server.
func (s *Server) ListenAndServe() error {
	return s.httpServer.ListenAndServe()
}

// probePath returns true for health/readiness probe paths that should not log at INFO.
func probePath(path string) bool {
	return path == "/healthz" || path == "/readyz"
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.statusCode = code
	sr.ResponseWriter.WriteHeader(code)
}

func loggingMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sr := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}

			next.ServeHTTP(sr, r)

			duration := time.Since(start)
			level := slog.LevelInfo
			if probePath(r.URL.Path) {
				level = slog.LevelDebug
			}

			logger.Log(r.Context(), level, "request",
				"component", "api",
				"method", r.Method,
				"path", r.URL.Path,
				"status", strconv.Itoa(sr.statusCode),
				"duration_ms", duration.Milliseconds(),
				"remote_ip", r.RemoteAddr,
			)
		})
	}
}
