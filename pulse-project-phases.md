# Pulse — Project Phases & Tasks

## Phase Overview

| Phase | Name | Description | Estimated Tasks |
|-------|------|-------------|-----------------|
| 1 | Foundation & Project Setup | Module init, config types, mount skeleton, engine struct | 7 tasks |
| 2 | Metric Types & Storage Layer | Ring buffers, metric types, storage interface, memory store | 8 tasks |
| 3 | Request Tracing Middleware | Gin middleware for latency, status codes, throughput | 7 tasks |
| 4 | GORM Query Plugin | Database query tracking, slow query log, N+1 detection | 8 tasks |
| 5 | Runtime Metrics Collector | Memory, goroutines, GC, CPU sampling | 6 tasks |
| 6 | Error Tracking System | Error aggregation, classification, stack traces, panic recovery | 7 tasks |
| 7 | Aggregation Engine | Percentile calculation, time-series rollups, trend detection | 6 tasks |
| 8 | Health Check System | Check registry, runner, health endpoint, built-in checks | 7 tasks |
| 9 | REST API for Dashboard | All API endpoints that the dashboard consumes | 8 tasks |
| 10 | WebSocket Live Updates | Real-time push to dashboard clients | 5 tasks |
| 11 | React Dashboard — Core | Embedded React app, overview page, route list, charts | 8 tasks |
| 12 | React Dashboard — Detail Pages | Route detail, database, errors, runtime, health pages | 7 tasks |
| 13 | Alerting System | Threshold engine, notification channels, alert history | 7 tasks |
| 14 | Dependency Monitoring | HTTP client wrapper, per-dependency metrics, dependency map | 5 tasks |
| 15 | Advanced Storage & Export | SQLite backend, Prometheus endpoint, JSON/CSV export, retention | 6 tasks |
| 16 | Demo Application & Examples | Realistic demo app, basic/full/sentinel examples | 5 tasks |
| 17 | Testing, Documentation & Release | Tests, README, CI/CD, publish | 7 tasks |

---

## Phase 1: Foundation & Project Setup

**Goal:** Initialize the Go module, define all configuration types, and create the `Mount()` function skeleton.

### Tasks

#### 1.1 — Initialize Go Module
- Create `go.mod` with module path `github.com/MUKE-coder/pulse`
- Add dependencies: `github.com/gin-gonic/gin`, `gorm.io/gorm`, `github.com/gorilla/websocket`
- Create `pulse/` package directory
- Create `main.go` at root as demo entry point (placeholder)
- Create directory structure: `pulse/`, `examples/basic/`, `examples/full/`, `docs/`

#### 1.2 — Define Configuration Types (`pulse/config.go`)
- Define `Config` struct:
  ```
  Prefix              string           // URL prefix (default: "/pulse")
  AppName             string           // Application name for display
  DevMode             bool             // Enable live reload, verbose logging
  
  // Dashboard auth
  Dashboard           DashboardConfig  // Username, Password, SecretKey (JWT)
  
  // Storage
  Storage             StorageConfig    // Driver (Memory/SQLite), DSN, RetentionHours
  
  // Request tracing
  Tracing             TracingConfig    // Enabled, SlowRequestThreshold, SampleRate, ExcludePaths
  
  // Database monitoring
  Database            DatabaseConfig   // Enabled, SlowQueryThreshold, DetectN1, TrackCallers
  
  // Runtime metrics
  Runtime             RuntimeConfig    // Enabled, SampleInterval
  
  // Error tracking
  Errors              ErrorConfig      // Enabled, CaptureStackTrace, CaptureRequestBody, MaxBodySize
  
  // Health checks
  Health              HealthConfig     // Enabled, CheckInterval, Timeout
  
  // Alerting
  Alerts              AlertConfig      // Enabled, channels (Slack, Email, Webhook, Discord)
  
  // Prometheus
  Prometheus          PrometheusConfig // Enabled, Path
  ```
- Define sub-config structs for each section
- Define constants: `StorageMemory`, `StorageSQLite`
- Implement `applyDefaults()` with sensible development defaults

#### 1.3 — Define Core Metric Types (`pulse/metrics.go`)
- Define `RequestMetric` struct:
  - `Method`, `Path`, `StatusCode`, `Latency time.Duration`
  - `RequestSize`, `ResponseSize int64`
  - `ClientIP`, `UserAgent string`
  - `Error string` (if any)
  - `Timestamp time.Time`
  - `TraceID string`
- Define `QueryMetric` struct:
  - `SQL string`, `Duration time.Duration`
  - `RowsAffected int64`, `Error string`
  - `Operation string` (SELECT/INSERT/UPDATE/DELETE)
  - `Table string`, `CallerFile string`, `CallerLine int`
  - `RequestTraceID string` (links to request)
  - `Timestamp time.Time`
- Define `RuntimeMetric` struct:
  - `HeapAlloc`, `HeapInUse`, `HeapObjects`, `StackInUse`, `TotalAlloc`, `Sys uint64`
  - `NumGoroutine int`
  - `GCPauseNs uint64`, `NumGC uint32`, `GCCPUFraction float64`
  - `Timestamp time.Time`
- Define `ErrorRecord` struct:
  - `ID string`, `Route string`, `Method string`
  - `ErrorMessage string`, `ErrorType string` (validation/database/timeout/panic/custom)
  - `StackTrace string`
  - `RequestContext *RequestContext` (headers, body snippet, query params)
  - `Count int`, `FirstSeen`, `LastSeen time.Time`
  - `Muted bool`, `Resolved bool`
- Define `HealthCheckResult` struct:
  - `Name string`, `Status string` (healthy/unhealthy/degraded)
  - `Latency time.Duration`, `Error string`
  - `Timestamp time.Time`, `Metadata map[string]interface{}`

#### 1.4 — Create Mount Function Skeleton (`pulse/mount.go`)
- Implement `Mount(router *gin.Engine, db *gorm.DB, configs ...Config) *Pulse`
- Accept variadic config (use first or default)
- Apply config defaults
- Initialize the Pulse engine
- Register Gin middleware on the router (request tracing)
- Register GORM plugin on db (if db is not nil)
- Start background goroutines (runtime sampler, health checker, aggregator)
- Register dashboard routes:
  - `GET /pulse` — dashboard UI
  - `GET /pulse/api/*` — REST API
  - `GET /pulse/ws/*` — WebSocket endpoints
  - `GET /pulse/health` — public health check endpoint
  - `GET /pulse/metrics` — optional Prometheus endpoint
- Return `*Pulse` instance for programmatic access

#### 1.5 — Create Engine Struct (`pulse/engine.go`)
- Define `Pulse` struct:
  - `config Config`
  - `router *gin.Engine`
  - `db *gorm.DB`
  - `storage Storage` (interface)
  - `aggregator *Aggregator`
  - `alertEngine *AlertEngine`
  - `healthRunner *HealthRunner`
  - `wsHub *WebSocketHub`
  - `startTime time.Time`
  - `mu sync.RWMutex`
- Implement lifecycle methods:
  - `Start()` — start background goroutines
  - `Stop()` — graceful shutdown
  - `RecordRequest(m RequestMetric)` — ingest request metric
  - `RecordQuery(m QueryMetric)` — ingest query metric
  - `RecordError(e ErrorRecord)` — ingest error
- Implement public API methods:
  - `AddHealthCheck(name string, fn func() error)`
  - `WrapHTTPClient(client *http.Client, name string) *http.Client`
  - `RegisterAlert(alert AlertRule)`

#### 1.6 — Create Makefile & CI Skeleton
- Makefile targets: `build`, `run`, `test`, `lint`, `tidy`, `demo`
- `.github/workflows/ci.yml` placeholder
- `.gitignore` (binaries, sqlite files, .env)
- `LICENSE` (MIT)

#### 1.7 — Verify Foundation
- `go mod tidy` succeeds
- `go run main.go` starts server
- `/pulse` returns placeholder HTML
- `/pulse/health` returns `{"status": "healthy"}`
- Write test: `TestMountRegistersRoutes`

---

## Phase 2: Metric Types & Storage Layer

**Goal:** Build the storage abstraction, ring buffer for high-throughput ingestion, and in-memory storage implementation.

### Tasks

#### 2.1 — Define Storage Interface (`pulse/storage.go`)
- Define `Storage` interface:
  ```go
  type Storage interface {
      // Request metrics
      StoreRequest(m RequestMetric) error
      GetRequests(filter RequestFilter) ([]RequestMetric, error)
      GetRouteStats(timeRange TimeRange) ([]RouteStats, error)
      GetRouteDetail(method, path string, timeRange TimeRange) (*RouteDetail, error)
      
      // Query metrics
      StoreQuery(m QueryMetric) error
      GetQueries(filter QueryFilter) ([]QueryMetric, error)
      GetSlowQueries(threshold time.Duration, limit int) ([]QueryMetric, error)
      GetQueryPatterns(timeRange TimeRange) ([]QueryPattern, error)
      
      // Runtime metrics
      StoreRuntime(m RuntimeMetric) error
      GetRuntimeHistory(timeRange TimeRange, resolution time.Duration) ([]RuntimeMetric, error)
      
      // Errors
      StoreError(e ErrorRecord) error
      GetErrors(filter ErrorFilter) ([]ErrorRecord, error)
      GetErrorGroups(timeRange TimeRange) ([]ErrorGroup, error)
      UpdateError(id string, updates map[string]interface{}) error
      
      // Health checks
      StoreHealthResult(r HealthCheckResult) error
      GetHealthHistory(name string, limit int) ([]HealthCheckResult, error)
      
      // Alerts
      StoreAlert(a AlertRecord) error
      GetAlerts(filter AlertFilter) ([]AlertRecord, error)
      
      // Aggregated stats
      GetOverview(timeRange TimeRange) (*Overview, error)
      
      // Maintenance
      Cleanup(retention time.Duration) error
      Reset() error
      Close() error
  }
  ```
- Define all filter and result types: `RequestFilter`, `QueryFilter`, `ErrorFilter`, `AlertFilter`, `TimeRange`
- Define aggregate types: `RouteStats`, `RouteDetail`, `QueryPattern`, `ErrorGroup`, `Overview`

#### 2.2 — Define Aggregate Types
- `RouteStats`:
  - `Method`, `Path string`
  - `TotalRequests int64`, `ErrorCount int64`, `ErrorRate float64`
  - `AvgLatency`, `P50`, `P75`, `P90`, `P95`, `P99 time.Duration`
  - `MinLatency`, `MaxLatency time.Duration`
  - `AvgRequestSize`, `AvgResponseSize int64`
  - `StatusCodes map[int]int64` (200→count, 404→count, etc.)
  - `RPM float64` (requests per minute)
- `RouteDetail`:
  - All of `RouteStats` plus:
  - `LatencyTimeline []TimePoint` (for charting)
  - `StatusTimeline []TimePoint`
  - `RecentErrors []ErrorRecord`
  - `TopQueries []QueryPattern`
- `QueryPattern`:
  - `Pattern string` (normalized SQL), `Operation string`, `Table string`
  - `TotalCalls int64`, `TotalDuration time.Duration`
  - `AvgDuration`, `P95Duration time.Duration`
  - `AvgRowsAffected float64`
- `ErrorGroup`:
  - `ErrorMessage string`, `ErrorType string`
  - `Route string`, `Method string`
  - `Count int64`, `FirstSeen`, `LastSeen time.Time`
  - `Resolved bool`, `Muted bool`
- `Overview`:
  - `TotalRequests`, `TotalErrors int64`
  - `AvgLatency`, `P95Latency time.Duration`
  - `ErrorRate float64`, `RPM float64`
  - `ActiveGoroutines int`, `HeapUsageMB float64`
  - `HealthStatus string` (healthy/degraded/unhealthy)
  - `ActiveAlerts int`
  - `TopRoutes []RouteStats` (top 5 by request count)
  - `RecentErrors []ErrorRecord` (last 10)
  - `SlowQueries []QueryMetric` (last 5)

#### 2.3 — Implement Ring Buffer (`pulse/ringbuffer.go`)
- Generic ring buffer `RingBuffer[T any]` with fixed capacity
- Lock-free for single writer (use atomic operations where possible)
- Methods: `Push(item T)`, `GetAll() []T`, `GetLast(n int) []T`, `Len() int`, `Reset()`
- Used for high-throughput metric ingestion (requests, queries)
- Default capacity: 100,000 entries for requests, 50,000 for queries

#### 2.4 — Implement In-Memory Storage (`pulse/storage_memory.go`)
- Implement `Storage` interface backed by ring buffers and maps
- Use `sync.RWMutex` for concurrent access
- Store requests and queries in ring buffers
- Store errors in a map keyed by error fingerprint (for deduplication)
- Store runtime metrics in a time-series slice
- Implement all query methods with in-memory filtering and aggregation
- Implement `Cleanup()` to remove entries older than retention period
- Run cleanup on a periodic timer

#### 2.5 — Implement Percentile Calculator
- Implement T-Digest or simple sorted-slice percentile calculation
- Support: p50, p75, p90, p95, p99
- Operate on `[]time.Duration` inputs
- Cache results with TTL to avoid recalculating on every API call
- Used by `GetRouteStats()` and `GetRouteDetail()`

#### 2.6 — Implement SQL Normalizer
- Normalize SQL queries for pattern grouping:
  - Replace literal values with `?` placeholders
  - Remove extra whitespace
  - Lowercase keywords
  - `SELECT * FROM users WHERE id = 42` → `select * from users where id = ?`
- Extract operation (SELECT/INSERT/UPDATE/DELETE) and table name
- Used for `QueryPattern` aggregation

#### 2.7 — Implement Time Range Helpers
- `TimeRange` struct with `Start`, `End time.Time`
- Predefined ranges: `Last5m`, `Last15m`, `Last1h`, `Last6h`, `Last24h`, `Last7d`
- Parse from query param: `?range=1h` or `?start=...&end=...`
- Resolution helper: determine appropriate data point interval for a given range
  - 5m → 5s intervals
  - 1h → 30s intervals
  - 24h → 5m intervals
  - 7d → 1h intervals

#### 2.8 — Write Storage Tests
- Test ring buffer push/get/overflow
- Test in-memory storage CRUD operations
- Test percentile calculation accuracy
- Test SQL normalizer with various query formats
- Test time range filtering
- Test cleanup/retention
- Benchmark ring buffer throughput

---

## Phase 3: Request Tracing Middleware

**Goal:** Build the Gin middleware that captures request/response metrics for every HTTP request.

### Tasks

#### 3.1 — Implement Tracing Middleware (`pulse/middleware.go`)
- Create `gin.HandlerFunc` that wraps request handling
- Capture before request: `startTime`, `method`, `path`, `clientIP`, `userAgent`, `requestSize`
- Capture after request: `statusCode`, `responseSize`, `duration`, `error`
- Normalize path: replace path parameters with placeholders (`:id` stays as `:id`)
- Handle Gin's `c.FullPath()` for the route pattern (vs actual path with values)
- Create `RequestMetric` and send to engine via channel (non-blocking)

#### 3.2 — Implement Response Writer Wrapper
- Wrap `gin.ResponseWriter` to capture:
  - Status code (intercept `WriteHeader()`)
  - Response body size (count bytes in `Write()`)
  - First byte time (TTFB)
- Ensure no overhead on streaming responses
- Pass through all other `http.ResponseWriter` methods

#### 3.3 — Implement Trace ID Generation
- Generate unique trace ID for each request
- Format: `pulse-{timestamp}-{random}` or UUID v4
- Attach to Gin context: `c.Set("pulse_trace_id", traceID)`
- Add to response header: `X-Pulse-Trace-ID`
- Used to correlate requests with their DB queries and errors

#### 3.4 — Implement Path Exclusion
- Check request path against `Config.Tracing.ExcludePaths`
- Always exclude: `/pulse/*` (dashboard routes), `/favicon.ico`
- Support glob patterns: `/health*`, `/debug/*`
- Skip metric recording for excluded paths (middleware still runs)

#### 3.5 — Implement Request Sampling
- When `Config.Tracing.SampleRate` < 1.0:
  - Only record a percentage of requests (for high-traffic routes)
  - Always record errors regardless of sample rate
  - Always record slow requests regardless of sample rate
- Use consistent hashing so the same client sees consistent behavior

#### 3.6 — Implement Slow Request Detection
- If request duration exceeds `Config.Tracing.SlowRequestThreshold` (default: 1s):
  - Capture additional context: request headers, query params, body snippet
  - Mark as slow in the metric
  - Emit to slow request log
  - Trigger alert if configured

#### 3.7 — Write Middleware Tests
- Test basic metric capture (method, path, status, latency)
- Test response writer wrapper (status code, body size)
- Test trace ID generation and header
- Test path exclusion
- Test sampling behavior
- Test slow request detection
- Benchmark middleware overhead (should be <100μs per request)

---

## Phase 4: GORM Query Plugin

**Goal:** Build a GORM plugin that tracks every database query with timing, rows affected, caller info, and N+1 detection.

### Tasks

#### 4.1 — Implement GORM Plugin Interface (`pulse/gorm_plugin.go`)
- Implement `gorm.Plugin` interface:
  ```go
  type GormPlugin struct { engine *Pulse }
  func (p *GormPlugin) Name() string { return "pulse" }
  func (p *GormPlugin) Initialize(db *gorm.DB) error { ... }
  ```
- Register callbacks on all GORM operations:
  - `db.Callback().Create().Before("pulse:before_create")`
  - `db.Callback().Create().After("pulse:after_create")`
  - Same for Query, Update, Delete, Raw, Row
- Store start time in callback context before operation
- Calculate duration and record metric in after callback

#### 4.2 — Capture Query Details
- In the `After` callback:
  - Extract SQL statement: `db.Statement.SQL.String()`
  - Extract vars: `db.Statement.Vars`
  - Extract rows affected: `db.RowsAffected`
  - Extract error: `db.Error`
  - Calculate duration from stored start time
  - Extract table name: `db.Statement.Table`
  - Determine operation type from callback name
- Build `QueryMetric` and send to engine

#### 4.3 — Implement Caller Tracking
- When `Config.Database.TrackCallers` is true:
  - Walk the runtime call stack to find the application-level caller
  - Skip GORM internals and Pulse internals
  - Capture file name and line number
  - Store in `QueryMetric.CallerFile` and `CallerLine`
- This has overhead, so make it configurable (default: true in dev, false in prod)

#### 4.4 — Implement N+1 Query Detection
- Track queries per request using the trace ID from Gin context
- When `Config.Database.DetectN1` is true:
  - Group queries by normalized SQL pattern within a single request
  - If the same pattern appears N+ times (configurable, default: 5), flag as N+1
  - Record: pattern, count, total duration, request that triggered it
- Store N+1 detections separately for the dashboard
- Emit alert if configured

#### 4.5 — Implement Connection Pool Monitoring
- Periodically sample `db.DB().Stats()`:
  - `MaxOpenConnections`
  - `OpenConnections`
  - `InUse`
  - `Idle`
  - `WaitCount`
  - `WaitDuration`
  - `MaxIdleClosed`
  - `MaxIdleTimeClosed`
  - `MaxLifetimeClosed`
- Store as a time series for charting
- Alert when pool is near exhaustion

#### 4.6 — Link Queries to Requests
- Use the Gin context's trace ID to correlate queries with requests
- Pass trace ID through GORM's context: `db.WithContext(ctx)`
- In the GORM callback, extract trace ID from context
- This enables the "queries for this request" view in route detail

#### 4.7 — Implement Slow Query Log
- If query duration exceeds `Config.Database.SlowQueryThreshold` (default: 200ms):
  - Capture full SQL with parameters
  - Capture caller info
  - Capture request trace ID
  - Mark as slow
- Serve slow queries as a dedicated dashboard view
- Include explain plan hint (suggest EXPLAIN ANALYZE for Postgres)

#### 4.8 — Write GORM Plugin Tests
- Test metric capture for all operations (Create, Query, Update, Delete)
- Test caller tracking accuracy
- Test N+1 detection with simulated scenarios
- Test connection pool monitoring
- Test query-request correlation via trace ID
- Test slow query detection
- Benchmark plugin overhead

---

## Phase 5: Runtime Metrics Collector

**Goal:** Build a background goroutine that samples Go runtime stats at configurable intervals.

### Tasks

#### 5.1 — Implement Runtime Sampler (`pulse/runtime.go`)
- Start a background goroutine in `Mount()`
- Sample at `Config.Runtime.SampleInterval` (default: 5s)
- Collect via `runtime.ReadMemStats()`:
  - `Alloc`, `TotalAlloc`, `Sys`, `HeapAlloc`, `HeapInuse`, `HeapIdle`, `HeapReleased`
  - `HeapObjects`, `StackInuse`, `StackSys`
  - `GCSys`, `NextGC`, `LastGC`
  - `PauseTotalNs`, `NumGC`, `GCCPUFraction`
- Collect `runtime.NumGoroutine()`
- Collect `runtime.NumCPU()`, `runtime.GOMAXPROCS(0)`
- Build `RuntimeMetric` and store

#### 5.2 — Implement CPU Usage Estimation
- Sample `runtime.ReadMemStats()` at two points with a small interval
- Calculate CPU usage from GC CPU fraction and total CPU time
- Alternative: use `/proc/self/stat` on Linux for more accurate process CPU
- Fall back to GC-based estimation on non-Linux

#### 5.3 — Implement Goroutine Leak Detection
- Track goroutine count over time
- If goroutine count increases by more than N% over M minutes with no corresponding decrease:
  - Flag as potential goroutine leak
  - Record the trend data
  - Trigger alert if configured
- Configurable thresholds: `Config.Runtime.LeakThreshold` (default: 100 goroutines/hour sustained)

#### 5.4 — Implement File Descriptor Monitoring
- On Linux: read `/proc/self/fd` to count open FDs
- Read `/proc/self/limits` for the max FD limit
- On other OS: use `syscall` to get rlimit
- Track FD count over time
- Alert when approaching limit (80%)

#### 5.5 — Implement Build/System Info Collection
- Collect once at startup:
  - `runtime.Version()` — Go version
  - `runtime.GOOS`, `runtime.GOARCH`
  - `runtime.NumCPU()`
  - `os.Hostname()`
  - Build info via `debug.ReadBuildInfo()` — module path, dependencies
  - Start time, PID
- Serve as static info in the dashboard

#### 5.6 — Write Runtime Collector Tests
- Test metric sampling produces valid values
- Test goroutine leak detection with simulated leak
- Test FD monitoring (on Linux)
- Test build info collection
- Test sampling interval configuration
- Test graceful shutdown of sampler goroutine

---

## Phase 6: Error Tracking System

**Goal:** Build an error aggregator that captures, classifies, deduplicates, and manages errors.

### Tasks

#### 6.1 — Implement Panic Recovery Middleware (`pulse/errors.go`)
- Add `gin.HandlerFunc` that wraps handler in `defer/recover`
- On panic:
  - Capture the panic value and stack trace
  - Create `ErrorRecord` with type "panic"
  - Respond with 500 status
  - Do NOT stop the server — recover gracefully
- Integrate with request tracing (same trace ID)

#### 6.2 — Implement Error Capture from Gin Context
- After each request, check for errors via `c.Errors`
- Check for 4xx/5xx status codes
- Create `ErrorRecord` for each error
- Classify error type automatically:
  - Status 400 + binding error → "validation"
  - Status 401/403 → "auth"
  - Status 404 → "not_found"
  - Status 408/504 → "timeout"
  - Status 500 + GORM error → "database"
  - Status 500 + panic → "panic"
  - Otherwise → "internal"

#### 6.3 — Implement Error Deduplication
- Generate fingerprint from: error message + route + method
- If fingerprint exists in storage:
  - Increment count
  - Update `LastSeen`
- If new:
  - Create new `ErrorRecord` with count=1
- This prevents 10,000 identical errors from flooding the dashboard

#### 6.4 — Implement Stack Trace Capture
- When `Config.Errors.CaptureStackTrace` is true:
  - Use `runtime.Stack()` to capture full stack
  - Clean up stack trace: remove internal Gin/GORM frames
  - Highlight application frames
  - Truncate very long traces
- For panics: always capture stack trace

#### 6.5 — Implement Request Context Capture
- When `Config.Errors.CaptureRequestBody` is true:
  - Capture request headers (redact `Authorization`, `Cookie`)
  - Capture query parameters
  - Capture request body up to `Config.Errors.MaxBodySize` (default: 4KB)
  - Store as `RequestContext` attached to `ErrorRecord`
- Useful for reproducing errors

#### 6.6 — Implement Error Management Actions
- **Mute** — stop alerting on this error (still track it)
- **Resolve** — mark as resolved (re-open if it recurs)
- **Delete** — remove from tracking
- Expose via API: `POST /pulse/api/errors/:id/mute`, `POST /pulse/api/errors/:id/resolve`

#### 6.7 — Write Error Tracking Tests
- Test panic recovery (handler panics → 500 returned, error recorded)
- Test error capture from Gin errors
- Test error classification
- Test deduplication (same error counted, not duplicated)
- Test stack trace capture and cleaning
- Test request context capture with redaction
- Test mute/resolve actions

---

## Phase 7: Aggregation Engine

**Goal:** Build the engine that periodically computes percentiles, rollups, and trend analysis from raw metrics.

### Tasks

#### 7.1 — Implement Aggregation Loop (`pulse/aggregator.go`)
- Background goroutine that runs on a configurable interval (default: 10s)
- On each tick:
  - Calculate per-route stats (latency percentiles, error rates, RPS)
  - Calculate global stats (total requests, overall error rate, overall latency)
  - Calculate database stats (slow queries, top patterns, pool utilization)
  - Update the overview snapshot
- Use `sync.RWMutex` for thread-safe access to aggregated data
- Cache results with TTL

#### 7.2 — Implement Per-Route Aggregation
- For each unique `Method + Path`:
  - Collect all request metrics in the time range
  - Calculate: count, error count, error rate
  - Calculate latency: avg, min, max, p50, p75, p90, p95, p99
  - Calculate throughput: RPM (requests per minute)
  - Calculate average request/response sizes
  - Build status code histogram
- Store as `RouteStats`

#### 7.3 — Implement Time-Series Rollup
- Downsample raw metrics into time buckets:
  - Last 5m → 5-second buckets
  - Last 1h → 30-second buckets
  - Last 24h → 5-minute buckets
  - Last 7d → 1-hour buckets
- Each bucket contains: request count, error count, avg latency, p95 latency
- Used for charting in the dashboard

#### 7.4 — Implement Trend Detection
- Compare current window (last 5m) against previous window (5-10m ago):
  - If p95 latency increased by >50% → "latency degradation"
  - If error rate increased by >100% → "error spike"
  - If RPS dropped by >50% → "traffic drop"
- Mark trends in route stats: `Trend: "degrading"` / `"improving"` / `"stable"`
- Used for visual indicators in the dashboard

#### 7.5 — Implement Overview Snapshot
- Compute a full `Overview` struct on each aggregation tick
- Include: total requests, total errors, error rate, avg latency, p95 latency
- Include: active goroutines, heap usage, health status, active alerts
- Include: top 5 slowest routes, top 5 highest error routes
- Include: last 10 errors, last 5 slow queries
- This powers the dashboard overview page with a single API call

#### 7.6 — Write Aggregation Tests
- Test per-route aggregation with known data
- Test percentile accuracy
- Test time-series rollup at different resolutions
- Test trend detection (inject degrading data, verify detection)
- Test overview snapshot completeness
- Benchmark aggregation performance with 100K metrics

---

## Phase 8: Health Check System

**Goal:** Build a health check registry with built-in checks, periodic runner, and structured health endpoint.

### Tasks

#### 8.1 — Implement Health Check Registry (`pulse/health.go`)
- Define `HealthCheck` struct:
  - `Name string`, `Type string` (database/redis/http/disk/custom)
  - `CheckFunc func(ctx context.Context) error`
  - `Interval time.Duration` (per-check interval, default: global interval)
  - `Timeout time.Duration`
  - `Critical bool` (if true, failure → system "unhealthy")
- Implement `AddHealthCheck(check HealthCheck)` on `Pulse`
- Support fluent registration:
  ```go
  p.AddHealthCheck(pulse.HealthCheck{
      Name: "postgres",
      Type: "database",
      CheckFunc: func(ctx context.Context) error { return db.WithContext(ctx).Exec("SELECT 1").Error },
      Critical: true,
  })
  ```

#### 8.2 — Implement Built-In Checks
- **Database check** — auto-registered if `db` is provided to `Mount()`
  - Ping database
  - Check connection pool stats (warn if >80% in use)
  - Execute simple query (`SELECT 1`)
- **Disk check** — check available disk space on `/` or configured paths
  - Warn if <10% free, unhealthy if <5%
- **Memory check** — compare heap alloc against system memory
  - Warn if >80% of system memory
- Register these automatically unless disabled

#### 8.3 — Implement Health Check Runner
- Background goroutine that runs at `Config.Health.CheckInterval` (default: 30s)
- For each registered check:
  - Run with timeout context
  - Record result (healthy/unhealthy, latency, error)
  - Store in health history
  - Detect flapping (alternating healthy/unhealthy)
- Calculate composite status:
  - All checks healthy → "healthy"
  - Any critical check unhealthy → "unhealthy"
  - Any non-critical check unhealthy → "degraded"

#### 8.4 — Implement Health API Endpoint
- `GET /pulse/health` — public endpoint (no auth required)
- Response:
  ```json
  {
    "status": "healthy",
    "timestamp": "2025-...",
    "uptime": "2h35m",
    "checks": {
      "postgres": {"status": "healthy", "latency_ms": 2},
      "disk": {"status": "healthy", "details": {"free_gb": 45.2}},
      "redis": {"status": "unhealthy", "error": "connection refused"}
    }
  }
  ```
- Support `?verbose=true` for detailed output
- Support `?check=postgres` to run a single check on demand
- Return appropriate HTTP status: 200 (healthy), 503 (unhealthy), 207 (degraded)

#### 8.5 — Implement Kubernetes Probes
- `GET /pulse/health/live` — liveness probe (always 200 unless application is hung)
- `GET /pulse/health/ready` — readiness probe (503 if critical checks fail)
- These are separate from the full health endpoint
- Minimal overhead, no verbose output

#### 8.6 — Implement Health History
- Store last N results for each check (default: 100)
- Track uptime percentage per check over time
- Detect flapping: if a check alternates status >3 times in 10 minutes → mark as flapping
- Serve history via API for charting in dashboard

#### 8.7 — Write Health Check Tests
- Test check registration and execution
- Test built-in database check (with mock db)
- Test composite status calculation
- Test health API endpoint responses
- Test Kubernetes probe behavior
- Test flapping detection
- Test timeout handling (slow check)

---

## Phase 9: REST API for Dashboard

**Goal:** Build all REST API endpoints that the React dashboard will consume.

### Tasks

#### 9.1 — Implement Authentication (`pulse/api.go`)
- JWT-based auth for dashboard API (same pattern as Sentinel)
- `POST /pulse/api/auth/login` — authenticate with username/password, return JWT
- `POST /pulse/api/auth/logout` — invalidate token
- `GET /pulse/api/auth/verify` — verify token validity
- Middleware that protects all `/pulse/api/*` routes except health
- Default credentials: admin/pulse (configurable)

#### 9.2 — Implement Overview API
- `GET /pulse/api/overview` — returns the `Overview` snapshot
- Query params: `?range=1h` for time range
- Returns: total requests, errors, latency, runtime stats, top routes, recent errors

#### 9.3 — Implement Routes API
- `GET /pulse/api/routes` — list all routes with stats
  - Query params: `?range=1h`, `?sort=p95_latency`, `?order=desc`, `?search=user`
  - Returns: array of `RouteStats` with percentiles, error rates, throughput
- `GET /pulse/api/routes/:method/:path` — detailed stats for one route
  - Returns: `RouteDetail` with time-series data, status distribution, related errors, top queries
- `GET /pulse/api/routes/compare?routes=GET:/users,POST:/users` — side-by-side comparison

#### 9.4 — Implement Database API
- `GET /pulse/api/database/overview` — connection pool stats, total queries, slow query count
- `GET /pulse/api/database/slow-queries` — slow query log with pagination
  - Query params: `?threshold=100ms`, `?limit=50`, `?offset=0`
- `GET /pulse/api/database/patterns` — query patterns sorted by total time
- `GET /pulse/api/database/n1` — N+1 detection results
- `GET /pulse/api/database/pool` — connection pool time series

#### 9.5 — Implement Errors API
- `GET /pulse/api/errors` — error groups with counts, pagination, filtering
  - Query params: `?type=database`, `?route=/api/users`, `?range=24h`, `?resolved=false`
- `GET /pulse/api/errors/:id` — single error detail with stack trace and request context
- `POST /pulse/api/errors/:id/mute` — mute error
- `POST /pulse/api/errors/:id/resolve` — mark resolved
- `DELETE /pulse/api/errors/:id` — delete error
- `GET /pulse/api/errors/timeline` — error count time series for charting

#### 9.6 — Implement Runtime API
- `GET /pulse/api/runtime/current` — current runtime snapshot (goroutines, memory, GC)
- `GET /pulse/api/runtime/history` — time-series for all runtime metrics
  - Query params: `?range=1h`, `?metrics=heap_alloc,num_goroutine`
- `GET /pulse/api/runtime/info` — static system info (Go version, OS, hostname, uptime)

#### 9.7 — Implement Health API (Dashboard Version)
- `GET /pulse/api/health/checks` — all checks with current status and history
- `GET /pulse/api/health/checks/:name/history` — history for one check
- `POST /pulse/api/health/checks/:name/run` — run a check on demand
- `GET /pulse/api/health/status` — composite health with uptime percentages

#### 9.8 — Implement Alerts & Settings API
- `GET /pulse/api/alerts` — alert history with pagination
- `GET /pulse/api/alerts/active` — currently active alerts
- `PUT /pulse/api/alerts/config` — update alert configuration
- `GET /pulse/api/settings` — current configuration (sanitized, no secrets)
- `PUT /pulse/api/settings` — update select settings at runtime
- `POST /pulse/api/data/export` — export metrics as JSON/CSV
- `POST /pulse/api/data/reset` — clear all collected data

---

## Phase 10: WebSocket Live Updates

**Goal:** Build a WebSocket hub that pushes real-time metrics to connected dashboard clients.

### Tasks

#### 10.1 — Implement WebSocket Hub (`pulse/websocket.go`)
- Define `WebSocketHub` struct:
  - `clients map[*Client]bool`
  - `broadcast chan []byte`
  - `register chan *Client`
  - `unregister chan *Client`
- Run hub as a background goroutine
- Handle client registration/deregistration
- Broadcast messages to all connected clients

#### 10.2 — Implement WebSocket Client Handler
- `GET /pulse/ws/live` — upgrade to WebSocket
- Handle connection lifecycle: register, read pump, write pump
- Implement ping/pong for connection health
- Handle reconnection gracefully on client side

#### 10.3 — Implement Metric Broadcasting
- On every aggregation tick, broadcast:
  - Overview snapshot (requests, errors, latency, health status)
  - Runtime metrics (goroutines, memory)
- On every request/error event, broadcast lightweight update:
  - `{"type": "request", "method": "GET", "path": "/users", "status": 200, "latency_ms": 45}`
  - `{"type": "error", "route": "POST /users", "message": "duplicate key"}`
- On health check completion: `{"type": "health", "name": "postgres", "status": "healthy"}`
- On alert: `{"type": "alert", "severity": "high", "message": "..."}`

#### 10.4 — Implement Channel Subscriptions
- Allow clients to subscribe to specific channels:
  - `overview` — periodic overview updates
  - `requests` — live request feed
  - `errors` — live error feed
  - `health` — health check updates
  - `alerts` — alert notifications
- Client sends: `{"subscribe": ["overview", "errors"]}`
- Only send subscribed data to reduce bandwidth

#### 10.5 — Write WebSocket Tests
- Test connection establishment
- Test message broadcasting
- Test client subscription filtering
- Test reconnection handling
- Test concurrent client connections
- Load test with many simultaneous clients

---

## Phase 11: React Dashboard — Core

**Goal:** Build the embedded React dashboard with the overview page, route list, and navigation.

### Tasks

#### 11.1 — Set Up React Project
- Create React 18 project with Vite in `pulse/ui/dashboard/`
- Dependencies: React, TailwindCSS, Recharts, Lucide React icons
- Configure build output to a single HTML file (inline CSS/JS)
- Create `go:embed` directive to embed the built HTML
- Serve from `GET /pulse`

#### 11.2 — Build App Shell & Navigation
- Sidebar navigation with pages:
  - Overview, Routes, Database, Errors, Runtime, Health, Dependencies, Alerts, Settings
- Header with: app name, time range selector, dark/light toggle, logout
- Responsive: sidebar collapses to hamburger on mobile
- Dark mode by default (developer-friendly)

#### 11.3 — Build Time Range Selector Component
- Dropdown with: 5m, 15m, 1h, 6h, 24h, 7d, Custom
- Custom mode: date-time pickers for start/end
- Auto-refresh indicator with pause/play button
- Stored in React context, used by all pages

#### 11.4 — Build Overview Page
- Health status badge (healthy/degraded/unhealthy with color)
- Key metric cards:
  - Total Requests (with trend arrow)
  - Error Rate % (with trend)
  - p95 Latency (with trend)
  - Active Goroutines
  - Heap Usage
  - Active Alerts count
- Request throughput chart (area chart, last 1h)
- Error timeline chart (bar chart)
- Top 5 slowest routes table
- Top 5 highest error routes table
- Recent errors list
- Live activity feed (WebSocket-powered)

#### 11.5 — Build Routes List Page
- Sortable, filterable table with columns:
  - Method (colored badge), Path
  - Requests (total count)
  - p50, p95, p99 latency
  - Error Rate %
  - RPM
  - Trend indicator (↑↓→)
- Search bar to filter routes
- Click row → navigate to Route Detail
- Color coding: green (<200ms p95), yellow (200ms-1s), red (>1s)

#### 11.6 — Build Reusable Chart Components
- `LatencyChart` — line chart with percentile lines (p50, p95, p99)
- `ThroughputChart` — area chart for RPS over time
- `StatusChart` — stacked bar chart for status code distribution
- `ErrorChart` — bar chart for error count over time
- All components accept time-series data and time range
- Responsive, dark mode support, tooltips

#### 11.7 — Build API Client & WebSocket Hook
- Axios/fetch wrapper for all `/pulse/api/*` endpoints
- Auto-attach JWT token from stored auth
- Handle 401 → redirect to login
- `usePulseWebSocket()` React hook:
  - Connect to `/pulse/ws/live`
  - Auto-reconnect on disconnect
  - Subscribe to channels
  - Return live data via state
- `usePulseAPI(endpoint, params)` React hook for data fetching with SWR-like caching

#### 11.8 — Build Login Page
- Simple login form (username + password)
- JWT token stored in memory (not localStorage for security)
- Auto-redirect to overview after login
- Session timeout handling

---

## Phase 12: React Dashboard — Detail Pages

**Goal:** Build the remaining dashboard pages with drill-down views.

### Tasks

#### 12.1 — Build Route Detail Page
- Header: method badge, path, key stats (p95, error rate, RPM)
- Latency chart: p50, p95, p99 over time
- Status code distribution: pie chart + timeline
- Recent errors for this route
- Top database queries triggered by this route
- Slow request log for this route
- Request size / response size chart

#### 12.2 — Build Database Page
- Connection pool status: gauge charts (in-use, idle, available)
- Slow query log: table with SQL, duration, caller, timestamp
  - Click to expand: full SQL, parameters, explain hint
- Query patterns: table sorted by total time spent
  - Pattern (normalized SQL), call count, avg duration, total time
- N+1 detection alerts: list of detected N+1 patterns with request context
- Table hotspots: bar chart of reads/writes per table

#### 12.3 — Build Errors Page
- Error groups table: message, type, route, count, last seen, status (active/muted/resolved)
- Error timeline: bar chart of error count over time
- Click error → detail view:
  - Full error message, type classification
  - Stack trace (syntax highlighted, collapsible)
  - Request context (headers, body, query params)
  - Occurrence history (sparkline)
  - Actions: mute, resolve, delete
- Filter: by type, route, status, time range

#### 12.4 — Build Runtime Page
- Memory chart: line chart with heap alloc, heap in-use, sys memory
- Goroutine chart: line chart with count, highlight anomalies
- GC chart: pause times as bar chart, GC frequency
- CPU usage chart (if available)
- File descriptors chart
- System info panel: Go version, OS, hostname, CPUs, GOMAXPROCS, uptime

#### 12.5 — Build Health Page
- Health check grid: card per check with status badge, latency, last check time
- Click check → history view: timeline of check results, uptime percentage
- Composite health indicator at top
- Flapping indicator for unstable checks
- "Run Now" button for on-demand checks
- Kubernetes probe status (if accessed)

#### 12.6 — Build Alerts Page
- Active alerts: list with severity badge, message, triggered time, affected route/metric
- Alert history: table with all past alerts, filterable by severity and type
- Alert configuration: edit thresholds for latency, error rate, memory, etc.
- Notification channel configuration: Slack webhook, email, webhook URLs
- Test notification button

#### 12.7 — Build Settings Page
- Current configuration display (read-only view of active config)
- Editable settings: retention period, sample rate, slow query threshold
- Data management: export button (JSON/CSV), reset button with confirmation
- About section: Pulse version, Go version, uptime, links to docs

---

## Phase 13: Alerting System

**Goal:** Build a threshold-based alerting engine with multiple notification channels.

### Tasks

#### 13.1 — Define Alert Rules (`pulse/alerts.go`)
- Built-in alert rules (enabled by default):
  - `high_latency` — p95 latency > threshold for N minutes
  - `high_error_rate` — error rate > threshold for N minutes
  - `health_check_failure` — critical check fails N times consecutively
  - `goroutine_leak` — sustained goroutine growth
  - `high_memory` — heap usage > threshold
  - `slow_queries` — queries consistently > threshold
  - `connection_pool_exhaustion` — pool usage > 90%
- Custom rules via config:
  ```go
  Alerts: pulse.AlertConfig{
      Rules: []pulse.AlertRule{
          {Name: "api-latency", Metric: "p95_latency", Threshold: 500 * time.Millisecond, Duration: 5 * time.Minute},
      },
  }
  ```

#### 13.2 — Implement Alert Evaluation Engine
- Background goroutine that evaluates rules on each aggregation tick
- For each rule:
  - Query the relevant metric from storage
  - Check against threshold
  - Track how long the condition has been true
  - If condition met for `Duration` → fire alert
- Support `AlertState`: `OK`, `Pending` (threshold exceeded but duration not met), `Firing`, `Resolved`

#### 13.3 — Implement Alert Lifecycle
- **Pending** → threshold exceeded, counting duration
- **Firing** → duration met, notifications sent
- **Resolved** → metric returned to normal, resolution notification sent
- **Cooldown** → don't re-fire same alert within cooldown window (default: 15m)
- Store full lifecycle history per alert

#### 13.4 — Implement Slack Notifications
- Send rich Slack messages via webhook:
  - Title: alert name and severity
  - Color: red (critical), orange (warning), green (resolved)
  - Fields: metric value, threshold, route, timestamp
  - Action link to dashboard
- Rate limit: max 1 Slack message per alert per cooldown period

#### 13.5 — Implement Email Notifications
- Send via SMTP:
  - HTML email with alert details
  - Subject: `[Pulse Alert] {severity}: {name}`
  - Support multiple recipients
- Config: `Email: &pulse.EmailConfig{Host, Port, From, To, Username, Password}`

#### 13.6 — Implement Webhook Notifications
- POST JSON payload to configured URL:
  - Alert name, severity, metric, value, threshold, timestamp
  - Include HMAC signature for verification
- Support multiple webhook URLs
- Retry: 3 attempts with exponential backoff

#### 13.7 — Write Alerting Tests
- Test rule evaluation with simulated metrics
- Test alert lifecycle (pending → firing → resolved)
- Test cooldown behavior
- Test Slack notification format (mock webhook)
- Test email sending (mock SMTP)
- Test webhook delivery and retry
- Test concurrent alert evaluation

---

## Phase 14: Dependency Monitoring

**Goal:** Build HTTP client wrappers and dependency health tracking.

### Tasks

#### 14.1 — Implement HTTP Client Wrapper (`pulse/dependencies.go`)
- `pulse.WrapHTTPClient(client *http.Client, name string) *http.Client`
- Replace the client's `Transport` with an instrumented transport
- On each request:
  - Record: URL, method, status code, latency, request/response size, error
  - Group by dependency name (e.g., "stripe-api", "sendgrid")
- Non-invasive: no changes to existing code beyond wrapping the client

#### 14.2 — Implement Per-Dependency Metrics
- For each named dependency, track:
  - Total requests, error count, error rate
  - Latency percentiles: p50, p95, p99
  - Throughput: requests per minute
  - Availability: uptime percentage (successful / total)
  - Time series for charting
- Store in the same storage backend as other metrics

#### 14.3 — Implement Circuit Breaker Detection
- If the wrapped client or transport implements a circuit breaker interface:
  - Detect state: open, closed, half-open
  - Track state transitions over time
- Generic interface detection via `CircuitBreaker` interface:
  ```go
  type CircuitBreaker interface {
      State() string  // "open", "closed", "half-open"
  }
  ```

#### 14.4 — Build Dependencies Dashboard Page
- Dependency list: card per dependency with name, status, latency, error rate
- Click dependency → detail view: latency chart, error timeline, request log
- Dependency map: simple visualization showing your app's outbound connections
- SLA tracking: uptime percentage per dependency over time

#### 14.5 — Write Dependency Tests
- Test HTTP client wrapping
- Test metric capture for outbound requests
- Test per-dependency aggregation
- Test with multiple dependencies
- Test error handling (timeout, connection refused, DNS failure)

---

## Phase 15: Advanced Storage & Export

**Goal:** Add SQLite persistent storage, Prometheus export, and data export capabilities.

### Tasks

#### 15.1 — Implement SQLite Storage (`pulse/storage_sqlite.go`)
- Implement `Storage` interface backed by SQLite (pure Go: `modernc.org/sqlite`)
- Schema: `requests`, `queries`, `runtime_metrics`, `errors`, `health_results`, `alerts` tables
- Indexed by timestamp for efficient range queries
- Auto-create tables on initialization
- Use WAL mode for better concurrent performance

#### 15.2 — Implement Data Retention & Cleanup
- Background goroutine that runs every hour
- Delete records older than `Config.Storage.RetentionHours` (default: 24h)
- For SQLite: VACUUM after cleanup to reclaim space
- For memory: already handled by ring buffer overflow

#### 15.3 — Implement Prometheus Endpoint (`pulse/prometheus.go`)
- `GET /pulse/metrics` — Prometheus exposition format
- Export key metrics:
  - `pulse_http_requests_total{method, path, status}` — counter
  - `pulse_http_request_duration_seconds{method, path}` — histogram
  - `pulse_db_query_duration_seconds{operation, table}` — histogram
  - `pulse_runtime_goroutines` — gauge
  - `pulse_runtime_heap_bytes` — gauge
  - `pulse_health_check_status{name}` — gauge (1=healthy, 0=unhealthy)
- Only enabled when `Config.Prometheus.Enabled = true`

#### 15.4 — Implement JSON/CSV Export
- `POST /pulse/api/data/export` with body: `{"format": "json", "type": "requests", "range": "24h"}`
- Support types: requests, queries, errors, runtime, health, alerts
- JSON: array of records
- CSV: header row + data rows
- Return as file download with `Content-Disposition` header
- Limit: max 100K records per export

#### 15.5 — Implement Data Reset
- `POST /pulse/api/data/reset` — clear all collected data
- Requires confirmation param: `{"confirm": true}`
- Reset ring buffers, clear storage, reset aggregation cache
- Keep configuration and health check registrations

#### 15.6 — Write Storage & Export Tests
- Test SQLite storage CRUD and queries
- Test data retention cleanup
- Test Prometheus endpoint format
- Test JSON/CSV export accuracy
- Test data reset
- Benchmark SQLite vs memory performance

---

## Phase 16: Demo Application & Examples

**Goal:** Build a realistic demo application and example projects.

### Tasks

#### 16.1 — Build Demo Application (`main.go`)
- Blog API with: Users, Posts, Comments, Tags
- GORM models with relationships
- Gin routes: auth, CRUD, search, pagination, file upload
- Simulate realistic traffic patterns:
  - Occasional slow queries (random sleep)
  - Occasional errors (random 500s)
  - External API calls (mock dependency)
- Mount Pulse with full configuration
- Register custom health checks
- Include seed data

#### 16.2 — Basic Example (`examples/basic/main.go`)
- 2 models, 4 routes, one-line mount
- Show default behavior with zero config
- README with setup instructions

#### 16.3 — Full Example (`examples/full/main.go`)
- All features configured:
  - SQLite storage, custom thresholds
  - Multiple health checks
  - Dependency monitoring
  - Slack alerting
  - Prometheus endpoint
  - Custom alert rules
- README with setup instructions

#### 16.4 — With Sentinel Example (`examples/with-sentinel/main.go`)
- Mount both Pulse and Sentinel on the same router
- Show how observability + security work together
- Cross-link dashboards

#### 16.5 — Traffic Simulator
- Helper function that generates realistic traffic:
  - Background goroutines making HTTP requests to the app
  - Varying latency patterns (some routes slow, some fast)
  - Error spikes at intervals
  - Goroutine creation/destruction
- Used in demo for impressive first-load dashboard experience

---

## Phase 17: Testing, Documentation & Release

**Goal:** Comprehensive testing, documentation, CI/CD pipeline, and publishing.

### Tasks

#### 17.1 — Unit Tests
- Achieve 80%+ coverage
- Test every storage method
- Test middleware with mock Gin contexts
- Test GORM plugin with mock DB
- Test alerting with simulated data
- Test ring buffer under concurrent access
- Use table-driven tests throughout

#### 17.2 — Integration Tests
- Full Mount → middleware → storage → API → WebSocket flow
- Test with real SQLite database
- Test concurrent request handling (race detector)
- Test metric accuracy under load
- Test dashboard UI serves correctly

#### 17.3 — Benchmarks
- Benchmark middleware overhead per request
- Benchmark GORM plugin overhead per query
- Benchmark ring buffer throughput
- Benchmark aggregation with large datasets
- Benchmark storage queries at scale
- Target: <100μs middleware overhead, <50μs GORM plugin overhead

#### 17.4 — README.md
- Badges: Go version, license, CI, Go Report Card, coverage
- Quick start (3 steps)
- Screenshot of dashboard
- Feature overview
- Full configuration reference
- Health check examples
- Dependency monitoring guide
- Alert configuration guide
- FAQ

#### 17.5 — Documentation
- `docs/configuration.md` — full config reference with defaults
- `docs/health-checks.md` — writing custom health checks
- `docs/alerting.md` — alert rules and notification channels
- `docs/dependencies.md` — monitoring external services
- `docs/architecture.md` — how Pulse works internally
- `docs/prometheus.md` — Prometheus integration guide

#### 17.6 — CI/CD Pipeline
- `.github/workflows/ci.yml`:
  - Run tests on push/PR
  - Race detector enabled
  - golangci-lint
  - Build verification
  - Coverage report
- `.github/workflows/release.yml`:
  - Tag-based releases
  - Publish to pkg.go.dev

#### 17.7 — Release v0.1.0
- Tag initial release
- Ensure `go get github.com/MUKE-coder/pulse/pulse` works
- Test on Go 1.21+ and Go 1.24+
- Announce on Go communities
- Submit to awesome-go list
