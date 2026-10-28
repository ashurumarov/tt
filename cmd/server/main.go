// Command server runs the user gRPC API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/ashur/ttr/internal/config"
	"github.com/ashur/ttr/internal/logger"
	"github.com/ashur/ttr/internal/repository/postgres"
	"github.com/ashur/ttr/internal/service"
	"github.com/ashur/ttr/internal/transport/grpcserver"
	userv1 "github.com/ashur/ttr/pkg/pb/user/v1"
)

// dbProbeInterval is how often readiness is re-checked against PostgreSQL.
const dbProbeInterval = 10 * time.Second

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		// The logger may not exist yet when config loading fails, so the
		// last-resort report goes straight to stderr.
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

// run holds the whole lifecycle so that every defer still executes: main()
// only translates the outcome into an exit code.
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log := logger.New(os.Stdout, cfg.Log).With(
		slog.String("service", "user-api"),
		slog.String("env", cfg.Env),
		slog.String("version", version),
	)

	// SIGINT/SIGTERM cancel this context, which unwinds the server and then
	// the pool, in that order.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.NewPool(ctx, cfg.Postgres, log)
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer pool.Close()
	log.Info("connected to postgres", slog.Int("max_conns", int(cfg.Postgres.MaxConns)))

	userRepo := postgres.NewUserRepository(pool)
	userSvc := service.NewUserService(userRepo, log)
	userHandler := grpcserver.NewUserHandler(userSvc, log)
	server := grpcserver.New(cfg.GRPC, log, userHandler)

	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error { return server.Run(groupCtx) })
	group.Go(func() error {
		watchDatabase(groupCtx, userRepo, server, log)
		return nil
	})

	if err := group.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	log.Info("shutdown complete")
	return nil
}

// watchDatabase keeps the gRPC health status in step with PostgreSQL, so an
// instance that has lost its database is pulled out of rotation instead of
// accepting traffic it cannot serve.
func watchDatabase(ctx context.Context, repo *postgres.UserRepository, server *grpcserver.Server, log *slog.Logger) {
	ticker := time.NewTicker(dbProbeInterval)
	defer ticker.Stop()

	healthy := true
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := repo.Ping(probeCtx)
		cancel()

		switch {
		case err != nil && healthy:
			healthy = false
			log.ErrorContext(ctx, "database unreachable, reporting NOT_SERVING", slog.String("error", err.Error()))
			setServingStatus(server, healthpb.HealthCheckResponse_NOT_SERVING)
		case err == nil && !healthy:
			healthy = true
			log.InfoContext(ctx, "database reachable again, reporting SERVING")
			setServingStatus(server, healthpb.HealthCheckResponse_SERVING)
		}
	}
}

func setServingStatus(server *grpcserver.Server, status healthpb.HealthCheckResponse_ServingStatus) {
	server.Health().SetServingStatus("", status)
	server.Health().SetServingStatus(userv1.UserService_ServiceDesc.ServiceName, status)
}
