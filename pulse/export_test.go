package pulse

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func setupExportPulse(t *testing.T) (*Pulse, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)

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

	router := gin.New()
	group := router.Group("/pulse/api")
	registerExportRoute(group, p)

	return p, router
}

func TestExport_JSON_Requests(t *testing.T) {
	p, router := setupExportPulse(t)

	// Seed data
	for i := 0; i < 5; i++ {
		p.storage.StoreRequest(RequestMetric{
			Method: "GET", Path: "/users", StatusCode: 200,
			Latency: 10 * time.Millisecond, Timestamp: time.Now(),
		})
	}

	body := `{"format": "json", "type": "requests", "range": "1h"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/pulse/api/data/export", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	contentDisp := w.Header().Get("Content-Disposition")
	if !strings.Contains(contentDisp, "pulse_requests_") {
		t.Errorf("expected Content-Disposition with filename, got %q", contentDisp)
	}

	var data []RequestMetric
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if len(data) != 5 {
		t.Errorf("expected 5 requests, got %d", len(data))
	}
}

func TestExport_CSV_Requests(t *testing.T) {
	p, router := setupExportPulse(t)

	for i := 0; i < 3; i++ {
		p.storage.StoreRequest(RequestMetric{
			Method: "POST", Path: "/orders", StatusCode: 201,
			Latency: 20 * time.Millisecond, Timestamp: time.Now(),
		})
	}

	body := `{"format": "csv", "type": "requests", "range": "1h"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/pulse/api/data/export", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	// Parse CSV
	reader := csv.NewReader(bytes.NewReader(w.Body.Bytes()))
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("invalid CSV: %v", err)
	}

	// Header + 3 rows
	if len(records) != 4 {
		t.Errorf("expected 4 CSV rows (1 header + 3 data), got %d", len(records))
	}

	// Check header
	if records[0][0] != "method" {
		t.Errorf("expected first header 'method', got %q", records[0][0])
	}
}

func TestExport_JSON_Errors(t *testing.T) {
	p, router := setupExportPulse(t)

	p.storage.StoreError(ErrorRecord{
		ID: "e1", Fingerprint: "fp1", Method: "GET", Route: "/test",
		ErrorMessage: "something broke", ErrorType: ErrorTypeInternal,
		Count: 3, FirstSeen: time.Now(), LastSeen: time.Now(),
	})

	body := `{"format": "json", "type": "errors", "range": "1h"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/pulse/api/data/export", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var data []ErrorRecord
	json.Unmarshal(w.Body.Bytes(), &data)
	if len(data) != 1 {
		t.Errorf("expected 1 error, got %d", len(data))
	}
}

func TestExport_CSV_Errors(t *testing.T) {
	p, router := setupExportPulse(t)

	p.storage.StoreError(ErrorRecord{
		ID: "e1", Fingerprint: "fp1", Method: "POST", Route: "/users",
		ErrorMessage: "duplicate key", ErrorType: ErrorTypeDatabase,
		Count: 7, FirstSeen: time.Now(), LastSeen: time.Now(),
	})

	body := `{"format": "csv", "type": "errors", "range": "1h"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/pulse/api/data/export", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	reader := csv.NewReader(bytes.NewReader(w.Body.Bytes()))
	records, _ := reader.ReadAll()
	if len(records) != 2 { // header + 1 row
		t.Errorf("expected 2 CSV rows, got %d", len(records))
	}
}

func TestExport_JSON_Runtime(t *testing.T) {
	p, router := setupExportPulse(t)

	p.storage.StoreRuntime(RuntimeMetric{
		HeapAlloc:    10 * 1024 * 1024,
		NumGoroutine: 42,
		Timestamp:    time.Now(),
	})

	body := `{"format": "json", "type": "runtime", "range": "1h"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/pulse/api/data/export", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var data []RuntimeMetric
	json.Unmarshal(w.Body.Bytes(), &data)
	if len(data) != 1 {
		t.Errorf("expected 1 runtime metric, got %d", len(data))
	}
}

func TestExport_JSON_Alerts(t *testing.T) {
	p, router := setupExportPulse(t)

	p.storage.StoreAlert(AlertRecord{
		ID: "a1", RuleName: "test", Metric: "error_rate",
		Severity: "warning", State: AlertStateFiring,
		Message: "test", FiredAt: time.Now(),
	})

	body := `{"format": "json", "type": "alerts", "range": "1h"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/pulse/api/data/export", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestExport_InvalidType(t *testing.T) {
	_, router := setupExportPulse(t)

	body := `{"format": "json", "type": "invalid_type", "range": "1h"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/pulse/api/data/export", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestExport_InvalidFormat(t *testing.T) {
	_, router := setupExportPulse(t)

	body := `{"format": "xml", "type": "requests", "range": "1h"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/pulse/api/data/export", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestExport_InvalidBody(t *testing.T) {
	_, router := setupExportPulse(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/pulse/api/data/export", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestExport_DefaultsApplied(t *testing.T) {
	p, router := setupExportPulse(t)

	p.storage.StoreRequest(RequestMetric{
		Method: "GET", Path: "/test", StatusCode: 200,
		Latency: 5 * time.Millisecond, Timestamp: time.Now(),
	})

	// Send with empty format and range — should use defaults
	body := `{"type": "requests"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/pulse/api/data/export", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 with defaults, got %d", w.Code)
	}
}

func TestExport_CSV_Runtime(t *testing.T) {
	p, router := setupExportPulse(t)

	p.storage.StoreRuntime(RuntimeMetric{
		HeapAlloc:    5 * 1024 * 1024,
		HeapInUse:    4 * 1024 * 1024,
		NumGoroutine: 10,
		NumGC:        3,
		Timestamp:    time.Now(),
	})

	body := `{"format": "csv", "type": "runtime", "range": "1h"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/pulse/api/data/export", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	reader := csv.NewReader(bytes.NewReader(w.Body.Bytes()))
	records, _ := reader.ReadAll()
	if len(records) != 2 {
		t.Errorf("expected 2 CSV rows, got %d", len(records))
	}
	if records[0][0] != "heap_alloc" {
		t.Errorf("expected header 'heap_alloc', got %q", records[0][0])
	}
}

func TestExport_CSV_Alerts(t *testing.T) {
	p, router := setupExportPulse(t)

	now := time.Now()
	p.storage.StoreAlert(AlertRecord{
		ID: "a1", RuleName: "high_latency", Metric: "p95_latency",
		Value: 3000, Threshold: 2000, Operator: ">",
		Severity: "warning", State: AlertStateFiring,
		Message: "latency high", FiredAt: now,
	})

	body := `{"format": "csv", "type": "alerts", "range": "1h"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/pulse/api/data/export", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	reader := csv.NewReader(bytes.NewReader(w.Body.Bytes()))
	records, _ := reader.ReadAll()
	if len(records) != 2 {
		t.Errorf("expected 2 CSV rows, got %d", len(records))
	}
}
