# Pulse — Real-Time Observability Dashboard for Go/Gin/GORM

## What Is Pulse?

Pulse is a **self-hosted observability and performance monitoring SDK** for Go applications built with **Gin** and **GORM**. It provides request tracing, database query analysis, runtime metrics, error tracking, dependency health checks, and a real-time dashboard — all mountable with a single function call.

```go
pulse.Mount(router, db, pulse.Config{})
// Dashboard → http://localhost:8080/pulse
```

Pulse is the **development and staging counterpart** to Sentinel. While Sentinel watches for threats, Pulse watches for performance issues, slow queries, memory leaks, error spikes, and unhealthy dependencies. Together, they give you complete visibility into your Go application.

## The Problem

Go developers using Gin + GORM lack lightweight, integrated observability tooling:

1. **No built-in performance visibility.** Gin logs requests to stdout, but there's no way to see latency percentiles, error rates by route, or slow endpoints at a glance. Developers resort to manually adding `time.Since()` calls or grepping logs.

2. **GORM query performance is a black box.** GORM's logger shows individual queries, but there's no aggregated view — no slow query log, no N+1 detection, no query frequency analysis. Developers discover slow queries in production after users complain.

3. **Runtime metrics require external tooling.** To see goroutine counts, memory usage, or GC pressure, developers need to set up Prometheus + Grafana, or pay for Datadog/New Relic. This is overkill for development and staging.

4. **Error tracking is scattered.** Errors happen in handlers, middleware, and GORM callbacks. There's no unified error dashboard that shows which routes are failing, how often, and what the errors are.

5. **Health checks are DIY.** Every team writes their own `/health` endpoint. There's no standard way to check database connectivity, Redis availability, external API health, or disk space — and no dashboard to see it all.

6. **Dependency monitoring doesn't exist.** When your app calls external APIs, there's no built-in way to track their latency, error rates, or availability. A slow downstream service silently degrades your app.

7. **Alerting requires infrastructure.** Setting up alerts for high error rates or slow queries means configuring PagerDuty, OpsGenie, or CloudWatch — infrastructure that development and staging environments don't warrant.

## The Solution

Pulse solves all of this with zero external dependencies:

- **Request Tracing Middleware** — automatically records latency, status codes, request/response sizes for every Gin request
- **GORM Query Plugin** — hooks into GORM's callback chain to track every database query with timing, rows affected, and caller location
- **Runtime Metrics Collector** — samples Go runtime stats (memory, goroutines, GC) at configurable intervals
- **Error Aggregator** — captures and groups errors by route, type, and frequency with full stack traces
- **Health Check Registry** — define checks for databases, Redis, external APIs, disk, and custom dependencies
- **Dependency Tracker** — wraps HTTP clients to monitor outbound request performance
- **Real-Time Dashboard** — embedded React dashboard with live WebSocket updates, interactive charts, and drill-down views
- **Smart Alerts** — configurable thresholds for latency, error rates, and health check failures with Slack, email, and webhook delivery

## Key Features

### Request Performance Monitoring
- **Per-route latency tracking** — p50, p75, p90, p95, p99 percentiles
- **Throughput metrics** — requests per second (RPS) per route and globally
- **Status code distribution** — 2xx, 3xx, 4xx, 5xx breakdown per route
- **Request/response size tracking** — detect payload bloat
- **Slow request log** — configurable threshold, captures full request context
- **Route comparison** — side-by-side performance comparison of any two routes
- **Latency heatmap** — visualize request latency distribution over time
- **Trend detection** — automatic detection of latency degradation over time

### Database Query Analysis (GORM Plugin)
- **Query timing** — duration of every query with percentile breakdown
- **Slow query log** — configurable threshold (default: 200ms), captures full SQL with params
- **N+1 query detection** — detects repeated similar queries within a single request
- **Query frequency analysis** — most called queries, total time spent per query pattern
- **Connection pool monitoring** — open connections, in-use, idle, wait count, wait duration
- **Query caller tracking** — which handler/function initiated each query (via runtime stack)
- **Table hotspot detection** — identify tables with the most reads/writes
- **Transaction monitoring** — track transaction duration, rollback rates
- **Migration tracking** — log schema migrations with duration

### Runtime Metrics
- **Memory** — heap alloc, heap in-use, heap objects, stack in-use, total alloc, sys memory
- **Goroutines** — count over time, detect goroutine leaks (sustained growth)
- **GC** — pause times, frequency, total pause, next GC target
- **CPU** — usage percentage (via runtime sampling)
- **File descriptors** — open FDs vs system limit
- **Uptime** — application start time, current uptime
- **Go version** — runtime version, GOMAXPROCS, number of CPUs

### Error Tracking
- **Error aggregation** — group identical errors, track frequency and first/last occurrence
- **Error rate by route** — which endpoints are failing and how often
- **Error timeline** — visualize error spikes over time
- **Stack trace capture** — full stack traces for panics and logged errors
- **Panic recovery** — middleware that catches panics, logs them, and returns 500
- **Error classification** — automatic categorization (validation, database, timeout, panic, custom)
- **Error context** — capture request headers, body snippet, query params for debugging
- **Muted errors** — silence known/expected errors from alerts

### Health Checks
- **Database health** — ping database, check connection pool, verify query execution
- **Redis health** — connectivity, latency, memory usage (if redis client provided)
- **External API health** — configurable HTTP checks with timeout and expected status
- **Disk health** — available space, inode usage
- **Custom checks** — register any `func() error` as a health check
- **Health history** — track check results over time, detect flapping
- **Composite status** — overall system health (healthy/degraded/unhealthy) based on all checks
- **Health endpoint** — `GET /pulse/health` returns structured health status (for load balancers/k8s)
- **Readiness vs liveness** — separate probes for Kubernetes deployment

### Dependency Monitoring
- **HTTP client wrapper** — `pulse.WrapHTTPClient(client)` to monitor outbound requests
- **Per-dependency metrics** — latency, error rate, throughput for each external service
- **Circuit breaker status** — if using circuit breakers, show open/closed/half-open state
- **Dependency map** — visual graph of your app's external dependencies and their health
- **SLA tracking** — track uptime percentage of each dependency over time

### Real-Time Dashboard
- **Live metrics via WebSocket** — no polling, instant updates
- **Interactive charts** — powered by Recharts (line, bar, area, heatmap)
- **Time range selector** — last 5m, 15m, 1h, 6h, 24h, 7d, custom range
- **Route drill-down** — click any route to see its detailed metrics, errors, queries
- **Dark/light mode** — theme toggle with persistence
- **Mobile responsive** — usable on phones for on-call debugging
- **Search and filter** — filter routes, errors, queries by keyword
- **Auto-refresh** — configurable refresh interval with pause/play
- **Shareable snapshots** — export current dashboard state as JSON for sharing

### Dashboard Pages
| Page | Description |
|------|-------------|
| **Overview** | System health score, key metrics, active alerts, quick stats |
| **Routes** | All routes with latency, throughput, error rate, sortable/filterable |
| **Route Detail** | Deep dive into a single route: latency chart, status codes, errors, queries |
| **Database** | Slow queries, query patterns, connection pool, N+1 alerts, table hotspots |
| **Errors** | Error groups, timeline, stack traces, mute/resolve actions |
| **Runtime** | Memory, goroutines, GC, CPU charts with anomaly highlights |
| **Health** | All health checks with status, history, and configuration |
| **Dependencies** | External service map, latency, availability, circuit breaker status |
| **Alerts** | Active and historical alerts, configuration, notification channels |
| **Settings** | Thresholds, retention, notification config, data export |

### Alerting
- **Latency alerts** — when p95 exceeds threshold for N minutes
- **Error rate alerts** — when error rate exceeds threshold
- **Health check alerts** — when a check fails N times consecutively
- **Goroutine leak alerts** — when goroutine count grows continuously
- **Memory alerts** — when heap usage exceeds threshold
- **Slow query alerts** — when queries consistently exceed threshold
- **Custom alerts** — define custom conditions via config
- **Notification channels** — Slack, email (SMTP), webhook, Discord
- **Alert cooldown** — don't re-fire the same alert within a configurable window
- **Alert history** — full log of all fired alerts with context

### Data Management
- **Configurable retention** — keep metrics for N hours/days (default: 24h for dev, 7d for staging)
- **Storage backends** — in-memory (default), SQLite (persistent), or your existing GORM database
- **Data aggregation** — automatically downsample old data (per-second → per-minute → per-hour)
- **Export** — download metrics as JSON or CSV for external analysis
- **Prometheus endpoint** — optional `/pulse/metrics` in Prometheus exposition format
- **Reset** — clear all collected data via API or dashboard button

### Developer Experience
- **One-line mount** — `pulse.Mount(router, db, pulse.Config{})`
- **Zero config** — works out of the box with sensible defaults
- **Minimal overhead** — async processing, ring buffers, sampling for high-traffic routes
- **No external dependencies** — no Prometheus, no Grafana, no Redis, no InfluxDB
- **No CGo** — pure Go SQLite via `modernc.org/sqlite`
- **Gin middleware** — standard `gin.HandlerFunc`, compatible with existing middleware chains
- **GORM plugin** — standard `gorm.Plugin` interface
- **Structured logging** — integrates with slog/zerolog if present

## Benefits

| Benefit | Description |
|---------|-------------|
| **Instant visibility** | See what's happening in your app in real time |
| **Zero infrastructure** | No Prometheus, Grafana, or cloud services needed |
| **Zero config** | One line to mount, works immediately |
| **Developer-first** | Built for dev/staging, not enterprise observability |
| **Catch issues early** | N+1 queries, goroutine leaks, slow endpoints — caught before production |
| **Debug faster** | Error context, stack traces, and query logs in one place |
| **Free forever** | Open source, self-hosted, no usage limits |

## Target Users

- **Go developers** building APIs with Gin + GORM who want instant observability
- **Small teams** that don't want to set up Prometheus + Grafana for development
- **Startups** that need monitoring but can't justify Datadog/New Relic costs for non-production
- **Open source projects** that want built-in diagnostics for contributors

## How It Differs from Existing Solutions

| Feature | Prometheus+Grafana | Datadog | Pulse |
|---------|-------------------|---------|-------|
| Setup complexity | High (multiple services) | Medium (agent + account) | **One line** |
| Cost | Free but complex | Expensive | **Free** |
| GORM integration | Manual instrumentation | Manual instrumentation | **Automatic** |
| Gin integration | Manual middleware | Auto (with agent) | **Automatic** |
| N+1 detection | No | No | **Yes** |
| Embedded in app | No | No | **Yes** |
| Real-time dashboard | Yes (with setup) | Yes | **Yes** |
| Health checks | External (Blackbox exporter) | Synthetic monitoring | **Built-in** |
| Slow query log | No | APM (paid) | **Yes** |
| Error tracking | No (need Sentry) | Yes (paid) | **Yes** |

## Ecosystem Fit

Pulse completes the MUKE-coder Go toolkit:

| Tool | Purpose | Mount Path |
|------|---------|------------|
| **GORM Studio** | Database browsing & editing | `/studio` |
| **Sentinel** | Security & threat detection | `/sentinel` |
| **Gin Docs** | API documentation | `/docs` |
| **Pulse** | Observability & performance | `/pulse` |

All four share the same `Mount(router, db, Config{})` pattern and can run side by side on the same Gin application.

## Repository Structure

```
pulse/
├── pulse/                     # Main package (users import this)
│   ├── mount.go               # Mount() entry point
│   ├── config.go              # Configuration types and defaults
│   ├── engine.go              # Pulse engine — orchestrates all components
│   ├── middleware.go           # Gin request tracing middleware
│   ├── gorm_plugin.go         # GORM query tracking plugin
│   ├── runtime.go             # Go runtime metrics sampler
│   ├── errors.go              # Error aggregator and classifier
│   ├── health.go              # Health check registry and runner
│   ├── dependencies.go        # HTTP client wrapper and dependency tracker
│   ├── alerts.go              # Alert engine with thresholds and notifications
│   ├── metrics.go             # Metric types, ring buffers, aggregation
│   ├── aggregator.go          # Periodic metric aggregation (percentiles, rollups)
│   ├── storage.go             # Storage interface
│   ├── storage_memory.go      # In-memory storage (default)
│   ├── storage_sqlite.go      # SQLite persistent storage
│   ├── api.go                 # REST API handlers for dashboard
│   ├── websocket.go           # WebSocket hub for live updates
│   ├── ui.go                  # Embedded React dashboard (go:embed)
│   ├── prometheus.go          # Optional Prometheus metrics endpoint
│   └── export.go              # JSON/CSV data export
├── examples/
│   ├── basic/main.go          # Minimal example
│   ├── full/main.go           # All features configured
│   └── with-sentinel/main.go  # Pulse + Sentinel together
├── main.go                    # Demo application
├── go.mod
├── go.sum
├── README.md
├── CONTRIBUTING.md
├── SECURITY.md
├── LICENSE (MIT)
└── Makefile
```

## License

MIT — consistent with GORM Studio, Sentinel, and Gin Docs.
