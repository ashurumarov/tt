// Package postgres implements the repository layer on top of pgx.
//
// Queries are written by hand against a connection pool: no ORM, no query
// builder. The schema is small and explicit SQL keeps the generated plan and
// the index usage obvious.
package postgres

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/tracelog"

	"github.com/ashur/ttr/internal/config"
)

// NewPool opens a connection pool and verifies it can reach the database.
// The caller owns the returned pool and must Close it.
func NewPool(ctx context.Context, cfg config.Postgres, log *slog.Logger) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse postgres dsn: %w", err)
	}

	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MinConns = cfg.MinConns
	poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	poolCfg.HealthCheckPeriod = cfg.HealthCheckPeriod
	poolCfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout

	// Statement cache mode: prepared statements are cached per connection,
	// which is the right default for a long-lived pool talking straight to
	// PostgreSQL (switch to QueryExecModeExec behind a transaction pooler).
	poolCfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeCacheStatement

	// Query logging rides on the service logger and only materializes at
	// debug level, so production pays nothing for it.
	poolCfg.ConnConfig.Tracer = &tracelog.TraceLog{
		Logger:   slogAdapter{log: log.With(slog.String("component", "pgx"))},
		LogLevel: tracelog.LogLevelWarn,
	}
	if log.Enabled(ctx, slog.LevelDebug) {
		poolCfg.ConnConfig.Tracer = &tracelog.TraceLog{
			Logger:   slogAdapter{log: log.With(slog.String("component", "pgx"))},
			LogLevel: tracelog.LogLevelDebug,
		}
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return pool, nil
}

// slogAdapter bridges pgx's tracelog interface to log/slog.
type slogAdapter struct {
	log *slog.Logger
}

// Log implements tracelog.Logger.
func (a slogAdapter) Log(ctx context.Context, level tracelog.LogLevel, msg string, data map[string]any) {
	attrs := make([]slog.Attr, 0, len(data))
	for k, v := range data {
		attrs = append(attrs, slog.Any(k, v))
	}

	var slogLevel slog.Level
	switch level {
	case tracelog.LogLevelTrace, tracelog.LogLevelDebug:
		slogLevel = slog.LevelDebug
	case tracelog.LogLevelInfo:
		slogLevel = slog.LevelInfo
	case tracelog.LogLevelWarn:
		slogLevel = slog.LevelWarn
	default:
		slogLevel = slog.LevelError
	}
	a.log.LogAttrs(ctx, slogLevel, msg, attrs...)
}
