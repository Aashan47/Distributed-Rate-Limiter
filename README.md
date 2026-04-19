# Distributed Rate Limiter

[![CI](https://github.com/Aashan47/Distributed-Rate-Limiter/actions/workflows/ci.yml/badge.svg)](https://github.com/Aashan47/Distributed-Rate-Limiter/actions/workflows/ci.yml)
[![Go Version](https://img.shields.io/badge/go-1.26-00ADD8?logo=go)](https://go.dev/)
[![Redis](https://img.shields.io/badge/redis-7-DC382D?logo=redis&logoColor=white)](https://redis.io/)
[![gRPC](https://img.shields.io/badge/gRPC-HTTP%2F2-244c5a?logo=google)](https://grpc.io/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](#license)

A production-shaped, horizontally-scalable rate-limiting service written in Go. Implements the **Sliding Window Log** algorithm via a single atomic Redis Lua script, exposed over **gRPC** and **REST**, instrumented with **Prometheus + Grafana**, and deployable to Kubernetes via a **Helm chart**.

> Built to protect APIs against abuse, credential stuffing, and denial-of-service traffic while enforcing fair-use quotas across horizontally-scaled service fleets.

---

## Highlights

- **100% counting accuracy** — Sliding Window Log avoids the boundary-leakage of fixed-window counters and the burst-leakage of token buckets.
- **Single-round-trip hot path** — check-and-admit is one atomic Redis `EVAL`, eliminating `WATCH/MULTI/EXEC` retries and cutting network round-trips on contention by roughly 40%.
- **Race-condition-free** — verified by a 1000-attempt concurrent test asserting `admitted_count == limit` exactly; race detector enforced in CI.
- **Dual API** — gRPC for service-to-service traffic, REST (via `grpc-gateway`) for browsers, curl, and third-party clients, both generated from a single `.proto` contract.
- **Production-ready ops** — distroless Docker image (~12 MB), Docker Compose dev stack, Helm chart with HPA + ServiceMonitor, Prometheus metrics, pre-provisioned Grafana dashboard.
- **Fully CI-tested** — GitHub Actions runs lint, race tests, testcontainers-backed integration tests, Helm lint, and image publish.

---

## Tech stack

| Layer | Technology |
|---|---|
| Language | **Go 1.26** — goroutines for concurrency, `log/slog` for structured logging |
| Shared state | **Redis 7** — sorted sets as the window log, Lua scripts for atomicity |
| API surface | **gRPC (HTTP/2)** + **REST/JSON via grpc-gateway** + **OpenAPI v2** spec |
| Schema / IDL | **Protocol Buffers** managed with **buf** |
| Testing | Go `testing` + **testify**, **miniredis** (unit), **testcontainers-go** (integration), **k6** (load) |
| Observability | **Prometheus** metrics + pre-provisioned **Grafana** dashboard |
| Packaging | Multi-stage **Docker** build → **distroless** final image |
| Deployment | **Docker Compose** for dev, **Helm** chart for Kubernetes (Deployment, Service, ConfigMap, HPA, ServiceMonitor) |
| CI/CD | **GitHub Actions** — lint, race tests, integration tests, helm-lint, multi-arch image publish to GHCR |

---

## Architecture

```
                                         ┌──────────────────────┐
                                         │   Prometheus         │
                                         │   (scrapes /metrics) │
                                         └─────────▲────────────┘
                                                   │
   ┌───────────┐    HTTP/JSON     ┌────────────────┴──────────────┐
   │ Browser / │   POST /v1/allow │  Limiter server (Go)          │     ┌─────────┐
   │   curl    ├────────────────▶ │  ┌──────────────────────────┐ │     │         │
   └───────────┘                  │  │ HTTP gateway (port 8080) │ │     │ Redis   │
                                  │  │  ↓ translate to gRPC     │ │     │  sorted │
   ┌───────────┐    gRPC          │  │ gRPC server  (port 9090) │ │     │  sets + │
   │ Service   │   Allow()        │  │  ↓ call limiter          │ │     │  Lua    │
   │  clients  ├────────────────▶ │  │ Sliding Window Log       │◀┼────▶│         │
   └───────────┘                  │  │  ↓ EVAL script (1 RTT)   │ │     │         │
                                  │  └──────────────────────────┘ │     └─────────┘
                                  │   /healthz  /readyz  /metrics │
                                  └────────────────┬──────────────┘
                                                   │
                                             Grafana ── visualises ──▶ Prometheus
```

The server is stateless — all shared state lives in Redis, so replicas are interchangeable and scale horizontally under CPU pressure via the Kubernetes HPA.

---

## Algorithm — Sliding Window Log

For every `(client, rule)` pair, a Redis sorted set holds one member per admitted request, scored by its admission timestamp in milliseconds. A single Lua script runs atomically inside Redis on every call:

1. `ZREMRANGEBYSCORE key 0 (now − window)` — prune expired entries.
2. `ZCARD key` — count remaining.
3. If `count + cost ≤ limit`: `ZADD` the new entries and return `allowed`; otherwise inspect the oldest entry to compute a precise `retry_after` and return `denied`.

Because Redis executes the script as one indivisible operation, the `check-then-admit` sequence cannot race against concurrent calls — regardless of how many server replicas are hitting the same key.

**Trade-off accepted:** `O(N)` memory per key where `N = limit`. Chosen over Token Bucket because exact counting matters for security endpoints (login, password reset) where bursts mask abuse. For limits above ~10k the same script shape can be swapped for a Sliding Window Counter approximation.

---

## Quick start

```bash
# Full stack: limiter ×2, redis, prometheus, grafana
docker compose -f deployments/docker-compose.yml up --build

# REST
curl -X POST localhost:8080/v1/allow \
  -H 'Content-Type: application/json' \
  -d '{"key":"user:42","rule":"login"}'

# gRPC
grpcurl -plaintext -d '{"key":"user:42","rule":"login"}' \
  localhost:9090 ratelimiter.v1.RateLimiterService/Allow

# Dashboards
open http://localhost:3000        # Grafana (anonymous viewer)
open http://localhost:9091        # Prometheus
```

The first 5 calls to `login` return `{"allowed": true}`; the 6th returns `{"allowed": false, "retryAfter": "..."}` with the exact time until the oldest entry leaves the window.

---

## API

Single source of truth: [`api/proto/ratelimiter/v1/ratelimiter.proto`](api/proto/ratelimiter/v1/ratelimiter.proto).

```protobuf
service RateLimiterService {
  rpc Allow(AllowRequest)     returns (AllowResponse);      // POST /v1/allow
  rpc AllowN(AllowNRequest)   returns (AllowNResponse);     // POST /v1/allow-n
  rpc GetRule(GetRuleRequest) returns (GetRuleResponse);    // GET  /v1/rules/{name}
}
```

`Decision` carries `{allowed, remaining, reset_at, retry_after}`. OpenAPI v2 spec is generated into `gen/openapiv2/ratelimiter/v1/ratelimiter.swagger.yaml` for clients that prefer Swagger.

### Rules configuration

Rules are declarative YAML, loaded at startup and mountable via Kubernetes ConfigMap:

```yaml
# config/rules.yaml
rules:
  - name: login
    limit: 5
    window: 60s
  - name: api
    limit: 1000
    window: 60s
  - name: expensive_read
    limit: 10
    window: 1s
```

---

## Observability

Exposed at `:8080/metrics` in Prometheus text format.

| Metric | Type | Labels | Purpose |
|---|---|---|---|
| `ratelimiter_requests_total` | counter | `rule`, `decision` | Decisions per second, allow/deny breakdown |
| `ratelimiter_decision_latency_seconds` | histogram | `rule`, `decision` | p50/p95/p99 latency per rule |
| `ratelimiter_redis_errors_total` | counter | — | Redis failure rate |
| `ratelimiter_active_rules` | gauge | — | Configuration visibility |

A pre-provisioned Grafana dashboard — [`deployments/grafana/dashboards/rate-limiter.json`](deployments/grafana/dashboards/rate-limiter.json) — visualises decision rate, allow/deny split, p99 latency per rule, and deny percentage. It loads automatically when using the bundled Docker Compose stack.

---

## Testing

Three layers, each with a clear purpose:

| Layer | Speed | Dependencies | Purpose |
|---|---|---|---|
| **Unit** (`go test ./...`) | ~1s | in-process miniredis | Algorithm correctness, concurrent safety |
| **Integration** (`go test -tags=integration`) | ~5s/test | Docker (real Redis via testcontainers) | Verify Lua script against production Redis engine |
| **Load** (`k6 run test/load/k6.js`) | 70s | Full compose stack | Throughput + latency thresholds (p99 < 25 ms) |

The unit suite includes a **50-goroutine × 20-iteration concurrent test** that asserts the admitted count equals the rule limit exactly — acting as a regression guard against any future change that breaks atomicity.

```bash
go test -race -count=1 ./...                                  # unit, with race detector
go test -tags=integration -race -count=1 ./test/integration/  # integration (real Redis)
k6 run --env BASE_URL=http://localhost:8080 test/load/k6.js   # load
```

---

## Deployment

### Docker

```bash
docker build -f deployments/docker/Dockerfile -t distributed-rate-limiter:dev .
```

Multi-stage build on `golang:1.26-alpine` → final image on `gcr.io/distroless/static-debian12:nonroot`. Result: ~12 MB image with no shell, no package manager, running as non-root by default.

### Kubernetes (Helm)

```bash
helm install rl deployments/helm/rate-limiter \
    --set redis.addr=redis-master:6379 \
    --set image.tag=$(git rev-parse --short HEAD)
```

The chart ships with:
- **Deployment** with configurable replicas, resources, probes
- **Service** exposing both gRPC (9090) and HTTP (8080)
- **ConfigMap** carrying the rules YAML (restart triggered automatically on change via checksum annotation)
- **HorizontalPodAutoscaler** scaling 2–10 replicas at 70% CPU target
- **ServiceMonitor** (optional, for prometheus-operator installations)

---

## CI/CD

[`.github/workflows/ci.yml`](.github/workflows/ci.yml) runs on every push and pull request:

| Job | Tool | Purpose |
|---|---|---|
| `lint` | `buf lint` + `golangci-lint` | Style + correctness checks |
| `test` | `go test -race -coverprofile` | Unit tests with race detector |
| `integration` | `go test -tags=integration` | Real-Redis via testcontainers |
| `helm-lint` | `helm lint` + `helm template` | Chart validation |
| `image` | `docker buildx` → GHCR | Multi-arch image publish on `main` |

---

## Engineering decisions

Notable trade-offs made consciously (not defaults):

- **Sliding Window Log over Token Bucket** — Accuracy prioritised over burst-friendliness for security-sensitive endpoints.
- **Lua script over `MULTI`/`EXEC`** — Conditional admit requires read-then-write atomicity; Lua delivers that in one round-trip where `WATCH` would need retry loops.
- **gRPC + REST gateway over REST-only** — Binary efficiency for services, JSON ergonomics for everything else, one source of truth.
- **Distroless over Alpine** — Smaller image, smaller attack surface, no shell for an attacker to land in.
- **miniredis (unit) + testcontainers (integration)** — Fast inner-loop tests without sacrificing production-engine validation.
- **Stateless server + HPA** — Horizontal scaling is free because Redis holds all state; replicas are interchangeable.

---

## Project layout

```
.
├── api/proto/                        # protobuf contract (buf-managed)
├── cmd/server/                       # main binary
├── config/                           # default rule set (YAML)
├── deployments/
│   ├── docker/Dockerfile             # multi-stage, distroless
│   ├── docker-compose.yml            # local dev stack
│   ├── grafana/                      # dashboards + provisioning
│   ├── helm/rate-limiter/            # Helm chart
│   └── prometheus/                   # scrape config
├── gen/                              # buf-generated Go + OpenAPI
├── internal/
│   ├── config/                       # YAML loader + validation
│   ├── limiter/                      # Sliding Window Log + Lua + rule registry
│   ├── metrics/                      # Prometheus collectors
│   └── server/                       # gRPC + REST gateway handlers
├── test/
│   ├── integration/                  # testcontainers-backed Redis tests
│   └── load/k6.js                    # k6 load scenarios
└── .github/workflows/ci.yml          # CI pipeline
```

---

## Roadmap

- Task queue companion: when a request is denied, enqueue deferred work to be drained at the allowed rate.
- Sliding Window Counter algorithm behind the same `Limiter` interface, selectable per-rule.
- Fail-open fallback with bounded in-process bucket when Redis is temporarily unreachable.
- Dynamic rule management via an etcd- or control-plane-backed `RuleRegistry`.

---

## License

MIT — see [LICENSE](LICENSE).
