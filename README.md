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
  - [Per-route baselines](#per-route-baselines)
  - [Prometheus](#prometheus)
  - [Log capture](#log-capture)
  - [OpenTelemetry and exporters](#opentelemetry-and-exporters)
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
- **SLOs with burn-rate alerts** — Multiwindow burn-rate alerting (Google SRE workbook), computed from exact per-minute counts of every request, so sampling never skews compliance. Survives restarts with the SQLite backend.
- **Query plans for slow queries** — Opt in and Pulse asks the database why a slow `SELECT` is slow, reading the sequential scan or the filesort off the plan. `EXPLAIN` only, one plan per pattern per hour, off the request path.
- **Per-route baselines** — Each route's hourly p95 is summarised and kept for 30 days, so Pulse can say a route *got slower*, not just that it is slow. A sustained regression against the median of the last week pages once, with the numbers to decide on a rollback.
- **Secret redaction** — Passwords, tokens, card numbers, JWTs and keys are stripped from captured request context, error messages and outbound URLs before anything is stored, whatever the field name or naming style. Configurable.
- **Load-test overlay** — k6 (or any harness) runs appear as bands on the dashboard timelines, with a before/during/after comparison per route, query and N+1 deltas, time to recovery, and a CPU profile captured over the run so you can see *which function* got slow under load.
- **Knows its own limits** — Alerts when buffer capacity rather than your retention setting limits history, when writes are dropped, and after an unclean restart. Pulse's own failures are counted and exported.
- **Dependency Monitoring** — Wrap any `http.Client` to track external API latency, error rates, and availability.
- **WebSocket Live Updates** — Real-time streaming of requests, errors, health checks, alerts, and runtime metrics to connected clients with per-client channel subscriptions.
- **Prometheus Export** — Standard exposition format endpoint for Grafana/Prometheus integration.
- **Data Export** — Export requests, queries, errors, runtime metrics, and alerts as JSON or CSV.
- **JWT Authentication** — Dashboard API protected with HS256 JWT tokens (no external auth library).
- **Embedded React Dashboard** — Full-featured UI with 12 pages (Overview, Routes, Database, Errors, Runtime, Health, Alerts, SLOs, USE, Test Runs, Flame Graph, Settings) embedded into the Go binary via `//go:embed`. No separate frontend deployment needed.
- **Zero External Dependencies** — No Redis, no Kafka, no external collectors. Everything runs in-process, in memory or in a local SQLite file.

---

## Dashboard

Pulse ships with a full React dashboard embedded directly into the Go binary using `//go:embed`. No separate frontend deployment, no CDN, no build step needed by consumers — just `go get` and it works.

**URL:** `http://localhost:8080/pulse/ui/` (default credentials: `admin` / `pulse`)

**Light and dark.** The dashboard follows the operating system by default and the toggle in the top bar overrides it, remembered per browser. Every colour comes from one set of CSS custom properties, so both themes share the same layout, spacing and type — only the values change. Charts read the same tokens and redraw when the theme flips.

**The shell.** A 240px sidebar groups the pages under *Observe*, *Reliability* and *Tools*, with live counts beside Errors and Alerts and a status dot beside Health. The top bar carries the breadcrumb, a `⌘K` search over routes, errors and pages, the environment, one time range shared by every page, a Live/Paused switch and the theme toggle.

**Type and numbers.** IBM Plex Sans for the interface, IBM Plex Mono with tabular figures for every number, route and SQL string, so columns line up and values are scannable. The fonts are self-hosted inside the binary — about 120KB — so the dashboard looks right on a host with no internet.

**Pages:**

| Page | Description |
|------|-------------|
| **Overview** | KPI cards (requests, error rate, latency, goroutines), throughput and error charts with load-test runs as bands and restarts as markers, top routes, recent errors, and a banner when data is missing after a restart |
| **Routes** | Searchable route table with method badges, latency percentiles (P50/P95/P99), RPM, trend indicators; click for detail modal with latency distribution chart |
| **Database** | KPI tiles with their window, a delta against the previous window and a status badge; a connection-pool card with a usage bar and Open / In use / Idle / Wait count readouts; tabs for slow queries, patterns, N+1 detections ranked by added latency with a severity band, and captured query plans |
| **Errors** | Filterable error list (by type, muted, resolved); detail modal with full stack trace, mute/resolve/delete actions |
| **Runtime** | Memory and goroutine line charts over time, system info (Go version, CPUs, PID); real-time updates via WebSocket |
| **Health** | Health check cards with status badges and latency; run checks on-demand; per-check history table |
| **Alerts** | Firing/critical/resolved summary; filterable alert table with rule, metric, value, threshold, and timestamps |
| **SLOs** | Compliance, error budget, and long- and short-window burn rates per SLO, with data coverage |
| **USE** | Utilization / Saturation / Errors grid per host resource |
| **Test Runs** | Load-test runs with a before/during/after comparison and time to recovery |
| **Flame Graph** | On-demand sampled flame graph (double-gated) |
| **Settings** | Data export (JSON/CSV), storage capacity, current configuration display, danger zone data reset |

**Login.** A split screen: a brand panel that stays dark in both themes, and the form. The panel's three readouts — uptime, p95 latency and status — are real or blank, never invented; they come from the opt-in public status endpoint (see [`WithPublicStatus`](#dashboard-authentication)). "Keep me signed in" is what decides whether the session outlives the tab.

**Tech Stack:** React 19, React Router 7, Vite 6, Tailwind CSS 4, Recharts 2, IBM Plex (self-hosted), Lucide icons

**Rebuilding the dashboard (contributors only):**

```bash
cd ui/dashboard
npm install
npm run build   # outputs to ui/dist/
```

The `ui/dist/` directory is committed to the repo so consumers don't need Node.js.

---

## Requirements

- **Go** 1.24 or later
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
    "context"
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

    // One line to mount Pulse. Pulse's background goroutines are tied to ctx —
    // cancel it (e.g. on SIGINT) and Pulse shuts down cleanly.
    ctx := context.Background()
    pulse.Mount(ctx, router, db,
        pulse.WithAppName("My API"),
        pulse.WithDevMode(),
    )

    // Your application routes
    router.GET("/api/users", func(c *gin.Context) {
        c.JSON(200, gin.H{"users": []string{"alice", "bob"}})
    })

    // Dashboard:  http://localhost:8080/pulse/ui/
    // API:        http://localhost:8080/pulse/api/
    // Health:     http://localhost:8080/pulse/health
    // WebSocket:  ws://localhost:8080/pulse/ws/live?token=<jwt>  (auth required)
    // Login:      admin / pulse (default credentials — DevMode only)
    log.Fatal(router.Run(":8080"))
}
```

After starting, Pulse automatically begins tracking every HTTP request, database query, runtime metric, and error. Open `http://localhost:8080/pulse/ui/` to access the dashboard (default login: `admin` / `pulse`). The React dashboard is embedded into the Go binary — no separate frontend deployment needed.

---

## Configuration

All configuration is optional. Pulse ships with sensible defaults — call `pulse.Mount(ctx, router, db)` with no options and everything works.

Configuration is supplied via **functional options**. The most common knobs have dedicated `With*` helpers; for everything else, build a [`pulse.Config`](pulse/config.go) and pass it via `pulse.WithConfig(...)`.

```go
// Granular form — preferred for one-off knobs.
pulse.Mount(ctx, router, db,
    pulse.WithAppName("My API"),
    pulse.WithDevMode(),
    pulse.WithCredentials("admin", os.Getenv("PULSE_PASSWORD")),
    pulse.WithSecretKeyFile("/var/lib/pulse/secret.key"),
    pulse.WithSlackAlerts(os.Getenv("SLACK_WEBHOOK_URL")),
    pulse.WithPrometheus(),
    pulse.WithSlowRequestThreshold(500 * time.Millisecond),
)

// Config-struct form — preferred when loading from YAML/env.
pulse.Mount(ctx, router, db, pulse.WithConfig(pulse.Config{
    Prefix:  "/pulse",
    AppName: "My API",
    DevMode: true,
    Dashboard: pulse.DashboardConfig{ /* ... */ },
    Tracing:   pulse.TracingConfig{ /* ... */ },
    // ...
}))
```

> **Upgrading from v0.1.0?** See the [migration notes in CHANGELOG.md](CHANGELOG.md#migrating-from-v010). The change is mechanical: prepend `ctx`, wrap your `Config{}` in `pulse.WithConfig(...)`.

### Dashboard Authentication

The REST API and the live WebSocket are both protected with JWT tokens. Login to get a token, then include it as a `Bearer` header on REST calls (or as `?token=…` on the WebSocket).

```go
Dashboard: pulse.DashboardConfig{
    Username:       "admin",         // default: "admin" (see warning below)
    Password:       "pulse",         // default: "pulse" (see warning below)
    SecretKey:      "my-secret",     // prefer SecretKeyFile in production
    SecretKeyFile:  "/var/lib/pulse/secret.key", // loaded if exists, else created (0600)
    LoginRateLimit: 10,              // login attempts/minute per IP (default: 10; 0 disables)

    // Optional escape hatch: lets Pulse start in production with the shipped
    // default credentials. Only use this behind a private network.
    AllowDefaultCredentials: false,

    // Serve <prefix>/api/public/status without authentication, so the login
    // screen can show whether the service is up, how long it has been up and
    // its p95 latency (default: false — those cards stay blank until you
    // turn it on).
    PublicStatus: pulse.Bool(false),
},
```

#### The one unauthenticated view of live data

`WithPublicStatus()` serves `GET /pulse/api/public/status`, which is what the login screen's three status cards read. It carries a status word, an uptime and a p95 latency, and nothing route-specific — a status card, not a dashboard. It is off by default because anyone who can reach the login page can then read those three numbers; with it off the cards show em-dashes rather than invented values.

#### Default credentials are blocked in production

Pulse ships with `admin` / `pulse` as a convenience for the demo. In production (`DevMode=false`) Pulse **refuses to start** while those values are still in use unless `Dashboard.AllowDefaultCredentials` is explicitly set to `true`. Set a strong password — or, ideally, source it from your secrets manager — before deploying.

#### Persistent JWT signing key

If you do not set `Dashboard.SecretKey`, Pulse falls back to `Dashboard.SecretKeyFile`. If that file exists, the key is loaded from it; if it does not exist, a fresh 256-bit key is generated and written to that path with mode `0600`. With neither field set, Pulse uses an in-memory key — **every restart invalidates all live tokens**, and a warning is logged in production.

#### Login rate limit

`/pulse/api/auth/login` is throttled per client IP via an in-memory token bucket. Default cap is 10 attempts per minute; configurable via `Dashboard.LoginRateLimit` (zero disables it). When exceeded, Pulse returns `429 Too Many Requests` with a `Retry-After: 60` header.

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

Pulse ships with three storage backends. Pick one when you mount:

```go
// In-memory (default, fastest):
pulse.Mount(ctx, router, db /*, no Storage option needed */)

// SQLite (persistent across restarts):
pulse.Mount(ctx, router, db, pulse.WithSQLite("pulse.db"))

// PostgreSQL (persistent, and shared by several replicas):
import _ "github.com/jackc/pgx/v5/stdlib"
pulse.Mount(ctx, router, db, pulse.WithPostgres("postgres://user:pass@db:5432/app"))
```

Or set them on the Config struct directly:

```go
Storage: pulse.StorageConfig{
    Driver:         pulse.SQLite,
    DSN:            "pulse.db",      // or ":memory:" for an ephemeral SQLite DB
    RetentionHours: 24,
},
```

**Memory backend** — fixed-capacity ring buffers (100K requests, 50K queries). Fast and bounded, but data is lost on restart. Under sustained traffic the buffer, not `RetentionHours`, limits how much history you have: 100K requests is about 17 minutes at 100 req/s.

**SQLite backend** — pure Go (no CGo), WAL journal mode, `busy_timeout=5000ms`. The schema is created automatically on first open. Metric writes are queued and committed in batches by a single writer (up to 500 rows, or every 250 ms), so storing never blocks a request. If the queue fills, records are dropped and counted rather than slowing your app. Reads always see earlier writes, and a crash loses at most the last quarter-second. Comes from `github.com/glebarez/go-sqlite`, already a transitive dependency, so no new module is pulled in for Memory-only consumers.

**PostgreSQL backend** — the same batched writer and schema as SQLite, in a schema of its own (`pulse` by default; `WithPostgresSchema`), so it can share the application's database. Several replicas can record into it at once; see [Running multiple replicas](#running-multiple-replicas). Pulse doesn't import a PostgreSQL driver itself: import `github.com/jackc/pgx/v5/stdlib` or `github.com/lib/pq`. To manage the connection pool yourself, build the backend with `pulse.NewPostgresStorage(sqlDB, appName, schema)` and pass it with `pulse.WithStorage`.

A background **retention sweeper** runs every minute (every 10 s in `DevMode`) against both backends, dropping records older than `RetentionHours`. Ring buffers also self-trim by overwriting their oldest entries.

**Exact counts, whatever the sample rate.** Alongside the stored records, Pulse keeps per-minute tallies of every request: counts, 4xx/5xx, a latency histogram, and SLO good/total events. Overview totals, error rates, SLOs and load-test comparisons come from these, so `Tracing.SampleRate` and ring-buffer size never skew them. With SQLite they are saved every 30 s and restored at startup.

**Knowing when history is limited.** `GET /pulse/api/storage` (and the Settings page) shows each buffer's fill level and how far back it actually reaches. The built-in `storage_retention_limited` alert fires when a full buffer holds less than half of `RetentionHours`. Raise the buffer sizes with `pulse.WithMemoryCapacity(pulse.MemoryCapacity{Requests: 500_000})`, or switch to SQLite. `storage_writes_dropped` fires when the SQLite write queue overflows.

**Restarts.** Pulse records a start and a stop event for each process. With SQLite, a start whose previous run never recorded a stop (crash, OOM kill, `kill -9`) is flagged, together with the time of the last data before it. The dashboard marks restarts on its charts and explains missing data, and `GET /pulse/api/lifecycle` returns the events. In production, Pulse logs a note at startup when it runs on in-memory storage.

#### Running multiple replicas

With the Memory or SQLite backend, each replica keeps its own data and its dashboard shows only its own traffic. To run several replicas as one application, give them shared storage in PostgreSQL:

```go
import _ "github.com/jackc/pgx/v5/stdlib" // or github.com/lib/pq

pulse.Mount(ctx, router, db,
    pulse.WithPostgres(os.Getenv("PULSE_DSN")),     // tables go in the "pulse" schema
    pulse.WithSecretKey(os.Getenv("PULSE_SECRET")), // the same on every replica
)
```

- **One dashboard.** Every replica records into the same tables, so any replica's dashboard shows all of them: totals, error rates, SLOs, charts and test-run comparisons count every replica's requests. The Overview lists each instance with its status and traffic; the Runtime and Health pages have an instance picker.
- **One page per incident.** One replica is elected leader through a PostgreSQL advisory lock. It evaluates the application-wide alert rules (latency, error rate, retention) and SLO burn rates, sends their notifications and sweeps retention, so three replicas send one page, not three. If the leader stops or its database connection dies, another replica takes over within seconds and picks up the alerts the leader left open.
- **Per-replica rules stay per replica.** Rules about a single process (memory, goroutines, health checks, dropped writes) are evaluated by every replica about itself, and their alerts name the replica.
- **Same signing key everywhere.** Use `pulse.WithSecretKey(os.Getenv("PULSE_SECRET"))` (recommended), or a `SecretKeyFile` on a shared volume; replicas that start at the same moment against one key file agree on a single key.
- **Distinct instance IDs.** `pulse.WithInstanceID(...)` defaults to the hostname, which is unique per container; set it when several replicas share a host.
- The login rate limit is per replica. `pulse.WithPostgresSchema(name)` keeps two applications that share a database apart.

Without shared storage, pin each browser's dashboard traffic to one replica (sticky sessions), so it doesn't flip between replicas' data, and expect each replica to send its own alerts.

[`examples/multi-instance`](examples/multi-instance) runs two replicas and PostgreSQL behind Caddy.

### Request Tracing

```go
Tracing: pulse.TracingConfig{
    Enabled:              pulse.Bool(true),    // default: true
    SlowRequestThreshold: 500 * time.Millisecond,  // default: 1s
    SampleRate:           float64Ptr(0.5),  // 50% sampling (default: 1.0 = 100%)
    ExcludePaths:         []string{"/healthz", "/metrics"},  // glob patterns
},
```

Errors and slow requests are **always** captured regardless of sample rate. Sampling only thins out the stored request records: request counts, error rates, SLO compliance and load-test comparisons still count every request. Every request gets a trace ID propagated via both the legacy `X-Pulse-Trace-ID` header and the W3C [`traceparent`](https://www.w3.org/TR/trace-context/) header.

If an inbound request already carries a valid `traceparent`, Pulse adopts the upstream `trace-id` instead of generating a new one — so traces continue cleanly across service boundaries when upstream services are instrumented with OpenTelemetry. Outbound calls sent through `pulse.WrapHTTPClient` get a `traceparent` header injected automatically.

### Database Monitoring

```go
Database: pulse.DatabaseConfig{
    Enabled:            pulse.Bool(true),       // default: true
    SlowQueryThreshold: 100 * time.Millisecond,  // default: 200ms
    DetectN1:           pulse.Bool(true),       // default: true
    N1Threshold:        5,                   // repeated patterns to flag (default: 5)
    TrackCallers:       pulse.Bool(true),       // capture file:line (default: true)
    ExplainSlowQueries: pulse.Bool(false),      // ask the database for plans (default: false)
    ExplainInterval:    time.Hour,              // at most one plan per pattern per... (default: 1h)
},
```

Pulse registers a GORM plugin that hooks into Create, Query, Update, Delete, Row, and Raw callbacks. It tracks:
- Query execution time and SQL normalization
- Caller source file and line number
- N+1 query detection per request (via trace ID correlation), each finding carrying the file and line that issued the repeated query
- Connection pool statistics (open, in-use, idle connections)

**Query plans (opt-in).** A slow query says what took long; its plan says why. With `pulse.WithExplainSlowQueries(0)` (or `ExplainSlowQueries: pulse.Bool(true)`), Pulse asks the database for the plan of a slow `SELECT` and shows it on the Database page under **Query Plans**, with the handful of things that usually explain it read off the top:

```
SELECT * FROM orders WHERE user_id = ?                                      412ms
reads the whole table: SCAN orders
  SCAN orders
captured 14:02:11
```

The guardrails are the point, since this runs queries against your database:

- **`EXPLAIN` only, never `EXPLAIN ANALYZE`** — the slow query is never run a second time.
- **`SELECT` only.** Explaining a write executes it on some databases, so writes are never explained.
- **At most one plan per query pattern per `ExplainInterval`** (default an hour), on a background goroutine with a 2s timeout. Nothing happens on the request path, and a full queue drops rather than waits.
- It goes **straight down the connection pool**, so Pulse's own `EXPLAIN` isn't recorded as one of your queries.
- Plans are **scrubbed** before storage, like everything else Pulse keeps, because a plan can echo the values a query filtered on.
- Pulse knows how to ask PostgreSQL, MySQL and SQLite. On any other dialect it asks nothing.

One plan is kept per pattern — always the latest — up to 200, and `GET /pulse/api/database/plans` serves them.

### Runtime Metrics

```go
Runtime: pulse.RuntimeConfig{
    Enabled:        pulse.Bool(true),  // default: true
    SampleInterval: 5 * time.Second,  // default: 5s
    LeakThreshold:  100,            // goroutines/hour growth (default: 100)
},
```

Sampled metrics: `HeapAlloc`, `HeapInUse`, `HeapObjects`, `StackInUse`, `TotalAlloc`, `Sys`, `NumGoroutine`, `GCPauseNs`, `NumGC`, `GCCPUFraction`.

### Error Tracking

```go
Errors: pulse.ErrorConfig{
    Enabled:            pulse.Bool(true),  // default: true
    CaptureStackTrace:  pulse.Bool(true),  // default: true
    CaptureRequestBody: pulse.Bool(true),  // default: true
    MaxBodySize:        4096,           // bytes (default: 4096)
},
```

Pulse automatically:
- Recovers from panics and logs full stack traces
- Captures request context on error responses: method, path, query string, headers, and the body up to `MaxBodySize` (your handlers always receive the complete body)
- Redacts secrets before anything is stored, broadcast, or exported:
  - **Headers:** `Authorization`, `Cookie`, `X-API-Key`, `X-CSRF-Token`, `X-Amz-Security-Token`, and similar
  - **JSON body fields, form keys, and query parameters** whose names look secret, at any nesting depth and in any naming style: `password`, `newPassword`, `card_number`, `cardNumber`, `api_key`, `client_secret`, `access_token`, `cvv`, `ssn`, `pin`, `otp`, …
  - **Secrets inside any text** — card numbers (Luhn-checked), JWTs, `Bearer` tokens, AWS access keys and private keys — in body values, header values, the path, error messages, and outbound URLs and their errors
  - **Outbound URLs** recorded by `WrapHTTPClient`: userinfo passwords and sensitive query parameters
- Fails closed: only JSON and form bodies are captured. Other content types (text, XML, multipart, binary) are stored as `[body omitted: N bytes of <type>]`, and a JSON body cut off at `MaxBodySize` is redacted up to the cut and flagged `body_truncated`
- Fingerprints errors for deduplication (same error at same route = same group; values redacted from a message don't split the group)
- Classifies errors: `panic`, `validation`, `database`, `timeout`, `auth`, `not_found`, `internal`

**Adding your own redaction rules:**

```go
pulse.Mount(ctx, router, db,
    pulse.WithRedactFields("date_of_birth", "medical_record_no"), // matched in any naming style
    pulse.WithRedactHeaders("X-Tenant-Secret"),
    pulse.WithRedactValuePatterns(regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)), // US SSNs
    pulse.WithRedactor(func(rc *pulse.RequestContext) { delete(rc.Headers, "X-Internal-Trace") }),
)
```

`Config.Errors.Redaction` also takes substring stems (`FieldContains`) and `DisableDefaults`. If a redaction hook panics, the captured context is cut down to method and path. Error records stored unredacted by v1.0.0 are re-redacted automatically, once, the first time v1.1 opens an SQLite database.

### Health Checks

```go
Health: pulse.HealthConfig{
    Enabled:       pulse.Bool(true),       // default: true
    CheckInterval: 30 * time.Second,    // default: 30s
    Timeout:       10 * time.Second,    // default: 10s
},
```

Pulse automatically registers a database health check when a GORM `*gorm.DB` is provided. You can add custom checks:

```go
p := pulse.Mount(ctx, router, db)

p.AddHealthCheck(pulse.HealthCheck{
    Name:     "redis",
    Type:     "redis",
    Critical: true,  // failure marks system as unhealthy
    CheckFunc: func(ctx context.Context) error {
        return redisClient.Ping(ctx).Err()
    },
})
```

Each check runs in its own goroutine on its own `Interval` (default: `CheckInterval`). Pulse enforces the `Timeout` even if a check ignores its context, so a hung dependency fails its own check without delaying the others, and isn't started again while it is stuck. A panicking check fails instead of crashing the process.

With the SQLite backend, Pulse also registers a non-critical `pulse_storage` check. It fails when Pulse's own storage writes fail or are dropped, so Pulse's trouble makes the app read `degraded`, never `unhealthy`.

**Public Health Endpoints (no auth required):**

These answer from the latest results held in memory, so they keep working when Pulse's own storage is slow.

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
    Enabled:  pulse.Bool(true),           // default: true
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
| `high_latency` | P95 latency | > 2000ms | 5 min | warning |
| `high_error_rate` | Error rate | > 10% | 3 min | critical |
| `high_memory` | Heap allocation | > 500MB | 5 min | warning |
| `goroutine_leak` | Goroutine growth | > 100/hour | 10 min | warning |
| `health_check_failure` | Health status | unhealthy | 2 min | critical |
| `storage_retention_limited` | Retention coverage | < 50% | 15 min | warning |
| `storage_writes_dropped` | Dropped writes | > 0 since the last check | immediate | warning |

Custom rules with the same name as a default will **override** the default.

**Alert Lifecycle:** `OK` -> `Pending` (condition met) -> `Firing` (duration exceeded) -> `Resolved` (condition cleared)

The two-phase transition (OK -> Pending -> Firing) prevents false alerts from transient spikes. Each incident is one alert record: resolving updates the firing record rather than adding a second. After a restart, alerts still open in storage are picked up again, so a rule that is still breaching doesn't page twice.

Notifications go through a bounded queue, and every channel has a timeout (email included). Delivery failures are counted as Pulse internal errors and logged without the webhook URL, since chat webhook URLs are credentials.

**Available Metrics for Rules:**
- `p95_latency` — P95 request latency in milliseconds
- `error_rate` — HTTP error rate percentage
- `heap_alloc_mb` — Heap allocation in megabytes
- `goroutine_growth` — Goroutine growth rate per hour
- `health_status` — Composite health check status (1 = healthy, 0 = unhealthy)
- `retention_coverage` — Fraction of `RetentionHours` held by the fullest storage buffer (0–1)
- `storage_dropped_writes` — Storage writes dropped since the previous evaluation

### Flame graph (profiling)

Pulse can sample live goroutine stacks on demand and return either an interactive **SVG flame graph** or the standard Brendan Gregg folded-stack text format. CPU profiles leak code structure, so this feature is **off by default** and **double-gated** — you must opt in *both* at the code level *and* at the environment level:

```go
pulse.Mount(ctx, router, db,
    pulse.WithProfiling(),       // gate 1
    // ...
)
```

```bash
export PULSE_PROFILE_ENABLED=true   # gate 2
```

Either gate alone returns `503` with a hint at the missing one. With both set:

```bash
# 10-second CPU profile, returned as SVG
curl -H "Authorization: Bearer $TOKEN" \
  "http://localhost:8080/pulse/api/profile/flamegraph?duration=10s" > flame.svg

# Goroutine stacks instead, including the ones parked in a read or a channel
curl -H "Authorization: Bearer $TOKEN" \
  "http://localhost:8080/pulse/api/profile/flamegraph?mode=goroutines&hz=100&parked=true" > goroutines.svg

# 5-second window, returned as folded text for diff-flame tooling
curl -H "Authorization: Bearer $TOKEN" \
  "http://localhost:8080/pulse/api/profile/folded?duration=5s" \
  | ./difffolded.pl - other.folded \
  | ./flamegraph.pl --colors=red > diff.svg
```

Only one sample window is active at a time. Concurrent requests get `409 Conflict` — running two profiles simultaneously doubles CPU cost and produces overlapping noise. Override the defaults in code via `Config.Profiling.DefaultDuration`, `Config.Profiling.MaxDuration`, `Config.Profiling.SampleHz` (goroutine mode only — the runtime picks the CPU sampling rate).

> **Why two modes?** A goroutine stack dump is cheap and answers "what is everything doing" — leaks, stuck workers, a pool that stopped draining. What it cannot do is attribute CPU, because a goroutine blocked in a syscall weighs exactly as much as one burning a core; that is what CPU mode is for. Neither answers what allocates or what contends, because neither is in a stack dump at all. For those, Pulse serves the runtime's own profiles.

#### The runtime's profiles, for `go tool pprof`

Behind the same two gates, Pulse serves the standard profiles as pprof protobuf, so existing tools read them as they are:

```bash
TOKEN=$(curl -s -XPOST -d '{"username":"admin","password":"..."}' localhost:8080/pulse/api/auth/login | jq -r .token)

go tool pprof "http://localhost:8080/pulse/api/profile/heap?token=$TOKEN"     # what is on the heap
go tool pprof "http://localhost:8080/pulse/api/profile/allocs?token=$TOKEN"   # everything allocated since start
go tool pprof "http://localhost:8080/pulse/api/profile/cpu?seconds=30&token=$TOKEN"
```

`go tool pprof` cannot set headers, so these endpoints also take the dashboard token as `?token=`. Capture two and compare them — `go tool pprof -diff_base before.pprof after.pprof` — which is the thing a heap profile is for.

**Mutex and block profiles are off until you ask**, because the runtime pays for them on every contended lock or blocking operation for as long as they are on. Rather than serve an empty profile that reads as "no contention", Pulse answers `409` and says how to turn it on:

```go
pulse.Mount(ctx, router, db,
    pulse.WithProfiling(),
    pulse.WithMutexProfiling(5),      // sample one in 5 contention events
    pulse.WithBlockProfiling(10_000), // sample one event per 10µs blocked
    pulse.WithPprofHandlers(),        // also mount /pulse/debug/pprof/*
)
```

`GET /pulse/api/profile/list` says which profiles this process is keeping and how many records each holds. `pulse.WithPprofHandlers()` additionally mounts the standard `net/http/pprof` handlers under `<prefix>/debug/pprof`, where muscle memory reaches for them — behind the same gates and the same authentication.

### k6 test-run overlay

Pulse exposes a `POST /pulse/api/test-runs` endpoint that any load-test harness can call at run start and end (posting the same `id` twice updates the run). Runs are listed on the dashboard's **Test Runs** page with their duration and metadata, and `GET /pulse/api/test-runs?range=…` returns them for your own tooling.

Runs appear as shaded, labelled bands on the Overview and Runtime charts. On the **Test Runs** page, **Compare** shows how production traffic behaved before, during and after a run: request rate, 5xx rate and p50/p95/p99, plus how long p95 took to recover (*"p95 went from 80ms to 1.1s, then recovered 2m after the run ended"*). The comparison counts every request at one-minute resolution. `GET /pulse/api/test-runs/:id/compare` returns it as JSON.

For k6 specifically, drop the bundled bridge into your test directory:

```js
import {
  pulseAuth, pulseStartRun, pulseEndRun,
} from './pulse-k6-bridge.js';

export function setup() {
  const token = pulseAuth(__ENV.PULSE_URL, __ENV.PULSE_USER, __ENV.PULSE_PASS);
  const run   = pulseStartRun(__ENV.PULSE_URL, token, {
    name: 'average-load',
    type: 'k6.average-load',
    metadata: { vus_peak: 50 },
  });
  return { token, run };
}

// ... your scenarios ...

export function teardown(data) {
  pulseEndRun(__ENV.PULSE_URL, data.token, data.run, {
    thresholds_passed: true,
  });
}
```

The bridge ([`examples/k6/pulse-k6-bridge.js`](examples/k6/pulse-k6-bridge.js)) and a runnable example ([`examples/k6/example-test.js`](examples/k6/example-test.js)) ship in the repo.

#### A flame graph of the run itself

k6 can tell you p95 went from 40ms to 310ms at 500 VUs. It cannot tell you *which function*, and neither can an external APM at the resolution that matters. Pulse is inside the process, so it can.

When a run is registered as **starting** (a `POST /test-runs` with no `ended_at`) and profiling is enabled, Pulse starts a CPU profile; when the run's end is registered, it stops and keeps it against the run:

```bash
# the flame graph of the run, in the browser
open "http://localhost:8080/pulse/api/test-runs/$RUN_ID/profile?format=svg"

# or the raw profile, for go tool pprof
curl -H "Authorization: Bearer $TOKEN"   "http://localhost:8080/pulse/api/test-runs/$RUN_ID/profile" > run.pprof
go tool pprof -list 'normalise' run.pprof
```

The Test Runs page shows it beside the comparison, so the screen reads *"p95 40ms → 310ms during the run, recovered after 95s — and here is where those 310ms went."*

Guardrails: it only happens when profiling is already enabled through [both of its gates](#flame-graph-profiling), only one profile runs at a time across the whole process (an on-demand flame graph and a run profile cannot fight over it), and it stops itself after `Profiling.MaxTestRunProfile` (default 5m) however long the run turns out to be. The response to the start call says whether a profile is being taken.

#### What the comparison says now

Alongside the global before/during/after, `GET /pulse/api/test-runs/:id/compare` carries:

- **`routes`** — the same windows per route, busiest first, with a `slowdown_ratio`. A load test usually hammers two or three endpoints, and a global average hides the one that fell over. Per-route latency is an average rather than a percentile, because per-route percentiles are not kept per minute and a mislabelled number is worse than an honest one.
- **`database`** — queries per request before against during, N+1 findings in each window, and slow queries during. An N+1 that is invisible at one request per second is exactly the thing that falls over at 500 VUs.
- **`profile`** — whether a CPU profile was captured, and how big it is.

### USE method (host U/S/E grid)

Pulse implements Brendan Gregg's [USE method](https://brendangregg.com/usemethod.html) — for every system resource, walk **U**tilization / **S**aturation / **E**rrors. A background sampler refreshes the grid every 5 s (2 s in DevMode), and `GET /pulse/api/use` returns the live snapshot:

| Resource | Utilization | Saturation | Errors |
|----------|-------------|------------|--------|
| **CPU** | % busy (1 s window) | load1 normalised by CPU count | CPU stalls (perf counters — usually `unknown`) |
| **Memory** | host memory % used | last GC pause | OOM events (tracked by the container runtime, not Pulse) |
| **Disk** | % full on the worst-mounted FS | max in-flight I/O ops | per-disk I/O errors (cross-platform: `unknown`) |
| **Network** | aggregate Mbps | drops/sec | cumulative interface errors |
| **DB pool** | `in-use / max` | total `WaitCount` since boot | (see Errors page) |
| **Goroutines** | live count | growth rate / hour | panics (see Errors page) |

Cells are colour-banded `green`/`amber`/`red`/`unknown` so the eye lands on the bottleneck. Host metrics come from [gopsutil](https://github.com/shirou/gopsutil/v4); on platforms where a metric isn't exposed (e.g. load average on Windows) the cell renders as `unknown` rather than failing.

Opt out with `pulse.WithUSEDisabled()` if you don't want Pulse touching gopsutil — handy in minimal-permission containers.

### N+1 ranked by impact

`GET /pulse/api/database/n1` returns the raw per-request detections. `GET /pulse/api/database/n1/ranked` groups them by `(route, normalised SQL)` and sorts by **impact score** — `occurrences × queries-per-occurrence × avg query duration` — so the entry at position 0 is the fix that buys you the most wall-clock back per hour spent.

Each ranked entry also carries a `suggested_fix` string with a one-sentence hint at the likely root cause (e.g. *"This looks like `.First(...)` in a loop. Use `.Preload(...)` on the outer query..."*). Pulse's matchers are intentionally conservative — when a SQL shape isn't recognised, a generic nudge is returned rather than a confident-but-wrong guess.

### SLOs and burn-rate alerts

Declare service-level objectives directly in your config. Pulse continuously evaluates them against live traffic, exposes the current state at `GET /pulse/api/slos`, and fires alerts through the same Slack/Discord/Email/Webhook channels you've already wired up.

```go
pulse.Mount(ctx, router, db,
    pulse.WithAppName("Checkout"),
    pulse.WithSlackAlerts(os.Getenv("SLACK_WEBHOOK_URL")),
    pulse.WithSLO(pulse.SLO{
        Name:      "API availability",
        Target:    0.999,                // 99.9% over 28 days
        Window:    28 * 24 * time.Hour,
        Indicator: pulse.SLIErrorRate{Routes: []string{"/api/*"}},
        // BurnRateAlerts: nil → DefaultBurnRateAlerts() — see below
    }),
    pulse.WithSLO(pulse.SLO{
        Name:      "API latency",
        Target:    0.95,                 // 95% under 500ms
        Window:    30 * 24 * time.Hour,
        Indicator: pulse.SLILatency{
            Routes:    []string{"/api/*"},
            Threshold: 500 * time.Millisecond,
        },
    }),
)
```

**Indicators:**

| SLI | "Good" event |
|-----|--------------|
| `SLIErrorRate{Routes}` | response with status code < 500 on a matching route (4xx counts as good — client errors don't burn server SLOs) |
| `SLILatency{Routes, Threshold}` | non-5xx response below `Threshold` on a matching route |

`Routes` uses the same glob style as `Tracing.ExcludePaths` (`/api/*` matches `/api/users/1` etc.). An empty `Routes` slice matches every request.

**Burn-rate alerts** (Google SRE workbook recommendation, applied by default):

| Name | Window | Confirmed over | Burn rate | Severity |
|------|--------|----------------|-----------|----------|
| `fast-burn` | 1h | 5m | 14.4× | critical |
| `slow-burn` | 6h | 30m | 6.0× | warning |

A burn rate of N× means the SLO is consuming its monthly error budget N× faster than allowed. Fast-burn fires when 2% of the monthly budget is spent in an hour; slow-burn fires when 5% is spent in 6 hours. Override per-SLO via `BurnRateAlerts: []pulse.BurnRateAlert{...}`.

An alert fires only when both its window and its shorter confirmation window burn faster than the threshold, and the window holds at least `MinEvents` events (default 20). So an alert resolves soon after a burn stops, and a handful of requests after a deploy or restart can't page. Set `ShortWindow` and `MinEvents` per alert to change this.

SLOs are computed from exact per-minute counts of every request, so sampling doesn't skew them. With the SQLite backend, those counts and any alerts still firing survive restarts. `data_coverage` in `/pulse/api/slos` (and a note on the SLOs page) says how much of the window Pulse actually has data for.

**Why burn rate, not raw error rate?** A 0.5% error rate is catastrophic for a 99.99% SLO (50× burn) and unremarkable for a 90% SLO (0.05× burn). Same number, totally different meaning. Burn rate normalises against the SLO you actually care about.

### Per-route baselines

Pulse can tell you a route is slow. Baselines are what let it tell you a route *got* slower.

Every hour, each route's traffic is summarised to a handful of numbers — count, errors, p50, p95, p99 — and those summaries are kept for 30 days, far longer than the request records they came from, because a regression is something you notice over weeks. A route's baseline is the **median of its hourly p95s** over the window: a median so one pathological hour doesn't become the yardstick, and hours too quiet to be representative are left out entirely.

The Routes page gets a **vs usual** column (+180% in red when a route is well off its baseline), `GET /pulse/api/routes/baselines` serves the comparison, and a route that stays above its baseline for an hour fires a `route_regression` alert through the same channels as everything else:

> `GET /checkout is 2.4× slower than usual: p95 240ms against a 100ms baseline over 142 hours, for the last 1h0m0s, which began 12m0s after a1b2c3d was deployed`

That last clause is the deploy markers doing their other job: when the slowdown began within a day of the running build's first start, the alert names the build, which turns "something got slower" into "this release got slower".

It is on by default and costs one row per route per hour. Tune it if you want to:

```go
Baselines: pulse.BaselineConfig{
    Enabled:         pulse.Bool(true),      // default: true
    Window:          7 * 24 * time.Hour,    // hours the baseline is drawn from (default: 7d)
    Retention:       30 * 24 * time.Hour,   // how long summaries are kept (default: 30d)
    MinRequests:     20,                    // an hour quieter than this isn't evidence (default: 20)
    MinHours:        6,                     // hours needed before a route has a baseline at all (default: 6)
    RegressionRatio: 1.5,                   // flag at 1.5× the baseline p95 (default: 1.5)
    For:             time.Hour,             // ...sustained this long before it pages (default: 1h)
},
```

Nothing is flagged before there is evidence for it: a route needs `MinHours` qualifying hours of history, the hour in progress needs `MinRequests` requests, and the slowdown has to last `For`. A deploy that is slow for ten minutes and recovers won't page; one that is slow for an hour will, once, and the alert closes itself when the route comes back. On shared storage the leader alerts, so a cluster pages once rather than once per instance.

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
| `pulse_storage_records` | gauge | kind | Records held per storage kind |
| `pulse_storage_effective_retention_seconds` | gauge | kind | How far back a full storage kind reaches |
| `pulse_storage_retention_coverage_ratio` | gauge | | Fraction of the configured retention the fullest kind holds |
| `pulse_storage_dropped_writes_total` | counter | | Writes dropped because the write queue was full |
| `pulse_internal_errors_total` | counter | component | Failures inside Pulse itself (storage, notifications, …) |
| `pulse_build_info` | gauge | version, instance_id, storage | Always 1; identifies the Pulse instance |

### Log capture

Send your application's logs through Pulse, and each error's detail shows the lines its request logged. The **Logs** page searches them and tails them live.

```go
p := pulse.Mount(ctx, router, db)

// log/slog: wrap the handler you already use.
slog.SetDefault(slog.New(p.SlogHandler(slog.NewJSONHandler(os.Stdout, nil))))

// The log package, zap, zerolog, or anything else that writes lines:
log.SetOutput(p.LogWriter(os.Stderr))
```

Lines logged with a request's context, `slog.InfoContext(c.Request.Context(), …)` or `slog.InfoContext(c, …)`, carry that request's trace and span IDs; that is how an error finds its lines. A line written to `LogWriter` is tied to a request when it has a `trace_id` field (log `pulse.TraceIDFromContext(ctx)`). JSON lines from zap and zerolog are parsed into level, message, time and attributes. When no line matches an error's request, its detail shows the lines logged within 30 seconds of it.

Your handler or writer still receives every line unchanged. What Pulse stores is redacted by the same rules as request bodies: values of sensitive attributes (`password`, `token`, …) and secret-looking values (card numbers, JWTs, keys) are replaced.

```go
pulse.WithLogMinLevel(slog.LevelDebug)                        // capture Debug too (default: Info)
pulse.WithMemoryCapacity(pulse.MemoryCapacity{Logs: 200_000}) // lines the Memory backend keeps (default 50,000)
```

With SQLite, logs are kept for `RetentionHours`, like everything else.

### OpenTelemetry and exporters

An `Exporter` receives everything Pulse records: every request (whatever `SampleRate` says), query, outbound call, error, captured log line and runtime sample.

The `otel` submodule turns them into OpenTelemetry traces and metrics, configured by the standard environment variables (`OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_HEADERS`, `OTEL_SERVICE_NAME`):

```go
import pulseotel "github.com/MUKE-coder/pulse/otel"

exp, err := pulseotel.NewOTLP(ctx, pulseotel.WithService("orders", "1.4.0"))
if err != nil {
    log.Fatal(err)
}
p := pulse.Mount(ctx, router, db, pulse.WithExporter(exp))
defer exp.Shutdown(context.Background())
```

Requests become server spans with their queries and outbound calls as children, keeping the trace and span IDs Pulse put in `traceparent` headers, so they join traces from upstream and downstream services. Alongside them go `http.server.request.duration`, `http.client.request.duration`, `db.client.operation.duration`, `go.memory.used` and `go.goroutine.count`. Pass `pulseotel.WithoutMetrics()` for spans only. The core module imports no OpenTelemetry packages: only applications that use the submodule pull them in. Records arrive in batches, already redacted, off the request path:

```go
type Exporter interface {
    Export(ctx context.Context, events []pulse.Event)
}

pulse.Mount(ctx, router, db, pulse.WithExporter(myExporter))
```

Each record carries `TraceID`, `SpanID` and `ParentSpanID`: the same IDs Pulse reads from and writes to `traceparent` headers. A request, its queries and its outbound calls form one span tree, joined to the caller's trace and to the downstream services'. If an exporter falls behind, events are dropped rather than slowing requests down, and counted in `pulse_internal_errors_total{component="export"}`.

For OpenTelemetry, the `pulseotel` module turns the records into spans with semantic-convention attributes:

```bash
go get github.com/MUKE-coder/pulse/otel
```

```go
import (
    pulseotel "github.com/MUKE-coder/pulse/otel"
    "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
    "go.opentelemetry.io/otel/sdk/resource"
    sdktrace "go.opentelemetry.io/otel/sdk/trace"
    semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

otlp, err := otlptracehttp.New(ctx) // honours OTEL_EXPORTER_OTLP_ENDPOINT
if err != nil {
    log.Fatal(err)
}
exp := pulseotel.New(
    sdktrace.WithBatcher(otlp),
    sdktrace.WithResource(resource.NewSchemaless(semconv.ServiceName("orders"))),
)
p := pulse.Mount(ctx, router, db, pulse.WithExporter(exp))

// On shutdown: Pulse first (it hands over its last events), then the exporter.
_ = p.Shutdown()
_ = exp.Shutdown(context.Background())
```

| Pulse record | Span | Key attributes |
|---|---|---|
| Request | server, `GET /orders/:id` | `http.request.method`, `http.route`, `http.response.status_code`; error status on 5xx |
| GORM query | client, `SELECT orders` | `db.query.text` (normalized, no literal values), `db.operation.name`, `db.collection.name`, `code.file.path` |
| Outbound call | client, `GET` | `url.full` (redacted), `server.address`, `service.peer.name`; error status on 4xx/5xx or no response |

`pulseotel` is a separate module, so the core module doesn't pull in the OpenTelemetry SDK. It needs Go 1.25.

---

## Dependency Monitoring

Track external HTTP API calls by wrapping your `http.Client`:

```go
p := pulse.Mount(ctx, router, db)

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

Connect to `ws://localhost:8080/pulse/ws/live?token=<jwt>` for real-time streaming. The WebSocket endpoint requires the same JWT that protects `/pulse/api`. Two ways to supply it:

- **Browsers / dashboards:** append `?token=<jwt>` to the URL — browsers cannot set `Authorization` on the upgrade request.
- **Non-browser clients:** send `Sec-WebSocket-Protocol: bearer, <jwt>` as the subprotocol header. Pulse will negotiate `bearer` back to you.

Unauthenticated upgrade attempts receive `401 Unauthorized` before the handshake completes.

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
| `GET` | `/pulse/api/routes/baselines` | — | Each route's usual p95 against the hour in progress |

### Database

| Method | Endpoint | Query Params | Description |
|--------|----------|--------------|-------------|
| `GET` | `/pulse/api/database/overview` | `?range=1h` | DB statistics summary |
| `GET` | `/pulse/api/database/slow-queries` | `?threshold=100ms&limit=50` | Slow queries |
| `GET` | `/pulse/api/database/patterns` | `?range=1h` | Aggregated query patterns |
| `GET` | `/pulse/api/database/n1` | `?range=1h` | N+1 query detections (raw, per-request) |
| `GET` | `/pulse/api/database/n1/ranked` | `?range=1h&limit=50` | N+1 grouped by (route, pattern), ranked by impact score |
| `GET` | `/pulse/api/database/pool` | | Connection pool stats |
| `GET` | `/pulse/api/database/plans` | `?limit=20` | Captured query plans, newest first |
| `POST` | `/pulse/api/database/explain` | — | Explain the slowest recorded `SELECT` now, or arm it for its next run |

### Errors

| Method | Endpoint | Query Params | Description |
|--------|----------|--------------|-------------|
| `GET` | `/pulse/api/errors` | `?type=database&route=/api&muted=false&resolved=false&limit=50&offset=0` | List errors |
| `GET` | `/pulse/api/errors/:id` | | Error details |
| `POST` | `/pulse/api/errors/:id/mute` | | Mute an error |
| `POST` | `/pulse/api/errors/:id/resolve` | | Resolve an error |
| `DELETE` | `/pulse/api/errors/:id` | | Delete an error |
| `GET` | `/pulse/api/errors/:id/logs` | | Log lines from the request behind the latest occurrence (`match: "trace"`), or from within 30s of it (`match: "time"`) |

### Logs

| Method | Endpoint | Query Params | Description |
|--------|----------|--------------|-------------|
| `GET` | `/pulse/api/logs` | `?range=1h&trace_id=…&level=warn&q=timeout&limit=200` | Captured log lines, oldest first. `level` is the lowest level shown; `q` searches messages. With `trace_id` and no `range`, the whole trace is returned whenever it happened |

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

### SLOs

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/pulse/api/slos` | Current SLOStatus per configured SLO, including per-window burn rate and budget consumed |

### USE Method (Utilization / Saturation / Errors)

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/pulse/api/use` | Live USE snapshot — CPU, Memory, Disk, Network, DB pool, Goroutines, each with U/S/E cells colour-banded as `green` / `amber` / `red` / `unknown` |

### Test runs (k6 / load-test overlay)

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/pulse/api/test-runs?range=…` | List test runs whose window overlaps the range |
| `POST` | `/pulse/api/test-runs` | Record a synthetic test run (k6 bridge POSTs here at run start + end) |
| `GET` | `/pulse/api/test-runs/:id/profile` | `?format=pprof\|svg\|json` — the CPU profile captured over the run |
| `GET` | `/pulse/api/test-runs/:id/compare` | Before / during / after comparison of production traffic, and time to recovery |

### Storage and lifecycle

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/pulse/api/storage` | Per-kind fill level, effective retention, dropped writes, database size |
| `GET` | `/pulse/api/lifecycle?range=…` | Process start/stop events (unclean restarts flagged), instance ID, and how far back data reaches |

### Profiling / flame graph

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/pulse/api/profile/flamegraph?duration=10s&hz=100` | Sample the runtime and return an SVG flame graph. `?format=json` returns the folded tree; `?cache=true` reuses a recent sample if available |
| `GET` | `/pulse/api/profile/folded` | Same window, but emits Brendan Gregg's folded-stack text format for piping into other tooling |
| `GET` | `/pulse/api/profile/heap` · `allocs` · `goroutine` · `mutex` · `block` · `threadcreate` | The runtime's own profiles as pprof protobuf, for `go tool pprof`. `?debug=1` returns text. Mutex and block answer `409` until turned on |
| `GET` | `/pulse/api/profile/cpu?seconds=30` | A CPU profile sampled over the window (default 10s, capped at 120s), one at a time |
| `GET` | `/pulse/api/profile/list` | Which profiles this process keeps, and how many records each holds |
| `GET` | `/pulse/debug/pprof/*` | The standard `net/http/pprof` handlers, when `pulse.WithPprofHandlers()` is set |

**Every endpoint here is double-gated** — they return `503` unless `pulse.WithProfiling()` is set AND `PULSE_PROFILE_ENABLED=true` is in the environment. See [Profiling section](#flame-graph-profiling).

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

Add `?end=<RFC3339>` to slide that window back in time — `?range=15m&end=2026-10-16T09:00:00Z` is the fifteen minutes before 09:00. The dashboard uses it to compare a window with the one before it, so a delta compares like with like.

### Authentication-free endpoints

| Method | Endpoint | Notes |
|--------|----------|-------|
| `POST` | `/pulse/api/auth/login` | Rate limited per IP |
| `GET` | `/pulse/health`, `/pulse/health/live`, `/pulse/health/ready` | Kubernetes probes |
| `GET` | `/pulse/api/public/status` | Only when `Dashboard.PublicStatus` is set; status, uptime and p95 for the login screen |

---

## Examples

### Basic Setup

The simplest way to get started — mount Pulse with defaults:

```go
package main

import (
    "context"
    "log"

    "github.com/MUKE-coder/pulse/pulse"
    "github.com/gin-gonic/gin"
    "github.com/glebarez/sqlite"
    "gorm.io/gorm"
)

func main() {
    db, _ := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
    router := gin.Default()

    pulse.Mount(context.Background(), router, db,
        pulse.WithAppName("Blog API"),
        pulse.WithDevMode(),
    )

    router.GET("/", func(c *gin.Context) {
        c.JSON(200, gin.H{"message": "Hello, World!"})
    })

    log.Fatal(router.Run(":8080"))
}
```

### Full Configuration

Complete configuration with all options:

```go
p := pulse.Mount(ctx, router, db, pulse.WithConfig(pulse.Config{
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
}))
```

### Custom Health Checks

Register checks for all your external dependencies:

```go
p := pulse.Mount(ctx, router, db)

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
pulse.Mount(ctx, router, db, pulse.WithConfig(pulse.Config{
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
}))
```

Webhooks include an `X-Pulse-Signature` header with HMAC-SHA256 for verification and automatically retry 3 times with exponential backoff.

### Monitoring External APIs

Track latency and availability of external services:

```go
p := pulse.Mount(ctx, router, db)

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
pulse.Mount(ctx, router, db,
    pulse.WithPrometheusPath("/metrics"), // or pulse.WithPrometheus() for the default "/pulse/metrics"
)
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
- **Ring Buffers** — `RingBuffer[T]` appends in O(1) into a fixed-size slice (an atomic head index plus a short write lock), so memory stays bounded no matter how much traffic arrives.
- **Non-blocking Storage** — Storing a metric is a ring-buffer push (Memory) or a queue append (SQLite, committed in batches by one writer), so the request path never waits on storage.
- **Exact Counts** — Per-minute rollups of every request back totals, error rates, SLOs and load-test comparisons, independent of sampling and buffer size.
- **Background Lifecycle** — All background goroutines are managed via `context.Context` + `sync.WaitGroup` for clean shutdown.
- **Pointer Config Fields** — `*bool` and `*float64` config fields distinguish between "not set" (use default) and "explicitly set to zero/false".

---

## Graceful Shutdown

Pulse manages background goroutines that should be stopped on shutdown:

```go
ctx, cancel := context.WithCancel(context.Background())
p := pulse.Mount(ctx, router, db /*, options... */)

// On shutdown, either cancel the context passed to Mount…
cancel()
// …or call Shutdown, which also waits for the goroutines to exit and closes storage.
p.Shutdown()
```

Either way, all background goroutines (runtime sampler, aggregator, health runner, alert engine, WebSocket hub) stop. `Shutdown` additionally waits for them to finish and closes the storage backend. With SQLite that commits the queued writes and the latest per-minute counts, and records a clean stop, so the next start isn't flagged as following a crash.

---

## Versioning & Roadmap

Pulse follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html). The current version is exposed as `pulse.Version`. Starting at **v1.0.0**, the public API surface defined in [STABILITY.md](STABILITY.md) is covered by semver guarantees — no breaking changes outside major releases, all removals preceded by at least one minor cycle of `// Deprecated:` notice.

See [CHANGELOG.md](CHANGELOG.md) for the full release history. The short version of the plan:

| Version | Theme |
|---------|-------|
| `v1.1.0` | Batched SQLite writes; exact per-minute counts; SLO fixes; configurable redaction; storage-capacity alerts; restart markers; load-test comparison; health-check isolation |
| `v1.2.0` | Shared storage (Postgres) with leader election; log capture and correlation; OpenTelemetry export |
| `v1.3.0` | Trace view; deploy markers; runtime profiles (heap/CPU/mutex/block); per-route baselines with regression alerts; query plans for slow queries |
| `v1.4.0` | Dashboard redesign: slate + teal palette, light and dark themes, new shell, rebuilt Database page and login |
| `v1.5.0` | Anomaly detection |

## License

MIT
