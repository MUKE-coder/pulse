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

		// Generate trace ID
		traceID := GenerateTraceID()
		c.Header(TraceIDHeader, traceID)

		// Attach trace ID and pulse instance to context
		ctx := ContextWithTraceID(c.Request.Context(), traceID)
		ctx = ContextWithPulse(ctx, p)
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

		// Determine if we should record this request (sampling)
		isError := statusCode >= 400
		isSlow := latency >= cfg.SlowRequestThreshold
		shouldRecord := isError || isSlow || shouldSample(float64Value(cfg.SampleRate))

		if !shouldRecord {
			return
		}

		// Get the route pattern (e.g., "/users/:id") instead of actual path
		routePattern := c.FullPath()
		if routePattern == "" {
			routePattern = requestPath
		}

		// Collect error message from gin errors
		var errMsg string
		if len(c.Errors) > 0 {
			errMsg = c.Errors.Last().Error()
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
			Timestamp:    start,
		}

		// Store asynchronously to avoid blocking the response
		go func() {
			if err := p.storage.StoreRequest(metric); err != nil && p.config.DevMode {
				p.logger.Printf("[pulse] failed to store request metric: %v", err)
			}
			p.BroadcastRequest(metric)
		}()
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
