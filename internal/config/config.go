// Package config loads service configuration from the environment.
//
// The environment is the only source: it is what Docker, Compose and every
// orchestrator inject natively, and it keeps secrets out of the image.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the fully resolved service configuration.
type Config struct {
	Env      string
	Log      Log
	GRPC     GRPC
	Postgres Postgres
}

// Log configures the structured logger.
type Log struct {
	Level  slog.Level
	Format string // "json" or "text"
}

// GRPC configures the gRPC server.
type GRPC struct {
	Addr              string
	ShutdownTimeout   time.Duration
	HandlerTimeout    time.Duration
	MaxRecvMsgBytes   int
	MaxConnectionIdle time.Duration
	MaxConnectionAge  time.Duration
	EnableReflection  bool
}

// Postgres configures the database connection pool.
type Postgres struct {
	DSN               string
	MaxConns          int32
	MinConns          int32
	MaxConnLifetime   time.Duration
	MaxConnIdleTime   time.Duration
	ConnectTimeout    time.Duration
	HealthCheckPeriod time.Duration
}

// Load reads and validates the configuration. It returns every problem it
// finds so a misconfigured deployment fails fast with a complete report.
func Load() (Config, error) {
	var errs []error
	collect := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	cfg := Config{Env: envString("APP_ENV", "development")}

	level, err := envLogLevel("LOG_LEVEL", slog.LevelInfo)
	collect(err)
	cfg.Log.Level = level

	cfg.Log.Format = envString("LOG_FORMAT", "json")
	if cfg.Log.Format != "json" && cfg.Log.Format != "text" {
		collect(fmt.Errorf("LOG_FORMAT: want \"json\" or \"text\", got %q", cfg.Log.Format))
	}

	cfg.GRPC.Addr = envString("GRPC_ADDR", ":50051")

	cfg.GRPC.ShutdownTimeout, err = envDuration("GRPC_SHUTDOWN_TIMEOUT", 15*time.Second)
	collect(err)
	cfg.GRPC.HandlerTimeout, err = envDuration("GRPC_HANDLER_TIMEOUT", 5*time.Second)
	collect(err)
	cfg.GRPC.MaxRecvMsgBytes, err = envInt("GRPC_MAX_RECV_MSG_BYTES", 4<<20)
	collect(err)
	cfg.GRPC.MaxConnectionIdle, err = envDuration("GRPC_MAX_CONNECTION_IDLE", 5*time.Minute)
	collect(err)
	cfg.GRPC.MaxConnectionAge, err = envDuration("GRPC_MAX_CONNECTION_AGE", 30*time.Minute)
	collect(err)
	cfg.GRPC.EnableReflection, err = envBool("GRPC_ENABLE_REFLECTION", cfg.Env != "production")
	collect(err)

	dsn, err := postgresDSN()
	collect(err)
	cfg.Postgres.DSN = dsn

	cfg.Postgres.MaxConns, err = envInt32("POSTGRES_MAX_CONNS", 10)
	collect(err)
	cfg.Postgres.MinConns, err = envInt32("POSTGRES_MIN_CONNS", 2)
	collect(err)
	cfg.Postgres.MaxConnLifetime, err = envDuration("POSTGRES_MAX_CONN_LIFETIME", time.Hour)
	collect(err)
	cfg.Postgres.MaxConnIdleTime, err = envDuration("POSTGRES_MAX_CONN_IDLE_TIME", 30*time.Minute)
	collect(err)
	cfg.Postgres.ConnectTimeout, err = envDuration("POSTGRES_CONNECT_TIMEOUT", 5*time.Second)
	collect(err)
	cfg.Postgres.HealthCheckPeriod, err = envDuration("POSTGRES_HEALTH_CHECK_PERIOD", time.Minute)
	collect(err)

	if cfg.Postgres.MinConns > cfg.Postgres.MaxConns {
		collect(fmt.Errorf("POSTGRES_MIN_CONNS (%d) must not exceed POSTGRES_MAX_CONNS (%d)",
			cfg.Postgres.MinConns, cfg.Postgres.MaxConns))
	}

	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// postgresDSN prefers an explicit DATABASE_URL and otherwise assembles one
// from the discrete POSTGRES_* variables that the Postgres image also uses.
func postgresDSN() (string, error) {
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		return dsn, nil
	}

	// An empty password is treated as missing rather than as "trust auth":
	// a service that silently starts without credentials is worse than one
	// that refuses to start.
	password := os.Getenv("POSTGRES_PASSWORD")
	if password == "" {
		return "", errors.New("DATABASE_URL or POSTGRES_PASSWORD must be set")
	}

	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(envString("POSTGRES_USER", "postgres"), password),
		Host: fmt.Sprintf("%s:%s",
			envString("POSTGRES_HOST", "localhost"),
			envString("POSTGRES_PORT", "5432")),
		Path: "/" + envString("POSTGRES_DB", "postgres"),
	}
	u.RawQuery = url.Values{"sslmode": {envString("POSTGRES_SSLMODE", "disable")}}.Encode()
	return u.String(), nil
}

func envString(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return fallback, nil
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if v <= 0 {
		return 0, fmt.Errorf("%s: must be positive, got %s", key, v)
	}
	return v, nil
}

func envInt(key string, fallback int) (int, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if v <= 0 {
		return 0, fmt.Errorf("%s: must be positive, got %d", key, v)
	}
	return v, nil
}

func envInt32(key string, fallback int32) (int32, error) {
	v, err := envInt(key, int(fallback))
	if err != nil {
		return 0, err
	}
	return int32(v), nil
}

func envBool(key string, fallback bool) (bool, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return fallback, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	return v, nil
}

func envLogLevel(key string, fallback slog.Level) (slog.Level, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return fallback, nil
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(strings.ToUpper(raw))); err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return level, nil
}
