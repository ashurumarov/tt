package config_test

import (
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ashur/ttr/internal/config"
)

// These tests use t.Setenv, which forbids t.Parallel: the environment is
// process-wide state.

func TestLoadDefaults(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "secret")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Env != "development" {
		t.Errorf("Env = %q, want %q", cfg.Env, "development")
	}
	if cfg.GRPC.Addr != ":50051" {
		t.Errorf("GRPC.Addr = %q, want %q", cfg.GRPC.Addr, ":50051")
	}
	if cfg.Log.Level != slog.LevelInfo {
		t.Errorf("Log.Level = %v, want INFO", cfg.Log.Level)
	}
	if !cfg.GRPC.EnableReflection {
		t.Error("EnableReflection = false, want true outside production")
	}
	if cfg.GRPC.HandlerTimeout != 5*time.Second {
		t.Errorf("HandlerTimeout = %v, want 5s", cfg.GRPC.HandlerTimeout)
	}
}

func TestReflectionIsOffInProductionByDefault(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "secret")
	t.Setenv("APP_ENV", "production")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.GRPC.EnableReflection {
		t.Error("EnableReflection = true, want false in production")
	}
}

func TestReflectionCanBeForcedOnInProduction(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "secret")
	t.Setenv("APP_ENV", "production")
	t.Setenv("GRPC_ENABLE_REFLECTION", "true")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.GRPC.EnableReflection {
		t.Error("EnableReflection = false, want the explicit override to win")
	}
}

func TestDSNIsAssembledFromParts(t *testing.T) {
	t.Setenv("POSTGRES_HOST", "db.internal")
	t.Setenv("POSTGRES_PORT", "6432")
	t.Setenv("POSTGRES_USER", "app")
	// A password with reserved characters must survive URL encoding.
	t.Setenv("POSTGRES_PASSWORD", "p@ss:w/rd?")
	t.Setenv("POSTGRES_DB", "users")
	t.Setenv("POSTGRES_SSLMODE", "require")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	for _, want := range []string{"db.internal:6432", "/users", "sslmode=require", "p%40ss%3Aw%2Frd%3F"} {
		if !strings.Contains(cfg.Postgres.DSN, want) {
			t.Errorf("DSN = %q, want it to contain %q", cfg.Postgres.DSN, want)
		}
	}
}

func TestExplicitDATABASEURLWins(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@example.com:5432/db?sslmode=verify-full")
	t.Setenv("POSTGRES_HOST", "ignored")
	t.Setenv("POSTGRES_PASSWORD", "ignored")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Postgres.DSN != "postgres://u:p@example.com:5432/db?sslmode=verify-full" {
		t.Errorf("DSN = %q, want the explicit DATABASE_URL", cfg.Postgres.DSN)
	}
}

func TestMissingCredentialsFail(t *testing.T) {
	tests := map[string]string{
		// Neither DATABASE_URL nor POSTGRES_PASSWORD: no safe default exists.
		"unset": "",
		// An empty value must not be read as "no password needed".
		"empty": "",
	}

	for name := range tests {
		t.Run(name, func(t *testing.T) {
			// t.Setenv registers the original value for restoration, so
			// unsetting afterwards is still undone when the test ends.
			t.Setenv("DATABASE_URL", "")
			t.Setenv("POSTGRES_PASSWORD", "")
			if name == "unset" {
				os.Unsetenv("POSTGRES_PASSWORD")
			}

			if _, err := config.Load(); err == nil {
				t.Fatal("Load() error = nil, want a failure")
			}
		})
	}
}

func TestEveryProblemIsReportedAtOnce(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "secret")
	t.Setenv("LOG_LEVEL", "verbose")
	t.Setenv("LOG_FORMAT", "yaml")
	t.Setenv("GRPC_HANDLER_TIMEOUT", "soon")
	t.Setenv("POSTGRES_MAX_CONNS", "-4")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() error = nil, want a failure")
	}

	// A misconfigured deployment should need one restart to see every fix,
	// not one per variable.
	for _, want := range []string{"LOG_LEVEL", "LOG_FORMAT", "GRPC_HANDLER_TIMEOUT", "POSTGRES_MAX_CONNS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %s", err, want)
		}
	}
}

func TestMinConnsAboveMaxConnsIsRejected(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "secret")
	t.Setenv("POSTGRES_MAX_CONNS", "2")
	t.Setenv("POSTGRES_MIN_CONNS", "8")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() error = nil, want a failure")
	}
	if !strings.Contains(err.Error(), "POSTGRES_MIN_CONNS") {
		t.Errorf("error = %v, want it to mention POSTGRES_MIN_CONNS", err)
	}
}

func TestLogLevelIsCaseInsensitive(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "secret")
	t.Setenv("LOG_LEVEL", "debug")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Log.Level != slog.LevelDebug {
		t.Errorf("Log.Level = %v, want DEBUG", cfg.Log.Level)
	}
}
