package pulse

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func setupDepPulse(t *testing.T) *Pulse {
	t.Helper()
	cfg := applyDefaults(Config{
		Health:  HealthConfig{Enabled: boolPtr(false)},
		Runtime: RuntimeConfig{Enabled: boolPtr(false)},
		Tracing: TracingConfig{Enabled: boolPtr(false)},
		Errors:  ErrorConfig{Enabled: boolPtr(false)},
		Alerts:  AlertConfig{Enabled: boolPtr(false)},
	})
	p := newPulse(cfg)
	p.storage = NewMemoryStorage("test")
	t.Cleanup(func() { p.Shutdown() })
	return p
}

func TestWrapHTTPClient_CapturesMetrics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello"))
	}))
	defer server.Close()

	p := setupDepPulse(t)
	client := WrapHTTPClient(p, &http.Client{}, "test-api")

	resp, err := client.Get(server.URL + "/test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// Wait for async storage
	time.Sleep(100 * time.Millisecond)

	stats, _ := p.storage.GetDependencyStats(Last1h())
	if len(stats) == 0 {
		t.Fatal("expected dependency stats to be recorded")
	}

	if stats[0].Name != "test-api" {
		t.Errorf("expected name 'test-api', got %q", stats[0].Name)
	}
	if stats[0].RequestCount != 1 {
		t.Errorf("expected 1 request, got %d", stats[0].RequestCount)
	}
	if stats[0].ErrorCount != 0 {
		t.Errorf("expected 0 errors, got %d", stats[0].ErrorCount)
	}
}

func TestWrapHTTPClient_CapturesErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("error"))
	}))
	defer server.Close()

	p := setupDepPulse(t)
	client := WrapHTTPClient(p, &http.Client{}, "failing-api")

	resp, err := client.Get(server.URL + "/fail")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	time.Sleep(100 * time.Millisecond)

	stats, _ := p.storage.GetDependencyStats(Last1h())
	if len(stats) == 0 {
		t.Fatal("expected dependency stats")
	}

	if stats[0].ErrorCount != 1 {
		t.Errorf("expected 1 error, got %d", stats[0].ErrorCount)
	}
	if stats[0].ErrorRate != 100 {
		t.Errorf("expected 100%% error rate, got %.1f%%", stats[0].ErrorRate)
	}
}

func TestWrapHTTPClient_CapturesConnectionErrors(t *testing.T) {
	p := setupDepPulse(t)
	// Use a client with a very short timeout pointing at a non-existent server
	client := WrapHTTPClient(p, &http.Client{Timeout: 100 * time.Millisecond}, "unreachable")

	_, err := client.Get("http://127.0.0.1:1") // port 1 should fail
	if err == nil {
		t.Fatal("expected connection error")
	}

	time.Sleep(100 * time.Millisecond)

	stats, _ := p.storage.GetDependencyStats(Last1h())
	if len(stats) == 0 {
		t.Fatal("expected dependency stats even on error")
	}

	if stats[0].Name != "unreachable" {
		t.Errorf("expected name 'unreachable', got %q", stats[0].Name)
	}
}

func TestWrapHTTPClient_MultipleDependencies(t *testing.T) {
	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server2.Close()

	p := setupDepPulse(t)

	client1 := WrapHTTPClient(p, &http.Client{}, "stripe")
	client2 := WrapHTTPClient(p, &http.Client{}, "sendgrid")

	// Make requests to both
	for i := 0; i < 3; i++ {
		resp, _ := client1.Get(server1.URL)
		if resp != nil {
			resp.Body.Close()
		}
	}

	for i := 0; i < 5; i++ {
		resp, _ := client2.Get(server2.URL)
		if resp != nil {
			resp.Body.Close()
		}
	}

	time.Sleep(100 * time.Millisecond)

	stats, _ := p.storage.GetDependencyStats(Last1h())
	if len(stats) != 2 {
		t.Fatalf("expected 2 dependencies, got %d", len(stats))
	}

	// Stats sorted by request count descending
	if stats[0].Name != "sendgrid" {
		t.Errorf("expected sendgrid first (5 requests), got %q", stats[0].Name)
	}
	if stats[0].RequestCount != 5 {
		t.Errorf("expected 5 requests for sendgrid, got %d", stats[0].RequestCount)
	}
	if stats[1].Name != "stripe" {
		t.Errorf("expected stripe second (3 requests), got %q", stats[1].Name)
	}
	if stats[1].RequestCount != 3 {
		t.Errorf("expected 3 requests for stripe, got %d", stats[1].RequestCount)
	}
}

func TestWrapHTTPClient_NilClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p := setupDepPulse(t)

	// Passing nil client should work (creates a default one)
	client := WrapHTTPClient(p, nil, "nil-test")

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	time.Sleep(100 * time.Millisecond)

	stats, _ := p.storage.GetDependencyStats(Last1h())
	if len(stats) == 0 {
		t.Fatal("expected stats from nil client wrapper")
	}
}

func TestWrapHTTPClient_PreservesClientConfig(t *testing.T) {
	p := setupDepPulse(t)

	original := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	wrapped := WrapHTTPClient(p, original, "config-test")

	if wrapped.Timeout != 30*time.Second {
		t.Errorf("expected timeout 30s, got %v", wrapped.Timeout)
	}
	if wrapped.CheckRedirect == nil {
		t.Error("expected CheckRedirect to be preserved")
	}
}

func TestWrapHTTPClient_LatencyMeasurement(t *testing.T) {
	delay := 50 * time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p := setupDepPulse(t)
	client := WrapHTTPClient(p, &http.Client{}, "latency-test")

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	time.Sleep(100 * time.Millisecond)

	stats, _ := p.storage.GetDependencyStats(Last1h())
	if len(stats) == 0 {
		t.Fatal("expected dependency stats")
	}

	if stats[0].AvgLatency < delay {
		t.Errorf("expected latency >= %v, got %v", delay, stats[0].AvgLatency)
	}
}

func TestWrapHTTPClient_MixedResponses(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount%3 == 0 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p := setupDepPulse(t)
	client := WrapHTTPClient(p, &http.Client{}, "mixed-api")

	for i := 0; i < 6; i++ {
		resp, _ := client.Get(server.URL)
		if resp != nil {
			resp.Body.Close()
		}
	}

	time.Sleep(100 * time.Millisecond)

	stats, _ := p.storage.GetDependencyStats(Last1h())
	if len(stats) == 0 {
		t.Fatal("expected stats")
	}

	if stats[0].RequestCount != 6 {
		t.Errorf("expected 6 requests, got %d", stats[0].RequestCount)
	}
	if stats[0].ErrorCount != 2 {
		t.Errorf("expected 2 errors, got %d", stats[0].ErrorCount)
	}

	expectedRate := float64(2) / float64(6) * 100
	if stats[0].ErrorRate < expectedRate-0.1 || stats[0].ErrorRate > expectedRate+0.1 {
		t.Errorf("expected error rate ~%.1f%%, got %.1f%%", expectedRate, stats[0].ErrorRate)
	}

	if stats[0].Availability < 66 || stats[0].Availability > 67 {
		t.Errorf("expected availability ~66.7%%, got %.1f%%", stats[0].Availability)
	}
}

func TestWrapHTTPClient_PercentileComputation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p := setupDepPulse(t)
	client := WrapHTTPClient(p, &http.Client{}, "percentile-test")

	// Make enough requests for meaningful percentiles
	for i := 0; i < 20; i++ {
		resp, _ := client.Get(server.URL)
		if resp != nil {
			resp.Body.Close()
		}
	}

	time.Sleep(100 * time.Millisecond)

	stats, _ := p.storage.GetDependencyStats(Last1h())
	if len(stats) == 0 {
		t.Fatal("expected stats")
	}

	if stats[0].AvgLatency <= 0 {
		t.Error("expected non-zero avg latency")
	}
	// Percentiles may be zero for very fast local requests, but should be valid
	if stats[0].P50Latency > stats[0].P95Latency {
		t.Error("expected p50 <= p95")
	}
	if stats[0].P95Latency > stats[0].P99Latency {
		t.Error("expected p95 <= p99")
	}
	if stats[0].RPM <= 0 {
		t.Error("expected non-zero RPM")
	}
}
