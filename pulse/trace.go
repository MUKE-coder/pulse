package pulse

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
)

type contextKey string

const (
	// TraceIDHeader is the legacy Pulse-specific trace ID header. Pulse still
	// sets it on outbound responses for backwards compatibility, but the
	// W3C [TraceparentHeader] is preferred and is read on inbound requests
	// when present.
	TraceIDHeader = "X-Pulse-Trace-ID"

	// TraceparentHeader is the W3C Trace Context propagation header. See
	// https://www.w3.org/TR/trace-context/#traceparent-header. Format:
	//   version "-" trace-id "-" parent-id "-" trace-flags
	//   00       32 hex     16 hex      2 hex
	TraceparentHeader = "traceparent"

	traceIDKey contextKey = "pulse_trace_id"
	pulseKey   contextKey = "pulse_instance"
)

// traceID pool to reduce allocations
var tracePool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, 16)
		return &b
	},
}

// GenerateTraceID creates a new random 32-character hex trace ID.
func GenerateTraceID() string {
	bp := tracePool.Get().(*[]byte)
	b := *bp
	defer tracePool.Put(bp)

	if _, err := rand.Read(b); err != nil {
		// Fallback — should never happen
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(b)
}

// ContextWithTraceID returns a new context with the trace ID attached.
func ContextWithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDKey, traceID)
}

// TraceIDFromContext extracts the trace ID from a context, or returns empty string.
func TraceIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(traceIDKey).(string); ok {
		return v
	}
	return ""
}

// ContextWithPulse returns a new context with the Pulse instance attached.
func ContextWithPulse(ctx context.Context, p *Pulse) context.Context {
	return context.WithValue(ctx, pulseKey, p)
}

// PulseFromContext extracts the Pulse instance from a context.
func PulseFromContext(ctx context.Context) *Pulse {
	if v, ok := ctx.Value(pulseKey).(*Pulse); ok {
		return v
	}
	return nil
}

// GenerateSpanID returns a new random 16-character hex span ID, suitable for
// the parent-id field of a W3C traceparent header.
func GenerateSpanID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "0000000000000000"
	}
	return hex.EncodeToString(b[:])
}

// ParseTraceparent extracts the trace-id and parent-id from a W3C traceparent
// header value. Returns ok=false if the value is missing, malformed, or uses
// an unsupported version. Pulse only understands version "00" today; other
// versions are ignored (per the spec: "an implementation MUST NOT treat a
// future version with a non-zero trace-id as invalid").
//
// On success, traceID is 32 hex chars and parentID is 16 hex chars.
func ParseTraceparent(header string) (traceID, parentID string, ok bool) {
	if header == "" {
		return "", "", false
	}
	parts := strings.Split(header, "-")
	if len(parts) != 4 {
		return "", "", false
	}
	version, tid, pid, flags := parts[0], parts[1], parts[2], parts[3]
	if version != "00" {
		return "", "", false
	}
	if len(tid) != 32 || len(pid) != 16 || len(flags) != 2 {
		return "", "", false
	}
	if !isHex(tid) || !isHex(pid) || !isHex(flags) {
		return "", "", false
	}
	// All-zero trace-id is invalid per spec.
	if tid == "00000000000000000000000000000000" {
		return "", "", false
	}
	return tid, pid, true
}

// BuildTraceparent assembles a W3C traceparent header value. Pulse always
// emits version "00" with the "sampled" flag (01) set, because anything that
// reaches the storage layer was, by definition, sampled.
func BuildTraceparent(traceID, parentID string) string {
	return "00-" + traceID + "-" + parentID + "-01"
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}
