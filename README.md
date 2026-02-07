# StarGo

Satellite tracking and visualization service.

## Quick Start

```bash
go run ./cmd/stargo
```

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `STARGO_HTTP_ADDR` | `:8080` | HTTP listen address |
| `STARGO_AUTH_ENABLED` | `false` | Enable Bearer token authentication |
| `STARGO_AUTH_TOKEN` | _(none)_ | Required when auth is enabled |

## Endpoints

| Path | Auth | Description |
|---|---|---|
| `GET /healthz` | No | Liveness probe |
| `GET /readyz` | No | Readiness probe |
| `GET /metrics` | No | Prometheus metrics |
| `GET /api/v1/test` | Yes | Test endpoint |

## Auth Example

```bash
export STARGO_AUTH_ENABLED=true
export STARGO_AUTH_TOKEN=mysecret
go run ./cmd/stargo

# Rejected (no token):
curl -i http://localhost:8080/api/v1/test

# Accepted:
curl -i -H "Authorization: Bearer mysecret" http://localhost:8080/api/v1/test
```
