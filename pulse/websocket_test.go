package pulse

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func setupWSPulse(t *testing.T) (*Pulse, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	cfg := applyDefaults(Config{
		Health: HealthConfig{
			Enabled:       boolPtr(false),
			CheckInterval: 100 * time.Millisecond,
		},
		Runtime: RuntimeConfig{
			Enabled: boolPtr(false),
		},
		Tracing: TracingConfig{
			Enabled: boolPtr(false),
		},
		Errors: ErrorConfig{
			Enabled: boolPtr(false),
		},
	})
	p := newPulse(cfg)
	p.storage = NewMemoryStorage("test")

	// Start WebSocket hub
	p.wsHub = newWebSocketHub(p)
	go p.wsHub.run()

	router := gin.New()
	registerWebSocketRoute(router, p)

	t.Cleanup(func() { p.Shutdown() })

	return p, router
}

// wsConnect establishes a WebSocket connection to the test server.
func wsConnect(t *testing.T, server *httptest.Server) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/pulse/ws/live"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial failed: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("expected 101, got %d", resp.StatusCode)
	}
	return conn
}

func TestWebSocketHub_ClientRegistration(t *testing.T) {
	p, router := setupWSPulse(t)
	server := httptest.NewServer(router)
	defer server.Close()

	if p.wsHub.ClientCount() != 0 {
		t.Fatalf("expected 0 clients, got %d", p.wsHub.ClientCount())
	}

	conn := wsConnect(t, server)
	defer conn.Close()

	// Give time for registration to propagate
	time.Sleep(100 * time.Millisecond)

	if p.wsHub.ClientCount() != 1 {
		t.Errorf("expected 1 client, got %d", p.wsHub.ClientCount())
	}

	conn.Close()
	time.Sleep(100 * time.Millisecond)

	if p.wsHub.ClientCount() != 0 {
		t.Errorf("expected 0 clients after disconnect, got %d", p.wsHub.ClientCount())
	}
}

func TestWebSocketHub_MultipleClients(t *testing.T) {
	p, router := setupWSPulse(t)
	server := httptest.NewServer(router)
	defer server.Close()

	const numClients = 5
	conns := make([]*websocket.Conn, numClients)
	for i := 0; i < numClients; i++ {
		conns[i] = wsConnect(t, server)
		defer conns[i].Close()
	}

	time.Sleep(100 * time.Millisecond)

	if p.wsHub.ClientCount() != numClients {
		t.Errorf("expected %d clients, got %d", numClients, p.wsHub.ClientCount())
	}

	// Disconnect half
	for i := 0; i < numClients/2; i++ {
		conns[i].Close()
	}

	time.Sleep(200 * time.Millisecond)

	remaining := numClients - numClients/2
	if p.wsHub.ClientCount() != remaining {
		t.Errorf("expected %d clients after partial disconnect, got %d", remaining, p.wsHub.ClientCount())
	}
}

func TestWebSocketHub_BroadcastMessage(t *testing.T) {
	p, router := setupWSPulse(t)
	server := httptest.NewServer(router)
	defer server.Close()

	conn := wsConnect(t, server)
	defer conn.Close()

	time.Sleep(100 * time.Millisecond)

	// Broadcast a request event
	p.BroadcastRequest(RequestMetric{
		Method:     "GET",
		Path:       "/users",
		StatusCode: 200,
		Latency:    45 * time.Millisecond,
		TraceID:    "test-trace-123",
	})

	// Read the message
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, message, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("failed to read ws message: %v", err)
	}

	var msg WSMessage
	if err := json.Unmarshal(message, &msg); err != nil {
		t.Fatalf("failed to unmarshal message: %v", err)
	}

	if msg.Type != WSTypeRequest {
		t.Errorf("expected type %q, got %q", WSTypeRequest, msg.Type)
	}

	payload, ok := msg.Payload.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map payload, got %T", msg.Payload)
	}

	if payload["method"] != "GET" {
		t.Errorf("expected method 'GET', got %v", payload["method"])
	}
	if payload["path"] != "/users" {
		t.Errorf("expected path '/users', got %v", payload["path"])
	}
}

func TestWebSocketHub_BroadcastError(t *testing.T) {
	p, router := setupWSPulse(t)
	server := httptest.NewServer(router)
	defer server.Close()

	conn := wsConnect(t, server)
	defer conn.Close()
	time.Sleep(100 * time.Millisecond)

	p.BroadcastError(ErrorRecord{
		Method:       "POST",
		Route:        "/users",
		ErrorMessage: "duplicate key",
		ErrorType:    ErrorTypeDatabase,
		Count:        3,
	})

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, message, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("failed to read ws message: %v", err)
	}

	var msg WSMessage
	if err := json.Unmarshal(message, &msg); err != nil {
		t.Fatalf("failed to unmarshal message: %v", err)
	}

	if msg.Type != WSTypeError {
		t.Errorf("expected type %q, got %q", WSTypeError, msg.Type)
	}

	payload := msg.Payload.(map[string]interface{})
	if payload["message"] != "duplicate key" {
		t.Errorf("expected 'duplicate key', got %v", payload["message"])
	}
}

func TestWebSocketHub_BroadcastHealth(t *testing.T) {
	p, router := setupWSPulse(t)
	server := httptest.NewServer(router)
	defer server.Close()

	conn := wsConnect(t, server)
	defer conn.Close()
	time.Sleep(100 * time.Millisecond)

	p.BroadcastHealthResult(HealthCheckResult{
		Name:    "database",
		Status:  "healthy",
		Latency: 2 * time.Millisecond,
	})

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, message, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("failed to read ws message: %v", err)
	}

	var msg WSMessage
	json.Unmarshal(message, &msg)

	if msg.Type != WSTypeHealth {
		t.Errorf("expected type %q, got %q", WSTypeHealth, msg.Type)
	}

	payload := msg.Payload.(map[string]interface{})
	if payload["name"] != "database" {
		t.Errorf("expected name 'database', got %v", payload["name"])
	}
	if payload["status"] != "healthy" {
		t.Errorf("expected status 'healthy', got %v", payload["status"])
	}
}

func TestWebSocketHub_BroadcastAlert(t *testing.T) {
	p, router := setupWSPulse(t)
	server := httptest.NewServer(router)
	defer server.Close()

	conn := wsConnect(t, server)
	defer conn.Close()
	time.Sleep(100 * time.Millisecond)

	p.BroadcastAlert(AlertRecord{
		ID:       "alert-1",
		RuleName: "high_error_rate",
		Severity: "critical",
		Message:  "Error rate exceeded 5%",
		State:    AlertStateFiring,
	})

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, message, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("failed to read ws message: %v", err)
	}

	var msg WSMessage
	json.Unmarshal(message, &msg)

	if msg.Type != WSTypeAlert {
		t.Errorf("expected type %q, got %q", WSTypeAlert, msg.Type)
	}

	payload := msg.Payload.(map[string]interface{})
	if payload["severity"] != "critical" {
		t.Errorf("expected severity 'critical', got %v", payload["severity"])
	}
}

func TestWebSocketHub_BroadcastRuntime(t *testing.T) {
	p, router := setupWSPulse(t)
	server := httptest.NewServer(router)
	defer server.Close()

	conn := wsConnect(t, server)
	defer conn.Close()
	time.Sleep(100 * time.Millisecond)

	p.BroadcastRuntime(RuntimeMetric{
		HeapAlloc:    10 * 1024 * 1024,
		HeapInUse:    8 * 1024 * 1024,
		NumGoroutine: 42,
		GCPauseNs:    500000,
		NumGC:        15,
	})

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, message, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("failed to read ws message: %v", err)
	}

	var msg WSMessage
	json.Unmarshal(message, &msg)

	if msg.Type != WSTypeRuntime {
		t.Errorf("expected type %q, got %q", WSTypeRuntime, msg.Type)
	}

	payload := msg.Payload.(map[string]interface{})
	goroutines := payload["goroutines"].(float64)
	if goroutines != 42 {
		t.Errorf("expected 42 goroutines, got %v", goroutines)
	}
}

func TestWebSocketHub_ChannelSubscription(t *testing.T) {
	p, router := setupWSPulse(t)
	server := httptest.NewServer(router)
	defer server.Close()

	conn := wsConnect(t, server)
	defer conn.Close()
	time.Sleep(100 * time.Millisecond)

	// Subscribe only to "error" channel
	sub := WSSubscription{Subscribe: []string{"error"}}
	subData, _ := json.Marshal(sub)
	if err := conn.WriteMessage(websocket.TextMessage, subData); err != nil {
		t.Fatalf("failed to send subscription: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Send a request event (should NOT be received)
	p.BroadcastRequest(RequestMetric{
		Method:     "GET",
		Path:       "/ignored",
		StatusCode: 200,
		Latency:    10 * time.Millisecond,
	})

	// Send an error event (should be received)
	p.BroadcastError(ErrorRecord{
		Method:       "POST",
		Route:        "/test",
		ErrorMessage: "subscribed error",
		ErrorType:    ErrorTypeInternal,
		Count:        1,
	})

	// Read with a deadline — should get the error, not the request
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, message, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("failed to read ws message: %v", err)
	}

	var msg WSMessage
	json.Unmarshal(message, &msg)

	if msg.Type != WSTypeError {
		t.Errorf("expected error message, got type %q", msg.Type)
	}
}

func TestWebSocketHub_DefaultSubscribesAll(t *testing.T) {
	p, router := setupWSPulse(t)
	server := httptest.NewServer(router)
	defer server.Close()

	conn := wsConnect(t, server)
	defer conn.Close()
	time.Sleep(100 * time.Millisecond)

	// Without subscribing, client should receive all types
	p.BroadcastRequest(RequestMetric{
		Method: "GET", Path: "/all", StatusCode: 200, Latency: 5 * time.Millisecond,
	})

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, message, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("expected to receive message without subscription: %v", err)
	}

	var msg WSMessage
	json.Unmarshal(message, &msg)
	if msg.Type != WSTypeRequest {
		t.Errorf("expected request type, got %q", msg.Type)
	}
}

func TestWebSocketHub_ConcurrentBroadcast(t *testing.T) {
	p, router := setupWSPulse(t)
	server := httptest.NewServer(router)
	defer server.Close()

	const numClients = 3
	const numMessages = 20

	conns := make([]*websocket.Conn, numClients)
	for i := 0; i < numClients; i++ {
		conns[i] = wsConnect(t, server)
		defer conns[i].Close()
	}
	time.Sleep(100 * time.Millisecond)

	// Broadcast messages concurrently
	var wg sync.WaitGroup
	for i := 0; i < numMessages; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			p.BroadcastRequest(RequestMetric{
				Method:     "GET",
				Path:       "/concurrent",
				StatusCode: 200,
				Latency:    time.Duration(idx) * time.Millisecond,
			})
		}(i)
	}
	wg.Wait()

	// Each client should receive messages
	for i, conn := range conns {
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("client %d: failed to read message: %v", i, err)
		}
	}
}

func TestWebSocketHub_NilHubSafe(t *testing.T) {
	p := newPulse(applyDefaults(Config{}))
	p.storage = NewMemoryStorage("test")
	// wsHub is nil — broadcast calls should not panic

	p.BroadcastRequest(RequestMetric{Method: "GET", Path: "/safe"})
	p.BroadcastError(ErrorRecord{ErrorMessage: "safe"})
	p.BroadcastHealthResult(HealthCheckResult{Name: "safe"})
	p.BroadcastAlert(AlertRecord{ID: "safe"})
	p.BroadcastOverview(nil)
	p.BroadcastRuntime(RuntimeMetric{})

	// If we reach here without panic, the test passes
}

func TestWebSocketHub_NoClientsSkipsBroadcast(t *testing.T) {
	p, _ := setupWSPulse(t)

	// With hub running but no clients, broadcast should be a no-op
	if p.wsHub.ClientCount() != 0 {
		t.Fatalf("expected 0 clients")
	}

	// These should all be no-ops (ClientCount == 0 short-circuit)
	p.BroadcastRequest(RequestMetric{Method: "GET", Path: "/noop"})
	p.BroadcastError(ErrorRecord{ErrorMessage: "noop"})
	p.BroadcastHealthResult(HealthCheckResult{Name: "noop"})
}

func TestWebSocketHub_OverviewBroadcast(t *testing.T) {
	p, router := setupWSPulse(t)
	server := httptest.NewServer(router)
	defer server.Close()

	conn := wsConnect(t, server)
	defer conn.Close()
	time.Sleep(100 * time.Millisecond)

	overview := &Overview{
		AppName:       "TestApp",
		TotalRequests: 1000,
		ErrorRate:     2.5,
		HealthStatus:  "healthy",
	}

	p.BroadcastOverview(overview)

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, message, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("failed to read ws message: %v", err)
	}

	var msg WSMessage
	json.Unmarshal(message, &msg)

	if msg.Type != WSTypeOverview {
		t.Errorf("expected type %q, got %q", WSTypeOverview, msg.Type)
	}
}

func TestWebSocketHub_MultiSubscription(t *testing.T) {
	p, router := setupWSPulse(t)
	server := httptest.NewServer(router)
	defer server.Close()

	conn := wsConnect(t, server)
	defer conn.Close()
	time.Sleep(100 * time.Millisecond)

	// Subscribe to both overview and health
	sub := WSSubscription{Subscribe: []string{"overview", "health"}}
	subData, _ := json.Marshal(sub)
	conn.WriteMessage(websocket.TextMessage, subData)
	time.Sleep(100 * time.Millisecond)

	// Send health — should be received
	p.BroadcastHealthResult(HealthCheckResult{
		Name:   "db",
		Status: "healthy",
	})

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, message, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("failed to read health message: %v", err)
	}

	var msg WSMessage
	json.Unmarshal(message, &msg)
	if msg.Type != WSTypeHealth {
		t.Errorf("expected health type, got %q", msg.Type)
	}

	// Send overview — should also be received
	p.BroadcastOverview(&Overview{AppName: "Multi"})

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, message, err = conn.ReadMessage()
	if err != nil {
		t.Fatalf("failed to read overview message: %v", err)
	}

	json.Unmarshal(message, &msg)
	if msg.Type != WSTypeOverview {
		t.Errorf("expected overview type, got %q", msg.Type)
	}
}
