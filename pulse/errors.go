package pulse

import (
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

// Sensitive header names that should be redacted in request context.
var sensitiveHeaders = map[string]bool{
	"authorization":   true,
	"cookie":          true,
	"set-cookie":      true,
	"x-api-key":       true,
	"x-auth-token":    true,
	"proxy-authorization": true,
}

// newErrorMiddleware creates a Gin middleware that recovers from panics, captures
// errors from c.Errors and status codes, and stores them as ErrorRecords.
func newErrorMiddleware(p *Pulse) gin.HandlerFunc {
	cfg := p.config.Errors

	return func(c *gin.Context) {
		// Capture request body early if configured (before it's consumed by handlers)
		var bodyBytes []byte
		if boolValue(cfg.CaptureRequestBody) && c.Request.Body != nil && c.Request.ContentLength > 0 {
			maxSize := int64(cfg.MaxBodySize)
			if c.Request.ContentLength < maxSize {
				maxSize = c.Request.ContentLength
			}
			bodyBytes, _ = io.ReadAll(io.LimitReader(c.Request.Body, maxSize))
			// Restore the body so handlers can still read it
			c.Request.Body = io.NopCloser(strings.NewReader(string(bodyBytes)))
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
					captureRequestContext(c, bodyBytes),
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
					captureRequestContext(c, bodyBytes),
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
				captureRequestContext(c, bodyBytes),
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

// captureRequestContext builds a RequestContext from the Gin context with sensitive data redacted.
func captureRequestContext(c *gin.Context, bodyBytes []byte) *RequestContext {
	headers := make(map[string]string)
	for key, values := range c.Request.Header {
		lowerKey := strings.ToLower(key)
		if sensitiveHeaders[lowerKey] {
			headers[key] = "[REDACTED]"
		} else if len(values) > 0 {
			headers[key] = values[0]
		}
	}

	reqCtx := &RequestContext{
		Method:      c.Request.Method,
		Path:        c.Request.URL.Path,
		Query:       c.Request.URL.RawQuery,
		Headers:     headers,
		ClientIP:    c.ClientIP(),
		UserAgent:   c.Request.UserAgent(),
		ContentType: c.ContentType(),
	}

	if len(bodyBytes) > 0 {
		reqCtx.Body = string(bodyBytes)
	}

	return reqCtx
}
