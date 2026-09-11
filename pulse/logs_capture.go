package pulse

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// --- log/slog ---

// SlogHandler returns a [slog.Handler] that copies records at or above
// Logs.MinLevel into Pulse, then passes every record to next unchanged.
// Wrap the handler you already use:
//
//	slog.SetDefault(slog.New(p.SlogHandler(slog.NewJSONHandler(os.Stdout, nil))))
//
// Records logged with a request's context — slog.InfoContext(c.Request.Context(), …),
// or slog.InfoContext(c, …) with the *gin.Context — carry that request's
// trace and span IDs; that is how an error's detail finds the lines its
// request logged. Attribute values are redacted by the same rules as
// request bodies; what next receives is not. A nil next only captures.
func (p *Pulse) SlogHandler(next slog.Handler) slog.Handler {
	p.logCapture.Store(true)
	return &slogHandler{p: p, next: next}
}

type slogHandler struct {
	p      *Pulse
	next   slog.Handler
	fixed  map[string]string // attributes from WithAttrs, flattened
	prefix string            // open groups, as "a.b."
}

func (h *slogHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= h.p.config.Logs.MinLevel || (h.next != nil && h.next.Enabled(ctx, l))
}

func (h *slogHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= h.p.config.Logs.MinLevel {
		h.capture(ctx, r)
	}
	if h.next != nil && h.next.Enabled(ctx, r.Level) {
		return h.next.Handle(ctx, r)
	}
	return nil
}

func (h *slogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	c := *h
	c.fixed = make(map[string]string, len(h.fixed)+len(attrs))
	for k, v := range h.fixed {
		c.fixed[k] = v
	}
	for _, a := range attrs {
		flattenAttr(c.fixed, h.prefix, a)
	}
	if h.next != nil {
		c.next = h.next.WithAttrs(attrs)
	}
	return &c
}

func (h *slogHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := *h
	c.prefix = h.prefix + name + "."
	if h.next != nil {
		c.next = h.next.WithGroup(name)
	}
	return &c
}

func (h *slogHandler) capture(ctx context.Context, r slog.Record) {
	attrs := make(map[string]string, len(h.fixed)+r.NumAttrs())
	for k, v := range h.fixed {
		attrs[k] = v
	}
	r.Attrs(func(a slog.Attr) bool {
		flattenAttr(attrs, h.prefix, a)
		return true
	})
	rec := LogRecord{Time: r.Time, Level: r.Level, Message: r.Message}
	if len(attrs) > 0 {
		rec.Attrs = attrs
	}
	if ctx != nil {
		// Without Engine.ContextWithFallback, a *gin.Context doesn't look up
		// values in its request's context, which is where the trace lives.
		if gc, ok := ctx.(*gin.Context); ok && gc.Request != nil {
			ctx = gc.Request.Context()
		}
		rec.TraceID = TraceIDFromContext(ctx)
		rec.SpanID = spanIDFromContext(ctx)
	}
	h.p.captureLog(rec)
}

// flattenAttr adds a to dst under prefix, expanding groups into dotted keys.
func flattenAttr(dst map[string]string, prefix string, a slog.Attr) {
	v := a.Value.Resolve()
	if v.Kind() == slog.KindGroup {
		inner := prefix
		if a.Key != "" { // an unnamed group's attributes are inlined
			inner = prefix + a.Key + "."
		}
		for _, g := range v.Group() {
			flattenAttr(dst, inner, g)
		}
		return
	}
	if a.Equal(slog.Attr{}) || len(dst) >= maxLogAttrs {
		return
	}
	s := v.String()
	if v.Kind() == slog.KindTime {
		s = v.Time().Format(time.RFC3339Nano)
	}
	dst[prefix+a.Key] = clipString(s, maxLogValueBytes)
}

// --- io.Writer ---

// LogWriter returns an [io.Writer] that passes everything written to it on
// to w unchanged and captures each line into Pulse. Use it for the standard
// log package, zap, zerolog, or anything else that writes log lines:
//
//	log.SetOutput(p.LogWriter(os.Stderr))
//	logger := zerolog.New(p.LogWriter(os.Stdout))
//
// JSON lines are parsed: the level ("level", "lvl", "severity"), message
// ("msg", "message"), time ("time", "ts", "timestamp") and trace ID
// ("trace_id") are recognized, and every other field becomes an attribute.
// Other lines are captured as they are, minus the standard log package's
// date prefix, at the logfmt level=… they carry or at Info.
//
// A line is tied to a request only if it carries the request's trace ID —
// log [TraceIDFromContext] as trace_id. An error's detail otherwise shows
// the lines logged around it. A nil w only captures.
func (p *Pulse) LogWriter(w io.Writer) io.Writer {
	p.logCapture.Store(true)
	return &logWriter{p: p, out: w}
}

// maxLogLineBytes caps a buffered line that never ends.
const maxLogLineBytes = 64 << 10

type logWriter struct {
	p   *Pulse
	out io.Writer
	mu  sync.Mutex
	buf []byte // the start of a line whose newline hasn't been written yet
}

func (lw *logWriter) Write(b []byte) (int, error) {
	n, err := len(b), error(nil)
	if lw.out != nil {
		n, err = lw.out.Write(b)
	}

	lw.mu.Lock()
	lw.buf = append(lw.buf, b[:n]...)
	var lines []string
	for {
		i := bytes.IndexByte(lw.buf, '\n')
		if i < 0 {
			break
		}
		lines = append(lines, string(lw.buf[:i]))
		lw.buf = lw.buf[i+1:]
	}
	if len(lw.buf) > maxLogLineBytes {
		lines = append(lines, string(lw.buf))
		lw.buf = nil
	}
	lw.mu.Unlock()

	// Captured outside the lock, so nothing downstream can deadlock by
	// writing to this writer.
	for _, line := range lines {
		lw.p.captureLine(line)
	}
	return n, err
}

var (
	stdLogPrefix  = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}(\.\d+)? `)
	logfmtLevel   = regexp.MustCompile(`\b(?:level|lvl)="?([A-Za-z]+)`)
	inlineTraceID = regexp.MustCompile(`\btrace_id[=:]\s*"?([0-9a-f]{32})\b`)
)

// captureLine captures one line written to a LogWriter.
func (p *Pulse) captureLine(line string) {
	line = strings.TrimRight(line, "\r")
	if strings.TrimSpace(line) == "" {
		return
	}
	rec, ok := parseJSONLogLine(line)
	if !ok {
		rec = LogRecord{Time: time.Now(), Level: slog.LevelInfo, Message: stdLogPrefix.ReplaceAllString(line, "")}
		if m := logfmtLevel.FindStringSubmatch(line); m != nil {
			if l, ok := parseLogLevel(m[1]); ok {
				rec.Level = l
			}
		}
		if m := inlineTraceID.FindStringSubmatch(line); m != nil {
			rec.TraceID = m[1]
		}
	}
	if rec.Level < p.config.Logs.MinLevel {
		return
	}
	p.captureLog(rec)
}

// parseJSONLogLine reads a structured log line, as written by slog's JSON
// handler, zap or zerolog.
func parseJSONLogLine(line string) (LogRecord, bool) {
	s := strings.TrimSpace(line)
	if len(s) < 2 || s[0] != '{' {
		return LogRecord{}, false
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var fields map[string]any
	if err := dec.Decode(&fields); err != nil {
		return LogRecord{}, false
	}

	rec := LogRecord{Level: slog.LevelInfo}
	attrs := make(map[string]string)
	for k, v := range fields {
		str, isStr := v.(string)
		switch strings.ToLower(k) {
		case "msg", "message":
			if isStr && rec.Message == "" {
				rec.Message = str
				continue
			}
		case "level", "lvl", "severity":
			if l, ok := parseLogLevel(str); isStr && ok {
				rec.Level = l
				continue
			}
		case "time", "ts", "timestamp":
			if t, ok := parseLogTime(v); ok {
				rec.Time = t
				continue
			}
		case "trace_id", "traceid":
			if isStr {
				rec.TraceID = str
				continue
			}
		case "span_id", "spanid":
			if isStr {
				rec.SpanID = str
				continue
			}
		}
		flattenJSON(attrs, k, v)
	}
	if rec.Time.IsZero() {
		rec.Time = time.Now()
	}
	if len(attrs) > 0 {
		rec.Attrs = attrs
	}
	return rec, true
}

// parseLogLevel maps the level names of common Go loggers onto slog levels.
func parseLogLevel(s string) (slog.Level, bool) {
	switch strings.ToLower(s) {
	case "trace":
		return slog.LevelDebug - 4, true
	case "debug":
		return slog.LevelDebug, true
	case "info", "information", "notice":
		return slog.LevelInfo, true
	case "warn", "warning":
		return slog.LevelWarn, true
	case "error", "err":
		return slog.LevelError, true
	case "fatal", "panic", "dpanic", "critical", "crit", "alert", "emergency":
		return slog.LevelError + 4, true
	}
	var l slog.Level // slog's own form, such as "WARN+2"
	if err := l.UnmarshalText([]byte(s)); err == nil {
		return l, true
	}
	return 0, false
}

// parseLogTime reads an RFC 3339 timestamp, or a Unix time in seconds (zap),
// milliseconds, microseconds or nanoseconds.
func parseLogTime(v any) (time.Time, bool) {
	switch t := v.(type) {
	case string:
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z0700", "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, t); err == nil {
				return parsed, true
			}
		}
	case json.Number:
		f, err := t.Float64()
		if err != nil || f <= 0 {
			return time.Time{}, false
		}
		switch {
		case f > 1e17:
			return time.Unix(0, int64(f)), true
		case f > 1e14:
			return time.UnixMicro(int64(f)), true
		case f > 1e11:
			return time.UnixMilli(int64(f)), true
		default:
			sec, frac := math.Modf(f)
			return time.Unix(int64(sec), int64(frac*1e9)), true
		}
	}
	return time.Time{}, false
}

// flattenJSON adds a decoded JSON value to dst, expanding objects into
// dotted keys.
func flattenJSON(dst map[string]string, key string, v any) {
	if obj, ok := v.(map[string]any); ok {
		for k, sub := range obj {
			flattenJSON(dst, key+"."+k, sub)
		}
		return
	}
	if len(dst) >= maxLogAttrs {
		return
	}
	switch t := v.(type) {
	case string:
		dst[key] = clipString(t, maxLogValueBytes)
	default: // numbers, booleans, null, arrays
		b, _ := json.Marshal(t)
		dst[key] = clipString(string(b), maxLogValueBytes)
	}
}
