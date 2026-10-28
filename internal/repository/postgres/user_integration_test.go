//go:build integration

// Integration tests for the SQL itself: the unique constraint, the keyset
// pagination query and the updated_at trigger cannot be verified with a stub.
//
// Run with:
//
//	make test-integration
//
// Each run gets its own PostgreSQL schema, created and dropped by the test, so
// concurrent runs and a dirty database cannot affect the outcome.
package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ashur/ttr/internal/domain"
	"github.com/ashur/ttr/internal/repository/postgres"
)

const migrationFile = "../../../migrations/000001_create_users_table.up.sql"

func newTestRepo(t *testing.T) *postgres.UserRepository {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	schema := "test_" + uuid.New().String()[:8]
	ctx := context.Background()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	// The migration file contains several statements in one script, which
	// only the simple protocol accepts in a single Exec.
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 4
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		// Every pooled connection must see the test schema, including the
		// ones opened lazily after setup.
		_, err := conn.Exec(ctx, "SET search_path TO "+schema)
		return err
	}

	// The schema has to exist before any pooled connection sets search_path
	// to it, so it is created on a standalone connection first.
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if err := admin.Close(ctx); err != nil {
		t.Fatalf("close admin connection: %v", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}

	migration, err := os.ReadFile(migrationFile)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := pool.Exec(ctx, string(migration)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()

		dropCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		conn, err := pgx.Connect(dropCtx, dsn)
		if err != nil {
			t.Errorf("connect for cleanup: %v", err)
			return
		}
		defer conn.Close(dropCtx)

		if _, err := conn.Exec(dropCtx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
			t.Errorf("drop schema: %v", err)
		}
	})

	return postgres.NewUserRepository(pool)
}

func TestCreateAndGet(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	created, err := repo.Create(ctx, domain.CreateUserInput{Email: "ada@example.com", FullName: "Ada Lovelace"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.ID == uuid.Nil {
		t.Error("ID = zero, want a generated UUID")
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Error("timestamps = zero, want database defaults")
	}

	got, err := repo.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if got.Email != created.Email || got.FullName != created.FullName {
		t.Errorf("GetByID() = %+v, want %+v", got, created)
	}
}

func TestCreateDuplicateEmail(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	in := domain.CreateUserInput{Email: "ada@example.com", FullName: "Ada"}

	if _, err := repo.Create(ctx, in); err != nil {
		t.Fatalf("first Create() error = %v", err)
	}

	_, err := repo.Create(ctx, in)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("second Create() error = %v, want ErrAlreadyExists", err)
	}
}

func TestGetUnknownID(t *testing.T) {
	repo := newTestRepo(t)

	if _, err := repo.GetByID(context.Background(), uuid.New()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestListKeysetPaginationCoversEveryRowExactlyOnce(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	const total = 25
	for i := range total {
		if _, err := repo.Create(ctx, domain.CreateUserInput{
			Email:    fmt.Sprintf("user%02d@example.com", i),
			FullName: fmt.Sprintf("User %02d", i),
		}); err != nil {
			t.Fatalf("Create(%d) error = %v", i, err)
		}
	}

	// Rows created in the same transaction-less burst can share a timestamp,
	// so the id tiebreaker in the cursor is what keeps paging exact here.
	seen := make(map[uuid.UUID]int)
	var cursor *domain.Cursor
	var previous *domain.User

	for page := 0; ; page++ {
		if page > total {
			t.Fatal("pagination did not terminate")
		}

		users, err := repo.List(ctx, 4, cursor)
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(users) == 0 {
			break
		}

		for i := range users {
			user := users[i]
			seen[user.ID]++

			if previous != nil {
				// Ordering must be strictly descending by (created_at, id).
				if user.CreatedAt.After(previous.CreatedAt) {
					t.Fatalf("row %s is newer than the previous row: ordering broken", user.ID)
				}
				if user.CreatedAt.Equal(previous.CreatedAt) && user.ID.String() >= previous.ID.String() {
					t.Fatalf("tie at %s not broken by descending id", user.CreatedAt)
				}
			}
			previous = &user
		}

		last := users[len(users)-1]
		cursor = &domain.Cursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}

	if len(seen) != total {
		t.Errorf("distinct rows = %d, want %d", len(seen), total)
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("row %s returned %d times, want once", id, count)
		}
	}
}

func TestListRespectsLimit(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	for i := range 5 {
		if _, err := repo.Create(ctx, domain.CreateUserInput{
			Email:    fmt.Sprintf("user%d@example.com", i),
			FullName: "User",
		}); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
	}

	users, err := repo.List(ctx, 2, nil)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(users) != 2 {
		t.Errorf("len(users) = %d, want 2", len(users))
	}
}

func TestUpdatePartialKeepsOtherFieldsAndBumpsUpdatedAt(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	created, err := repo.Create(ctx, domain.CreateUserInput{Email: "ada@example.com", FullName: "Ada Lovelace"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	newName := "Augusta Ada King"
	updated, err := repo.Update(ctx, domain.UpdateUserInput{ID: created.ID, FullName: &newName})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	if updated.FullName != newName {
		t.Errorf("FullName = %q, want %q", updated.FullName, newName)
	}
	if updated.Email != created.Email {
		t.Errorf("Email = %q, want it untouched (%q)", updated.Email, created.Email)
	}
	if !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Error("CreatedAt changed on update")
	}
	if !updated.UpdatedAt.After(created.UpdatedAt) {
		t.Errorf("UpdatedAt = %v, want later than %v (trigger should bump it)", updated.UpdatedAt, created.UpdatedAt)
	}
}

func TestUpdateUnknownID(t *testing.T) {
	repo := newTestRepo(t)

	name := "Nobody"
	_, err := repo.Update(context.Background(), domain.UpdateUserInput{ID: uuid.New(), FullName: &name})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestUpdateToDuplicateEmail(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	if _, err := repo.Create(ctx, domain.CreateUserInput{Email: "taken@example.com", FullName: "Taken"}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	other, err := repo.Create(ctx, domain.CreateUserInput{Email: "free@example.com", FullName: "Free"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	taken := "taken@example.com"
	if _, err := repo.Update(ctx, domain.UpdateUserInput{ID: other.ID, Email: &taken}); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("error = %v, want ErrAlreadyExists", err)
	}
}

func TestDelete(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	created, err := repo.Create(ctx, domain.CreateUserInput{Email: "ada@example.com", FullName: "Ada"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if err := repo.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if err := repo.Delete(ctx, created.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second Delete() error = %v, want ErrNotFound", err)
	}
	if _, err := repo.GetByID(ctx, created.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetByID() after delete error = %v, want ErrNotFound", err)
	}
}

func TestEmailLowercaseConstraintIsEnforced(t *testing.T) {
	repo := newTestRepo(t)

	// The service normalizes, but the constraint must hold even if a future
	// caller forgets to.
	_, err := repo.Create(context.Background(), domain.CreateUserInput{Email: "ADA@example.com", FullName: "Ada"})
	if err == nil {
		t.Fatal("Create() with an uppercase email succeeded, want a constraint violation")
	}
	if errors.Is(err, domain.ErrAlreadyExists) || errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want a raw constraint error", err)
	}
}
