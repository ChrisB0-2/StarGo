# Architecture

## Phase 0 — Repo Skeleton

```
cmd/stargo/main.go        Entrypoint: config, signal handling, graceful shutdown
internal/api/server.go     HTTP server, routing, request logging middleware
internal/auth/auth.go      Bearer token auth middleware
internal/health/health.go  /healthz and /readyz handlers
internal/metrics/metrics.go Prometheus counters, histogram, instrumentation middleware
```

## Request Flow

```
Client -> metrics middleware -> logging middleware -> auth middleware -> route handler
```

## Configuration

All configuration is via environment variables (no config files in Phase 0).
