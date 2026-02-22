# Claude Code Prompt — Build Pulse

Copy and paste this prompt into Claude Code to build the Pulse observability tool phase by phase.

---

## The Prompt

```
You are building a Go open-source tool called **Pulse** — a self-hosted observability and performance monitoring SDK for Go applications using Gin and GORM. The module path is `github.com/MUKE-coder/pulse` and the main package is `pulse/`.

## WHAT THIS TOOL DOES

Pulse is a drop-in middleware + GORM plugin that tracks request latency, database query performance, runtime metrics, errors, dependency health, and serves a real-time dashboard — all mountable with a single function call:

```go
pulse.Mount(router, db, pulse.Config{})
// Dashboard at http://localhost:8080/pulse
```

Think of it as a lightweight, self-hosted alternative to Datadog/New Relic for development and staging environments. It's the performance counterpart to my security tool Sentinel (github.com/MUKE-coder/sentinel).

## DESIGN PRINCIPLES

1. **One-line mount** — follows `Mount(router, db, Config{})` pattern like my other tools (github.com/MUKE-coder/gorm-studio and github.com/MUKE-coder/sentinel).
2. **Zero external dependencies** — no Prometheus, Grafana, Redis, or InfluxDB required. Everything is embedded.
3. **Minimal overhead** — async processing, ring buffers, sampling. Middleware should add <100μs per request, GORM plugin <50μs per query.
4. **Developer-first** — designed for dev/staging, not enterprise production observability.
5. **Real-time** — WebSocket-powered live dashboard, not polling.
6. **No CGo** — pure Go SQLite via modernc.org/sqlite for persistent storage option.

## ARCHITECTURE OVERVIEW

```
pulse/
├── pulse/                      # Main package (users import this)
│   ├── mount.go                # Mount() entry point — registers middleware, GORM plugin, routes
│   ├── config.go               # Config struct with all sub-configs and defaults
│   ├── engine.go               # Pulse engine — orchestrates all subsystems
│   ├── middleware.go            # Gin request tracing middleware
│   ├── gorm_plugin.go          # GORM query tracking plugin (implements gorm.Plugin)
│   ├── runtime.go              # Go runtime metrics sampler (memory, goroutines, GC)
│   ├── errors.go               # Error aggregator, classifier, panic recovery middleware
│   ├── health.go               # Health check registry and periodic runner
│   ├── dependencies.go         # HTTP client wrapper for dependency monitoring
│   ├── alerts.go               # Threshold-based alert engine with notifications
│   ├── metrics.go              # Core metric types (RequestMetric, QueryMetric, etc.)
│   ├── ringbuffer.go           # Generic ring buffer for high-throughput ingestion
│   ├── aggregator.go           # Periodic metric aggregation (percentiles, rollups, trends)
│   ├── storage.go              # Storage interface definition
│   ├── storage_memory.go       # In-memory storage (default, ring buffer-backed)
│   ├── storage_sqlite.go       # SQLite persistent storage (optional)
│   ├── api.go                  # REST API handlers for dashboard (JWT-protected)
│   ├── websocket.go            # WebSocket hub for live dashboard updates
│   ├── ui.go                   # Embedded React dashboard (go:embed)
│   ├── prometheus.go           # Optional Prometheus /metrics endpoint
│   └── export.go               # JSON/CSV data export
├── examples/
│   ├── basic/main.go           # Minimal example
│   ├── full/main.go            # All features configured
│   └── with-sentinel/main.go   # Pulse + Sentinel together
├── main.go                     # Demo application with traffic simulator
├── go.mod
├── go.sum
├── README.md
├── CONTRIBUTING.md
├── SECURITY.md
├── LICENSE
└── Makefile
```

## CONFIGURATION

```go
type Config struct {
    // URL prefix for dashboard (default: "/pulse")
    Prefix  string
    
    // Application name displayed in dashboard
    AppName string
    
    // Dashboard authentication
    Dashboard DashboardConfig  // Username, Password, SecretKey for JWT
    
    // Storage backend
    Storage StorageConfig      // Driver: Memory (default) or SQLite; DSN; RetentionHours
    
    // Request tracing
    Tracing TracingConfig
    // Fields: Enabled (default: true), SlowRequestThreshold (default: 1s),
    //         SampleRate (default: 1.0), ExcludePaths []string
    
    // Database monitoring
    Database DatabaseConfig
    // Fields: Enabled (true), SlowQueryThreshold (200ms),
    //         DetectN1 (true), N1Threshold (5), TrackCallers (true)
    
    // Runtime metrics
    Runtime RuntimeConfig
    // Fields: Enabled (true), SampleInterval (5s), LeakThreshold (100/hr)
    
    // Error tracking
    Errors ErrorConfig
    // Fields: Enabled (true), CaptureStackTrace (true),
    //         CaptureRequestBody (true), MaxBodySize (4096)
    
    // Health checks
    Health HealthConfig
    // Fields: Enabled (true), CheckInterval (30s), Timeout (10s)
    
    // Alerting
    Alerts AlertConfig
    // Fields: Enabled (true), Cooldown (15m),
    //         Slack *SlackConfig, Email *EmailConfig,
    //         Webhooks []WebhookConfig, Discord *DiscordConfig,
    //         Rules []AlertRule
    
    // Prometheus metrics endpoint
    Prometheus PrometheusConfig
    // Fields: Enabled (false), Path ("/pulse/metrics")
    
    // Dev mode: verbose logging, more frequent aggregation
    DevMode bool
}
```

### Sub-Config Details

```go
type DashboardConfig struct {
    Username  string  // Default: "admin"
    Password  string  // Default: "pulse"
    SecretKey string  // JWT signing key
}

type StorageConfig struct {
    Driver        StorageDriver  // Memory (default) or SQLite
    DSN           string         // SQLite path (default: "pulse.db")
    RetentionHours int           // Default: 24 for dev, 168 for staging
}

type StorageDriver int
const (
    Memory StorageDriver = iota
    SQLite
)

type TracingConfig struct {
    Enabled               bool
    SlowRequestThreshold  time.Duration
    SampleRate            float64    // 0.0-1.0, always record errors/slow
    ExcludePaths          []string   // Glob patterns
}

type DatabaseConfig struct {
    Enabled             bool
    SlowQueryThreshold  time.Duration
    DetectN1            bool
    N1Threshold         int    // Repeated pattern count to flag as N+1
    TrackCallers        bool   // Capture caller file:line for queries
}

type AlertRule struct {
    Name       string
    Metric     string          // "p95_latency", "error_rate", "goroutine_count", etc.
    Operator   string          // ">", "<", ">=", "<="
    Threshold  float64
    Duration   time.Duration   // How long condition must be true
    Severity   string          // "critical", "warning", "info"
    Route      string          // Optional: only for specific route
}
```

## MOUNT FUNCTION SIGNATURE

```go
func Mount(router *gin.Engine, db *gorm.DB, configs ...Config) *Pulse
```

The returned `*Pulse` supports additional configuration:

```go
p := pulse.Mount(router, db, config)

// Register health checks
p.AddHealthCheck(pulse.HealthCheck{
    Name:     "postgres",
    Type:     "database",
    Critical: true,
    CheckFunc: func(ctx context.Context) error {
        return db.WithContext(ctx).Exec("SELECT 1").Error
    },
})

p.AddHealthCheck(pulse.HealthCheck{
    Name:     "redis",
    Type:     "redis",
    Critical: false,
    CheckFunc: func(ctx context.Context) error {
        return redisClient.Ping(ctx).Err()
    },
})

p.AddHealthCheck(pulse.HealthCheck{
    Name:     "stripe-api",
    Type:     "http",
    CheckFunc: pulse.HTTPCheck("https://api.stripe.com/v1/health", 5*time.Second),
})

// Wrap HTTP clients for dependency monitoring
stripeClient := pulse.WrapHTTPClient(http.DefaultClient, "stripe")
sendgridClient := pulse.WrapHTTPClient(http.DefaultClient, "sendgrid")
```

## CORE METRIC TYPES

```go
type RequestMetric struct {
    Method       string
    Path         string        // Route pattern, e.g., "/users/:id"
    StatusCode   int
    Latency      time.Duration
    RequestSize  int64
    ResponseSize int64
    ClientIP     string
    UserAgent    string
    Error        string
    TraceID      string        // Correlates with queries and errors
    Timestamp    time.Time
}

type QueryMetric struct {
    SQL             string
    NormalizedSQL   string        // For pattern grouping
    Duration        time.Duration
    RowsAffected    int64
    Error           string
    Operation       string        // SELECT, INSERT, UPDATE, DELETE
    Table           string
    CallerFile      string        // Source file that triggered query
    CallerLine      int
    RequestTraceID  string        // Links to the HTTP request
    Timestamp       time.Time
}

type RuntimeMetric struct {
    HeapAlloc       uint64
    HeapInUse       uint64
    HeapObjects     uint64
    StackInUse      uint64
    TotalAlloc      uint64
    Sys             uint64
    NumGoroutine    int
    GCPauseNs       uint64
    NumGC           uint32
    GCCPUFraction   float64
    Timestamp       time.Time
}

type ErrorRecord struct {
    ID              string
    Fingerprint     string        // For deduplication
    Method          string
    Route           string
    ErrorMessage    string
    ErrorType       string        // validation, database, timeout, panic, auth, internal
    StackTrace      string
    RequestContext  *RequestContext
    Count           int64
    FirstSeen       time.Time
    LastSeen        time.Time
    Muted           bool
    Resolved        bool
}

type HealthCheckResult struct {
    Name       string
    Type       string
    Status     string            // healthy, unhealthy, degraded
    Latency    time.Duration
    Error      string
    Metadata   map[string]interface{}
    Timestamp  time.Time
}
```

## STORAGE INTERFACE

```go
type Storage interface {
    // Request metrics
    StoreRequest(m RequestMetric) error
    GetRequests(filter RequestFilter) ([]RequestMetric, error)
    GetRouteStats(timeRange TimeRange) ([]RouteStats, error)
    GetRouteDetail(method, path string, timeRange TimeRange) (*RouteDetail, error)
    
    // Query metrics
    StoreQuery(m QueryMetric) error
    GetSlowQueries(threshold time.Duration, limit int) ([]QueryMetric, error)
    GetQueryPatterns(timeRange TimeRange) ([]QueryPattern, error)
    GetN1Detections(timeRange TimeRange) ([]N1Detection, error)
    GetConnectionPoolStats() (*PoolStats, error)
    
    // Runtime metrics
    StoreRuntime(m RuntimeMetric) error
    GetRuntimeHistory(timeRange TimeRange) ([]RuntimeMetric, error)
    
    // Errors
    StoreError(e ErrorRecord) error
    GetErrors(filter ErrorFilter) ([]ErrorRecord, error)
    GetErrorGroups(timeRange TimeRange) ([]ErrorGroup, error)
    UpdateError(id string, updates map[string]interface{}) error
    
    // Health
    StoreHealthResult(r HealthCheckResult) error
    GetHealthHistory(name string, limit int) ([]HealthCheckResult, error)
    
    // Alerts
    StoreAlert(a AlertRecord) error
    GetAlerts(filter AlertFilter) ([]AlertRecord, error)
    
    // Dependencies
    StoreDependencyMetric(m DependencyMetric) error
    GetDependencyStats(timeRange TimeRange) ([]DependencyStats, error)
    
    // Overview
    GetOverview(timeRange TimeRange) (*Overview, error)
    
    // Maintenance
    Cleanup(retention time.Duration) error
    Reset() error
    Close() error
}
```

## RING BUFFER

For high-throughput metric ingestion, implement a generic ring buffer:

```go
type RingBuffer[T any] struct {
    data     []T
    head     int64  // atomic
    size     int64
    capacity int64
}

func NewRingBuffer[T any](capacity int) *RingBuffer[T]
func (rb *RingBuffer[T]) Push(item T)
func (rb *RingBuffer[T]) GetAll() []T
func (rb *RingBuffer[T]) GetLast(n int) []T
func (rb *RingBuffer[T]) Len() int
func (rb *RingBuffer[T]) Reset()
```

Default capacities: 100,000 for requests, 50,000 for queries, 10,000 for runtime samples.

## SQL NORMALIZER

Normalize queries for pattern grouping:
- Replace string literals ('value') with ?
- Replace number literals with ?
- Replace IN lists with IN (?)
- Remove extra whitespace, lowercase keywords
- Extract operation (SELECT/INSERT/UPDATE/DELETE) and table name

Example: `SELECT * FROM users WHERE id = 42 AND name = 'John'` → `select * from users where id = ? and name = ?`

## AGGREGATION ENGINE

The aggregator runs on a background goroutine every 10 seconds and computes:

1. **Per-route stats** — for each unique Method+Path:
   - Count, error count, error rate
   - Latency percentiles: p50, p75, p90, p95, p99
   - Avg/min/max latency, RPM
   - Status code histogram
   
2. **Time-series rollups** — downsample raw metrics:
   - Last 5m → 5s buckets
   - Last 1h → 30s buckets
   - Last 24h → 5m buckets
   - Last 7d → 1h buckets

3. **Trend detection** — compare current 5m window vs previous 5m:
   - p95 latency increased >50% → "degrading"
   - Error rate increased >100% → "error_spike"
   - RPS dropped >50% → "traffic_drop"

4. **Overview snapshot** — single struct powering the overview page

## N+1 QUERY DETECTION

Track queries per request using the trace ID:
1. Group queries by normalized SQL pattern within a single request
2. If the same pattern appears ≥ N1Threshold times (default: 5), flag as N+1
3. Record: pattern, count, total duration, request trace ID, route
4. Store separately for the N+1 dashboard section
5. Trigger alert if configured

## ERROR CLASSIFICATION

Auto-classify errors by examining status code and error message:
- Status 400 + binding/validation keywords → `"validation"`
- Status 401/403 → `"auth"`
- Status 404 → `"not_found"`
- Status 408/504 + timeout keywords → `"timeout"`
- Status 500 + GORM/SQL keywords → `"database"`
- Panic recovery → `"panic"`
- Everything else → `"internal"`

Deduplicate by fingerprint: hash of (error_message + route + method). Same fingerprint → increment count + update LastSeen.

## HEALTH CHECK SYSTEM

```go
type HealthCheck struct {
    Name      string
    Type      string                              // database, redis, http, disk, custom
    CheckFunc func(ctx context.Context) error
    Interval  time.Duration                       // Per-check override (default: global)
    Timeout   time.Duration
    Critical  bool                                // Failure → system "unhealthy"
}
```

Built-in convenience functions:
```go
pulse.HTTPCheck(url string, timeout time.Duration) func(context.Context) error
pulse.TCPCheck(address string, timeout time.Duration) func(context.Context) error
pulse.DiskCheck(path string, minFreePercent float64) func(context.Context) error
```

Health endpoint at `GET /pulse/health` (no auth required):
```json
{
  "status": "healthy",
  "timestamp": "2025-06-15T10:30:00Z",
  "uptime": "4h32m",
  "checks": {
    "postgres": {"status": "healthy", "latency_ms": 2},
    "redis": {"status": "unhealthy", "error": "connection refused", "latency_ms": 5003},
    "disk": {"status": "healthy", "details": {"free_percent": 72.5}}
  }
}
```

Kubernetes probes:
- `GET /pulse/health/live` → 200 (always, unless hung)
- `GET /pulse/health/ready` → 200 if critical checks pass, 503 if not

## DEPENDENCY MONITORING

```go
func WrapHTTPClient(client *http.Client, name string) *http.Client
```

Wraps the client's Transport to intercept every outbound request:
- Record: dependency name, URL, method, status, latency, size, error
- Group metrics per dependency name
- Calculate: latency percentiles, error rate, RPM, availability %
- Display in dashboard Dependencies page

## ALERTING

Built-in default rules (all configurable):
| Rule | Metric | Default Threshold | Duration |
|------|--------|-------------------|----------|
| High Latency | p95_latency | > 1s | 5 min |
| High Error Rate | error_rate | > 5% | 3 min |
| Health Check Failure | health_check | fails | 3 consecutive |
| Goroutine Leak | goroutine_count | +100/hr sustained | 30 min |
| High Memory | heap_alloc_mb | > 1024 MB | 5 min |
| Slow Queries | slow_query_count | > 10/min | 5 min |
| Pool Exhaustion | pool_in_use_pct | > 90% | 1 min |

Alert lifecycle: OK → Pending → Firing → Resolved
Cooldown: don't re-fire same alert within 15 minutes (configurable)

Notification channels:
- **Slack**: webhook URL, rich message with severity color, metric values, dashboard link
- **Email**: SMTP config, HTML email
- **Webhook**: POST JSON payload with HMAC signature, 3 retries with backoff
- **Discord**: webhook URL, embedded message

## WEBSOCKET PROTOCOL

Connection: `ws://host/pulse/ws/live`

Client subscribes: `{"subscribe": ["overview", "requests", "errors", "health", "alerts"]}`

Server pushes:
- Overview (every 10s): `{"type": "overview", "data": {...}}`
- Request (real-time): `{"type": "request", "data": {"method": "GET", "path": "/users", "status": 200, "latency_ms": 45}}`
- Error (real-time): `{"type": "error", "data": {"route": "POST /users", "message": "..."}}`
- Health (on check): `{"type": "health", "data": {"name": "postgres", "status": "healthy"}}`
- Alert (on fire/resolve): `{"type": "alert", "data": {"name": "high_latency", "state": "firing"}}`

## REST API ENDPOINTS

### Authentication
| Method | Path | Description |
|--------|------|-------------|
| POST | /pulse/api/auth/login | Get JWT token |
| POST | /pulse/api/auth/logout | Logout |
| GET | /pulse/api/auth/verify | Verify token |

### Overview
| Method | Path | Description |
|--------|------|-------------|
| GET | /pulse/api/overview | Full overview snapshot |

### Routes
| Method | Path | Description |
|--------|------|-------------|
| GET | /pulse/api/routes | All routes with stats |
| GET | /pulse/api/routes/:method/*path | Route detail |
| GET | /pulse/api/routes/compare | Side-by-side comparison |

### Database
| Method | Path | Description |
|--------|------|-------------|
| GET | /pulse/api/database/overview | DB stats summary |
| GET | /pulse/api/database/slow-queries | Slow query log |
| GET | /pulse/api/database/patterns | Query patterns |
| GET | /pulse/api/database/n1 | N+1 detections |
| GET | /pulse/api/database/pool | Connection pool stats |

### Errors
| Method | Path | Description |
|--------|------|-------------|
| GET | /pulse/api/errors | Error groups |
| GET | /pulse/api/errors/:id | Error detail + stack trace |
| POST | /pulse/api/errors/:id/mute | Mute error |
| POST | /pulse/api/errors/:id/resolve | Resolve error |
| DELETE | /pulse/api/errors/:id | Delete error |
| GET | /pulse/api/errors/timeline | Error timeline for charts |

### Runtime
| Method | Path | Description |
|--------|------|-------------|
| GET | /pulse/api/runtime/current | Current snapshot |
| GET | /pulse/api/runtime/history | Time-series history |
| GET | /pulse/api/runtime/info | Static system info |

### Health
| Method | Path | Description |
|--------|------|-------------|
| GET | /pulse/api/health/checks | All checks + status |
| GET | /pulse/api/health/checks/:name/history | Check history |
| POST | /pulse/api/health/checks/:name/run | Run check now |

### Dependencies
| Method | Path | Description |
|--------|------|-------------|
| GET | /pulse/api/dependencies | All dependencies + stats |
| GET | /pulse/api/dependencies/:name | Dependency detail |

### Alerts & Settings
| Method | Path | Description |
|--------|------|-------------|
| GET | /pulse/api/alerts | Alert history |
| GET | /pulse/api/alerts/active | Currently firing alerts |
| PUT | /pulse/api/alerts/config | Update alert rules |
| GET | /pulse/api/settings | Current config |
| PUT | /pulse/api/settings | Update settings |
| POST | /pulse/api/data/export | Export data (JSON/CSV) |
| POST | /pulse/api/data/reset | Clear all data |

### Public (No Auth)
| Method | Path | Description |
|--------|------|-------------|
| GET | /pulse/health | Structured health response |
| GET | /pulse/health/live | Kubernetes liveness probe |
| GET | /pulse/health/ready | Kubernetes readiness probe |
| GET | /pulse/metrics | Prometheus exposition (if enabled) |

## DASHBOARD UI

Build with React 18 + Vite + TailwindCSS + Recharts + Lucide icons.
Bundle into a single HTML file, embed via go:embed.

### Pages

1. **Overview** — health badge, metric cards (requests, error rate, p95, goroutines, heap, alerts), throughput chart, error timeline, top routes table, recent errors, live activity feed
2. **Routes** — sortable table (method, path, requests, p50/p95/p99, error rate, RPM, trend), click → route detail
3. **Route Detail** — latency chart (p50/p95/p99 lines), status distribution, related errors, top queries, slow requests
4. **Database** — connection pool gauges, slow query log (expandable SQL), query patterns table, N+1 alerts, table hotspots bar chart
5. **Errors** — error group table (message, type, route, count, last seen, status), error timeline chart, click → detail with stack trace + request context, mute/resolve buttons
6. **Runtime** — memory chart, goroutine chart (with leak detection highlight), GC chart, CPU chart, FD chart, system info panel
7. **Health** — check cards with status/latency, click → history timeline, composite health indicator, flapping warnings, run-now button
8. **Dependencies** — dependency cards with status/latency/error rate, click → detail with charts, dependency map visualization
9. **Alerts** — active alerts list, alert history table, config editor for thresholds, notification channel config, test button
10. **Settings** — config display, editable settings (retention, thresholds), export/reset buttons, about section

### UI Components
- Time range selector (5m, 15m, 1h, 6h, 24h, 7d, custom)
- Auto-refresh toggle with interval selector
- Dark mode (default) / light mode toggle
- Responsive sidebar navigation
- Login page with JWT auth

### Color Coding
- Latency: green (<200ms), yellow (200ms-1s), red (>1s)
- Error rate: green (<1%), yellow (1-5%), red (>5%)
- Health: green (healthy), yellow (degraded), red (unhealthy)
- Trends: green arrow (improving), gray arrow (stable), red arrow (degrading)

## IMPLEMENTATION ORDER

Build in this exact order. Each step should compile and be testable.

### Step 1: Foundation
- Init go.mod with module path `github.com/MUKE-coder/pulse`
- Create all directories and files
- Define Config struct with all sub-configs and defaults
- Define all metric types (RequestMetric, QueryMetric, RuntimeMetric, ErrorRecord, etc.)
- Create Mount() skeleton that serves placeholder HTML at /pulse
- Create Pulse engine struct with lifecycle methods
- Verify: `go run main.go` → localhost:8080/pulse shows placeholder

### Step 2: Ring Buffer & Storage
- Implement generic RingBuffer[T] with Push, GetAll, GetLast, Len, Reset
- Implement SQL normalizer (strip literals, extract operation/table)
- Define Storage interface with all methods
- Implement in-memory storage backed by ring buffers and maps
- Implement percentile calculator
- Implement TimeRange helpers (Last5m, Last1h, etc.)
- Write thorough tests for ring buffer, normalizer, percentiles

### Step 3: Request Tracing Middleware
- Implement Gin middleware that captures RequestMetric for every request
- Implement ResponseWriter wrapper to capture status code and response size
- Implement trace ID generation (attach to context + response header)
- Implement path exclusion (skip /pulse/*, /favicon.ico, custom globs)
- Implement sampling (SampleRate config, always record errors/slow)
- Implement slow request detection
- Write tests and benchmark middleware overhead

### Step 4: GORM Query Plugin
- Implement gorm.Plugin with Before/After callbacks for all operations
- Capture: SQL, duration, rows affected, error, operation, table
- Implement caller tracking via runtime stack inspection
- Implement N+1 detection (group queries by normalized pattern per request)
- Implement connection pool monitoring (periodic db.DB().Stats() sampling)
- Link queries to requests via trace ID in GORM context
- Implement slow query detection
- Write tests for all operations

### Step 5: Runtime Metrics
- Implement background goroutine sampling runtime.ReadMemStats()
- Collect: memory stats, goroutine count, GC stats
- Implement goroutine leak detection (sustained growth over time)
- Implement file descriptor monitoring (Linux /proc/self/fd)
- Collect static build/system info at startup
- Write tests

### Step 6: Error Tracking
- Implement panic recovery middleware (defer/recover → ErrorRecord)
- Implement error capture from c.Errors and status codes
- Implement error classification (validation/database/timeout/panic/auth/internal)
- Implement deduplication via fingerprint (hash of message+route+method)
- Implement stack trace capture and cleaning
- Implement request context capture (headers, body, query params — redact sensitive)
- Implement mute/resolve/delete actions
- Write tests

### Step 7: Aggregation Engine
- Implement background aggregation goroutine (10s interval)
- Implement per-route stats calculation (percentiles, error rate, RPM)
- Implement time-series rollup at multiple resolutions
- Implement trend detection (compare current vs previous window)
- Implement Overview snapshot computation
- Write tests with known data, verify percentile accuracy

### Step 8: Health Check System
- Implement HealthCheck struct and registry
- Implement built-in checks: database (auto from db arg), disk, memory
- Implement convenience functions: HTTPCheck, TCPCheck, DiskCheck
- Implement periodic health runner goroutine
- Implement composite status (healthy/degraded/unhealthy)
- Implement health API endpoints: /pulse/health, /pulse/health/live, /pulse/health/ready
- Implement flapping detection
- Write tests

### Step 9: REST API
- Implement JWT auth (login/logout/verify, protect /pulse/api/*)
- Implement all API endpoints listed above
- Overview, Routes, Route Detail, Database, Errors, Runtime, Health, Dependencies, Alerts, Settings
- All endpoints accept ?range= query param for time filtering
- Return proper JSON responses with pagination where applicable
- Write API tests

### Step 10: WebSocket
- Implement WebSocket hub (gorilla/websocket)
- Implement client handler with subscribe/unsubscribe
- Implement metric broadcasting (overview every 10s, requests/errors real-time)
- Implement channel subscriptions (overview, requests, errors, health, alerts)
- Implement ping/pong heartbeat
- Write tests

### Step 11: Dashboard Core
- Set up React 18 + Vite + TailwindCSS + Recharts in pulse/ui/dashboard/
- Build app shell: sidebar nav, header, time range selector, dark/light mode
- Build login page
- Build Overview page with all cards and charts
- Build Routes list page with sortable/filterable table
- Build reusable chart components (LatencyChart, ThroughputChart, etc.)
- Build API client and usePulseWebSocket() hook
- Bundle to single HTML, embed via go:embed

### Step 12: Dashboard Detail Pages
- Build Route Detail page
- Build Database page (pool gauges, slow queries, patterns, N+1)
- Build Errors page (groups, timeline, detail with stack trace)
- Build Runtime page (memory, goroutines, GC, CPU charts)
- Build Health page (check cards, history, composite status)
- Build Dependencies page (cards, detail, map)
- Build Alerts page (active, history, config editor)
- Build Settings page

### Step 13: Alerting
- Define built-in alert rules with defaults
- Implement alert evaluation engine (background goroutine)
- Implement alert lifecycle: OK → Pending → Firing → Resolved
- Implement cooldown
- Implement Slack notifications (webhook, rich message)
- Implement email notifications (SMTP, HTML)
- Implement webhook notifications (JSON, HMAC, retry)
- Write tests

### Step 14: Dependency Monitoring
- Implement WrapHTTPClient() with instrumented transport
- Implement per-dependency metric aggregation
- Implement circuit breaker state detection (optional interface)
- Update dashboard Dependencies page with real data
- Write tests

### Step 15: Advanced Storage & Export
- Implement SQLite storage backend (modernc.org/sqlite, WAL mode)
- Implement data retention cleanup (background goroutine)
- Implement Prometheus exposition endpoint (optional)
- Implement JSON/CSV export endpoint
- Implement data reset endpoint
- Write tests

### Step 16: Demo Application
- Build realistic blog API in main.go (Users, Posts, Comments, Tags)
- GORM models with relationships, auth middleware, pagination
- Register multiple health checks (db, disk, external API)
- Wrap external HTTP clients for dependency monitoring
- Build traffic simulator: background goroutines making varied requests
  - Normal requests, slow requests, errors, external calls
- Mount Pulse with full config
- Ensure impressive first-load dashboard experience

### Step 17: Testing & Release
- Achieve 80%+ code coverage
- Integration tests: full Mount → request → storage → API → WebSocket flow
- Benchmarks: middleware <100μs, GORM plugin <50μs, ring buffer throughput
- Write comprehensive README.md (badges, quick start, screenshots, config reference)
- Write docs/ (configuration, health checks, alerting, dependencies, architecture)
- Create Makefile, CI/CD workflows
- CONTRIBUTING.md, SECURITY.md, LICENSE (MIT)
- Ensure `go get github.com/MUKE-coder/pulse/pulse` works

## QUALITY STANDARDS

- Every public function has a doc comment
- No panics — recover gracefully everywhere
- Async processing: use channels and goroutines, never block the request path
- sync.RWMutex for all shared state
- Table-driven tests for parsers and aggregators
- go vet and golangci-lint clean
- No CGo — pure Go SQLite via modernc.org/sqlite
- go:embed for all static assets
- Proper HTTP cache headers
- Graceful shutdown: stop all background goroutines on SIGINT/SIGTERM
- Ring buffers sized to avoid OOM (fixed capacity, oldest dropped)
- Minimal allocations in hot path (middleware, GORM callbacks)

## IMPORTANT NOTES

- Module path: `github.com/MUKE-coder/pulse`, users import `github.com/MUKE-coder/pulse/pulse`
- Follow the same Mount() pattern as github.com/MUKE-coder/gorm-studio and github.com/MUKE-coder/sentinel
- The GORM plugin MUST implement the standard `gorm.Plugin` interface
- The Gin middleware MUST be a standard `gin.HandlerFunc`
- WebSocket MUST use gorilla/websocket
- Dashboard MUST be a single embedded HTML file
- N+1 detection is a killer feature — make it reliable and visible in the dashboard
- The health endpoint (/pulse/health) MUST be public (no JWT required) for load balancers and k8s
- Performance overhead matters — benchmark everything in the hot path

Start with Step 1 and proceed sequentially. After each step, ensure the project compiles, tests pass, and the demo runs. Ask me if you need clarification on any requirement.
```
