package pulse

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// AlertEngine evaluates alert rules against metrics and manages alert lifecycle.
type AlertEngine struct {
	pulse *Pulse

	mu         sync.RWMutex
	ruleStates map[string]*ruleState // keyed by rule name

	// lastDropped is the storage's dropped-write total at the previous
	// storage_dropped_writes evaluation, so the metric reports new drops.
	lastDropped int64

	// notifyQ feeds the notification workers. It is bounded, so a slow or
	// unreachable notification channel can't pile up goroutines; nil for
	// engines not built by newAlertEngine.
	notifyQ chan AlertRecord
}

const (
	notificationQueueSize = 256
	notificationWorkers   = 2
)

// notify queues an alert for delivery to the notification channels. When the
// queue is full the notification is dropped and counted as an internal error.
func (ae *AlertEngine) notify(alert AlertRecord) {
	if ae.notifyQ == nil {
		go ae.sendNotifications(alert)
		return
	}
	select {
	case ae.notifyQ <- alert:
	default:
		ae.pulse.internalError("notifications",
			fmt.Errorf("notification queue full; dropped the %s notification for %q", alert.State, alert.RuleName))
	}
}

// ruleState tracks the evaluation state of a single alert rule.
type ruleState struct {
	rule         AlertRule
	state        AlertState
	pendingSince time.Time // when condition first became true
	lastFired    time.Time // when last notification was sent
	alertID      string    // open (firing) alert record ID; "" when none was stored
	firedValue   float64   // metric value when the open alert fired
}

// Built-in default alert rules.
var defaultAlertRules = []AlertRule{
	{
		Name:      "high_latency",
		Metric:    "p95_latency",
		Operator:  ">",
		Threshold: 2000, // 2000ms
		Duration:  5 * time.Minute,
		Severity:  "warning",
	},
	{
		Name:      "high_error_rate",
		Metric:    "error_rate",
		Operator:  ">",
		Threshold: 10, // 10%
		Duration:  3 * time.Minute,
		Severity:  "critical",
	},
	{
		Name:      "high_memory",
		Metric:    "heap_alloc_mb",
		Operator:  ">",
		Threshold: 500, // 500MB
		Duration:  5 * time.Minute,
		Severity:  "warning",
	},
	{
		Name:      "goroutine_leak",
		Metric:    "goroutine_growth",
		Operator:  ">",
		Threshold: 100, // 100/hour
		Duration:  10 * time.Minute,
		Severity:  "warning",
	},
	{
		Name:      "health_check_failure",
		Metric:    "health_status",
		Operator:  "==",
		Threshold: 0, // 0 = unhealthy
		Duration:  2 * time.Minute,
		Severity:  "critical",
	},
	{
		// Buffer capacity, not RetentionHours, is limiting history.
		Name:      "storage_retention_limited",
		Metric:    "retention_coverage",
		Operator:  "<",
		Threshold: 0.5, // holding less than half the configured retention
		Duration:  15 * time.Minute,
		Severity:  "warning",
	},
	{
		// The SQLite write queue overflowed since the last evaluation.
		Name:      "storage_writes_dropped",
		Metric:    "storage_dropped_writes",
		Operator:  ">",
		Threshold: 0,
		Duration:  0,
		Severity:  "warning",
	},
}

// newAlertEngine creates and starts the alert evaluation engine.
func newAlertEngine(p *Pulse) *AlertEngine {
	ae := &AlertEngine{
		pulse:      p,
		ruleStates: make(map[string]*ruleState),
	}

	// Initialize rules: merge defaults with user-configured rules
	rules := make([]AlertRule, 0, len(defaultAlertRules)+len(p.config.Alerts.Rules))
	rules = append(rules, defaultAlertRules...)
	// User rules override defaults with matching names
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
		ae.ruleStates[r.Name] = &ruleState{
			rule:  r,
			state: AlertStateOK,
		}
	}

	interval := 30 * time.Second
	if p.config.DevMode {
		interval = 10 * time.Second
	}

	ae.restoreFiring()

	ae.notifyQ = make(chan AlertRecord, notificationQueueSize)
	for i := 0; i < notificationWorkers; i++ {
		p.startBackground("alert-notifier", func(ctx context.Context) {
			for {
				select {
				case <-ctx.Done():
					return
				case alert := <-ae.notifyQ:
					ae.sendNotifications(alert)
				}
			}
		})
	}

	p.startBackground("alert-engine", func(ctx context.Context) {
		// Initial delay to let metrics accumulate
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		ae.evaluate()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				ae.evaluate()
			}
		}
	})

	return ae
}

// restoreFiring reloads open alerts from storage, so after a restart a rule
// that is still breaching doesn't fire a duplicate, and one that has
// recovered resolves its original alert.
func (ae *AlertEngine) restoreFiring() {
	open, err := ae.pulse.storage.GetAlerts(AlertFilter{State: AlertStateFiring})
	if err != nil {
		return
	}
	for _, a := range open { // newest first
		rs, ok := ae.ruleStates[a.RuleName]
		if !ok || rs.state == AlertStateFiring {
			continue
		}
		rs.state = AlertStateFiring
		rs.alertID = a.ID
		rs.lastFired = a.FiredAt
		rs.firedValue = a.Value
	}
}

// evaluate runs all alert rules against current metrics.
func (ae *AlertEngine) evaluate() {
	ae.mu.Lock()
	defer ae.mu.Unlock()

	now := time.Now()

	for _, rs := range ae.ruleStates {
		value, ok := ae.getMetricValue(rs.rule)
		if !ok {
			continue
		}

		conditionMet := ae.checkCondition(value, rs.rule.Operator, rs.rule.Threshold)

		switch rs.state {
		case AlertStateOK:
			if conditionMet {
				rs.state = AlertStatePending
				rs.pendingSince = now
			}

		case AlertStatePending:
			if !conditionMet {
				// Condition cleared before duration met
				rs.state = AlertStateOK
				rs.pendingSince = time.Time{}
			} else if now.Sub(rs.pendingSince) >= rs.rule.Duration {
				// Duration met — fire alert
				rs.state = AlertStateFiring
				ae.fireAlert(rs, value)
			}

		case AlertStateFiring:
			if !conditionMet {
				// Resolved
				rs.state = AlertStateResolved
				ae.resolveAlert(rs)
				// Transition back to OK after resolving
				rs.state = AlertStateOK
				rs.pendingSince = time.Time{}
			}

		case AlertStateResolved:
			// Already handled above — reset to OK
			rs.state = AlertStateOK
			rs.pendingSince = time.Time{}
		}
	}
}

// getMetricValue retrieves the current value for a metric referenced by a rule.
func (ae *AlertEngine) getMetricValue(rule AlertRule) (float64, bool) {
	switch rule.Metric {
	case "p95_latency":
		if ae.pulse.aggregator == nil {
			return 0, false
		}
		stats := ae.pulse.aggregator.GetCachedRouteStats()
		if len(stats) == 0 {
			return 0, false
		}
		if rule.Route != "" {
			for _, s := range stats {
				if s.Method+" "+s.Path == rule.Route {
					return float64(s.P95Latency) / float64(time.Millisecond), true
				}
			}
			return 0, false
		}
		// Global p95: take the max p95 across all routes
		var maxP95 float64
		for _, s := range stats {
			v := float64(s.P95Latency) / float64(time.Millisecond)
			if v > maxP95 {
				maxP95 = v
			}
		}
		return maxP95, true

	case "error_rate":
		overview := ae.pulse.aggregator.GetCachedOverview()
		if overview == nil {
			return 0, false
		}
		if rule.Route != "" {
			stats := ae.pulse.aggregator.GetCachedRouteStats()
			for _, s := range stats {
				if s.Method+" "+s.Path == rule.Route {
					return s.ErrorRate, true
				}
			}
			return 0, false
		}
		return overview.ErrorRate, true

	case "heap_alloc_mb":
		runtimeHistory, _ := ae.pulse.storage.GetRuntimeHistory(Last5m())
		if len(runtimeHistory) == 0 {
			return 0, false
		}
		latest := runtimeHistory[len(runtimeHistory)-1]
		return float64(latest.HeapAlloc) / (1024 * 1024), true

	case "goroutine_growth":
		if ae.pulse.runtimeSampler == nil {
			return 0, false
		}
		return ae.pulse.runtimeSampler.GoroutineGrowthRate(), true

	case "retention_coverage":
		coverage, _, ok := retentionCoverage(ae.pulse, time.Now())
		return coverage, ok

	case "storage_dropped_writes":
		total, ok := droppedWrites(ae.pulse)
		if !ok {
			return 0, false
		}
		fresh := total - ae.lastDropped
		ae.lastDropped = total
		return float64(fresh), true

	case "health_status":
		if ae.pulse.healthRunner == nil {
			return 1, true // healthy by default
		}
		status := ae.pulse.healthRunner.GetCompositeStatus()
		switch status {
		case "healthy":
			return 1, true
		case "degraded":
			return 0.5, true
		default:
			return 0, true
		}

	default:
		return 0, false
	}
}

// checkCondition evaluates value against threshold using the operator.
func (ae *AlertEngine) checkCondition(value float64, operator string, threshold float64) bool {
	switch operator {
	case ">":
		return value > threshold
	case ">=":
		return value >= threshold
	case "<":
		return value < threshold
	case "<=":
		return value <= threshold
	case "==":
		return value == threshold
	case "!=":
		return value != threshold
	default:
		return false
	}
}

// fireAlert creates an alert record, stores it, sends notifications, and broadcasts.
func (ae *AlertEngine) fireAlert(rs *ruleState, value float64) {
	cooldown := ae.pulse.config.Alerts.Cooldown
	if cooldown > 0 && !rs.lastFired.IsZero() && time.Since(rs.lastFired) < cooldown {
		// Still in cooldown: nothing is stored or announced, so there is no
		// open record for resolveAlert to close either.
		rs.alertID = ""
		return
	}

	alertID := GenerateTraceID()
	rs.alertID = alertID
	rs.lastFired = time.Now()
	rs.firedValue = value

	alert := AlertRecord{
		ID:        alertID,
		RuleName:  rs.rule.Name,
		Metric:    rs.rule.Metric,
		Value:     value,
		Threshold: rs.rule.Threshold,
		Operator:  rs.rule.Operator,
		Severity:  rs.rule.Severity,
		State:     AlertStateFiring,
		Route:     rs.rule.Route,
		Message:   formatAlertMessage(rs.rule, value),
		FiredAt:   time.Now(),
	}

	if rs.rule.Metric == "retention_coverage" {
		if detail := retentionDetail(ae.pulse); detail != "" {
			alert.Message += " — " + detail
		}
	}

	ae.pulse.internalError("storage: alerts", ae.pulse.storage.StoreAlert(alert))

	// Broadcast to WebSocket clients
	ae.pulse.BroadcastAlert(alert)

	// Send notifications asynchronously
	ae.notify(alert)

	if ae.pulse.config.DevMode {
		ae.pulse.logger.Printf("[pulse] alert fired: %s — %s", rs.rule.Name, alert.Message)
	}
}

// resolveAlert closes the open alert record in place — same ID, original
// firing time and value, state resolved — and sends resolution
// notifications.
func (ae *AlertEngine) resolveAlert(rs *ruleState) {
	if rs.alertID == "" {
		return // the firing was suppressed by cooldown; nothing was announced
	}
	now := time.Now()
	alert := AlertRecord{
		ID:         rs.alertID,
		RuleName:   rs.rule.Name,
		Metric:     rs.rule.Metric,
		Value:      rs.firedValue,
		Threshold:  rs.rule.Threshold,
		Operator:   rs.rule.Operator,
		Severity:   rs.rule.Severity,
		State:      AlertStateResolved,
		Route:      rs.rule.Route,
		Message:    fmt.Sprintf("[Resolved] %s has returned to normal", rs.rule.Name),
		FiredAt:    rs.lastFired,
		ResolvedAt: &now,
	}

	ae.pulse.internalError("storage: alerts", ae.pulse.storage.StoreAlert(alert))
	rs.alertID = ""

	ae.pulse.BroadcastAlert(alert)

	ae.notify(alert)

	if ae.pulse.config.DevMode {
		ae.pulse.logger.Printf("[pulse] alert resolved: %s", rs.rule.Name)
	}
}

// formatAlertMessage generates a human-readable alert message.
func formatAlertMessage(rule AlertRule, value float64) string {
	var valueStr string
	switch rule.Metric {
	case "p95_latency":
		valueStr = fmt.Sprintf("%.0fms", value)
	case "error_rate":
		valueStr = fmt.Sprintf("%.1f%%", value)
	case "heap_alloc_mb":
		valueStr = fmt.Sprintf("%.0fMB", value)
	case "goroutine_growth":
		valueStr = fmt.Sprintf("%.0f/hr", value)
	case "retention_coverage":
		valueStr = fmt.Sprintf("%.0f%%", value*100)
	case "storage_dropped_writes":
		valueStr = fmt.Sprintf("%.0f dropped", value)
	case "health_status":
		if value == 0 {
			valueStr = "unhealthy"
		} else if value == 0.5 {
			valueStr = "degraded"
		} else {
			valueStr = "healthy"
		}
	default:
		valueStr = fmt.Sprintf("%.2f", value)
	}

	var thresholdStr string
	switch rule.Metric {
	case "p95_latency":
		thresholdStr = fmt.Sprintf("%.0fms", rule.Threshold)
	case "error_rate":
		thresholdStr = fmt.Sprintf("%.1f%%", rule.Threshold)
	case "heap_alloc_mb":
		thresholdStr = fmt.Sprintf("%.0fMB", rule.Threshold)
	case "goroutine_growth":
		thresholdStr = fmt.Sprintf("%.0f/hr", rule.Threshold)
	case "retention_coverage":
		thresholdStr = fmt.Sprintf("%.0f%%", rule.Threshold*100)
	default:
		thresholdStr = fmt.Sprintf("%.2f", rule.Threshold)
	}

	msg := fmt.Sprintf("%s: %s %s %s (current: %s)", rule.Name, rule.Metric, rule.Operator, thresholdStr, valueStr)
	if rule.Route != "" {
		msg += fmt.Sprintf(" [route: %s]", rule.Route)
	}
	return msg
}

// --- Notifications ---

// sendNotifications dispatches alert to all configured notification channels.
func (ae *AlertEngine) sendNotifications(alert AlertRecord) {
	cfg := ae.pulse.config.Alerts

	if cfg.Slack != nil && cfg.Slack.WebhookURL != "" {
		ae.sendSlack(cfg.Slack, alert)
	}

	if cfg.Discord != nil && cfg.Discord.WebhookURL != "" {
		ae.sendDiscord(cfg.Discord, alert)
	}

	if cfg.Email != nil && cfg.Email.Host != "" {
		ae.sendEmail(cfg.Email, alert)
	}

	for _, wh := range cfg.Webhooks {
		ae.sendWebhook(wh, alert)
	}
}

// sendSlack sends a Slack notification via webhook.
func (ae *AlertEngine) sendSlack(cfg *SlackConfig, alert AlertRecord) {
	color := slackColor(alert)

	payload := map[string]interface{}{
		"attachments": []map[string]interface{}{
			{
				"color":  color,
				"title":  fmt.Sprintf("[Pulse] %s: %s", strings.ToUpper(alert.Severity), alert.RuleName),
				"text":   alert.Message,
				"fields": slackFields(alert),
				"ts":     alert.FiredAt.Unix(),
			},
		},
	}

	if cfg.Channel != "" {
		payload["channel"] = cfg.Channel
	}

	ae.postJSON(cfg.WebhookURL, payload, "slack")
}

// sendDiscord sends a Discord notification via webhook.
func (ae *AlertEngine) sendDiscord(cfg *DiscordConfig, alert AlertRecord) {
	color := discordColor(alert)

	payload := map[string]interface{}{
		"embeds": []map[string]interface{}{
			{
				"title":       fmt.Sprintf("[Pulse] %s: %s", strings.ToUpper(alert.Severity), alert.RuleName),
				"description": alert.Message,
				"color":       color,
				"fields":      discordFields(alert),
				"timestamp":   alert.FiredAt.Format(time.RFC3339),
			},
		},
	}

	ae.postJSON(cfg.WebhookURL, payload, "discord")
}

// sendEmail sends an email notification via SMTP.
func (ae *AlertEngine) sendEmail(cfg *EmailConfig, alert AlertRecord) {
	subject := fmt.Sprintf("[Pulse Alert] %s: %s", strings.ToUpper(alert.Severity), alert.RuleName)

	body := fmt.Sprintf(
		"Alert: %s\nSeverity: %s\nMetric: %s\nValue: %s\nThreshold: %s %s\nState: %s\nTime: %s\n\n%s",
		alert.RuleName,
		alert.Severity,
		alert.Metric,
		fmt.Sprintf("%.2f", alert.Value),
		alert.Operator,
		fmt.Sprintf("%.2f", alert.Threshold),
		alert.State,
		alert.FiredAt.Format(time.RFC3339),
		alert.Message,
	)

	msg := fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		cfg.From,
		strings.Join(cfg.To, ","),
		subject,
		body,
	)

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	var auth smtp.Auth
	if cfg.Username != "" {
		auth = smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	}

	if err := sendMail(addr, auth, cfg.From, cfg.To, []byte(msg)); err != nil {
		ae.pulse.internalError("notifications", fmt.Errorf("email via %s: %w", addr, err))
	}
}

// smtpTimeout bounds a whole SMTP conversation. net/smtp has no timeouts of
// its own, so an unresponsive mail server would otherwise hang the sender
// indefinitely.
var smtpTimeout = 15 * time.Second

// sendMail is smtp.SendMail — STARTTLS when offered, AUTH when configured —
// with a dial timeout and a deadline on the connection.
func sendMail(addr string, auth smtp.Auth, from string, to []string, msg []byte) error {
	for _, line := range append([]string{from}, to...) {
		if strings.ContainsAny(line, "\r\n") {
			return errors.New("smtp: address contains CR or LF")
		}
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout("tcp", addr, smtpTimeout)
	if err != nil {
		return err
	}
	if err := conn.SetDeadline(time.Now().Add(smtpTimeout)); err != nil {
		conn.Close()
		return err
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()

	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return err
		}
	}
	if auth != nil {
		if ok, _ := c.Extension("AUTH"); !ok {
			return errors.New("smtp: server doesn't support AUTH")
		}
		if err := c.Auth(auth); err != nil {
			return err
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// notificationError describes a failed webhook call without its URL: chat
// webhook URLs embed their credentials.
func notificationError(service string, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s webhook: %v", service, ue.Err)
	}
	return fmt.Errorf("%s webhook: %v", service, err)
}

// sendWebhook sends a POST to a generic webhook URL with optional HMAC signature.
func (ae *AlertEngine) sendWebhook(cfg WebhookConfig, alert AlertRecord) {
	payload := map[string]interface{}{
		"alert":     alert.RuleName,
		"severity":  alert.Severity,
		"metric":    alert.Metric,
		"value":     alert.Value,
		"threshold": alert.Threshold,
		"operator":  alert.Operator,
		"state":     alert.State,
		"message":   alert.Message,
		"route":     alert.Route,
		"fired_at":  alert.FiredAt.Format(time.RFC3339),
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return
	}

	req, err := http.NewRequest("POST", cfg.URL, bytes.NewReader(data))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Pulse-Alert/1.0")

	// Add HMAC signature if secret is configured
	if cfg.Secret != "" {
		mac := hmac.New(sha256.New, []byte(cfg.Secret))
		mac.Write(data)
		sig := hex.EncodeToString(mac.Sum(nil))
		req.Header.Set("X-Pulse-Signature", sig)
	}

	// Add custom headers
	for k, v := range cfg.Headers {
		req.Header.Set(k, v)
	}

	// Retry up to 3 times with exponential backoff
	client := &http.Client{Timeout: 10 * time.Second}
	for attempt := 0; attempt < 3; attempt++ {
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 300 {
				return // Success
			}
		}
		if attempt < 2 {
			time.Sleep(time.Duration(1<<uint(attempt)) * time.Second)
			// Recreate request body for retry
			req.Body = io.NopCloser(bytes.NewReader(data))
		}
	}

	host := "webhook"
	if u, err := url.Parse(cfg.URL); err == nil && u.Host != "" {
		host = u.Host
	}
	ae.pulse.internalError("notifications", fmt.Errorf("webhook to %s failed after 3 attempts", host))
}

// postJSON sends a JSON POST request (for Slack/Discord webhooks).
func (ae *AlertEngine) postJSON(url string, payload interface{}, service string) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		ae.pulse.internalError("notifications", notificationError(service, err))
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		ae.pulse.internalError("notifications", fmt.Errorf("%s webhook returned HTTP %d", service, resp.StatusCode))
	}
}

// --- Helpers ---

func slackColor(alert AlertRecord) string {
	if alert.State == AlertStateResolved {
		return "#22c55e" // green
	}
	switch alert.Severity {
	case "critical":
		return "#ef4444" // red
	case "warning":
		return "#f97316" // orange
	default:
		return "#3b82f6" // blue
	}
}

func slackFields(alert AlertRecord) []map[string]interface{} {
	fields := []map[string]interface{}{
		{"title": "Severity", "value": alert.Severity, "short": true},
		{"title": "Metric", "value": alert.Metric, "short": true},
		{"title": "State", "value": string(alert.State), "short": true},
	}
	if alert.Route != "" {
		fields = append(fields, map[string]interface{}{
			"title": "Route", "value": alert.Route, "short": true,
		})
	}
	return fields
}

func discordColor(alert AlertRecord) int {
	if alert.State == AlertStateResolved {
		return 0x22c55e // green
	}
	switch alert.Severity {
	case "critical":
		return 0xef4444 // red
	case "warning":
		return 0xf97316 // orange
	default:
		return 0x3b82f6 // blue
	}
}

func discordFields(alert AlertRecord) []map[string]interface{} {
	fields := []map[string]interface{}{
		{"name": "Severity", "value": alert.Severity, "inline": true},
		{"name": "Metric", "value": alert.Metric, "inline": true},
		{"name": "State", "value": string(alert.State), "inline": true},
	}
	if alert.Route != "" {
		fields = append(fields, map[string]interface{}{
			"name": "Route", "value": alert.Route, "inline": true,
		})
	}
	return fields
}

// GetRuleStates returns the current state of all alert rules (for API/dashboard).
func (ae *AlertEngine) GetRuleStates() []map[string]interface{} {
	ae.mu.RLock()
	defer ae.mu.RUnlock()

	result := make([]map[string]interface{}, 0, len(ae.ruleStates))
	for _, rs := range ae.ruleStates {
		entry := map[string]interface{}{
			"name":      rs.rule.Name,
			"metric":    rs.rule.Metric,
			"operator":  rs.rule.Operator,
			"threshold": rs.rule.Threshold,
			"duration":  rs.rule.Duration.String(),
			"severity":  rs.rule.Severity,
			"state":     string(rs.state),
			"route":     rs.rule.Route,
		}
		if !rs.lastFired.IsZero() {
			entry["last_fired"] = rs.lastFired
		}
		if !rs.pendingSince.IsZero() {
			entry["pending_since"] = rs.pendingSince
		}
		result = append(result, entry)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i]["name"].(string) < result[j]["name"].(string)
	})

	return result
}
