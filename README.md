# Pulse

**Self-hosted observability and performance monitoring for Go applications built with [Gin](https://github.com/gin-gonic/gin) and [GORM](https://gorm.io).**

Pulse gives you full visibility into your application's HTTP requests, database queries, runtime metrics, errors, health checks, and external dependencies — all from a single `Mount()` call with zero external services required.

---

## Table of Contents

- [Features](#features)
- [Dashboard](#dashboard)
- [Requirements](#requirements)
- [Installation](#installation)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
  - [Dashboard Authentication](#dashboard-authentication)
  - [Storage](#storage)
  - [Request Tracing](#request-tracing)
  - [Database Monitoring](#database-monitoring)
  - [Runtime Metrics](#runtime-metrics)
  - [Error Tracking](#error-tracking)
  - [Health Checks](#health-checks)
  - [Alerting](#alerting)
  - [Prometheus](#prometheus)
- [Dependency Monitoring](#dependency-monitoring)
- [WebSocket Live Updates](#websocket-live-updates)
- [Data Export](#data-export)
- [REST API Reference](#rest-api-reference)
- [Examples](#examples)
  - [Basic Setup](#basic-setup)
  - [Full Configuration](#full-configuration)
  - [Custom Health Checks](#custom-health-checks)
  - [Alerting with Notifications](#alerting-with-notifications)
  - [Dependency Monitoring](#monitoring-external-apis)
  - [Prometheus + Grafana](#prometheus--grafana)
- [Architecture](#architecture)
- [License](#license)

---

## Features

- **Request Tracing** — Automatic trace ID generation, latency tracking, throughput analysis, and slow request detection with configurable thresholds and sampling rates.
- **Database Monitoring** — GORM plugin that captures every query with duration, caller file:line, N+1 detection, query pattern aggregation, and connection pool stats.
- **Runtime Metrics** — Continuous sampling of heap memory, goroutines, GC pauses, and goroutine leak detection.
- **Error Tracking** — Panic recovery, stack traces, request body capture, error fingerprinting for deduplication, and automatic classification (validation, database, timeout, auth, etc.).
- **Health Checks** — Pluggable health check system with Kubernetes-compatible endpoints (`/live`, `/ready`), composite status, and flapping detection.
- **Alerting Engine** — Threshold-based rules with two-phase firing (prevents false alerts), cooldown periods, and multi-channel notifications (Slack, Discord, Email, Webhooks with HMAC signatures).
- **Dependency Monitoring** — Wrap any `http.Client` to track external API latency, error rates, and availability.
- **WebSocket Live Updates** — Real-time streaming of requests, errors, health checks, alerts, and runtime metrics to connected clients with per-client channel subscriptions.
- **Prometheus Export** — Standard exposition format endpoint for Grafana/Prometheus integration.
- **Data Export** — Export requests, queries, errors, runtime metrics, and alerts as JSON or CSV.
- **JWT Authentication** — Dashboard API protected with HS256 JWT tokens (no external auth library).
- **Embedded React Dashboard** — Full-featured UI with 8 pages (Overview, Routes, Database, Errors, Runtime, Health, Alerts, Settings) embedded into the Go binary via `//go:embed`. No separate frontend deployment needed.
- **Zero External Dependencies** — No Redis, no Kafka, no external collectors. Everything runs in-process with ring buffer storage.

---

## Dashboard

Pulse ships with a full React dashboard embedded directly into the Go binary using `//go:embed`. No separate frontend deployment, no CDN, no build step needed by consumers — just `go get` and it works.

**URL:** `http://localhost:8080/pulse/ui/` (default credentials: `admin` / `pulse`)

**Pages:**

| Page | Description |
|------|-------------|
| **Overview** | KPI cards (requests, error rate, latency, goroutines), throughput and error charts, top routes, recent errors |
| **Routes** | Searchable route table with method badges, latency percentiles (P50/P95/P99), RPM, trend indicators; click for detail modal with latency distribution chart |
| **Database** | Slow queries, query patterns, N+1 detection with tabs; connection pool stats (open, in-use, idle) |
| **Errors** | Filterable error list (by type, muted, resolved); detail modal with full stack trace, mute/resolve/delete actions |
| **Runtime** | Memory and goroutine line charts over time, system info (Go version, CPUs, PID); real-time updates via WebSocket |
| **Health** | Health check cards with status badges and latency; run checks on-demand; per-check history table |
| **Alerts** | Firing/critical/resolved summary; filterable alert table with rule, metric, value, threshold, and timestamps |
| **Settings** | Data export (JSON/CSV), current configuration display, danger zone data reset |

**Tech Stack:** React 19, React Router 7, Vite 6, Tailwind CSS 4, Recharts 2

**Rebuilding the dashboard (contributors only):**

```bash
cd ui/dashboard
npm install
npm run build   # outputs to ui/dist/
```

The `ui/dist/` directory is committed to the repo so consumers don't need Node.js.

---

## Requirements

- **Go** 1.22 or later
- **Gin** v1.9+
- **GORM** v1.25+ (optional — pass `nil` if not using a database)

---

## Installation

```bash
go get github.com/MUKE-coder/pulse
```

---

## Quick Start

```go
package main

import (
    "log"

    "github.com/MUKE-coder/pulse/pulse"
    "github.com/gin-gonic/gin"
    "github.com/glebarez/sqlite"
    "gorm.io/gorm"
)

func main() {
    // Database (optional — pass nil to Mount if not using GORM)
    db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
    if err != nil {
        log.Fatal(err)
    }

    router := gin.Default()

    // One line to mount Pulse
    p := pulse.Mount(router, db, pulse.Config{
        AppName: "My API",
        DevMode: true,
    })

    // Your application routes
    router.GET("/api/users", func(c *gin.Context) {
        c.JSON(200, gin.H{"users": []string{"alice", "bob"}})
    })

    // Dashboard:  http://localhost:8080/pulse/ui/
    // API:        http://localhost:8080/pulse/api/
    // Health:     http://localhost:8080/pulse/health
    // WebSocket:  ws://localhost:8080/pulse/ws/live
    // Login:      admin / pulse (default credentials)
    log.Fatal(router.Run(":8080"))
}
```

After starting, Pulse automatically begins tracking every HTTP request, database query, runtime metric, and error. Open `http://localhost:8080/pulse/ui/` to access the dashboard (default login: `admin` / `pulse`). The React dashboard is embedded into the Go binary — no separate frontend deployment needed.

---

## Configuration

All configuration is optional. Pulse ships with sensible defaults — just pass `pulse.Config{}` and everything works.

```go
pulse.Mount(router, db, pulse.Config{
    // URL prefix for all Pulse endpoints (default: "/pulse")
    Prefix:  "/pulse",

    // Application name shown in dashboard (default: "Pulse")
    AppName: "My API",

    // Enable verbose logging and faster aggregation cycles (default: false)
    DevMode: true,

    Dashboard: pulse.DashboardConfig{ ... },
    Storage:   pulse.StorageConfig{ ... },
    Tracing:   pulse.TracingConfig{ ... },
    Database:  pulse.DatabaseConfig{ ... },
    Runtime:   pulse.RuntimeConfig{ ... },
    Errors:    pulse.ErrorConfig{ ... },
    Health:    pulse.HealthConfig{ ... },
    Alerts:    pulse.AlertConfig{ ... },
    Prometheus: pulse.PrometheusConfig{ ... },
})
```

### Dashboard Authentication

The REST API is protected with JWT tokens. Login to get a token, then include it as a `Bearer` header.

```go
Dashboard: pulse.DashboardConfig{
    Username:  "admin",       // default: "admin"
    Password:  "pulse",       // default: "pulse"
    SecretKey: "my-secret",   // auto-generated if empty
},
```

**Login:**

```bash
curl -X POST http://localhost:8080/pulse/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"pulse"}'

# Response: {"token":"eyJhbGci...", "expires_in":86400}
```

**Authenticated Request:**

```bash
curl http://localhost:8080/pulse/api/overview \
  -H "Authorization: Bearer eyJhbGci..."
```

### Storage

```go
Storage: pulse.StorageConfig{
    Driver:         pulse.Memory,   // pulse.Memory (default) or pulse.SQLite
    DSN:            "pulse.db",     // SQLite file path (default: "pulse.db")
    RetentionHours: 24,             // Data retention in hours (default: 24)
},
```

The default in-memory storage uses lock-free ring buffers with ~100K request capacity. For persistence across restarts, use `pulse.SQLite`.

### Request Tracing

```go
Tracing: pulse.TracingConfig{
    Enabled:              boolPtr(true),    // default: true
    SlowRequestThreshold: 500 * time.Millisecond,  // default: 1s
    SampleRate:           float64Ptr(0.5),  // 50% sampling (default: 1.0 = 100%)
    ExcludePaths:         []string{"/healthz", "/metrics"},  // glob patterns
},
```

Errors and slow requests are **always** captured regardless of sample rate. Every request gets a unique trace ID propagated via the `X-Pulse-Trace-ID` header.

### Database Monitoring

```go
Database: pulse.DatabaseConfig{
    Enabled:            boolPtr(true),       // default: true
    SlowQueryThreshold: 100 * time.Millisecond,  // default: 200ms
    DetectN1:           boolPtr(true),       // default: true
    N1Threshold:        5,                   // repeated patterns to flag (default: 5)
    TrackCallers:       boolPtr(true),       // capture file:line (default: true)
},
```

Pulse registers a GORM plugin that hooks into Create, Query, Update, Delete, Row, and Raw callbacks. It tracks:
- Query execution time and SQL normalization
- Caller source file and line number
- N+1 query detection per request (via trace ID correlation)
- Connection pool statistics (open, in-use, idle connections)

### Runtime Metrics

```go
Runtime: pulse.RuntimeConfig{
    Enabled:        boolPtr(true),  // default: true
    SampleInterval: 5 * time.Second,  // default: 5s
    LeakThreshold:  100,            // goroutines/hour growth (default: 100)
},
```

Sampled metrics: `HeapAlloc`, `HeapInUse`, `HeapObjects`, `StackInUse`, `TotalAlloc`, `Sys`, `NumGoroutine`, `GCPauseNs`, `NumGC`, `GCCPUFraction`.

### Error Tracking

```go
Errors: pulse.ErrorConfig{
    Enabled:            boolPtr(true),  // default: true
    CaptureStackTrace:  boolPtr(true),  // default: true
    CaptureRequestBody: boolPtr(true),  // default: true
    MaxBodySize:        4096,           // bytes (default: 4096)
},
```

Pulse automatically:
- Recovers from panics and logs full stack traces
- Captures request body on error responses (with size limit)
- Redacts sensitive headers (`Authorization`, `Cookie`, `X-API-Key`, etc.)
- Fingerprints errors for deduplication (same error at same route = same group)
- Classifies errors: `panic`, `validation`, `database`, `timeout`, `auth`, `not_found`, `internal`

### Health Checks

```go
Health: pulse.HealthConfig{
    Enabled:       boolPtr(true),       // default: true
    CheckInterval: 30 * time.Second,    // default: 30s
    Timeout:       10 * time.Second,    // default: 10s
},
```

Pulse automatically registers a database health check when a GORM `*gorm.DB` is provided. You can add custom checks:

```go
p := pulse.Mount(router, db, cfg)

p.AddHealthCheck(pulse.HealthCheck{
    Name:     "redis",
    Type:     "redis",
    Critical: true,  // failure marks system as unhealthy
    CheckFunc: func(ctx context.Context) error {
        return redisClient.Ping(ctx).Err()
    },
})
```

**Public Health Endpoints (no auth required):**

| Endpoint | Description |
|----------|-------------|
| `GET /pulse/health` | Full health status with all checks |
| `GET /pulse/health/live` | Kubernetes liveness probe (always 200) |
| `GET /pulse/health/ready` | Kubernetes readiness probe (200 or 503) |

**Response Example:**

```json
{
  "status": "healthy",
  "timestamp": "2025-01-15T10:30:00Z",
  "uptime": "2h 15m 30s",
  "checks": {
    "database": {
      "status": "healthy",
      "latency_ms": 1.23
    },
    "redis": {
      "status": "healthy",
      "latency_ms": 0.45
    }
  }
}
```

### Alerting

```go
Alerts: pulse.AlertConfig{
    Enabled:  boolPtr(true),           // default: true
    Cooldown: 15 * time.Minute,        // re-fire cooldown (default: 15m)

    // Custom rules (merged with built-in defaults)
    Rules: []pulse.AlertRule{
        {
            Name:      "api_slow",
            Metric:    "p95_latency",
            Operator:  ">",
            Threshold: 1000,  // milliseconds
            Duration:  3 * time.Minute,
            Severity:  "warning",
        },
    },

    // Notification channels
    Slack: &pulse.SlackConfig{
        WebhookURL: "https://hooks.slack.com/services/...",
        Channel:    "#alerts",  // optional override
    },
    Discord: &pulse.DiscordConfig{
        WebhookURL: "https://discord.com/api/webhooks/...",
    },
    Email: &pulse.EmailConfig{
        Host:     "smtp.gmail.com",
        Port:     587,
        Username: "alerts@example.com",
        Password: "app-password",
        From:     "alerts@example.com",
        To:       []string{"team@example.com"},
    },
    Webhooks: []pulse.WebhookConfig{
        {
            URL:    "https://api.example.com/pulse-webhook",
            Secret: "webhook-secret",  // HMAC-SHA256 signature
            Headers: map[string]string{
                "X-Custom-Header": "value",
            },
        },
    },
},
```

**Built-in Default Rules:**

| Rule | Metric | Condition | Duration | Severity |
|------|--------|-----------|----------|----------|
| `high_latency` | P95 latency | > 2000ms | 5 min | critical |
| `high_error_rate` | Error rate | > 10% | 3 min | critical |
| `high_memory` | Heap allocation | > 500MB | 5 min | warning |
| `goroutine_leak` | Goroutine growth | > 100/hour | 10 min | warning |
| `health_check_failure` | Health status | unhealthy | 2 min | critical |

Custom rules with the same name as a default will **override** the default.

**Alert Lifecycle:** `OK` -> `Pending` (condition met) -> `Firing` (duration exceeded) -> `Resolved` (condition cleared)

The two-phase transition (OK -> Pending -> Firing) prevents false alerts from transient spikes.

**Available Metrics for Rules:**
- `p95_latency` — P95 request latency in milliseconds
- `error_rate` — HTTP error rate percentage
- `heap_alloc_mb` — Heap allocation in megabytes
- `goroutine_growth` — Goroutine growth rate per hour
- `health_status` — Composite health check status (1 = healthy, 0 = unhealthy)

### Prometheus

```go
Prometheus: pulse.PrometheusConfig{
    Enabled: true,                  // default: false
    Path:    "/pulse/metrics",      // default: "/pulse/metrics"
},
```

**Exported Metrics:**

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `pulse_http_requests_total` | counter | method, path, status | Total HTTP requests |
| `pulse_http_request_duration_seconds` | summary | method, path | Request latency (p50, p95, p99) |
| `pulse_http_error_rate` | gauge | method, path | Error rate percentage |
| `pulse_runtime_goroutines` | gauge | | Active goroutine count |
| `pulse_runtime_heap_bytes` | gauge | | Heap memory allocated |
| `pulse_runtime_heap_inuse_bytes` | gauge | | Heap memory in use |
| `pulse_runtime_sys_bytes` | gauge | | Total memory from OS |
| `pulse_runtime_gc_pause_ns` | gauge | | Last GC pause duration |
| `pulse_runtime_gc_total` | counter | | Total GC cycles |
| `pulse_health_check_status` | gauge | name | Health check status (1/0) |
| `pulse_health_check_duration_seconds` | gauge | name | Health check latency |
| `pulse_errors_total` | counter | type | Error count by type |
| `pulse_db_query_duration_seconds` | summary | operation, table | DB query latency |
| `pulse_db_pool_open_connections` | gauge | | Open DB connections |
| `pulse_db_pool_in_use` | gauge | | In-use DB connections |
| `pulse_db_pool_idle` | gauge | | Idle DB connections |
| `pulse_uptime_seconds` | gauge | | Pulse uptime |

---

## Dependency Monitoring

Track external HTTP API calls by wrapping your `http.Client`:

```go
p := pulse.Mount(router, db, cfg)

// Wrap any http.Client — returns a drop-in replacement
stripeClient := pulse.WrapHTTPClient(p, &http.Client{
    Timeout: 10 * time.Second,
}, "stripe")

sendgridClient := pulse.WrapHTTPClient(p, &http.Client{}, "sendgrid")

// Use as normal — metrics are captured automatically
resp, err := stripeClient.Do(req)
```

Captured per-dependency: request count, error count, error rate, availability, average/p50/p95/p99 latency, and RPM.

---

## WebSocket Live Updates

Connect to `ws://localhost:8080/pulse/ws/live` for real-time streaming.

**Subscribe to specific channels:**

```json
{"subscribe": ["request", "error", "alert"]}
```

**Available channels:** `overview`, `request`, `error`, `health`, `alert`, `runtime`

If no subscription message is sent, the client receives **all** channels by default.

**Message format:**

```json
{
  "type": "request",
  "data": {
    "method": "GET",
    "path": "/api/users",
    "status_code": 200,
    "latency": 12345678
  },
  "timestamp": "2025-01-15T10:30:00Z"
}
```

---

## Data Export

Export data via the authenticated API:

```bash
# Export requests as JSON
curl -X POST http://localhost:8080/pulse/api/data/export \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{"format":"json","type":"requests","range":"1h"}'

# Export errors as CSV
curl -X POST http://localhost:8080/pulse/api/data/export \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{"format":"csv","type":"errors","range":"24h"}'
```

**Supported types:** `requests`, `queries`, `errors`, `runtime`, `alerts`
**Supported formats:** `json`, `csv`
**Supported ranges:** `5m`, `15m`, `1h`, `6h`, `24h`, `7d`

Maximum 100,000 records per export. Response includes a `Content-Disposition` header with a timestamped filename.

---

## REST API Reference

All endpoints under `/pulse/api/` require JWT authentication (except login).

### Authentication

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/pulse/api/auth/login` | Login, returns JWT token |
| `GET` | `/pulse/api/auth/verify` | Verify token validity |

### Overview

| Method | Endpoint | Query Params | Description |
|--------|----------|--------------|-------------|
| `GET` | `/pulse/api/overview` | `?range=1h` | Dashboard snapshot |

### Routes

| Method | Endpoint | Query Params | Description |
|--------|----------|--------------|-------------|
| `GET` | `/pulse/api/routes` | `?range=1h&search=users` | List all routes with stats |
| `GET` | `/pulse/api/routes/:method/*path` | `?range=1h` | Detailed route info |

### Database

| Method | Endpoint | Query Params | Description |
|--------|----------|--------------|-------------|
| `GET` | `/pulse/api/database/overview` | `?range=1h` | DB statistics summary |
| `GET` | `/pulse/api/database/slow-queries` | `?threshold=100ms&limit=50` | Slow queries |
| `GET` | `/pulse/api/database/patterns` | `?range=1h` | Aggregated query patterns |
| `GET` | `/pulse/api/database/n1` | `?range=1h` | N+1 query detections |
| `GET` | `/pulse/api/database/pool` | | Connection pool stats |

### Errors

| Method | Endpoint | Query Params | Description |
|--------|----------|--------------|-------------|
| `GET` | `/pulse/api/errors` | `?type=database&route=/api&muted=false&resolved=false&limit=50&offset=0` | List errors |
| `GET` | `/pulse/api/errors/:id` | | Error details |
| `POST` | `/pulse/api/errors/:id/mute` | | Mute an error |
| `POST` | `/pulse/api/errors/:id/resolve` | | Resolve an error |
| `DELETE` | `/pulse/api/errors/:id` | | Delete an error |

### Runtime

| Method | Endpoint | Query Params | Description |
|--------|----------|--------------|-------------|
| `GET` | `/pulse/api/runtime/current` | | Latest runtime metrics |
| `GET` | `/pulse/api/runtime/history` | `?range=1h` | Runtime metrics over time |
| `GET` | `/pulse/api/runtime/info` | | System info (Go version, CPU, etc.) |

### Health (authenticated)

| Method | Endpoint | Query Params | Description |
|--------|----------|--------------|-------------|
| `GET` | `/pulse/api/health/checks` | | All health check results |
| `GET` | `/pulse/api/health/checks/:name/history` | `?limit=100` | Single check history |
| `POST` | `/pulse/api/health/checks/:name/run` | | Run a check on-demand |

### Alerts

| Method | Endpoint | Query Params | Description |
|--------|----------|--------------|-------------|
| `GET` | `/pulse/api/alerts` | `?state=firing&severity=critical&limit=50` | List alerts |

### Settings & Data

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/pulse/api/settings` | Current config (secrets redacted) |
| `POST` | `/pulse/api/data/reset` | Reset all data (requires `{"confirm":true}`) |
| `POST` | `/pulse/api/data/export` | Export data as JSON/CSV |

### Time Range Parameter

Most endpoints accept a `?range=` query parameter:

| Value | Duration |
|-------|----------|
| `5m` | Last 5 minutes |
| `15m` | Last 15 minutes |
| `1h` | Last 1 hour (default) |
| `6h` | Last 6 hours |
| `24h` | Last 24 hours |
| `7d` | Last 7 days |

---

## Examples

### Basic Setup

The simplest way to get started — mount Pulse with defaults:

```go
package main

import (
    "log"

    "github.com/MUKE-coder/pulse/pulse"
    "github.com/gin-gonic/gin"
    "github.com/glebarez/sqlite"
    "gorm.io/gorm"
)

func main() {
    db, _ := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
    router := gin.Default()

    pulse.Mount(router, db, pulse.Config{
        AppName: "Blog API",
        DevMode: true,
    })

    router.GET("/", func(c *gin.Context) {
        c.JSON(200, gin.H{"message": "Hello, World!"})
    })

    log.Fatal(router.Run(":8080"))
}
```

### Full Configuration

Complete configuration with all options:

```go
p := pulse.Mount(router, db, pulse.Config{
    Prefix:  "/pulse",
    AppName: "Production API",
    DevMode: false,

    Dashboard: pulse.DashboardConfig{
        Username: "admin",
        Password: "strong-password-here",
    },

    Storage: pulse.StorageConfig{
        Driver:         pulse.Memory,
        RetentionHours: 48,
    },

    Tracing: pulse.TracingConfig{
        SlowRequestThreshold: 500 * time.Millisecond,
        ExcludePaths:         []string{"/healthz", "/readyz"},
    },

    Database: pulse.DatabaseConfig{
        SlowQueryThreshold: 100 * time.Millisecond,
        N1Threshold:        3,
    },

    Runtime: pulse.RuntimeConfig{
        SampleInterval: 10 * time.Second,
        LeakThreshold:  50,
    },

    Errors: pulse.ErrorConfig{
        MaxBodySize: 8192,
    },

    Health: pulse.HealthConfig{
        CheckInterval: 15 * time.Second,
        Timeout:       5 * time.Second,
    },

    Alerts: pulse.AlertConfig{
        Cooldown: 30 * time.Minute,
        Slack: &pulse.SlackConfig{
            WebhookURL: os.Getenv("SLACK_WEBHOOK_URL"),
        },
        Rules: []pulse.AlertRule{
            {
                Name:      "critical_latency",
                Metric:    "p95_latency",
                Operator:  ">",
                Threshold: 5000,
                Duration:  2 * time.Minute,
                Severity:  "critical",
            },
        },
    },

    Prometheus: pulse.PrometheusConfig{
        Enabled: true,
    },
})
```

### Custom Health Checks

Register checks for all your external dependencies:

```go
p := pulse.Mount(router, db, cfg)

// Redis
p.AddHealthCheck(pulse.HealthCheck{
    Name:     "redis",
    Type:     "redis",
    Critical: true,
    CheckFunc: func(ctx context.Context) error {
        return redisClient.Ping(ctx).Err()
    },
})

// External API
p.AddHealthCheck(pulse.HealthCheck{
    Name:     "payment-gateway",
    Type:     "http",
    Critical: false,  // degraded, not unhealthy
    Timeout:  5 * time.Second,
    CheckFunc: func(ctx context.Context) error {
        req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.stripe.com/v1/health", nil)
        resp, err := http.DefaultClient.Do(req)
        if err != nil {
            return err
        }
        defer resp.Body.Close()
        if resp.StatusCode != 200 {
            return fmt.Errorf("status %d", resp.StatusCode)
        }
        return nil
    },
})

// Disk space
p.AddHealthCheck(pulse.HealthCheck{
    Name: "disk",
    Type: "disk",
    CheckFunc: func(ctx context.Context) error {
        // Check available disk space
        return nil
    },
})
```

### Alerting with Notifications

Set up alerts with Slack and webhook notifications:

```go
pulse.Mount(router, db, pulse.Config{
    Alerts: pulse.AlertConfig{
        Slack: &pulse.SlackConfig{
            WebhookURL: "https://hooks.slack.com/services/T.../B.../xxx",
            Channel:    "#production-alerts",
        },
        Webhooks: []pulse.WebhookConfig{
            {
                URL:    "https://api.pagerduty.com/pulse",
                Secret: "pd-webhook-secret",
                Headers: map[string]string{
                    "X-PagerDuty-Token": "your-token",
                },
            },
        },
        Rules: []pulse.AlertRule{
            {
                Name:      "cart_slow",
                Metric:    "p95_latency",
                Operator:  ">",
                Threshold: 3000,
                Duration:  2 * time.Minute,
                Severity:  "critical",
                Route:     "/api/cart/checkout",  // route-specific alert
            },
        },
    },
})
```

Webhooks include an `X-Pulse-Signature` header with HMAC-SHA256 for verification and automatically retry 3 times with exponential backoff.

### Monitoring External APIs

Track latency and availability of external services:

```go
p := pulse.Mount(router, db, cfg)

// Each wrapped client tracks metrics independently
stripeClient := pulse.WrapHTTPClient(p, &http.Client{
    Timeout: 10 * time.Second,
}, "stripe")

twilioClient := pulse.WrapHTTPClient(p, &http.Client{
    Timeout: 15 * time.Second,
}, "twilio")

s3Client := pulse.WrapHTTPClient(p, &http.Client{}, "aws-s3")

// Use them as normal http.Client instances
resp, err := stripeClient.Post(
    "https://api.stripe.com/v1/charges",
    "application/x-www-form-urlencoded",
    body,
)
```

Pulse tracks per-dependency: request count, error count, error rate, availability percentage, average/p50/p95/p99 latency, and requests per minute.

### Prometheus + Grafana

Enable the Prometheus endpoint and scrape it with Prometheus:

```go
pulse.Mount(router, db, pulse.Config{
    Prometheus: pulse.PrometheusConfig{
        Enabled: true,
        Path:    "/metrics",  // or default "/pulse/metrics"
    },
})
```

**prometheus.yml:**

```yaml
scrape_configs:
  - job_name: 'my-api'
    scrape_interval: 15s
    metrics_path: '/metrics'
    static_configs:
      - targets: ['localhost:8080']
```

---

## Architecture

```
┌──────────────────────────────────────────────────────────────┐
│                        Gin Router                            │
│  ┌──────────────┐  ┌──────────────┐  ┌────────────────────┐ │
│  │ Error MW     │  │ Tracing MW   │  │ Your App Routes    │ │
│  │ (panic/5xx)  │  │ (trace IDs)  │  │ GET /api/users     │ │
│  └──────┬───────┘  └──────┬───────┘  └────────────────────┘ │
└─────────┼─────────────────┼──────────────────────────────────┘
          │                 │
          ▼                 ▼
┌──────────────────────────────────────────────────────────────┐
│                      Pulse Engine                            │
│                                                              │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────┐  │
│  │ GORM Plugin │  │ Runtime     │  │ Health Runner       │  │
│  │ (queries,   │  │ Sampler     │  │ (checks, K8s       │  │
│  │  N+1 detect)│  │ (5s ticks)  │  │  probes, flapping) │  │
│  └──────┬──────┘  └──────┬──────┘  └──────────┬──────────┘  │
│         │                │                     │             │
│         ▼                ▼                     ▼             │
│  ┌───────────────────────────────────────────────────────┐   │
│  │                    Storage Layer                      │   │
│  │  ┌──────────────────────┐  ┌──────────────────────┐   │   │
│  │  │ MemoryStorage        │  │ SQLiteStorage        │   │   │
│  │  │ (RingBuffer[T])      │  │ (persistent)         │   │   │
│  │  └──────────────────────┘  └──────────────────────┘   │   │
│  └───────────────────────────────────────────────────────┘   │
│         │                │                     │             │
│         ▼                ▼                     ▼             │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────┐  │
│  │ Aggregator  │  │ Alert       │  │ WebSocket Hub       │  │
│  │ (rollups,   │  │ Engine      │  │ (live broadcast,    │  │
│  │  trends)    │  │ (rules,     │  │  subscriptions)     │  │
│  │             │  │  notify)    │  │                     │  │
│  └─────────────┘  └─────────────┘  └─────────────────────┘  │
│                                                              │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────┐  │
│  │ REST API    │  │ Prometheus  │  │ Data Export         │  │
│  │ (JWT auth)  │  │ Endpoint    │  │ (JSON/CSV)          │  │
│  └─────────────┘  └─────────────┘  └─────────────────────┘  │
└──────────────────────────────────────────────────────────────┘
```

**Key Design Decisions:**

- **No CGo** — Uses `github.com/glebarez/sqlite` (pure Go SQLite driver) for maximum portability.
- **Lock-free Ring Buffers** — `RingBuffer[T]` provides O(1) append with atomic operations, no locks on the hot path.
- **Async Storage** — All metric writes happen in goroutines to avoid blocking request handling.
- **Background Lifecycle** — All background goroutines are managed via `context.Context` + `sync.WaitGroup` for clean shutdown.
- **Pointer Config Fields** — `*bool` and `*float64` config fields distinguish between "not set" (use default) and "explicitly set to zero/false".

---

## Graceful Shutdown

Pulse manages background goroutines that should be stopped on shutdown:

```go
p := pulse.Mount(router, db, cfg)

// On shutdown
p.Shutdown()
```

This stops all background goroutines (runtime sampler, aggregator, health runner, alert engine, WebSocket hub) and waits for them to finish.

---

## License

MIT
