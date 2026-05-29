# Changelog

All notable changes to **Pulse** are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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

- **v0.3.0** — [#1](https://github.com/MUKE-coder/pulse/issues/1) SLO objects
  and burn-rate alerts.
- **v0.4.0** — [#2](https://github.com/MUKE-coder/pulse/issues/2) N+1 detector
  enhancements + [#3](https://github.com/MUKE-coder/pulse/issues/3) USE
  method dashboard.
- **v0.5.0** — [#4](https://github.com/MUKE-coder/pulse/issues/4) k6 test-run
  overlay and pprof flame graph.
- **v1.0.0** — Persistent SQLite storage backend, embedded dashboard ported to
  Tailwind, public API freeze.

[0.2.0]: https://github.com/MUKE-coder/pulse/releases/tag/v0.2.0
[0.1.0]: https://github.com/MUKE-coder/pulse/releases/tag/v0.1.0
