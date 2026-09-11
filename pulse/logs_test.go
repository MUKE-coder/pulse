package pulse

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func mountForLogs(t *testing.T, opts ...Option) (*Pulse, *gin.Engine) {
	t.Helper()
	router := gin.New()
	p := Mount(context.Background(), router, nil, append([]Option{WithDevMode()}, opts...)...)
	t.Cleanup(func() { _ = p.Shutdown() })
	return p, router
}

func allLogs(t *testing.T, p *Pulse, f logFilter) []LogRecord {
	t.Helper()
	if f.MinLevel == 0 {
		f.MinLevel = lowestLogLevel
	}
	got, err := p.storage.(logStore).queryLogs(f)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// callLogsAPI runs a logs API handler directly, without dashboard auth.
func callLogsAPI(t *testing.T, h gin.HandlerFunc, target string, params gin.Params, into any) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	c.Params = params
	h(c)
	if w.Code != http.StatusOK {
		t.Fatalf("%s: status %d: %s", target, w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), into); err != nil {
		t.Fatalf("%s: %v", target, err)
	}
}

type logsResponse struct {
	Logs      []LogRecord `json:"logs"`
	Match     string      `json:"match"`
	Capturing bool        `json:"capturing"`
}

// Lines logged with the request context (or the *gin.Context) carry the
// request's trace and span; attributes are flattened and redacted, and the
// wrapped handler still gets the original record.
func TestSlogHandler_CorrelatesAndRedacts(t *testing.T) {
	p, router := mountForLogs(t)
	var out bytes.Buffer
	logger := slog.New(p.SlogHandler(slog.NewJSONHandler(&out, nil)))

	router.GET("/checkout", func(c *gin.Context) {
		logger.InfoContext(c.Request.Context(), "charging card 4111 1111 1111 1111",
			"password", "hunter2", slog.Group("user", "id", 42, "api_token", "abc123"))
		logger.With("order", 7).WithGroup("payment").WarnContext(c, "declined", "code", "insufficient_funds")
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/checkout", nil))
	traceID, spanID, _ := ParseTraceparent(w.Header().Get(TraceparentHeader))

	logs := allLogs(t, p, logFilter{TraceID: traceID})
	if len(logs) != 2 {
		t.Fatalf("captured %d lines for the request's trace, want 2: %+v", len(logs), logs)
	}
	first, second := logs[0], logs[1]
	if first.SpanID != spanID || second.SpanID != spanID {
		t.Errorf("span IDs = %s, %s; want the request's %s", first.SpanID, second.SpanID, spanID)
	}
	if strings.Contains(first.Message, "4111") {
		t.Errorf("card number not scrubbed from the message: %q", first.Message)
	}
	for key, want := range map[string]string{"password": "[REDACTED]", "user.id": "42", "user.api_token": "[REDACTED]"} {
		if got := first.Attrs[key]; got != want {
			t.Errorf("attr %s = %q, want %q", key, got, want)
		}
	}
	if second.Level != slog.LevelWarn || second.Attrs["order"] != "7" || second.Attrs["payment.code"] != "insufficient_funds" {
		t.Errorf("second line = %+v", second)
	}

	if n := strings.Count(out.String(), "\n"); n != 2 || !strings.Contains(out.String(), "hunter2") {
		t.Errorf("wrapped handler should get both records unchanged, got:\n%s", out.String())
	}

	var resp logsResponse
	callLogsAPI(t, logsHandler(p), "/logs?trace_id="+traceID+"&level=warn", nil, &resp)
	if !resp.Capturing || len(resp.Logs) != 1 || resp.Logs[0].Message != "declined" {
		t.Errorf("GET /logs?trace_id&level=warn = %+v", resp)
	}
}

// Logs.MinLevel limits what Pulse captures, not what the wrapped handler
// receives.
func TestSlogHandler_MinLevel(t *testing.T) {
	p, _ := mountForLogs(t, WithLogMinLevel(slog.LevelWarn))
	var out bytes.Buffer
	h := p.SlogHandler(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logger := slog.New(h)
	logger.Debug("d")
	logger.Info("i")
	logger.Warn("w")

	if n := strings.Count(out.String(), "\n"); n != 3 {
		t.Errorf("wrapped handler got %d lines, want 3", n)
	}
	if logs := allLogs(t, p, logFilter{}); len(logs) != 1 || logs[0].Message != "w" {
		t.Errorf("captured %+v, want only the warning", logs)
	}
	if p.SlogHandler(nil).Enabled(context.Background(), slog.LevelInfo) {
		t.Error("capture-only handler should not enable levels below MinLevel")
	}
}

// An error's logs are the lines its own request logged — not a concurrent
// request's.
func TestErrorLogs_LinesFromTheFailingRequest(t *testing.T) {
	p, router := mountForLogs(t)
	logger := slog.New(p.SlogHandler(nil))
	router.POST("/pay", func(c *gin.Context) {
		logger.InfoContext(c, "starting payment", "amount", 30)
		logger.ErrorContext(c, "gateway timeout")
		_ = c.Error(fmt.Errorf("payment failed"))
		c.Status(http.StatusBadGateway)
	})
	router.GET("/other", func(c *gin.Context) {
		logger.InfoContext(c, "unrelated")
		c.Status(http.StatusOK)
	})
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/other", nil))
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/pay", nil))

	errs, _ := p.storage.GetErrors(ErrorFilter{TimeRange: Last1h(), Limit: 10})
	if len(errs) != 1 {
		t.Fatalf("got %d errors", len(errs))
	}
	var resp logsResponse
	callLogsAPI(t, errorLogsHandler(p), "/errors/x/logs", gin.Params{{Key: "id", Value: errs[0].ID}}, &resp)
	var msgs []string
	for _, l := range resp.Logs {
		msgs = append(msgs, l.Message)
	}
	if resp.Match != "trace" || strings.Join(msgs, "|") != "starting payment|gateway timeout" {
		t.Errorf("error logs = match %q, %v; want the two lines /pay logged", resp.Match, msgs)
	}
}

// Without a trace match, an error's logs are the lines around it.
func TestErrorLogs_FallsBackToTimeWindow(t *testing.T) {
	p, _ := mountForLogs(t)
	now := time.Now()
	rec := buildErrorRecord("GET", "/job", "job failed", ErrorTypeInternal, "", nil, "")
	if err := p.storage.StoreError(rec); err != nil {
		t.Fatal(err)
	}
	p.logCapture.Store(true)
	p.captureLog(LogRecord{Time: now.Add(-5 * time.Minute), Message: "long before"})
	p.captureLog(LogRecord{Time: now.Add(-2 * time.Second), Level: slog.LevelError, Message: "worker crashed"})

	var resp logsResponse
	callLogsAPI(t, errorLogsHandler(p), "/errors/x/logs", gin.Params{{Key: "id", Value: rec.ID}}, &resp)
	if resp.Match != "time" || len(resp.Logs) != 1 || resp.Logs[0].Message != "worker crashed" {
		t.Errorf("fallback = match %q, %+v; want only the line within 30s", resp.Match, resp.Logs)
	}
}

// LogWriter passes bytes through unchanged, reassembles lines split across
// writes, and understands zerolog, zap and standard log output.
func TestLogWriter_ParsesCommonFormats(t *testing.T) {
	p, _ := mountForLogs(t)
	var out bytes.Buffer
	w := p.LogWriter(&out)

	const trace1, trace2 = "4bf92f3577b34da6a3ce929d0e0e4736", "0af7651916cd43dd8448eb211c80319c"
	zerolog := `{"level":"error","time":"2026-09-11T10:00:00Z","message":"db down","trace_id":"` + trace1 +
		`","user":{"email":"a@example.com","password":"x"}}` + "\n"
	zap := `{"level":"warn","ts":1757584800.25,"msg":"slow","caller":"orders.go:42"}` + "\n"
	var written strings.Builder
	for _, chunk := range []string{zerolog[:30], zerolog[30:], zap} {
		written.WriteString(chunk)
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	std := log.New(w, "", log.LstdFlags)
	std.Printf("plain line trace_id=%s level=warn", trace2)

	if !strings.HasPrefix(out.String(), written.String()) || !strings.HasSuffix(out.String(), "level=warn\n") {
		t.Errorf("output not passed through unchanged:\n%s", out.String())
	}

	byMsg := map[string]LogRecord{}
	for _, l := range allLogs(t, p, logFilter{}) {
		byMsg[l.Message] = l
	}
	if l := byMsg["db down"]; l.Level != slog.LevelError || l.TraceID != trace1 ||
		!l.Time.Equal(time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)) ||
		l.Attrs["user.password"] != "[REDACTED]" || l.Attrs["user.email"] != "a@example.com" {
		t.Errorf("zerolog line = %+v", l)
	}
	if l := byMsg["slow"]; l.Level != slog.LevelWarn || l.Time.UnixMilli() != 1757584800250 || l.Attrs["caller"] != "orders.go:42" {
		t.Errorf("zap line = %+v", l)
	}
	plain := "plain line trace_id=" + trace2 + " level=warn"
	if l, ok := byMsg[plain]; !ok || l.TraceID != trace2 || l.Level != slog.LevelWarn {
		t.Errorf("standard log line = %+v (found %v); want the date prefix stripped, trace and level read", l, ok)
	}
}

// When Pulse's own log output is routed into capture, a failure to store a
// line must be counted, not logged — logging it would capture it again,
// forever.
func TestLogCapture_StorageFailureDoesNotLoop(t *testing.T) {
	p := newPulse(context.Background(), applyDefaults(Config{DevMode: true}))
	t.Cleanup(p.cancel)
	s, err := NewSQLiteStorage(":memory:", "test")
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close() // every storeLog now fails
	p.storage = s
	p.logger = log.New(p.LogWriter(nil), "", 0)

	p.logger.Print("hello")
	if n := p.internal.snapshot()["storage: logs"]; n != 1 {
		t.Errorf("storage: logs failures = %d, want exactly 1", n)
	}
}

func TestConformance_LogsStoreAndQuery(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s Storage) {
		ls := s.(logStore)
		now := time.Now()
		for _, r := range []LogRecord{
			{Time: now.Add(-3 * time.Minute), Level: slog.LevelDebug, Message: "cache miss", TraceID: "t1"},
			{Time: now.Add(-2 * time.Minute), Level: slog.LevelInfo, Message: "Order placed", TraceID: "t1",
				SpanID: "s1", Attrs: map[string]string{"order.id": "7"}},
			{Time: now.Add(-time.Minute), Level: slog.LevelError, Message: "order failed", TraceID: "t2"},
			{Time: now, Level: slog.LevelWarn, Message: "retrying"},
		} {
			if err := ls.storeLog(r); err != nil {
				t.Fatal(err)
			}
		}
		query := func(f logFilter) []string {
			if f.MinLevel == 0 {
				f.MinLevel = lowestLogLevel
			}
			got, err := ls.queryLogs(f)
			if err != nil {
				t.Fatal(err)
			}
			var msgs []string
			for _, r := range got {
				msgs = append(msgs, r.Message)
			}
			return msgs
		}
		check := func(name string, got []string, want string) {
			if strings.Join(got, "|") != want {
				t.Errorf("%s = %v, want %s", name, got, want)
			}
		}
		check("by trace", query(logFilter{TraceID: "t1"}), "cache miss|Order placed")
		check("warn and up", query(logFilter{MinLevel: slog.LevelWarn}), "order failed|retrying")
		check("contains", query(logFilter{Contains: "ORDER"}), "Order placed|order failed")
		check("newest 2", query(logFilter{Limit: 2}), "order failed|retrying")
		check("time range", query(logFilter{TimeRange: TimeRange{Start: now.Add(-150 * time.Second), End: now.Add(-30 * time.Second)}}),
			"Order placed|order failed")

		got, _ := ls.queryLogs(logFilter{TraceID: "t1", MinLevel: slog.LevelInfo})
		if len(got) != 1 || got[0].Attrs["order.id"] != "7" || got[0].SpanID != "s1" || got[0].Level != slog.LevelInfo {
			t.Errorf("round trip = %+v", got)
		}

		if sq, ok := s.(*SQLiteStorage); ok {
			_ = sq.storeLog(LogRecord{Time: now.Add(-48 * time.Hour), Message: "ancient"})
			if err := sq.Cleanup(24 * time.Hour); err != nil {
				t.Fatal(err)
			}
			check("after cleanup", query(logFilter{Contains: "ancient"}), "")
		}
	})
}
