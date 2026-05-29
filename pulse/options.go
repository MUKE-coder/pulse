package pulse

import "time"

// Option configures Pulse during Mount. Options are applied in order over a
// base [DefaultConfig], so later options override earlier ones.
//
// Use the granular With* helpers for the common knobs, or fall back to
// [WithConfig] when you already have a fully constructed [Config] struct
// (for example because it was loaded from YAML).
type Option func(*Config)

// WithConfig replaces the working config wholesale. This is the escape hatch
// for callers that already produce a [Config] from another source. Subsequent
// options applied after this one still mutate the same struct.
func WithConfig(c Config) Option {
	return func(cfg *Config) { *cfg = c }
}

// --- Top-level ---

// WithAppName sets the application name displayed in the dashboard.
func WithAppName(name string) Option {
	return func(c *Config) { c.AppName = name }
}

// WithPrefix overrides the URL prefix Pulse mounts under (default "/pulse").
func WithPrefix(prefix string) Option {
	return func(c *Config) { c.Prefix = prefix }
}

// WithDevMode enables verbose logging and faster background cycles.
func WithDevMode() Option {
	return func(c *Config) { c.DevMode = true }
}

// --- Dashboard / auth ---

// WithCredentials sets the dashboard username and password. Strongly preferred
// over relying on the shipped defaults: in production (DevMode=false) Pulse
// refuses to start while the defaults are still in use.
func WithCredentials(username, password string) Option {
	return func(c *Config) {
		c.Dashboard.Username = username
		c.Dashboard.Password = password
	}
}

// WithSecretKey sets the JWT signing key directly. Prefer [WithSecretKeyFile]
// in production so the secret does not have to live in source.
func WithSecretKey(key string) Option {
	return func(c *Config) { c.Dashboard.SecretKey = key }
}

// WithSecretKeyFile sets the path Pulse uses to load (or persist) the JWT
// signing key. See resolveSecretKey for the precedence rules.
func WithSecretKeyFile(path string) Option {
	return func(c *Config) { c.Dashboard.SecretKeyFile = path }
}

// WithAllowDefaultCredentials lets Pulse start in production while the
// dashboard is still using the shipped defaults. Only use this when access
// to /pulse is already protected at the network layer.
func WithAllowDefaultCredentials() Option {
	return func(c *Config) { c.Dashboard.AllowDefaultCredentials = true }
}

// WithLoginRateLimit caps successful + failed login attempts per client IP
// per minute. Zero disables rate limiting.
func WithLoginRateLimit(perMinute int) Option {
	return func(c *Config) { c.Dashboard.LoginRateLimit = perMinute }
}

// --- Storage / retention ---

// WithRetention sets how many hours of data Pulse keeps before the retention
// sweeper drops it.
func WithRetention(hours int) Option {
	return func(c *Config) { c.Storage.RetentionHours = hours }
}

// --- Tracing ---

// WithTracingDisabled turns off the request-tracing middleware entirely.
func WithTracingDisabled() Option {
	return func(c *Config) { c.Tracing.Enabled = boolPtr(false) }
}

// WithSlowRequestThreshold flags requests slower than the given duration.
func WithSlowRequestThreshold(d time.Duration) Option {
	return func(c *Config) { c.Tracing.SlowRequestThreshold = d }
}

// WithSampleRate sets the request sampling rate (0.0–1.0). Errors and slow
// requests are always captured regardless of sample rate.
func WithSampleRate(rate float64) Option {
	return func(c *Config) { c.Tracing.SampleRate = float64Ptr(rate) }
}

// WithExcludePaths appends one or more glob patterns to the tracing exclusion
// list. Multiple calls accumulate.
func WithExcludePaths(paths ...string) Option {
	return func(c *Config) {
		c.Tracing.ExcludePaths = append(c.Tracing.ExcludePaths, paths...)
	}
}

// --- Database ---

// WithDatabaseMonitoringDisabled turns off the GORM plugin entirely.
func WithDatabaseMonitoringDisabled() Option {
	return func(c *Config) { c.Database.Enabled = boolPtr(false) }
}

// WithSlowQueryThreshold flags queries slower than the given duration.
func WithSlowQueryThreshold(d time.Duration) Option {
	return func(c *Config) { c.Database.SlowQueryThreshold = d }
}

// WithN1Threshold sets how many repeated query patterns within a single
// request trigger an N+1 detection.
func WithN1Threshold(n int) Option {
	return func(c *Config) { c.Database.N1Threshold = n }
}

// --- Runtime ---

// WithRuntimeSampleInterval sets the interval between runtime metric samples.
func WithRuntimeSampleInterval(d time.Duration) Option {
	return func(c *Config) { c.Runtime.SampleInterval = d }
}

// WithGoroutineLeakThreshold sets the goroutine growth rate (per hour) that
// triggers a leak alert.
func WithGoroutineLeakThreshold(n int) Option {
	return func(c *Config) { c.Runtime.LeakThreshold = n }
}

// --- Errors ---

// WithRequestBodyCaptureDisabled turns off the body capture on errors. Use
// this if you handle sensitive payloads not covered by the built-in
// redactor.
func WithRequestBodyCaptureDisabled() Option {
	return func(c *Config) { c.Errors.CaptureRequestBody = boolPtr(false) }
}

// WithMaxBodySize sets the cap on captured request body size in bytes.
func WithMaxBodySize(bytes int) Option {
	return func(c *Config) { c.Errors.MaxBodySize = bytes }
}

// --- Health ---

// WithHealthCheckInterval sets the default interval for registered health
// checks. Individual checks may override it per [HealthCheck.Interval].
func WithHealthCheckInterval(d time.Duration) Option {
	return func(c *Config) { c.Health.CheckInterval = d }
}

// WithHealthCheckTimeout sets the default timeout for registered health
// checks.
func WithHealthCheckTimeout(d time.Duration) Option {
	return func(c *Config) { c.Health.Timeout = d }
}

// --- Alerts ---

// WithAlertCooldown sets the per-rule cooldown between firings.
func WithAlertCooldown(d time.Duration) Option {
	return func(c *Config) { c.Alerts.Cooldown = d }
}

// WithAlertRule appends a single alert rule. Multiple calls accumulate.
func WithAlertRule(rule AlertRule) Option {
	return func(c *Config) { c.Alerts.Rules = append(c.Alerts.Rules, rule) }
}

// WithSlackAlerts wires Slack as a notification channel.
func WithSlackAlerts(webhookURL string) Option {
	return func(c *Config) {
		c.Alerts.Slack = &SlackConfig{WebhookURL: webhookURL}
	}
}

// WithDiscordAlerts wires Discord as a notification channel.
func WithDiscordAlerts(webhookURL string) Option {
	return func(c *Config) {
		c.Alerts.Discord = &DiscordConfig{WebhookURL: webhookURL}
	}
}

// WithEmailAlerts wires SMTP email as a notification channel.
func WithEmailAlerts(cfg EmailConfig) Option {
	return func(c *Config) { c.Alerts.Email = &cfg }
}

// WithWebhookAlerts appends a generic webhook notification channel. Multiple
// calls accumulate.
func WithWebhookAlerts(hook WebhookConfig) Option {
	return func(c *Config) {
		c.Alerts.Webhooks = append(c.Alerts.Webhooks, hook)
	}
}

// --- Prometheus ---

// WithPrometheus enables the Prometheus exposition endpoint at the default
// path. Use [WithPrometheusPath] to override.
func WithPrometheus() Option {
	return func(c *Config) { c.Prometheus.Enabled = true }
}

// WithPrometheusPath enables Prometheus at a custom path.
func WithPrometheusPath(path string) Option {
	return func(c *Config) {
		c.Prometheus.Enabled = true
		c.Prometheus.Path = path
	}
}

// --- SLOs ---

// WithSLO appends a single SLO to track. Multiple calls accumulate.
//
// If the SLO's BurnRateAlerts is nil, [DefaultBurnRateAlerts] is applied at
// evaluation time.
func WithSLO(slo SLO) Option {
	return func(c *Config) { c.SLOs = append(c.SLOs, slo) }
}
