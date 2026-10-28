// Package service holds the application logic: it validates input, applies
// business rules and orchestrates the repository. It knows nothing about gRPC
// or SQL, which is what makes it directly unit testable.
package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/ashur/ttr/internal/domain"
)

// UserStore is the persistence contract the user service needs. It is declared
// here, next to its consumer, so the service can be tested with a fake and the
// repository stays free of interfaces nobody else uses.
type UserStore interface {
	Create(ctx context.Context, in domain.CreateUserInput) (domain.User, error)
	GetByID(ctx context.Context, id uuid.UUID) (domain.User, error)
	List(ctx context.Context, limit int32, cursor *domain.Cursor) ([]domain.User, error)
	Update(ctx context.Context, in domain.UpdateUserInput) (domain.User, error)
	Delete(ctx context.Context, id uuid.UUID) error
}

// UserService implements the user use cases.
type UserService struct {
	store UserStore
	log   *slog.Logger
}

// NewUserService wires a service to its store.
func NewUserService(store UserStore, log *slog.Logger) *UserService {
	return &UserService{store: store, log: log}
}

// Create validates and persists a new user.
func (s *UserService) Create(ctx context.Context, in domain.CreateUserInput) (domain.User, error) {
	in.Normalize()
	if err := in.Validate(); err != nil {
		return domain.User{}, err
	}

	user, err := s.store.Create(ctx, in)
	if err != nil {
		return domain.User{}, fmt.Errorf("create user: %w", err)
	}

	s.log.InfoContext(ctx, "user created", slog.String("user_id", user.ID.String()))
	return user, nil
}

// Get returns a user by its raw id, rejecting ids that are not UUIDs before
// touching the database.
func (s *UserService) Get(ctx context.Context, rawID string) (domain.User, error) {
	id, err := parseID(rawID)
	if err != nil {
		return domain.User{}, err
	}

	user, err := s.store.GetByID(ctx, id)
	if err != nil {
		return domain.User{}, fmt.Errorf("get user: %w", err)
	}
	return user, nil
}

// List returns one page of users. It asks the store for one row more than the
// page size: if that extra row comes back there is a next page, and the last
// returned row becomes the cursor. This keeps pagination exact without a
// second COUNT query.
func (s *UserService) List(ctx context.Context, pageSize int32, pageToken string) (domain.UserPage, error) {
	size, err := domain.NormalizePageSize(pageSize)
	if err != nil {
		return domain.UserPage{}, err
	}

	var cursor *domain.Cursor
	if pageToken != "" {
		decoded, err := domain.DecodeCursor(pageToken)
		if err != nil {
			return domain.UserPage{}, err
		}
		cursor = &decoded
	}

	users, err := s.store.List(ctx, size+1, cursor)
	if err != nil {
		return domain.UserPage{}, fmt.Errorf("list users: %w", err)
	}

	page := domain.UserPage{Users: users}
	if int32(len(users)) > size {
		last := users[size-1]
		page.Users = users[:size]
		page.NextCursor = &domain.Cursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return page, nil
}

// Update applies a partial update to an existing user.
func (s *UserService) Update(ctx context.Context, rawID string, email, fullName *string) (domain.User, error) {
	id, err := parseID(rawID)
	if err != nil {
		return domain.User{}, err
	}

	in := domain.UpdateUserInput{ID: id, Email: email, FullName: fullName}
	in.Normalize()
	if err := in.Validate(); err != nil {
		return domain.User{}, err
	}

	user, err := s.store.Update(ctx, in)
	if err != nil {
		return domain.User{}, fmt.Errorf("update user: %w", err)
	}

	s.log.InfoContext(ctx, "user updated", slog.String("user_id", user.ID.String()))
	return user, nil
}

// Delete removes a user.
func (s *UserService) Delete(ctx context.Context, rawID string) error {
	id, err := parseID(rawID)
	if err != nil {
		return err
	}

	if err := s.store.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete user: %w", err)
	}

	s.log.InfoContext(ctx, "user deleted", slog.String("user_id", id.String()))
	return nil
}

func parseID(rawID string) (uuid.UUID, error) {
	id, err := uuid.Parse(rawID)
	if err != nil {
		return uuid.Nil, &domain.ValidationError{Violations: []domain.FieldViolation{{
			Field: "id", Description: "must be a valid UUID",
		}}}
	}
	return id, nil
}
