package pulse

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// Error type constants for classification.
const (
	ErrorTypePanic      = "panic"
	ErrorTypeValidation = "validation"
	ErrorTypeDatabase   = "database"
	ErrorTypeTimeout    = "timeout"
	ErrorTypeAuth       = "auth"
	ErrorTypeNotFound   = "not_found"
	ErrorTypeInternal   = "internal"
)

// capturedBody is what the error middleware kept of a request body.
type capturedBody struct {
	data    []byte // first ≤ MaxBodySize bytes; nil when omitted
	size    int64  // the request's Content-Length
	omitted bool   // content type Pulse cannot redact — record the size only
}

// captureBody buffers up to maxSize bytes of req's body for later capture,
// then re-attaches a reader that replays those bytes followed by the unread
// remainder, so handlers always receive the complete body.
//
// Bodies of content types the redactor does not understand are never read;
// they are recorded as a size marker instead.
func captureBody(req *http.Request, maxSize int) capturedBody {
	body := capturedBody{size: req.ContentLength}
	if !redactableContentType(req.Header.Get("Content-Type")) {
		body.omitted = true
		return body
	}
	limit := req.ContentLength
	if maxSize > 0 && limit > int64(maxSize) {
		limit = int64(maxSize)
	}
	orig := req.Body
	body.data, _ = io.ReadAll(io.LimitReader(orig, limit))
	req.Body = replayBody{Reader: io.MultiReader(bytes.NewReader(body.data), orig), Closer: orig}
	return body
}

// replayBody is a request body that yields buffered bytes before the rest of
// the original stream, and closes the original.
type replayBody struct {
	io.Reader
	io.Closer
}

// newErrorMiddleware creates a Gin middleware that recovers from panics, captures
// errors from c.Errors and status codes, and stores them as ErrorRecords.
func newErrorMiddleware(p *Pulse) gin.HandlerFunc {
	cfg := p.config.Errors

	return func(c *gin.Context) {
		// Capture request body early if configured (before it's consumed by handlers)
		var body capturedBody
		if boolValue(cfg.CaptureRequestBody) && c.Request.Body != nil && c.Request.ContentLength > 0 {
			body = captureBody(c.Request, cfg.MaxBodySize)
		}

		// Panic recovery
		defer func() {
			if recovered := recover(); recovered != nil {
				// Build error message from panic value
				errMsg := fmt.Sprintf("%v", recovered)

				// Capture stack trace
				stack := captureStackTrace(3) // skip recover, defer, runtime.gopanic

				// Get trace ID and route
				traceID := TraceIDFromContext(c.Request.Context())
				routePattern := c.FullPath()
				if routePattern == "" {
					routePattern = c.Request.URL.Path
				}

				// Build and store error record
				record := buildErrorRecord(
					c.Request.Method,
					routePattern,
					errMsg,
					ErrorTypePanic,
					stack,
					captureRequestContext(c, body),
					traceID,
				)

				if err := p.storage.StoreError(record); err != nil && p.config.DevMode {
					p.logger.Printf("[pulse] failed to store panic error: %v", err)
				}
				p.BroadcastError(record)

				// Abort with 500
				c.AbortWithStatus(http.StatusInternalServerError)
			}
		}()

		// Process request
		c.Next()

		// After handler: capture errors from c.Errors and status codes
		statusCode := c.Writer.Status()
		routePattern := c.FullPath()
		if routePattern == "" {
			routePattern = c.Request.URL.Path
		}
		traceID := TraceIDFromContext(c.Request.Context())

		// Capture Gin errors (set via c.Error())
		if len(c.Errors) > 0 {
			for _, ginErr := range c.Errors {
				errMsg := ginErr.Error()
				errType := classifyError(errMsg, statusCode)

				var stack string
				if boolValue(cfg.CaptureStackTrace) {
					stack = captureStackTrace(2)
				}

				record := buildErrorRecord(
					c.Request.Method,
					routePattern,
					errMsg,
					errType,
					stack,
					captureRequestContext(c, body),
					traceID,
				)

				go func(r ErrorRecord) {
					if err := p.storage.StoreError(r); err != nil && p.config.DevMode {
						p.logger.Printf("[pulse] failed to store error: %v", err)
					}
					p.BroadcastError(r)
				}(record)
			}
		} else if statusCode >= 500 {
			// No explicit Gin errors, but 5xx status code — record as internal error
			errMsg := fmt.Sprintf("HTTP %d: %s", statusCode, http.StatusText(statusCode))
			errType := classifyError(errMsg, statusCode)

			var stack string
			if boolValue(cfg.CaptureStackTrace) {
				stack = captureStackTrace(2)
			}

			record := buildErrorRecord(
				c.Request.Method,
				routePattern,
				errMsg,
				errType,
				stack,
				captureRequestContext(c, body),
				traceID,
			)

			go func(r ErrorRecord) {
				if err := p.storage.StoreError(r); err != nil && p.config.DevMode {
					p.logger.Printf("[pulse] failed to store error: %v", err)
				}
				p.BroadcastError(r)
			}(record)
		}
	}
}

// buildErrorRecord constructs a complete ErrorRecord with fingerprint and timestamps.
func buildErrorRecord(method, route, errMsg, errType, stack string, reqCtx *RequestContext, traceID string) ErrorRecord {
	now := time.Now()
	fingerprint := generateFingerprint(method, route, errMsg)

	return ErrorRecord{
		ID:             GenerateTraceID(), // reuse trace ID generator for unique IDs
		Fingerprint:    fingerprint,
		Method:         method,
		Route:          route,
		ErrorMessage:   errMsg,
		ErrorType:      errType,
		StackTrace:     stack,
		RequestContext: reqCtx,
		Count:          1,
		FirstSeen:      now,
		LastSeen:       now,
	}
}

// generateFingerprint creates a stable hash from method+route+error message for dedup.
func generateFingerprint(method, route, errMsg string) string {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte("|"))
	h.Write([]byte(route))
	h.Write([]byte("|"))
	h.Write([]byte(errMsg))
	return fmt.Sprintf("%x", h.Sum(nil))[:16] // 16-char hex prefix
}

// classifyError determines the error type from the message and status code.
func classifyError(errMsg string, statusCode int) string {
	lower := strings.ToLower(errMsg)

	// Check for panic (usually caught separately, but included for completeness)
	if strings.Contains(lower, "panic") || strings.Contains(lower, "runtime error") {
		return ErrorTypePanic
	}

	// Check for timeout
	if strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline exceeded") ||
		strings.Contains(lower, "context canceled") || statusCode == http.StatusGatewayTimeout ||
		statusCode == http.StatusRequestTimeout {
		return ErrorTypeTimeout
	}

	// Check for auth
	if strings.Contains(lower, "unauthorized") || strings.Contains(lower, "forbidden") ||
		strings.Contains(lower, "authentication") || strings.Contains(lower, "permission denied") ||
		statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		return ErrorTypeAuth
	}

	// Check for not found
	if strings.Contains(lower, "not found") || strings.Contains(lower, "no rows") ||
		statusCode == http.StatusNotFound {
		return ErrorTypeNotFound
	}

	// Check for validation
	if strings.Contains(lower, "validation") || strings.Contains(lower, "invalid") ||
		strings.Contains(lower, "required") || strings.Contains(lower, "must be") ||
		statusCode == http.StatusBadRequest || statusCode == http.StatusUnprocessableEntity {
		return ErrorTypeValidation
	}

	// Check for database
	if strings.Contains(lower, "sql") || strings.Contains(lower, "database") ||
		strings.Contains(lower, "connection refused") || strings.Contains(lower, "duplicate key") ||
		strings.Contains(lower, "constraint") || strings.Contains(lower, "deadlock") {
		return ErrorTypeDatabase
	}

	return ErrorTypeInternal
}

// captureStackTrace captures a cleaned stack trace, skipping the specified number of frames.
func captureStackTrace(skip int) string {
	const maxDepth = 32
	var pcs [maxDepth]uintptr
	n := runtime.Callers(skip, pcs[:])
	if n == 0 {
		return ""
	}

	frames := runtime.CallersFrames(pcs[:n])
	var b strings.Builder

	for {
		frame, more := frames.Next()

		// Skip internal runtime/reflect/gin frames for cleaner traces
		if shouldSkipFrame(frame.Function) {
			if !more {
				break
			}
			continue
		}

		fmt.Fprintf(&b, "%s\n\t%s:%d\n", frame.Function, frame.File, frame.Line)

		if !more {
			break
		}
	}

	return b.String()
}

// shouldSkipFrame returns true for frames that should be excluded from stack traces.
func shouldSkipFrame(fn string) bool {
	skips := []string{
		"runtime.gopanic",
		"runtime.goexit",
		"runtime.main",
		"net/http.",
		"github.com/gin-gonic/gin.",
	}
	for _, s := range skips {
		if strings.HasPrefix(fn, s) || strings.Contains(fn, s) {
			return true
		}
	}
	return false
}

// captureRequestContext builds a RequestContext from the Gin context with
// sensitive headers, query parameters and body fields redacted.
func captureRequestContext(c *gin.Context, body capturedBody) *RequestContext {
	headers := make(map[string]string)
	for key, values := range c.Request.Header {
		lowerKey := strings.ToLower(key)
		if sensitiveHeaders[lowerKey] {
			headers[key] = redactedPlaceholder
		} else if len(values) > 0 {
			headers[key] = values[0]
		}
	}

	reqCtx := &RequestContext{
		Method:      c.Request.Method,
		Path:        c.Request.URL.Path,
		Query:       redactQuery(c.Request.URL.RawQuery),
		Headers:     headers,
		ClientIP:    c.ClientIP(),
		UserAgent:   c.Request.UserAgent(),
		ContentType: c.ContentType(),
	}

	switch {
	case body.omitted:
		reqCtx.Body = omittedBodyMarker(body.size, reqCtx.ContentType)
	case len(body.data) > 0:
		reqCtx.Body = string(redactBody(reqCtx.ContentType, body.data))
		reqCtx.BodyTruncated = int64(len(body.data)) < body.size
	}

	return reqCtx
}
