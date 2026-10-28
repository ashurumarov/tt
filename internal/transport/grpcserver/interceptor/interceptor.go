// Package interceptor holds the unary server interceptors the gRPC server
// installs. They are hand-rolled on purpose: each one is a few lines, and
// keeping them here makes the request lifecycle readable end to end.
//
// Order matters. The server chains them as:
//
//	RequestID -> Logging -> Recovery -> Timeout -> handler
//
// so that a panic is already logged with its request id, and the timeout
// applies to the handler rather than to the logging work around it.
package interceptor

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/ashur/ttr/internal/logger"
)

// RequestIDHeader is the metadata key carrying the client-supplied request id.
const RequestIDHeader = "x-request-id"

// RequestID reuses the caller's request id or mints one, attaches it to the
// context for logging and echoes it back as a response header so a client can
// correlate its own logs with the server's.
func RequestID() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		id := incomingRequestID(ctx)
		if id == "" {
			id = uuid.NewString()
		}

		ctx = logger.With(ctx, slog.String("request_id", id))
		// A failed SetHeader only means the stream is already gone; the
		// request itself is still worth serving.
		_ = grpc.SetHeader(ctx, metadata.Pairs(RequestIDHeader, id))
		return handler(ctx, req)
	}
}

func incomingRequestID(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	values := md.Get(RequestIDHeader)
	if len(values) == 0 {
		return ""
	}
	// Cap the echoed value: it is attacker-controlled and ends up in logs.
	if id := values[0]; len(id) <= 128 {
		return id
	}
	return ""
}

// Logging emits one record per request with its method, duration and status.
// Server faults log at error level, client faults at warn, successes at info.
func Logging(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		code := status.Code(err)

		attrs := []slog.Attr{
			slog.String("method", info.FullMethod),
			slog.String("code", code.String()),
			slog.Duration("duration", time.Since(start)),
		}
		if err != nil {
			attrs = append(attrs, slog.String("error", err.Error()))
		}
		log.LogAttrs(ctx, levelFor(code), "grpc request", attrs...)

		return resp, err
	}
}

// levelFor keeps client mistakes out of the error budget: only codes that
// indicate a server-side fault are logged at error level.
func levelFor(code codes.Code) slog.Level {
	switch code {
	case codes.OK:
		return slog.LevelInfo
	case codes.Unknown, codes.Internal, codes.DataLoss, codes.Unavailable, codes.Unimplemented:
		return slog.LevelError
	default:
		return slog.LevelWarn
	}
}

// Recovery turns a panic into an Internal status so one bad request cannot take
// the process down. The stack trace is logged, never returned to the client.
func Recovery(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if p := recover(); p != nil {
				log.LogAttrs(ctx, slog.LevelError, "recovered from panic",
					slog.Any("panic", p),
					slog.String("method", info.FullMethod),
					slog.String("stack", string(debug.Stack())),
				)
				err = status.Error(codes.Internal, "internal error")
				resp = nil
			}
		}()
		return handler(ctx, req)
	}
}

// Timeout bounds every handler. A client deadline that is already tighter wins,
// because context.WithTimeout never extends an existing deadline.
func Timeout(d time.Duration) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		return handler(ctx, req)
	}
}
