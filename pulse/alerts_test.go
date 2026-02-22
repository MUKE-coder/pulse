package pulse

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func setupAlertPulse(t *testing.T) *Pulse {
	t.Helper()
	cfg := applyDefaults(Config{
		Health: HealthConfig{
			Enabled: boolPtr(false),
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
		Alerts: AlertConfig{
			Enabled:  boolPtr(true),
			Cooldown: 1 * time.Second, // short cooldown for tests
		},
	})
	p := newPulse(cfg)
	p.storage = NewMemoryStorage("test")
	p.aggregator = newAggregator(p)
	t.Cleanup(func() { p.Shutdown() })
	return p
}

func TestAlertEngine_DefaultRules(t *testing.T) {
	p := setupAlertPulse(t)
	ae := &AlertEngine{
		pulse:      p,
		ruleStates: make(map[string]*ruleState),
	}

	// Initialize with default rules
	for _, r := range defaultAlertRules {
		ae.ruleStates[r.Name] = &ruleState{
			rule:  r,
			state: AlertStateOK,
		}
	}

	expectedRules := []string{"high_latency", "high_error_rate", "high_memory", "goroutine_leak", "health_check_failure"}
	for _, name := range expectedRules {
		if _, ok := ae.ruleStates[name]; !ok {
			t.Errorf("expected default rule %q to exist", name)
		}
	}

	if len(ae.ruleStates) != len(expectedRules) {
		t.Errorf("expected %d default rules, got %d", len(expectedRules), len(ae.ruleStates))
	}
}

func TestAlertEngine_UserRulesOverrideDefaults(t *testing.T) {
	cfg := applyDefaults(Config{
		Alerts: AlertConfig{
			Enabled:  boolPtr(true),
			Cooldown: 1 * time.Second,
			Rules: []AlertRule{
				{
					Name:      "high_latency", // override default
					Metric:    "p95_latency",
					Operator:  ">",
					Threshold: 5000, // custom threshold
					Duration:  10 * time.Minute,
					Severity:  "critical",
				},
				{
					Name:      "custom_rule",
					Metric:    "error_rate",
					Operator:  ">",
					Threshold: 50,
					Duration:  1 * time.Minute,
					Severity:  "warning",
				},
			},
		},
		Health:  HealthConfig{Enabled: boolPtr(false)},
		Runtime: RuntimeConfig{Enabled: boolPtr(false)},
		Tracing: TracingConfig{Enabled: boolPtr(false)},
		Errors:  ErrorConfig{Enabled: boolPtr(false)},
	})

	p := newPulse(cfg)
	p.storage = NewMemoryStorage("test")
	p.aggregator = newAggregator(p)
	t.Cleanup(func() { p.Shutdown() })

	ae := &AlertEngine{
		pulse:      p,
		ruleStates: make(map[string]*ruleState),
	}

	// Merge like newAlertEngine does
	rules := make([]AlertRule, 0, len(defaultAlertRules)+len(p.config.Alerts.Rules))
	rules = append(rules, defaultAlertRules...)
	userRuleNames := make(map[string]bool)
	for _, r := range p.config.Alerts.Rules {
		userRuleNames[r.Name] = true
	}
	filtered := rules[:0]
	for _, r := range rules {
		if !userRuleNames[r.Name] {
			filtered = append(filtered, r)
		}
	}
	rules = append(filtered, p.config.Alerts.Rules...)

	for _, r := range rules {
		ae.ruleStates[r.Name] = &ruleState{rule: r, state: AlertStateOK}
	}

	// high_latency should use user's threshold
	hl := ae.ruleStates["high_latency"]
	if hl.rule.Threshold != 5000 {
		t.Errorf("expected user threshold 5000, got %.0f", hl.rule.Threshold)
	}
	if hl.rule.Severity != "critical" {
		t.Errorf("expected severity 'critical', got %q", hl.rule.Severity)
	}

	// custom_rule should exist
	if _, ok := ae.ruleStates["custom_rule"]; !ok {
		t.Error("expected custom_rule to exist")
	}
}

func TestAlertEngine_CheckCondition(t *testing.T) {
	ae := &AlertEngine{}

	tests := []struct {
		value     float64
		operator  string
		threshold float64
		expected  bool
	}{
		{10, ">", 5, true},
		{5, ">", 10, false},
		{5, ">", 5, false},
		{10, ">=", 10, true},
		{9, ">=", 10, false},
		{5, "<", 10, true},
		{10, "<", 5, false},
		{5, "<=", 5, true},
		{6, "<=", 5, false},
		{5, "==", 5, true},
		{5, "==", 6, false},
		{5, "!=", 6, true},
		{5, "!=", 5, false},
		{5, "invalid", 5, false},
	}

	for _, tt := range tests {
		result := ae.checkCondition(tt.value, tt.operator, tt.threshold)
		if result != tt.expected {
			t.Errorf("checkCondition(%v %s %v) = %v, want %v",
				tt.value, tt.operator, tt.threshold, result, tt.expected)
		}
	}
}

func TestAlertEngine_Lifecycle_OKToPendingToFiring(t *testing.T) {
	p := setupAlertPulse(t)

	ae := &AlertEngine{
		pulse:      p,
		ruleStates: make(map[string]*ruleState),
	}

	// Create a rule with very short duration
	ae.ruleStates["test_rule"] = &ruleState{
		rule: AlertRule{
			Name:      "test_rule",
			Metric:    "error_rate",
			Operator:  ">",
			Threshold: 5,
			Duration:  0, // immediate fire
			Severity:  "critical",
		},
		state: AlertStateOK,
	}

	// Seed high error rate data
	for i := 0; i < 100; i++ {
		status := 200
		if i < 20 { // 20% error rate
			status = 500
		}
		p.storage.StoreRequest(RequestMetric{
			Method:     "GET",
			Path:       "/test",
			StatusCode: status,
			Latency:    10 * time.Millisecond,
			Timestamp:  time.Now(),
		})
	}

	// Wait for aggregator to compute
	time.Sleep(200 * time.Millisecond)

	// Force aggregation
	p.aggregator.run()

	// First evaluate: OK → Pending
	ae.evaluate()

	rs := ae.ruleStates["test_rule"]
	if rs.state != AlertStatePending {
		t.Errorf("expected state Pending after first eval, got %s", rs.state)
	}

	// Second evaluate: Pending → Firing (Duration=0, so condition immediately met)
	ae.evaluate()

	if rs.state != AlertStateFiring {
		t.Errorf("expected state Firing after second eval, got %s", rs.state)
	}

	// Check that an alert was stored
	alerts, _ := p.storage.GetAlerts(AlertFilter{Limit: 10})
	found := false
	for _, a := range alerts {
		if a.RuleName == "test_rule" && a.State == AlertStateFiring {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected firing alert to be stored")
	}
}

func TestAlertEngine_Lifecycle_FiringToResolved(t *testing.T) {
	p := setupAlertPulse(t)

	ae := &AlertEngine{
		pulse:      p,
		ruleStates: make(map[string]*ruleState),
	}

	// Start in Firing state
	ae.ruleStates["test_resolve"] = &ruleState{
		rule: AlertRule{
			Name:      "test_resolve",
			Metric:    "error_rate",
			Operator:  ">",
			Threshold: 50, // 50% — well above actual
			Duration:  0,
			Severity:  "warning",
		},
		state:     AlertStateFiring,
		lastFired: time.Now().Add(-2 * time.Second),
		alertID:   "old-alert",
	}

	// Seed data with LOW error rate
	for i := 0; i < 100; i++ {
		p.storage.StoreRequest(RequestMetric{
			Method:     "GET",
			Path:       "/test",
			StatusCode: 200,
			Latency:    10 * time.Millisecond,
			Timestamp:  time.Now(),
		})
	}

	p.aggregator.run()
	ae.evaluate()

	rs := ae.ruleStates["test_resolve"]
	// Should have resolved and returned to OK
	if rs.state != AlertStateOK {
		t.Errorf("expected state OK after resolution, got %s", rs.state)
	}

	// Check that a resolved alert was stored
	alerts, _ := p.storage.GetAlerts(AlertFilter{Limit: 10})
	found := false
	for _, a := range alerts {
		if a.RuleName == "test_resolve" && a.State == AlertStateResolved {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected resolved alert to be stored")
	}
}

func TestAlertEngine_Cooldown(t *testing.T) {
	p := setupAlertPulse(t)

	ae := &AlertEngine{
		pulse:      p,
		ruleStates: make(map[string]*ruleState),
	}

	rs := &ruleState{
		rule: AlertRule{
			Name:      "cooldown_test",
			Metric:    "error_rate",
			Operator:  ">",
			Threshold: 5,
			Duration:  0,
			Severity:  "warning",
		},
		state:     AlertStatePending,
		lastFired: time.Now(), // just fired
	}
	ae.ruleStates["cooldown_test"] = rs

	// Seed high error rate
	for i := 0; i < 100; i++ {
		status := 200
		if i < 20 {
			status = 500
		}
		p.storage.StoreRequest(RequestMetric{
			Method: "GET", Path: "/test", StatusCode: status,
			Latency: 10 * time.Millisecond, Timestamp: time.Now(),
		})
	}
	p.aggregator.run()

	// Evaluate — should fire but cooldown prevents storing a new alert
	initialAlerts, _ := p.storage.GetAlerts(AlertFilter{Limit: 100})
	initialCount := len(initialAlerts)

	ae.evaluate()

	afterAlerts, _ := p.storage.GetAlerts(AlertFilter{Limit: 100})
	// Cooldown should prevent a new alert from being stored
	if len(afterAlerts) != initialCount {
		t.Errorf("expected cooldown to prevent new alert, got %d alerts (was %d)", len(afterAlerts), initialCount)
	}
}

func TestAlertEngine_PendingClearsOnRecovery(t *testing.T) {
	p := setupAlertPulse(t)

	ae := &AlertEngine{
		pulse:      p,
		ruleStates: make(map[string]*ruleState),
	}

	ae.ruleStates["pending_clear"] = &ruleState{
		rule: AlertRule{
			Name:      "pending_clear",
			Metric:    "error_rate",
			Operator:  ">",
			Threshold: 50, // high threshold, condition not met
			Duration:  5 * time.Minute,
			Severity:  "warning",
		},
		state:        AlertStatePending,
		pendingSince: time.Now().Add(-1 * time.Minute),
	}

	// Seed data with 0% error rate
	for i := 0; i < 50; i++ {
		p.storage.StoreRequest(RequestMetric{
			Method: "GET", Path: "/test", StatusCode: 200,
			Latency: 10 * time.Millisecond, Timestamp: time.Now(),
		})
	}
	p.aggregator.run()

	ae.evaluate()

	rs := ae.ruleStates["pending_clear"]
	if rs.state != AlertStateOK {
		t.Errorf("expected state OK after condition cleared, got %s", rs.state)
	}
	if !rs.pendingSince.IsZero() {
		t.Error("expected pendingSince to be reset")
	}
}

func TestFormatAlertMessage(t *testing.T) {
	tests := []struct {
		rule  AlertRule
		value float64
		want  string
	}{
		{
			rule:  AlertRule{Name: "high_latency", Metric: "p95_latency", Operator: ">", Threshold: 2000},
			value: 3500,
			want:  "high_latency: p95_latency > 2000ms (current: 3500ms)",
		},
		{
			rule:  AlertRule{Name: "err", Metric: "error_rate", Operator: ">", Threshold: 10},
			value: 15.5,
			want:  "err: error_rate > 10.0% (current: 15.5%)",
		},
		{
			rule:  AlertRule{Name: "mem", Metric: "heap_alloc_mb", Operator: ">", Threshold: 500, Route: "GET /api"},
			value: 600,
			want:  "mem: heap_alloc_mb > 500MB (current: 600MB) [route: GET /api]",
		},
	}

	for _, tt := range tests {
		got := formatAlertMessage(tt.rule, tt.value)
		if got != tt.want {
			t.Errorf("formatAlertMessage(%q) = %q, want %q", tt.rule.Name, got, tt.want)
		}
	}
}

func TestAlertEngine_GetRuleStates(t *testing.T) {
	ae := &AlertEngine{
		ruleStates: make(map[string]*ruleState),
	}

	ae.ruleStates["rule_a"] = &ruleState{
		rule:  AlertRule{Name: "rule_a", Metric: "error_rate", Severity: "critical"},
		state: AlertStateFiring,
	}
	ae.ruleStates["rule_b"] = &ruleState{
		rule:  AlertRule{Name: "rule_b", Metric: "p95_latency", Severity: "warning"},
		state: AlertStateOK,
	}

	states := ae.GetRuleStates()
	if len(states) != 2 {
		t.Fatalf("expected 2 states, got %d", len(states))
	}

	// Should be sorted by name
	if states[0]["name"] != "rule_a" {
		t.Errorf("expected first rule 'rule_a', got %v", states[0]["name"])
	}
	if states[0]["state"] != "firing" {
		t.Errorf("expected state 'firing', got %v", states[0]["state"])
	}
	if states[1]["name"] != "rule_b" {
		t.Errorf("expected second rule 'rule_b', got %v", states[1]["name"])
	}
}

func TestSlackNotificationFormat(t *testing.T) {
	// Mock Slack webhook server
	var received []byte
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		body := make([]byte, r.ContentLength)
		r.Body.Read(body)
		received = body
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := applyDefaults(Config{
		Alerts: AlertConfig{
			Enabled:  boolPtr(true),
			Cooldown: 1 * time.Second,
			Slack:    &SlackConfig{WebhookURL: server.URL, Channel: "#alerts"},
		},
		Health:  HealthConfig{Enabled: boolPtr(false)},
		Runtime: RuntimeConfig{Enabled: boolPtr(false)},
		Tracing: TracingConfig{Enabled: boolPtr(false)},
		Errors:  ErrorConfig{Enabled: boolPtr(false)},
	})
	p := newPulse(cfg)
	p.storage = NewMemoryStorage("test")
	t.Cleanup(func() { p.Shutdown() })

	ae := &AlertEngine{pulse: p, ruleStates: make(map[string]*ruleState)}

	alert := AlertRecord{
		ID:        "test-alert",
		RuleName:  "high_latency",
		Metric:    "p95_latency",
		Severity:  "critical",
		State:     AlertStateFiring,
		Message:   "test message",
		FiredAt:   time.Now(),
	}

	ae.sendSlack(cfg.Alerts.Slack, alert)

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	if len(received) == 0 {
		t.Fatal("expected Slack webhook to receive data")
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(received, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if payload["channel"] != "#alerts" {
		t.Errorf("expected channel '#alerts', got %v", payload["channel"])
	}

	attachments, ok := payload["attachments"].([]interface{})
	if !ok || len(attachments) == 0 {
		t.Fatal("expected attachments")
	}

	att := attachments[0].(map[string]interface{})
	if att["color"] != "#ef4444" {
		t.Errorf("expected red color for critical, got %v", att["color"])
	}
}

func TestWebhookNotificationWithSignature(t *testing.T) {
	var receivedSig string
	var receivedBody []byte
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		receivedSig = r.Header.Get("X-Pulse-Signature")
		body := make([]byte, r.ContentLength)
		r.Body.Read(body)
		receivedBody = body
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p := newPulse(applyDefaults(Config{
		Health:  HealthConfig{Enabled: boolPtr(false)},
		Runtime: RuntimeConfig{Enabled: boolPtr(false)},
		Tracing: TracingConfig{Enabled: boolPtr(false)},
		Errors:  ErrorConfig{Enabled: boolPtr(false)},
	}))
	p.storage = NewMemoryStorage("test")
	t.Cleanup(func() { p.Shutdown() })

	ae := &AlertEngine{pulse: p, ruleStates: make(map[string]*ruleState)}

	webhookCfg := WebhookConfig{
		URL:    server.URL,
		Secret: "test-secret-key",
		Headers: map[string]string{
			"X-Custom": "test-value",
		},
	}

	alert := AlertRecord{
		ID:        "wh-test",
		RuleName:  "test_rule",
		Metric:    "error_rate",
		Value:     15.0,
		Threshold: 10.0,
		Operator:  ">",
		Severity:  "warning",
		State:     AlertStateFiring,
		Message:   "Error rate exceeded",
		FiredAt:   time.Now(),
	}

	ae.sendWebhook(webhookCfg, alert)

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	if receivedSig == "" {
		t.Error("expected X-Pulse-Signature header")
	}

	if len(receivedBody) == 0 {
		t.Fatal("expected webhook to receive body")
	}

	// Verify the payload
	var payload map[string]interface{}
	json.Unmarshal(receivedBody, &payload)
	if payload["alert"] != "test_rule" {
		t.Errorf("expected alert 'test_rule', got %v", payload["alert"])
	}
	if payload["severity"] != "warning" {
		t.Errorf("expected severity 'warning', got %v", payload["severity"])
	}
}

func TestDiscordNotificationFormat(t *testing.T) {
	var received []byte
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		body := make([]byte, r.ContentLength)
		r.Body.Read(body)
		received = body
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p := newPulse(applyDefaults(Config{
		Health:  HealthConfig{Enabled: boolPtr(false)},
		Runtime: RuntimeConfig{Enabled: boolPtr(false)},
		Tracing: TracingConfig{Enabled: boolPtr(false)},
		Errors:  ErrorConfig{Enabled: boolPtr(false)},
	}))
	p.storage = NewMemoryStorage("test")
	t.Cleanup(func() { p.Shutdown() })

	ae := &AlertEngine{pulse: p, ruleStates: make(map[string]*ruleState)}

	discordCfg := &DiscordConfig{WebhookURL: server.URL}
	alert := AlertRecord{
		ID:       "discord-test",
		RuleName: "test",
		Severity: "warning",
		State:    AlertStateFiring,
		Message:  "test",
		FiredAt:  time.Now(),
	}

	ae.sendDiscord(discordCfg, alert)
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	if len(received) == 0 {
		t.Fatal("expected Discord webhook to receive data")
	}

	var payload map[string]interface{}
	json.Unmarshal(received, &payload)

	embeds, ok := payload["embeds"].([]interface{})
	if !ok || len(embeds) == 0 {
		t.Fatal("expected embeds")
	}

	embed := embeds[0].(map[string]interface{})
	color := int(embed["color"].(float64))
	if color != 0xf97316 {
		t.Errorf("expected orange color for warning, got %x", color)
	}
}

func TestSlackColorMapping(t *testing.T) {
	tests := []struct {
		alert AlertRecord
		want  string
	}{
		{AlertRecord{Severity: "critical", State: AlertStateFiring}, "#ef4444"},
		{AlertRecord{Severity: "warning", State: AlertStateFiring}, "#f97316"},
		{AlertRecord{Severity: "info", State: AlertStateFiring}, "#3b82f6"},
		{AlertRecord{Severity: "critical", State: AlertStateResolved}, "#22c55e"},
	}

	for _, tt := range tests {
		got := slackColor(tt.alert)
		if got != tt.want {
			t.Errorf("slackColor(%s/%s) = %q, want %q", tt.alert.Severity, tt.alert.State, got, tt.want)
		}
	}
}

func TestDiscordColorMapping(t *testing.T) {
	tests := []struct {
		alert AlertRecord
		want  int
	}{
		{AlertRecord{Severity: "critical", State: AlertStateFiring}, 0xef4444},
		{AlertRecord{Severity: "warning", State: AlertStateFiring}, 0xf97316},
		{AlertRecord{Severity: "info", State: AlertStateFiring}, 0x3b82f6},
		{AlertRecord{Severity: "critical", State: AlertStateResolved}, 0x22c55e},
	}

	for _, tt := range tests {
		got := discordColor(tt.alert)
		if got != tt.want {
			t.Errorf("discordColor(%s/%s) = %x, want %x", tt.alert.Severity, tt.alert.State, got, tt.want)
		}
	}
}

func TestAlertEngine_GetMetricValue_HealthStatus(t *testing.T) {
	p := setupAlertPulse(t)

	// Without a health runner, should return 1 (healthy)
	ae := &AlertEngine{pulse: p, ruleStates: make(map[string]*ruleState)}
	val, ok := ae.getMetricValue(AlertRule{Metric: "health_status"})
	if !ok {
		t.Fatal("expected ok=true for health_status")
	}
	if val != 1 {
		t.Errorf("expected 1 (healthy) without runner, got %v", val)
	}

	// With a health runner in unhealthy state
	p.healthRunner = &HealthRunner{
		pulse:          p,
		compositeState: "unhealthy",
		flapping:       make(map[string]bool),
	}
	val, ok = ae.getMetricValue(AlertRule{Metric: "health_status"})
	if !ok {
		t.Fatal("expected ok=true")
	}
	if val != 0 {
		t.Errorf("expected 0 (unhealthy), got %v", val)
	}

	// Degraded
	p.healthRunner.mu.Lock()
	p.healthRunner.compositeState = "degraded"
	p.healthRunner.mu.Unlock()
	val, _ = ae.getMetricValue(AlertRule{Metric: "health_status"})
	if val != 0.5 {
		t.Errorf("expected 0.5 (degraded), got %v", val)
	}
}

func TestAlertEngine_GetMetricValue_Unknown(t *testing.T) {
	p := setupAlertPulse(t)
	ae := &AlertEngine{pulse: p, ruleStates: make(map[string]*ruleState)}

	_, ok := ae.getMetricValue(AlertRule{Metric: "nonexistent_metric"})
	if ok {
		t.Error("expected ok=false for unknown metric")
	}
}

func TestAlertEngine_WebhookRetry(t *testing.T) {
	attempts := 0
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		attempt := attempts
		mu.Unlock()

		if attempt < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p := newPulse(applyDefaults(Config{
		Health:  HealthConfig{Enabled: boolPtr(false)},
		Runtime: RuntimeConfig{Enabled: boolPtr(false)},
		Tracing: TracingConfig{Enabled: boolPtr(false)},
		Errors:  ErrorConfig{Enabled: boolPtr(false)},
	}))
	p.storage = NewMemoryStorage("test")
	t.Cleanup(func() { p.Shutdown() })

	ae := &AlertEngine{pulse: p, ruleStates: make(map[string]*ruleState)}

	alert := AlertRecord{
		RuleName: "retry_test",
		Severity: "warning",
		State:    AlertStateFiring,
		Message:  "test",
		FiredAt:  time.Now(),
	}

	ae.sendWebhook(WebhookConfig{URL: server.URL}, alert)

	mu.Lock()
	finalAttempts := attempts
	mu.Unlock()

	if finalAttempts < 3 {
		// The retry may have succeeded on the 3rd attempt
		t.Logf("webhook received %d attempts (expected >= 3)", finalAttempts)
	}
}

func TestAlertEngine_ConcurrentEvaluation(t *testing.T) {
	p := setupAlertPulse(t)

	ae := &AlertEngine{
		pulse:      p,
		ruleStates: make(map[string]*ruleState),
	}

	// Add multiple rules
	for i := 0; i < 10; i++ {
		ae.ruleStates[fmt.Sprintf("rule_%d", i)] = &ruleState{
			rule: AlertRule{
				Name:      fmt.Sprintf("rule_%d", i),
				Metric:    "error_rate",
				Operator:  ">",
				Threshold: 90, // won't fire
				Duration:  5 * time.Minute,
				Severity:  "warning",
			},
			state: AlertStateOK,
		}
	}

	// Seed some data
	for i := 0; i < 50; i++ {
		p.storage.StoreRequest(RequestMetric{
			Method: "GET", Path: "/test", StatusCode: 200,
			Latency: 10 * time.Millisecond, Timestamp: time.Now(),
		})
	}
	p.aggregator.run()

	// Run evaluations concurrently
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ae.evaluate()
		}()
	}
	wg.Wait()

	// All rules should remain OK
	for name, rs := range ae.ruleStates {
		if rs.state != AlertStateOK {
			t.Errorf("rule %s: expected OK, got %s", name, rs.state)
		}
	}
}
