package pulse

import "time"

// Storage defines the interface for all metric storage backends.
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
