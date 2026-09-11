package pulse

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// Log capture. Application logs reach Pulse through [Pulse.SlogHandler]
// (log/slog) or [Pulse.LogWriter] (the log package, zap, zerolog — anything
// that writes lines); see logs_capture.go. Each line is redacted like
// everything else Pulse stores and, when it was logged with a request's
// context, tagged with that request's trace and span IDs, so an error's
// detail can show the lines its request logged.

// LogRecord is one captured log line.
type LogRecord struct {
	Time    time.Time  `json:"time"`
	Level   slog.Level `json:"level"`
	Message string     `json:"message"`
	// Attrs holds the line's attributes, flattened: nested groups and JSON
	// objects become dotted keys ("user.id").
	Attrs map[string]string `json:"attrs,omitempty"`
	// TraceID and SpanID identify the request the line was logged in.
	TraceID    string `json:"trace_id,omitempty"`
	SpanID     string `json:"span_id,omitempty"`
	InstanceID string `json:"instance_id,omitempty"`
}

const (
	defaultLogCapacity = 50000

	// Bounds on what one captured line may hold.
	maxLogMessageBytes = 8 << 10
	maxLogAttrs        = 64
	maxLogValueBytes   = 2 << 10

	// When no line carries an error's trace, its logs are the last
	// errorLogLines lines within errorLogWindow of its latest occurrence.
	errorLogWindow = 30 * time.Second
	errorLogLines  = 100

	defaultLogQueryLimit = 200
	maxLogQueryLimit     = 1000
)

// lowestLogLevel makes a logFilter match every level.
const lowestLogLevel = slog.Level(math.MinInt32)

// logFilter selects captured lines.
type logFilter struct {
	TimeRange TimeRange // zero bounds are open
	TraceID   string
	MinLevel  slog.Level
	Contains  string // case-insensitive substring of the message
	Limit     int    // keep the newest Limit matches; 0 keeps all
}

func (f logFilter) match(r LogRecord) bool {
	if !f.TimeRange.Start.IsZero() && r.Time.Before(f.TimeRange.Start) {
		return false
	}
	if !f.TimeRange.End.IsZero() && r.Time.After(f.TimeRange.End) {
		return false
	}
	if f.TraceID != "" && r.TraceID != f.TraceID {
		return false
	}
	if r.Level < f.MinLevel {
		return false
	}
	return f.Contains == "" || strings.Contains(strings.ToLower(r.Message), strings.ToLower(f.Contains))
}

// logStore is implemented by storage backends that keep captured logs.
// Unexported for the same reason as rollupStore.
type logStore interface {
	storeLog(r LogRecord) error
	// queryLogs returns the newest f.Limit matching lines, oldest first.
	queryLogs(f logFilter) ([]LogRecord, error)
}

// captureLog redacts r, stores it, and hands it to live tails and exporters.
//
// Nothing on this path may log: Pulse's own log output can be flowing
// through SlogHandler or LogWriter, and a log line about a failed capture
// would be captured in turn. Failures are counted instead.
func (p *Pulse) captureLog(r LogRecord) {
	r.Message = clipString(p.redactor.scrubValue(r.Message), maxLogMessageBytes)
	for k, v := range r.Attrs {
		r.Attrs[k] = p.redactor.logAttr(k, v)
	}
	if r.Time.IsZero() {
		r.Time = time.Now()
	}
	r.InstanceID = p.config.InstanceID

	if ls, ok := p.storage.(logStore); ok {
		p.internal.count("storage: logs", ls.storeLog(r))
	}
	p.tailLog(r)
	exported := r
	p.export(Event{Kind: EventLog, Log: &exported})
}

// logAttr redacts an attribute value: all of it when the key names a
// sensitive field, otherwise any secrets the value detectors find in it.
func (r *redactor) logAttr(key, value string) string {
	name := key
	if i := strings.LastIndexByte(key, '.'); i >= 0 {
		name = key[i+1:]
	}
	if r.sensitiveField(name) || r.sensitiveField(key) {
		return redactedPlaceholder
	}
	return r.scrubValue(value)
}

// clipString cuts s to at most n bytes, on a rune boundary.
func clipString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// --- Live tail ---

// logTail batches captured lines for dashboards tailing them over
// WebSocket, so a burst of logging becomes a few messages, not thousands.
type logTail struct {
	mu      sync.Mutex
	pending []LogRecord
}

const (
	logTailInterval = 250 * time.Millisecond
	logTailMax      = 500 // lines sent per interval; the rest of a burst is skipped
)

func (p *Pulse) tailLog(r LogRecord) {
	if p.wsHub == nil || p.wsHub.ClientCount() == 0 {
		return
	}
	p.logTail.mu.Lock()
	if len(p.logTail.pending) < logTailMax {
		p.logTail.pending = append(p.logTail.pending, r)
	}
	p.logTail.mu.Unlock()
}

// startLogTail sends batched lines to the WebSocket clients subscribed to
// the logs channel.
func startLogTail(p *Pulse) {
	p.startBackground("log-tail", func(ctx context.Context) {
		ticker := time.NewTicker(logTailInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.logTail.mu.Lock()
				batch := p.logTail.pending
				p.logTail.pending = nil
				p.logTail.mu.Unlock()
				if len(batch) > 0 {
					p.wsHub.broadcastSubscribed(WSTypeLogs, batch)
				}
			}
		}
	})
}

// --- API ---

// logsHandler serves GET /pulse/api/logs: captured lines, oldest first.
// Parameters: range, trace_id, level (the lowest level shown), q (message
// text), limit.
func logsHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		f := logFilter{
			TimeRange: parseTimeRangeParam(c),
			TraceID:   c.Query("trace_id"),
			MinLevel:  lowestLogLevel,
			Contains:  c.Query("q"),
			Limit:     min(queryInt(c, "limit", defaultLogQueryLimit), maxLogQueryLimit),
		}
		if f.Limit == 0 {
			f.Limit = defaultLogQueryLimit
		}
		// A trace is looked up whenever it happened, not just in the last hour.
		if f.TraceID != "" && c.Query("range") == "" {
			f.TimeRange = TimeRange{}
		}
		if lv := c.Query("level"); lv != "" {
			var l slog.Level
			if err := l.UnmarshalText([]byte(lv)); err == nil {
				f.MinLevel = l
			}
		}

		logs := []LogRecord{}
		ls, supported := p.storage.(logStore)
		if supported {
			if got, err := ls.queryLogs(f); err == nil && got != nil {
				logs = got
			}
		}
		c.JSON(http.StatusOK, gin.H{"logs": logs, "capturing": p.logCapture.Load(), "supported": supported})
	}
}

// errorLogsHandler serves GET /pulse/api/errors/:id/logs: the lines logged
// by the request behind the error's latest occurrence (match "trace"); when
// none carry its trace, the last lines logged within errorLogWindow of that
// occurrence (match "time").
func errorLogsHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		rec, err := p.storage.GetErrorByID(c.Param("id"))
		if err != nil || rec == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "error not found"})
			return
		}
		resp := gin.H{"logs": []LogRecord{}, "match": "none", "capturing": p.logCapture.Load()}
		ls, ok := p.storage.(logStore)
		if !ok {
			c.JSON(http.StatusOK, resp)
			return
		}
		if rec.TraceID != "" {
			got, _ := ls.queryLogs(logFilter{TraceID: rec.TraceID, MinLevel: lowestLogLevel, Limit: maxLogQueryLimit})
			if len(got) > 0 {
				resp["logs"], resp["match"] = got, "trace"
				c.JSON(http.StatusOK, resp)
				return
			}
		}
		got, _ := ls.queryLogs(logFilter{
			TimeRange: TimeRange{Start: rec.LastSeen.Add(-errorLogWindow), End: rec.LastSeen.Add(errorLogWindow)},
			MinLevel:  lowestLogLevel,
			Limit:     errorLogLines,
		})
		if len(got) > 0 {
			resp["logs"], resp["match"] = got, "time"
		}
		c.JSON(http.StatusOK, resp)
	}
}

// --- MemoryStorage ---

// logBuffer returns the log ring buffer, creating it when create is set.
// It is created on first use so apps that don't capture logs don't pay for
// it.
func (s *MemoryStorage) logBuffer(create bool) *RingBuffer[LogRecord] {
	if rb := s.logs.Load(); rb != nil || !create {
		return rb
	}
	s.logs.CompareAndSwap(nil, NewRingBuffer[LogRecord](s.logCapacity))
	return s.logs.Load()
}

func (s *MemoryStorage) storeLog(r LogRecord) error {
	s.logBuffer(true).Push(r)
	return nil
}

func (s *MemoryStorage) queryLogs(f logFilter) ([]LogRecord, error) {
	rb := s.logBuffer(false)
	if rb == nil {
		return nil, nil
	}
	return newestLogs(rb.Filter(f.match), f.Limit), nil
}

// newestLogs sorts lines by time and keeps the newest limit of them.
func newestLogs(recs []LogRecord, limit int) []LogRecord {
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].Time.Before(recs[j].Time) })
	if limit > 0 && len(recs) > limit {
		recs = recs[len(recs)-limit:]
	}
	return recs
}

// --- SQLiteStorage ---

func (s *SQLiteStorage) storeLog(r LogRecord) error {
	var attrs string
	if len(r.Attrs) > 0 {
		b, err := json.Marshal(r.Attrs)
		if err != nil {
			return err
		}
		attrs = string(b)
	}
	return s.enqueue(
		`INSERT INTO logs (timestamp, level, message, attrs, trace_id, span_id, instance_id) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.Time.UnixNano(), int(r.Level), r.Message, attrs, r.TraceID, r.SpanID, r.InstanceID,
	)
}

func (s *SQLiteStorage) queryLogs(f logFilter) ([]LogRecord, error) {
	s.sync()
	q := `SELECT timestamp, level, message, attrs, trace_id, span_id, instance_id FROM logs WHERE level >= ?`
	args := []any{int(f.MinLevel)}
	if !f.TimeRange.Start.IsZero() {
		q += ` AND timestamp >= ?`
		args = append(args, f.TimeRange.Start.UnixNano())
	}
	if !f.TimeRange.End.IsZero() {
		q += ` AND timestamp <= ?`
		args = append(args, f.TimeRange.End.UnixNano())
	}
	if f.TraceID != "" {
		q += ` AND trace_id = ?`
		args = append(args, f.TraceID)
	}
	if f.Contains != "" {
		q += ` AND instr(lower(message), ?) > 0`
		args = append(args, strings.ToLower(f.Contains))
	}
	q += ` ORDER BY timestamp DESC`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
	}

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LogRecord
	for rows.Next() {
		var (
			r     LogRecord
			ts    int64
			level int
			attrs sql.NullString
		)
		if err := rows.Scan(&ts, &level, &r.Message, &attrs, &r.TraceID, &r.SpanID, &r.InstanceID); err != nil {
			return nil, err
		}
		r.Time, r.Level = time.Unix(0, ts), slog.Level(level)
		if attrs.Valid && attrs.String != "" {
			_ = json.Unmarshal([]byte(attrs.String), &r.Attrs)
		}
		out = append(out, r)
	}
	// Newest first from the query; callers get them oldest first.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

var (
	_ logStore = (*MemoryStorage)(nil)
	_ logStore = (*SQLiteStorage)(nil)
)
