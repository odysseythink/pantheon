package tracing

import "context"

type ctxKey int

const spanCtxKey ctxKey = 1

// ContextWithSpan returns a new context that carries span as the
// current span. Child Start calls will see this span as their parent.
func ContextWithSpan(ctx context.Context, span *Span) context.Context {
	return context.WithValue(ctx, spanCtxKey, span)
}

// SpanFromContext returns the current span or nil if none is attached.
func SpanFromContext(ctx context.Context) *Span {
	if v, ok := ctx.Value(spanCtxKey).(*Span); ok {
		return v
	}
	return nil
}
