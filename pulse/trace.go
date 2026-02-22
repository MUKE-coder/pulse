package pulse

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
)

type contextKey string

const (
	// TraceIDHeader is the HTTP header used to propagate trace IDs.
	TraceIDHeader = "X-Pulse-Trace-ID"

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
