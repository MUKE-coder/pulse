# Changelog

All notable changes to **Pulse** are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [0.5.0] — 2026-05-29

Closes [#4](https://github.com/MUKE-coder/pulse/issues/4) — k6 test-run
timeline overlay + flame graph from sampled stack traces. Additive; no
breaking changes.

### Added — k6 test-run overlay

- **`pulse.TestRun`** type capturing a synthetic load-test execution
  (k6, Vegeta, custom harness). Stored in MemoryStorage with a 1000-record
  cap; subject to the same retention sweeper as everything else.
- **`POST /pulse/api/test-runs`** (auth-required) accepts the JSON shape
  from the issue:
  ```json
  { "name": "average-load", "type": "k6.average-load",
    "started_at": "...", "ended_at": "...",
    "metadata": { "vus_peak": 100, "thresholds_passed": true } }
  ```
  Pulse assigns an `id` when none is supplied and defaults `started_at` to
  `time.Now()`. Returns `201` with the canonical record.
- **`GET /pulse/api/test-runs?range=…`** (auth-required) returns runs whose
  window overlaps the requested range, so the dashboard can render them as
  vertical bands on the timeline charts.
- **k6 bridge helper** at
  [`examples/k6/pulse-k6-bridge.js`](examples/k6/pulse-k6-bridge.js) wrapping
  the three calls (`pulseAuth`, `pulseStartRun`, `pulseEndRun`). Drop into
  any k6 script — usage in [`examples/k6/example-test.js`](examples/k6/example-test.js).

### Added — Flame graph from sampled stack traces

- **`pulse.WithProfiling()`** option + **`Config.Profiling`** block. Profile
  sampling is **off by default** and **double-gated**: both the config flag
  and the `PULSE_PROFILE_ENABLED` env var must be truthy for sampling to
  run. Either alone returns `503` with a hint at the missing gate. CPU
  profiles leak code structure, so a rogue config edit alone cannot enable
  them in production.
- **`pulse.profileSampler`** uses `runtime.GoroutineProfile` rather than
  pulling in `github.com/google/pprof` for protobuf parsing — keeps the
  binary small and the implementation auditable. Only one sample window
  runs at a time; concurrent requests get `409 Conflict`.
- **`pulse.FlameGraphSVG()`** renders the folded sample tree as an
  interactive SVG flame graph (Brendan Gregg layout, deterministic colour
  per frame, sortable, tooltips). No Perl, no external tooling.
- **`pulse.Folded()`** emits the standard `frame;frame;frame count` format
  so operators can pipe Pulse profiles into existing diff-flame tooling.
- **`GET /pulse/api/profile/flamegraph`** — returns SVG by default; pass
  `?format=json` for the tree, `?duration=…&hz=…` to override the sample
  window, `?cache=true` to reuse the last sample if it's <60s old.
- **`GET /pulse/api/profile/folded`** — returns the folded-stack text
  format for pipelining.

### Why GoroutineProfile (not StartCPUProfile + pprof parser)

`runtime.GoroutineProfile` gives us the call stack of every live goroutine
without needing to parse the pprof protobuf. Pulse treats each frame as one
sample. The resulting flame graph emphasises hot code paths rather than
literal CPU time — which, when chasing a regression, is usually what
operators want. It also keeps the dependency footprint zero: no
`google/pprof` tree pulled into every consumer's binary.

### Storage interface

`Storage` gains `StoreTestRun(TestRun) error` and `GetTestRuns(TimeRange)
([]TestRun, error)`. Backwards-compatible at the consumer level since
`MemoryStorage` is the only implementation.

### Migration notes

Both additions are zero-config. Test-run endpoints start working
immediately; the flame graph endpoint requires both opt-ins before it
will sample. See the issue ([#4](https://github.com/MUKE-coder/pulse/issues/4))
for the operational rationale on the gating.

---

## [0.4.0] — 2026-05-29

Closes [#2](https://github.com/MUKE-coder/pulse/issues/2) (N+1 detector
enhancements) and [#3](https://github.com/MUKE-coder/pulse/issues/3)
(USE-method dashboard). No breaking changes.

### Added

**N+1 detector (issue #2):**

- Each `N1Detection` now carries the matched **route** (e.g. `GET /api/orders`)
  alongside the trace ID, so the dashboard can point directly at the
  offending handler. The route is propagated from the tracing middleware via
  the new `pulse.ContextWithRoute` / `pulse.RouteFromContext` helpers.
- Each `N1Detection` now carries a `SuggestedFix` string with a one-sentence
  hint at the likely root cause:
  - `WHERE id = ?` → use `Preload` / `Joins`
  - `WHERE x_id = ?` → batch-load with `Preload` or an `IN (?)` clause
  - `LIMIT 1` → looks like `.First(...)` in a loop
  - `SELECT count(*)` → aggregate with `GROUP BY` instead
  - `SELECT 1 / SELECT EXISTS` → replace per-row check with a join
  Match logic lives in `suggestN1Fix` ([pulse/n1.go](pulse/n1.go)) and is
  intentionally conservative — if no shape matches, a generic fallback nudge
  is returned.
- New `N1Detection.AvgDuration` field for the per-query average duration of
  the burst.
- **`GET /pulse/api/database/n1/ranked`** — new authenticated endpoint
  returning N+1 findings grouped by `(route, normalised SQL)` and sorted by
  an **impact score** equal to `occurrences × queries-per-occurrence × avg
  query duration`. This is the "fix-this-first" ordering the issue calls
  out. Accepts `?range=` and `?limit=`. Response shape: `[]N1Ranking`.
- N+1 dev-mode log lines now include the handler route and the suggested
  fix, so the error is actionable at log-tail time.

**USE-method dashboard (issue #3):**

- New host-resource sampler ([pulse/use.go](pulse/use.go)) implementing
  Brendan Gregg's [USE method](https://brendangregg.com/usemethod.html).
  Every tick (5 s, or 2 s in DevMode) emits a `USESnapshot` covering six
  resources — **CPU**, **Memory**, **Disk**, **Network**, **DB pool**,
  **Goroutines** — each with a **Utilization / Saturation / Errors** triple.
  Cells are colour-banded (`green` / `amber` / `red` / `unknown`) so the
  dashboard rendering can be a single glance.
- Host metrics are sourced via
  [gopsutil](https://github.com/shirou/gopsutil/v4) (CPU%, load average,
  memory, disk usage and I/O, network counters). DB-pool, goroutine, and GC
  cells reuse data Pulse already collects, so they work even when host
  metrics are unavailable (containers with restricted `/proc`, etc.). Cells
  that the host can't expose render as "unknown" rather than failing.
- **`GET /pulse/api/use`** — new authenticated endpoint returning the
  current `USESnapshot`. Returns an empty snapshot (not 404) when the
  sampler is disabled, so the dashboard can render a single empty-state
  message.
- **`pulse.WithUSEDisabled()`** option (and `Config.USE.Enabled *bool`) for
  callers running in environments where gopsutil shouldn't be called at all
  (minimal-permission containers, scratch images, etc.).
- New dependency: `github.com/shirou/gopsutil/v4 v4.26.4`.

### Changed

- `pulse.Config` gains a `USE USEConfig` field. Zero value defaults to
  enabled (matching `WithUSEDisabled()` being opt-out, not opt-in).
- `N1Detection` JSON now includes `route`, `avg_duration`, and
  `suggested_fix` (the last one is omitted when empty).

### Migration notes

Both additions are zero-config — if you already use `pulse.Mount(ctx, router,
db, ...)`, you'll see `/pulse/api/use` and `/pulse/api/database/n1/ranked`
start working immediately. The N+1 detector continues to populate
`/pulse/api/database/n1` with the existing payload shape, plus the new
fields.

---

## [0.3.0] — 2026-05-29

Implements [#1](https://github.com/MUKE-coder/pulse/issues/1) — SLO objects
with multi-window burn-rate alerts. This is purely additive; no breaking
changes.

### Added

- **`pulse.SLO` type** declares a service-level objective: a target
  compliance ratio, a sliding window, and an `SLI` that decides which events
  count as "good". Two SLIs ship today — both match routes with
  `filepath.Match` globs and are scope-aware:
  - **`pulse.SLIErrorRate`** — non-5xx responses are good. (4xx counts as
    good because client errors don't burn server SLOs.)
  - **`pulse.SLILatency{Threshold}`** — non-5xx responses below the latency
    threshold are good.
- **`pulse.BurnRateAlert`** declares one window-and-multiple alert rule
  attached to an SLO. **`pulse.DefaultBurnRateAlerts()`** returns the
  multi-window set recommended by Google's SRE workbook: a fast-burn
  page (1h window, 14.4× burn rate, critical) and a slow-burn ticket
  (6h window, 6× burn rate, warning). When `SLO.BurnRateAlerts` is nil the
  defaults are applied at evaluation time.
- **`pulse.WithSLO(SLO)` option** appends an SLO to the config.
- **SLO evaluator goroutine.** Tied to the Mount ctx like every other Pulse
  background. Ticks every 30 s (10 s in DevMode), scans the request ring
  buffer once across the widest configured window, then evaluates each SLO
  + each burn-rate sub-window in memory. Burn-rate transitions go through
  the existing alert pipeline, so they land in `/pulse/api/alerts` and fire
  Slack / Discord / Email / Webhook notifications just like rule-based
  alerts.
- **`GET /pulse/api/slos`** — authenticated endpoint returning the live
  `SLOStatus` for every configured SLO, including per-window burn rate and
  budget remaining/consumed percentages. Returns `[]` when no SLOs are
  configured.
- New tests: `pulse/slo_test.go` covers SLI classification, budget-consumed
  math, end-to-end fire/resolve over two evaluator ticks, and the API
  endpoint round-trip.

### Changed

- `Config` gains a `SLOs []SLO` field. Zero-value (empty slice or `nil`)
  preserves the previous behaviour — the evaluator is not started.

### Why burn rate, not raw error rate?

Burn rate normalises against the SLO's own error budget. A 0.5% error rate is
catastrophic for a 99.99% SLO (50× burn) and unremarkable for a 90% SLO
(0.05× burn). Same number, totally different operational meaning. See
[Google's "Alerting on SLOs"](https://sre.google/workbook/alerting-on-slos/)
for the full argument.

### Example

```go
ctx := context.Background()
p := pulse.Mount(ctx, router, db,
    pulse.WithAppName("Checkout"),
    pulse.WithSlackAlerts(os.Getenv("SLACK_WEBHOOK_URL")),
    pulse.WithSLO(pulse.SLO{
        Name:      "API availability",
        Target:    0.999,             // 99.9% over 28 days
        Window:    28 * 24 * time.Hour,
        Indicator: pulse.SLIErrorRate{Routes: []string{"/api/*"}},
        // BurnRateAlerts: nil → DefaultBurnRateAlerts() applied automatically
    }),
    pulse.WithSLO(pulse.SLO{
        Name:      "API latency",
        Target:    0.95,              // 95% under 500ms over 30 days
        Window:    30 * 24 * time.Hour,
        Indicator: pulse.SLILatency{Routes: []string{"/api/*"}, Threshold: 500 * time.Millisecond},
    }),
)
```

---

## [0.2.0] — 2026-05-29

API ergonomics + OpenTelemetry interop. **This release contains breaking
changes** to the public `Mount` signature; the upgrade is small and
mechanical (see [Migrating from v0.1.0](#migrating-from-v010) below).

### Added

- **Functional-options API ([#10](https://github.com/MUKE-coder/pulse/issues/10) from the v0.1.0 review).**
  A new `Option` type and a family of `With*` helpers (`WithAppName`,
  `WithDevMode`, `WithCredentials`, `WithSecretKeyFile`, `WithSlackAlerts`,
  `WithPrometheus`, `WithSlowRequestThreshold`, `WithSampleRate`, …) replace
  the previous variadic `Config` argument. For callers that already build a
  full `Config` (e.g. from YAML), `WithConfig(Config) Option` is the escape
  hatch. See [pulse/options.go](pulse/options.go) for the full list.
- **`context.Context` on `Mount` ([#15](https://github.com/MUKE-coder/pulse/issues/15) from the v0.1.0 review).**
  `Mount` now takes a parent `context.Context` as its first argument. All
  Pulse background goroutines (runtime sampler, retention sweeper, alert
  engine, health runner, websocket hub) are tied to that context, so
  cancelling it shuts Pulse down cleanly. `Pulse.Shutdown()` remains valid
  but is now optional when the caller already manages a root context.
- **W3C Trace Context interop ([#7](https://github.com/MUKE-coder/pulse/issues/7) from the v0.1.0 review).**
  Pulse now reads the standard `traceparent` header on inbound requests
  and adopts its `trace-id` as the Pulse trace ID, so traces continue across
  service boundaries. Outbound requests sent through `WrapHTTPClient` get a
  `traceparent` header injected with the current trace ID. Responses always
  carry both `X-Pulse-Trace-ID` (legacy) and `traceparent` (W3C).
- New public helpers: `pulse.GenerateSpanID()`, `pulse.ParseTraceparent()`,
  `pulse.BuildTraceparent()`, and the `pulse.TraceparentHeader` constant.

### Changed

- **BREAKING:** `Mount(router, db, cfg)` → `Mount(ctx, router, db, opts...)`.
  See [Migrating from v0.1.0](#migrating-from-v010).
- Inbound request tracing now uses the upstream `traceparent` `trace-id`
  when present and valid, instead of always generating a new ID. This is a
  no-op for callers that don't talk to upstream services, but it makes
  Pulse work alongside OTel-instrumented systems.
- `examples/full/main.go` and `examples/with-sentinel/main.go` continue to
  use `Config` via `WithConfig(...)` to demonstrate the escape hatch.
- `examples/basic/main.go` and `test-app/main.go` are migrated to the
  granular `With*` style.

### Migrating from v0.1.0

Smallest possible upgrade — wrap your existing `Config` in `WithConfig`:

```go
// v0.1.0
p := pulse.Mount(router, db, pulse.Config{AppName: "Blog API", DevMode: true})

// v0.2.0
p := pulse.Mount(ctx, router, db, pulse.WithConfig(pulse.Config{
    AppName: "Blog API",
    DevMode: true,
}))
```

Idiomatic upgrade — switch to granular options:

```go
p := pulse.Mount(ctx, router, db,
    pulse.WithAppName("Blog API"),
    pulse.WithDevMode(),
)
```

If you do not have a `ctx` handy and don't want to manage one, pass
`context.Background()`. To tie Pulse's lifecycle to your application's:

```go
ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
defer cancel()
p := pulse.Mount(ctx, router, db, ...)
// On SIGINT, ctx cancels and Pulse's background goroutines drain.
```

`Pulse.Shutdown()` still works; it's now redundant if you use ctx
cancellation.

---

## [0.1.0] — 2026-05-29

First tagged release. This is a security and correctness pass on top of the
initial implementation. Subsequent feature work (SLOs, USE dashboard, k6
overlay, flame graph, SQLite backend) is tracked under separate GitHub
milestones.

### Added

- **Default-credentials guard.** Pulse now refuses to start when
  `DevMode=false` and the dashboard is still configured with the shipped
  defaults (`admin` / `pulse`). Operators who deliberately rely on the
  defaults (e.g. behind a private VPN) can opt in with the new
  `Dashboard.AllowDefaultCredentials` field.
- **Persistent JWT signing key.** New `Dashboard.SecretKeyFile` option. If
  provided, Pulse loads the key from that path on startup or — when the file
  does not exist — writes a freshly generated key to it with mode `0600`. This
  prevents tokens from being invalidated on every restart. When neither
  `SecretKey` nor `SecretKeyFile` is set in production, Pulse now logs a
  warning explaining the consequence.
- **Login rate limiter.** `/pulse/api/auth/login` is now throttled per client
  IP via an in-memory token bucket. The default cap is 10 attempts per minute
  per IP; configurable via `Dashboard.LoginRateLimit` (zero disables it). The
  endpoint responds `429 Too Many Requests` with a `Retry-After: 60` header
  when the bucket is empty.
- **Request-body redaction.** Captured request bodies on errors are now scanned
  before storage. JSON and `application/x-www-form-urlencoded` bodies have
  their sensitive field values (password, token, secret, api_key, card_number,
  cvv, ssn, etc.) replaced with `[REDACTED]`. Other content types are
  unchanged. Redaction is field-name based (case-insensitive, exact match)
  and is applied at arbitrary JSON depth.
- **WebSocket authentication.** The `/pulse/ws/live` endpoint now requires the
  same JWT that protects `/pulse/api`. Browsers can supply the token via the
  `?token=<jwt>` query parameter; non-browser clients may use the
  `Sec-WebSocket-Protocol: bearer, <jwt>` subprotocol header. Unauthenticated
  upgrade requests are rejected with `401` before the handshake.
- **Retention sweeper.** A background goroutine now invokes
  `Storage.Cleanup(Storage.RetentionHours)` every minute (every 10s in
  `DevMode`). This trims the unbounded maps in `MemoryStorage`
  (error fingerprints, alert log, N+1 detections), which previously grew
  forever even though ring buffers self-trimmed.
- `Version` constant in the `pulse` package, plus `DefaultUsername` /
  `DefaultPassword` constants so callers can compare against the shipped
  defaults.

### Changed

- **Constant-time credential comparison.** `loginHandler` now compares username
  and password via `crypto/subtle.ConstantTimeCompare` instead of `!=`,
  eliminating the timing side-channel on the credential check.
- **Secret-key resolution is lazy.** `DefaultConfig()` no longer calls
  `crypto/rand` to generate a JWT signing key. Key resolution happens once
  inside `Mount()` via `resolveSecretKey(cfg.Dashboard)`, with the precedence
  documented on that function. This makes `DefaultConfig()` cheap and
  deterministic — relevant for tests that construct it in tight loops.
- **Storage config is smaller.** `StorageConfig.DSN` is gone (it was only
  meaningful for the unimplemented SQLite backend). `RetentionHours` is now
  enforced by a real background sweeper, not just documentation.
- Dashboard `useWebSocket` hook now appends the JWT to the WS URL and waits
  for the user to authenticate before connecting. Live updates therefore
  start one round-trip later than before.

### Removed

- **`pulse.SQLite` storage driver enum** (and the corresponding `Driver`
  documentation in the README). The SQLite-backed `Storage` implementation
  was advertised but never written. Removing the enum prevents silent
  fallback to `Memory` for users who set it expecting persistence.
  A real persistent backend will land in `v1.0.0` (see the
  [Roadmap](#roadmap) section in the README).
- Stale `basic.exe`, `full.exe`, `with-sentinel.exe` build artefacts at the
  repo root. (`.gitignore` already excluded them; they were just lingering.)

### Fixed

- **Confirmed (not a regression):** `c.FullPath()` is used everywhere a route
  appears as a metric label (`pulse/middleware.go`, `pulse/errors.go`,
  `pulse/prometheus.go`). This prevents the Prometheus high-cardinality
  series explosion that occurs when raw `URL.Path` is used as a label on
  routes like `/users/:id`.
- **Confirmed (not a regression):** `PulsePlugin.n1Tracker` is guarded by
  `n1TrackerMu sync.Mutex`. GORM callbacks fire from multiple goroutines
  concurrently; this prevents a data race on the per-request N+1 counter.

### Security

- Removed timing side-channel on the dashboard login.
- Removed default-credentials footgun in production.
- Removed plaintext leak of sensitive POST bodies into the error capture path.
- Closed unauthenticated WebSocket subscription channel.

---

## Roadmap

These items were scoped during earlier reviews but deferred to focused
milestones:

- **v1.0.0** — Persistent SQLite storage backend, embedded dashboard ported to
  Tailwind, public API freeze.

[0.5.0]: https://github.com/MUKE-coder/pulse/releases/tag/v0.5.0
[0.4.0]: https://github.com/MUKE-coder/pulse/releases/tag/v0.4.0
[0.3.0]: https://github.com/MUKE-coder/pulse/releases/tag/v0.3.0
[0.2.0]: https://github.com/MUKE-coder/pulse/releases/tag/v0.2.0
[0.1.0]: https://github.com/MUKE-coder/pulse/releases/tag/v0.1.0
