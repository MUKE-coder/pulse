package pulse

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// StorageDriver represents the storage backend type.
type StorageDriver int

const (
	// Memory is the default in-memory storage backend using ring buffers.
	Memory StorageDriver = iota
	// SQLite uses modernc.org/sqlite for persistent storage.
	SQLite
)

// Config holds all configuration for Pulse.
type Config struct {
	// Prefix is the URL prefix for the dashboard (default: "/pulse").
	Prefix string

	// AppName is the application name displayed in the dashboard.
	AppName string

	// Dashboard holds authentication settings.
	Dashboard DashboardConfig

	// Storage configures the storage backend.
	Storage StorageConfig

	// Tracing configures request tracing middleware.
	Tracing TracingConfig

	// Database configures GORM query monitoring.
	Database DatabaseConfig

	// Runtime configures Go runtime metrics sampling.
	Runtime RuntimeConfig

	// Errors configures error tracking.
	Errors ErrorConfig

	// Health configures the health check system.
	Health HealthConfig

	// Alerts configures the alerting engine.
	Alerts AlertConfig

	// Prometheus configures the optional Prometheus endpoint.
	Prometheus PrometheusConfig

	// DevMode enables verbose logging and more frequent aggregation.
	DevMode bool
}

// DashboardConfig holds dashboard authentication settings.
type DashboardConfig struct {
	// Username for dashboard login (default: "admin").
	Username string
	// Password for dashboard login (default: "pulse").
	Password string
	// SecretKey is the JWT signing key. Auto-generated if empty.
	SecretKey string
}

// StorageConfig configures the storage backend.
type StorageConfig struct {
	// Driver selects the storage backend (default: Memory).
	Driver StorageDriver
	// DSN is the SQLite database path (default: "pulse.db").
	DSN string
	// RetentionHours sets data retention period (default: 24).
	RetentionHours int
}

// TracingConfig configures request tracing.
type TracingConfig struct {
	// Enabled toggles request tracing (default: true).
	Enabled *bool
	// SlowRequestThreshold flags requests slower than this (default: 1s).
	SlowRequestThreshold time.Duration
	// SampleRate controls sampling between 0.0-1.0 (default: 1.0).
	// Errors and slow requests are always recorded regardless of sample rate.
	// Use a pointer so that an explicit 0.0 is distinguishable from unset.
	SampleRate *float64
	// ExcludePaths lists glob patterns for paths to skip tracing.
	ExcludePaths []string
}

// DatabaseConfig configures GORM query monitoring.
type DatabaseConfig struct {
	// Enabled toggles database monitoring (default: true).
	Enabled *bool
	// SlowQueryThreshold flags queries slower than this (default: 200ms).
	SlowQueryThreshold time.Duration
	// DetectN1 enables N+1 query detection (default: true).
	DetectN1 *bool
	// N1Threshold is the repeated pattern count to flag as N+1 (default: 5).
	N1Threshold int
	// TrackCallers captures the source file:line for queries (default: true).
	TrackCallers *bool
}

// RuntimeConfig configures Go runtime metrics sampling.
type RuntimeConfig struct {
	// Enabled toggles runtime metrics collection (default: true).
	Enabled *bool
	// SampleInterval is the interval between runtime samples (default: 5s).
	SampleInterval time.Duration
	// LeakThreshold is the goroutine growth rate per hour to flag as leak (default: 100).
	LeakThreshold int
}

// ErrorConfig configures error tracking.
type ErrorConfig struct {
	// Enabled toggles error tracking (default: true).
	Enabled *bool
	// CaptureStackTrace enables stack trace capture (default: true).
	CaptureStackTrace *bool
	// CaptureRequestBody captures request body on errors (default: true).
	CaptureRequestBody *bool
	// MaxBodySize limits captured request body size in bytes (default: 4096).
	MaxBodySize int
}

// HealthConfig configures the health check system.
type HealthConfig struct {
	// Enabled toggles health checks (default: true).
	Enabled *bool
	// CheckInterval is the default interval between health checks (default: 30s).
	CheckInterval time.Duration
	// Timeout is the default timeout for health checks (default: 10s).
	Timeout time.Duration
}

// AlertConfig configures the alerting engine.
type AlertConfig struct {
	// Enabled toggles alerting (default: true).
	Enabled *bool
	// Cooldown prevents re-firing the same alert within this duration (default: 15m).
	Cooldown time.Duration
	// Slack configures Slack webhook notifications.
	Slack *SlackConfig
	// Email configures SMTP email notifications.
	Email *EmailConfig
	// Webhooks configures generic webhook notifications.
	Webhooks []WebhookConfig
	// Discord configures Discord webhook notifications.
	Discord *DiscordConfig
	// Rules defines custom alert rules (merged with built-in defaults).
	Rules []AlertRule
}

// SlackConfig holds Slack notification settings.
type SlackConfig struct {
	// WebhookURL is the Slack incoming webhook URL.
	WebhookURL string
	// Channel overrides the default webhook channel.
	Channel string
}

// EmailConfig holds SMTP email notification settings.
type EmailConfig struct {
	// Host is the SMTP server hostname.
	Host string
	// Port is the SMTP server port.
	Port int
	// Username for SMTP authentication.
	Username string
	// Password for SMTP authentication.
	Password string
	// From is the sender email address.
	From string
	// To is a list of recipient email addresses.
	To []string
}

// WebhookConfig holds generic webhook notification settings.
type WebhookConfig struct {
	// URL is the webhook endpoint.
	URL string
	// Secret is used for HMAC signature verification.
	Secret string
	// Headers are additional HTTP headers to include.
	Headers map[string]string
}

// DiscordConfig holds Discord notification settings.
type DiscordConfig struct {
	// WebhookURL is the Discord webhook URL.
	WebhookURL string
}

// AlertRule defines a threshold-based alert rule.
type AlertRule struct {
	// Name is the human-readable rule name.
	Name string
	// Metric is the metric to evaluate (e.g., "p95_latency", "error_rate").
	Metric string
	// Operator is the comparison operator (">", "<", ">=", "<=").
	Operator string
	// Threshold is the value to compare against.
	Threshold float64
	// Duration is how long the condition must be true before firing.
	Duration time.Duration
	// Severity is the alert severity ("critical", "warning", "info").
	Severity string
	// Route optionally limits the rule to a specific route.
	Route string
}

// PrometheusConfig configures the optional Prometheus endpoint.
type PrometheusConfig struct {
	// Enabled toggles the Prometheus endpoint (default: false).
	Enabled bool
	// Path is the endpoint path (default: "/pulse/metrics").
	Path string
}

// DefaultConfig returns a Config with sensible defaults applied.
func DefaultConfig() Config {
	return Config{
		Prefix:  "/pulse",
		AppName: "Pulse",
		Dashboard: DashboardConfig{
			Username:  "admin",
			Password:  "pulse",
			SecretKey: generateSecretKey(),
		},
		Storage: StorageConfig{
			Driver:         Memory,
			DSN:            "pulse.db",
			RetentionHours: 24,
		},
		Tracing: TracingConfig{
			Enabled:              boolPtr(true),
			SlowRequestThreshold: 1 * time.Second,
			SampleRate:           float64Ptr(1.0),
			ExcludePaths:         []string{},
		},
		Database: DatabaseConfig{
			Enabled:            boolPtr(true),
			SlowQueryThreshold: 200 * time.Millisecond,
			DetectN1:           boolPtr(true),
			N1Threshold:        5,
			TrackCallers:       boolPtr(true),
		},
		Runtime: RuntimeConfig{
			Enabled:        boolPtr(true),
			SampleInterval: 5 * time.Second,
			LeakThreshold:  100,
		},
		Errors: ErrorConfig{
			Enabled:            boolPtr(true),
			CaptureStackTrace:  boolPtr(true),
			CaptureRequestBody: boolPtr(true),
			MaxBodySize:        4096,
		},
		Health: HealthConfig{
			Enabled:       boolPtr(true),
			CheckInterval: 30 * time.Second,
			Timeout:       10 * time.Second,
		},
		Alerts: AlertConfig{
			Enabled:  boolPtr(true),
			Cooldown: 15 * time.Minute,
		},
		Prometheus: PrometheusConfig{
			Enabled: false,
			Path:    "/pulse/metrics",
		},
		DevMode: false,
	}
}

// applyDefaults merges user config with defaults, filling in zero values.
func applyDefaults(cfg Config) Config {
	defaults := DefaultConfig()

	if cfg.Prefix == "" {
		cfg.Prefix = defaults.Prefix
	}
	if cfg.AppName == "" {
		cfg.AppName = defaults.AppName
	}

	// Dashboard
	if cfg.Dashboard.Username == "" {
		cfg.Dashboard.Username = defaults.Dashboard.Username
	}
	if cfg.Dashboard.Password == "" {
		cfg.Dashboard.Password = defaults.Dashboard.Password
	}
	if cfg.Dashboard.SecretKey == "" {
		cfg.Dashboard.SecretKey = defaults.Dashboard.SecretKey
	}

	// Storage
	if cfg.Storage.DSN == "" {
		cfg.Storage.DSN = defaults.Storage.DSN
	}
	if cfg.Storage.RetentionHours == 0 {
		cfg.Storage.RetentionHours = defaults.Storage.RetentionHours
	}

	// Tracing
	if cfg.Tracing.Enabled == nil {
		cfg.Tracing.Enabled = defaults.Tracing.Enabled
	}
	if cfg.Tracing.SlowRequestThreshold == 0 {
		cfg.Tracing.SlowRequestThreshold = defaults.Tracing.SlowRequestThreshold
	}
	if cfg.Tracing.SampleRate == nil {
		cfg.Tracing.SampleRate = defaults.Tracing.SampleRate
	}

	// Database
	if cfg.Database.Enabled == nil {
		cfg.Database.Enabled = defaults.Database.Enabled
	}
	if cfg.Database.SlowQueryThreshold == 0 {
		cfg.Database.SlowQueryThreshold = defaults.Database.SlowQueryThreshold
	}
	if cfg.Database.DetectN1 == nil {
		cfg.Database.DetectN1 = defaults.Database.DetectN1
	}
	if cfg.Database.N1Threshold == 0 {
		cfg.Database.N1Threshold = defaults.Database.N1Threshold
	}
	if cfg.Database.TrackCallers == nil {
		cfg.Database.TrackCallers = defaults.Database.TrackCallers
	}

	// Runtime
	if cfg.Runtime.Enabled == nil {
		cfg.Runtime.Enabled = defaults.Runtime.Enabled
	}
	if cfg.Runtime.SampleInterval == 0 {
		cfg.Runtime.SampleInterval = defaults.Runtime.SampleInterval
	}
	if cfg.Runtime.LeakThreshold == 0 {
		cfg.Runtime.LeakThreshold = defaults.Runtime.LeakThreshold
	}

	// Errors
	if cfg.Errors.Enabled == nil {
		cfg.Errors.Enabled = defaults.Errors.Enabled
	}
	if cfg.Errors.CaptureStackTrace == nil {
		cfg.Errors.CaptureStackTrace = defaults.Errors.CaptureStackTrace
	}
	if cfg.Errors.CaptureRequestBody == nil {
		cfg.Errors.CaptureRequestBody = defaults.Errors.CaptureRequestBody
	}
	if cfg.Errors.MaxBodySize == 0 {
		cfg.Errors.MaxBodySize = defaults.Errors.MaxBodySize
	}

	// Health
	if cfg.Health.Enabled == nil {
		cfg.Health.Enabled = defaults.Health.Enabled
	}
	if cfg.Health.CheckInterval == 0 {
		cfg.Health.CheckInterval = defaults.Health.CheckInterval
	}
	if cfg.Health.Timeout == 0 {
		cfg.Health.Timeout = defaults.Health.Timeout
	}

	// Alerts
	if cfg.Alerts.Enabled == nil {
		cfg.Alerts.Enabled = defaults.Alerts.Enabled
	}
	if cfg.Alerts.Cooldown == 0 {
		cfg.Alerts.Cooldown = defaults.Alerts.Cooldown
	}

	// Prometheus
	if cfg.Prometheus.Path == "" {
		cfg.Prometheus.Path = defaults.Prometheus.Path
	}

	return cfg
}

func boolPtr(b bool) *bool {
	return &b
}

func boolValue(b *bool) bool {
	if b == nil {
		return false
	}
	return *b
}

func float64Ptr(f float64) *float64 {
	return &f
}

func float64Value(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

func generateSecretKey() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// Fallback — should never happen
		return "pulse-default-secret-key-change-me"
	}
	return hex.EncodeToString(b)
}
