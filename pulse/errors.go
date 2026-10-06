package pulse

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync"
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
	rec     *bodyRecorder // records what the handler reads; nil when nothing is kept
	size    int64         // the request's Content-Length, negative when chunked
	omitted bool          // content type Pulse cannot redact — record the size only
}

// data returns up to MaxBodySize bytes of the body. It is called only when a
// request has failed, so it reads whatever the handler left unread — a
// handler that rejected the request before touching the body still gets its
// error reported with the body the client sent.
func (b capturedBody) data() []byte {
	if b.rec == nil {
		return nil
	}
	b.rec.fill()
	return b.rec.captured()
}

// truncated reports whether the kept bytes are less than the request carried.
func (b capturedBody) truncated(read int) bool {
	if b.size < 0 { // chunked: all that is known is whether the cap was reached
		return b.rec != nil && read >= b.rec.max
	}
	return int64(read) < b.size
}

// captureBody wraps req's body so that the first maxSize bytes the handler
// reads are kept, in case the request ends in an error. Nothing is read,
// buffered or copied ahead of the handler: the handler receives the body
// exactly as the client sent it, and a request that neither fails nor reads
// its body costs one small wrapper.
//
// Bodies of content types the redactor does not understand are never kept;
// they are recorded as a size marker instead.
func captureBody(req *http.Request, maxSize int) capturedBody {
	body := capturedBody{size: req.ContentLength}
	if !redactableContentType(req.Header.Get("Content-Type")) {
		body.omitted = true
		return body
	}
	if maxSize <= 0 {
		return body
	}
	body.rec = &bodyRecorder{ReadCloser: req.Body, max: maxSize}
	req.Body = body.rec
	return body
}

// bodyRecorder is a request body that keeps the first max bytes read through
// it. Reads pass straight through, so the handler sees the whole body.
type bodyRecorder struct {
	io.ReadCloser
	max int

	mu   sync.Mutex // a handler may read the body from another goroutine
	seen []byte
}

func (b *bodyRecorder) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.mu.Lock()
		if room := b.max - len(b.seen); room > 0 {
			b.seen = append(b.seen, p[:min(n, room)]...)
		}
		b.mu.Unlock()
	}
	return n, err
}

// fill reads what the handler left unread, up to max. The bytes kept stay a
// contiguous prefix of the body, since reading continues where the handler
// stopped.
func (b *bodyRecorder) fill() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.max - len(b.seen); room > 0 {
		rest, _ := io.ReadAll(io.LimitReader(b.ReadCloser, int64(room)))
		b.seen = append(b.seen, rest...)
	}
}

// captured returns the bytes kept so far.
func (b *bodyRecorder) captured() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.seen)
}

// newErrorMiddleware creates a Gin middleware that recovers from panics, captures
// errors from c.Errors and status codes, and stores them as ErrorRecords.
func newErrorMiddleware(p *Pulse) gin.HandlerFunc {
	cfg := p.config.Errors

	return func(c *gin.Context) {
		// Wrap the request body, so an error can report what the request
		// carried. Nothing is read until the handler reads it; a body sent
		// without a Content-Length (chunked) is kept too.
		var body capturedBody
		if boolValue(cfg.CaptureRequestBody) && c.Request.Body != nil && c.Request.ContentLength != 0 {
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
					captureRequestContext(c, body, p.redactor),
					traceID,
				)

				record.SpanID = spanIDFromContext(c.Request.Context())
				p.recordError(record)

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
					captureRequestContext(c, body, p.redactor),
					traceID,
				)

				record.SpanID = spanIDFromContext(c.Request.Context())
				p.recordError(record)
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
				captureRequestContext(c, body, p.redactor),
				traceID,
			)

			record.SpanID = spanIDFromContext(c.Request.Context())
			p.recordError(record)
		}
	}
}

// recordError applies final redaction to an error record, stores it, and
// broadcasts it to dashboard clients. Both storage backends store without
// blocking, so this is safe to call on the request path.
func (p *Pulse) recordError(r ErrorRecord) {
	r.InstanceID = p.config.InstanceID
	p.redactor.finishRecord(&r)
	p.internalError("storage: errors", p.storage.StoreError(r))
	p.BroadcastError(r)
	exported := r
	p.export(Event{Kind: EventError, Error: &exported})
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
		TraceID:        traceID,
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

// captureRequestContext builds a RequestContext from the Gin context, with
// secrets in the headers, path, query string and body redacted by r.
func captureRequestContext(c *gin.Context, body capturedBody, r *redactor) *RequestContext {
	reqCtx := &RequestContext{
		Method:      c.Request.Method,
		Path:        r.scrubValue(c.Request.URL.Path),
		Query:       r.query(c.Request.URL.RawQuery),
		Headers:     r.headerMap(c.Request.Header),
		ClientIP:    c.ClientIP(),
		UserAgent:   c.Request.UserAgent(),
		ContentType: c.ContentType(),
	}

	data := body.data()
	switch {
	case body.omitted:
		reqCtx.Body = omittedBodyMarker(body.size, reqCtx.ContentType)
	case len(data) > 0:
		reqCtx.Body = string(r.body(reqCtx.ContentType, data))
		reqCtx.BodyTruncated = body.truncated(len(data))
	case body.rec != nil:
		reqCtx.Body = unreadBodyMarker(body.size, reqCtx.ContentType)
	}

	return reqCtx
}
