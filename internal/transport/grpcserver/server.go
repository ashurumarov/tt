package grpcserver

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"

	"github.com/ashur/ttr/internal/config"
	"github.com/ashur/ttr/internal/transport/grpcserver/interceptor"
	userv1 "github.com/ashur/ttr/pkg/pb/user/v1"
)

// Server owns the gRPC server and its lifecycle.
type Server struct {
	grpc   *grpc.Server
	health *health.Server
	cfg    config.GRPC
	log    *slog.Logger
}

// New builds a gRPC server with the interceptor chain, the standard health
// service and, outside production, server reflection.
func New(cfg config.GRPC, log *slog.Logger, userHandler *UserHandler) *Server {
	healthSrv := health.NewServer()

	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			interceptor.RequestID(),
			interceptor.Logging(log),
			interceptor.Recovery(log),
			interceptor.Timeout(cfg.HandlerTimeout),
		),
		grpc.MaxRecvMsgSize(cfg.MaxRecvMsgBytes),
		// Recycling connections spreads load when instances come and go, and
		// the enforcement policy rejects clients that ping too aggressively.
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle:     cfg.MaxConnectionIdle,
			MaxConnectionAge:      cfg.MaxConnectionAge,
			MaxConnectionAgeGrace: 10 * time.Second,
			Time:                  2 * time.Minute,
			Timeout:               20 * time.Second,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             30 * time.Second,
			PermitWithoutStream: true,
		}),
	)

	userv1.RegisterUserServiceServer(srv, userHandler)
	healthpb.RegisterHealthServer(srv, healthSrv)

	// Reflection lets grpcurl and friends call the API without a local copy
	// of the protos. It also exposes the full schema, so it stays off in
	// production unless explicitly enabled.
	if cfg.EnableReflection {
		reflection.Register(srv)
	}

	return &Server{grpc: srv, health: healthSrv, cfg: cfg, log: log}
}

// Health exposes the health service so a probe can publish readiness.
func (s *Server) Health() *health.Server { return s.health }

// Run listens on the configured address and serves until ctx is canceled.
func (s *Server) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.cfg.Addr, err)
	}
	return s.Serve(ctx, listener)
}

// Serve serves on the given listener until ctx is canceled, then drains
// in-flight requests. Taking the listener as an argument keeps the transport
// decoupled from TCP, which is what lets the tests drive the real server over
// an in-memory connection.
//
// Shutdown order is deliberate: health flips to NOT_SERVING first so load
// balancers stop sending new work, and only then does the server drain. If
// draining outlasts the timeout, remaining connections are cut so the process
// cannot hang forever.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	s.health.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	s.health.SetServingStatus(userv1.UserService_ServiceDesc.ServiceName, healthpb.HealthCheckResponse_SERVING)

	serveErr := make(chan error, 1)
	go func() {
		s.log.Info("grpc server listening",
			slog.String("addr", listener.Addr().String()),
			slog.Bool("reflection", s.cfg.EnableReflection),
		)
		if err := s.grpc.Serve(listener); err != nil {
			serveErr <- fmt.Errorf("serve grpc: %w", err)
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	s.log.Info("grpc server shutting down", slog.Duration("timeout", s.cfg.ShutdownTimeout))
	s.health.Shutdown()

	drained := make(chan struct{})
	go func() {
		s.grpc.GracefulStop()
		close(drained)
	}()

	select {
	case <-drained:
		s.log.Info("grpc server stopped gracefully")
	case <-time.After(s.cfg.ShutdownTimeout):
		s.log.Warn("graceful shutdown timed out, forcing stop")
		s.grpc.Stop()
	}

	// Serve returns once the server is stopped; surface any error it had.
	if err := <-serveErr; err != nil {
		return err
	}
	return nil
}
