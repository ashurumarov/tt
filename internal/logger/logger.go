// Package logger builds the service logger.
//
// It wraps a standard slog handler with one addition: attributes can be
// attached to a context.Context and every record logged with that context
// carries them. That is how a request id reaches log lines in the service and
// repository layers without threading a logger through every signature.
package logger

import (
	"context"
	"io"
	"log/slog"

	"github.com/ashur/ttr/internal/config"
)

type ctxKey struct{}

// New returns a logger writing to w in the configured format and level.
func New(w io.Writer, cfg config.Log) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.Level}

	var handler slog.Handler
	if cfg.Format == "text" {
		handler = slog.NewTextHandler(w, opts)
	} else {
		handler = slog.NewJSONHandler(w, opts)
	}
	return slog.New(&contextHandler{Handler: handler})
}

// With returns a context carrying attrs in addition to any already present.
// Logging with that context emits them on every record.
func With(ctx context.Context, attrs ...slog.Attr) context.Context {
	if len(attrs) == 0 {
		return ctx
	}
	existing := fromContext(ctx)
	merged := make([]slog.Attr, 0, len(existing)+len(attrs))
	merged = append(merged, existing...)
	merged = append(merged, attrs...)
	return context.WithValue(ctx, ctxKey{}, merged)
}

func fromContext(ctx context.Context) []slog.Attr {
	attrs, _ := ctx.Value(ctxKey{}).([]slog.Attr)
	return attrs
}

// contextHandler decorates records with the attributes stored in the context.
type contextHandler struct {
	slog.Handler
}

// Handle implements slog.Handler.
func (h *contextHandler) Handle(ctx context.Context, record slog.Record) error {
	record.AddAttrs(fromContext(ctx)...)
	return h.Handler.Handle(ctx, record)
}

// WithAttrs implements slog.Handler, preserving the context decoration.
func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{Handler: h.Handler.WithAttrs(attrs)}
}

// WithGroup implements slog.Handler, preserving the context decoration.
func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{Handler: h.Handler.WithGroup(name)}
}
