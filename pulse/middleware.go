package pulse

import (
	"bufio"
	"math/rand/v2"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// responseWriter wraps gin.ResponseWriter to capture the status code and bytes written.
type responseWriter struct {
	gin.ResponseWriter
	statusCode  int
	bytesWriten int64
	written     bool
}

func newResponseWriter(w gin.ResponseWriter) *responseWriter {
	return &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
}

func (rw *responseWriter) WriteHeader(code int) {
	if !rw.written {
		rw.statusCode = code
		rw.written = true
	}
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(data []byte) (int, error) {
	if !rw.written {
		rw.written = true
	}
	n, err := rw.ResponseWriter.Write(data)
	rw.bytesWriten += int64(n)
	return n, err
}

func (rw *responseWriter) WriteString(s string) (int, error) {
	if !rw.written {
		rw.written = true
	}
	n, err := rw.ResponseWriter.WriteString(s)
	rw.bytesWriten += int64(n)
	return n, err
}

// Hijack implements http.Hijacker for WebSocket support.
func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return rw.ResponseWriter.Hijack()
}

// Flush implements http.Flusher.
func (rw *responseWriter) Flush() {
	rw.ResponseWriter.Flush()
}

// newTracingMiddleware creates the Gin middleware for request tracing.
func newTracingMiddleware(p *Pulse) gin.HandlerFunc {
	cfg := p.config.Tracing
	prefix := p.config.Prefix

	// Pre-compile exclude patterns
	excludePatterns := make([]string, 0, len(cfg.ExcludePaths)+2)
	excludePatterns = append(excludePatterns, prefix+"/*")
	excludePatterns = append(excludePatterns, "/favicon.ico")
	excludePatterns = append(excludePatterns, cfg.ExcludePaths...)

	return func(c *gin.Context) {
		// Skip if tracing disabled
		if !boolValue(cfg.Enabled) {
			c.Next()
			return
		}

		// Check path exclusion
		requestPath := c.Request.URL.Path
		if shouldExclude(requestPath, excludePatterns) {
			c.Next()
			return
		}

		// Honor an incoming W3C traceparent if present and valid: adopt its
		// trace ID, and its parent-id becomes this request's parent span.
		// Otherwise start a new trace. Either way the request gets its own
		// span ID, which the response's traceparent carries back.
		var traceID, parentSpanID string
		if tp, parent, ok := ParseTraceparent(c.GetHeader(TraceparentHeader)); ok {
			traceID, parentSpanID = tp, parent
		} else {
			traceID = GenerateTraceID()
		}
		spanID := GenerateSpanID()
		c.Header(TraceIDHeader, traceID)
		c.Header(TraceparentHeader, BuildTraceparent(traceID, spanID))

		// Attach trace ID, span ID and pulse instance to context. The route
		// pattern is best-effort here — Gin only resolves c.FullPath() *after*
		// it has matched the route, so the value may be empty for 404s. We
		// refresh it in the GORM plugin via RouteFromContext when needed.
		ctx := ContextWithTraceID(c.Request.Context(), traceID)
		ctx = contextWithSpanID(ctx, spanID)
		ctx = ContextWithPulse(ctx, p)
		ctx = ContextWithRoute(ctx, c.Request.Method+" "+c.FullPath())
		c.Request = c.Request.WithContext(ctx)

		// Wrap response writer
		rw := newResponseWriter(c.Writer)
		c.Writer = rw

		// Record start time
		start := time.Now()

		// Process request
		c.Next()

		// Calculate latency
		latency := time.Since(start)
		statusCode := rw.statusCode

		// Get the route pattern (e.g., "/users/:id") instead of actual path
		routePattern := c.FullPath()
		if routePattern == "" {
			// Unmatched: the raw path is all there is, and it may carry a
			// token (/reset/eyJ…), so scrub it.
			routePattern = p.redactor.scrubValue(requestPath)
		}

		// Count every request before sampling, so totals, error rates and
		// SLO compliance stay exact at any sample rate.
		p.rollups.observe(c.Request.Method, c.FullPath(), routePattern, statusCode, latency, start)

		// The request is done, so its N+1 tallies are final. Record them
		// whether or not this request is sampled below.
		if p.gormPlugin != nil {
			p.gormPlugin.finalizeTrace(traceID, c.Request.Method+" "+routePattern)
		}

		// Determine if we should record this request (sampling)
		isError := statusCode >= 400
		isSlow := latency >= cfg.SlowRequestThreshold
		shouldRecord := isError || isSlow || shouldSample(float64Value(cfg.SampleRate))

		// Exporters receive every request; sampling only thins out storage.
		if !shouldRecord && p.exporter == nil {
			return
		}

		// Collect error message from gin errors
		var errMsg string
		if len(c.Errors) > 0 {
			errMsg = p.redactor.scrubValue(c.Errors.Last().Error())
		}

		// Build metric
		metric := RequestMetric{
			Method:       c.Request.Method,
			Path:         routePattern,
			StatusCode:   statusCode,
			Latency:      latency,
			RequestSize:  c.Request.ContentLength,
			ResponseSize: rw.bytesWriten,
			ClientIP:     c.ClientIP(),
			UserAgent:    c.Request.UserAgent(),
			Error:        errMsg,
			TraceID:      traceID,
			SpanID:       spanID,
			ParentSpanID: parentSpanID,
			Timestamp:    start,
		}

		exported := metric
		p.export(Event{Kind: EventRequest, Request: &exported})
		if !shouldRecord {
			return
		}

		// Both backends store without blocking (a ring-buffer push, or a
		// queue append for SQLite), so no goroutine per request is needed.
		p.internalError("storage: requests", p.storage.StoreRequest(metric))
		p.BroadcastRequest(metric)
	}
}

// shouldExclude checks if a path matches any exclusion pattern.
func shouldExclude(path string, patterns []string) bool {
	for _, pattern := range patterns {
		if matched, _ := filepath.Match(pattern, path); matched {
			return true
		}
		// Also check with a simpler prefix match for patterns like "/pulse/*"
		if strings.HasSuffix(pattern, "/*") {
			prefix := strings.TrimSuffix(pattern, "/*")
			if strings.HasPrefix(path, prefix+"/") || path == prefix {
				return true
			}
		}
	}
	return false
}

// shouldSample returns true if this request should be recorded based on sample rate.
func shouldSample(rate float64) bool {
	if rate >= 1.0 {
		return true
	}
	if rate <= 0.0 {
		return false
	}
	return rand.Float64() < rate
}
