package pulse

import (
	"net/http"
	"time"
)

// WrapHTTPClient instruments an http.Client with dependency monitoring.
// The name parameter identifies the dependency (e.g., "stripe-api", "sendgrid").
// The returned client can be used as a drop-in replacement.
//
// Usage:
//
//	client := pulse.WrapHTTPClient(p, &http.Client{}, "stripe-api")
//	resp, err := client.Do(req)
func WrapHTTPClient(p *Pulse, client *http.Client, name string) *http.Client {
	if client == nil {
		client = &http.Client{}
	}

	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}

	wrapped := &http.Client{
		Transport:     newInstrumentedTransport(p, transport, name),
		CheckRedirect: client.CheckRedirect,
		Jar:           client.Jar,
		Timeout:       client.Timeout,
	}

	return wrapped
}

// instrumentedTransport wraps an http.RoundTripper to capture per-request metrics.
type instrumentedTransport struct {
	pulse     *Pulse
	wrapped   http.RoundTripper
	name      string
}

// newInstrumentedTransport creates a new instrumented transport.
func newInstrumentedTransport(p *Pulse, rt http.RoundTripper, name string) *instrumentedTransport {
	return &instrumentedTransport{
		pulse:   p,
		wrapped: rt,
		name:    name,
	}
}

// RoundTrip implements http.RoundTripper.
func (t *instrumentedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()

	resp, err := t.wrapped.RoundTrip(req)

	latency := time.Since(start)

	metric := DependencyMetric{
		Name:        t.name,
		Method:      req.Method,
		URL:         req.URL.String(),
		Latency:     latency,
		RequestSize: req.ContentLength,
		Timestamp:   start,
	}

	if err != nil {
		metric.Error = err.Error()
	} else {
		metric.StatusCode = resp.StatusCode
		metric.ResponseSize = resp.ContentLength
	}

	// Store asynchronously
	go func() {
		if storeErr := t.pulse.storage.StoreDependencyMetric(metric); storeErr != nil && t.pulse.config.DevMode {
			t.pulse.logger.Printf("[pulse] failed to store dependency metric: %v", storeErr)
		}
	}()

	return resp, err
}

// CircuitBreaker is an optional interface that wrapped transports can implement
// to expose circuit breaker state to Pulse.
type CircuitBreaker interface {
	State() string // "open", "closed", "half-open"
}
