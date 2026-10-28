package service_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ashur/ttr/internal/domain"
	"github.com/ashur/ttr/internal/service"
)

// storeStub is a hand-written double for service.UserStore. A stub beats a
// generated mock here: the interface is five methods and the test stays
// readable without a framework.
type storeStub struct {
	createFn func(context.Context, domain.CreateUserInput) (domain.User, error)
	getFn    func(context.Context, uuid.UUID) (domain.User, error)
	listFn   func(context.Context, int32, *domain.Cursor) ([]domain.User, error)
	updateFn func(context.Context, domain.UpdateUserInput) (domain.User, error)
	deleteFn func(context.Context, uuid.UUID) error

	// Recorded arguments, for assertions about what the service asked for.
	gotListLimit  int32
	gotListCursor *domain.Cursor
	gotCreate     domain.CreateUserInput
	gotUpdate     domain.UpdateUserInput
	calls         int
}

func (s *storeStub) Create(ctx context.Context, in domain.CreateUserInput) (domain.User, error) {
	s.calls++
	s.gotCreate = in
	if s.createFn != nil {
		return s.createFn(ctx, in)
	}
	return domain.User{ID: uuid.New(), Email: in.Email, FullName: in.FullName}, nil
}

func (s *storeStub) GetByID(ctx context.Context, id uuid.UUID) (domain.User, error) {
	s.calls++
	if s.getFn != nil {
		return s.getFn(ctx, id)
	}
	return domain.User{ID: id}, nil
}

func (s *storeStub) List(ctx context.Context, limit int32, cursor *domain.Cursor) ([]domain.User, error) {
	s.calls++
	s.gotListLimit, s.gotListCursor = limit, cursor
	if s.listFn != nil {
		return s.listFn(ctx, limit, cursor)
	}
	return nil, nil
}

func (s *storeStub) Update(ctx context.Context, in domain.UpdateUserInput) (domain.User, error) {
	s.calls++
	s.gotUpdate = in
	if s.updateFn != nil {
		return s.updateFn(ctx, in)
	}
	return domain.User{ID: in.ID}, nil
}

func (s *storeStub) Delete(ctx context.Context, id uuid.UUID) error {
	s.calls++
	if s.deleteFn != nil {
		return s.deleteFn(ctx, id)
	}
	return nil
}

func newService(store service.UserStore) *service.UserService {
	return service.NewUserService(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestCreateNormalizesBeforeStoring(t *testing.T) {
	t.Parallel()

	store := &storeStub{}
	user, err := newService(store).Create(context.Background(), domain.CreateUserInput{
		Email:    " ADA@Example.com ",
		FullName: "  Ada Lovelace ",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if store.gotCreate.Email != "ada@example.com" {
		t.Errorf("stored email = %q, want %q", store.gotCreate.Email, "ada@example.com")
	}
	if store.gotCreate.FullName != "Ada Lovelace" {
		t.Errorf("stored full name = %q, want %q", store.gotCreate.FullName, "Ada Lovelace")
	}
	if user.Email != "ada@example.com" {
		t.Errorf("returned email = %q, want normalized value", user.Email)
	}
}

func TestCreateRejectsInvalidInputWithoutTouchingStore(t *testing.T) {
	t.Parallel()

	store := &storeStub{}
	_, err := newService(store).Create(context.Background(), domain.CreateUserInput{Email: "nope"})

	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("error = %v, want ErrInvalidArgument", err)
	}
	if store.calls != 0 {
		t.Errorf("store calls = %d, want 0: validation must short-circuit", store.calls)
	}
}

func TestCreatePropagatesConflict(t *testing.T) {
	t.Parallel()

	store := &storeStub{createFn: func(context.Context, domain.CreateUserInput) (domain.User, error) {
		return domain.User{}, domain.ErrAlreadyExists
	}}

	_, err := newService(store).Create(context.Background(), domain.CreateUserInput{
		Email: "ada@example.com", FullName: "Ada",
	})
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("error = %v, want ErrAlreadyExists", err)
	}
}

func TestGetRejectsNonUUID(t *testing.T) {
	t.Parallel()

	store := &storeStub{}
	_, err := newService(store).Get(context.Background(), "42")

	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("error = %v, want ErrInvalidArgument", err)
	}
	if store.calls != 0 {
		t.Errorf("store calls = %d, want 0", store.calls)
	}
}

func TestGetPropagatesNotFound(t *testing.T) {
	t.Parallel()

	store := &storeStub{getFn: func(context.Context, uuid.UUID) (domain.User, error) {
		return domain.User{}, domain.ErrNotFound
	}}

	_, err := newService(store).Get(context.Background(), uuid.NewString())
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestListFullPageYieldsNextCursor(t *testing.T) {
	t.Parallel()

	// Three rows exist and the caller asked for two: the store is probed for
	// three so the service can tell there is more without a COUNT query.
	rows := makeUsers(3)
	store := &storeStub{listFn: func(_ context.Context, limit int32, _ *domain.Cursor) ([]domain.User, error) {
		return rows[:limit], nil
	}}

	page, err := newService(store).List(context.Background(), 2, "")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if store.gotListLimit != 3 {
		t.Errorf("store limit = %d, want 3 (page size + 1)", store.gotListLimit)
	}
	if len(page.Users) != 2 {
		t.Fatalf("len(users) = %d, want 2: the probe row must not leak out", len(page.Users))
	}
	if page.NextCursor == nil {
		t.Fatal("NextCursor = nil, want a cursor")
	}
	if page.NextCursor.ID != rows[1].ID {
		t.Errorf("cursor id = %v, want last returned row %v", page.NextCursor.ID, rows[1].ID)
	}
}

func TestListLastPageHasNoCursor(t *testing.T) {
	t.Parallel()

	rows := makeUsers(2)
	store := &storeStub{listFn: func(context.Context, int32, *domain.Cursor) ([]domain.User, error) {
		return rows, nil
	}}

	page, err := newService(store).List(context.Background(), 5, "")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(page.Users) != 2 {
		t.Errorf("len(users) = %d, want 2", len(page.Users))
	}
	if page.NextCursor != nil {
		t.Errorf("NextCursor = %v, want nil on the last page", page.NextCursor)
	}
}

func TestListForwardsDecodedCursor(t *testing.T) {
	t.Parallel()

	want := domain.Cursor{CreatedAt: time.Now().UTC().Truncate(time.Microsecond), ID: uuid.New()}
	store := &storeStub{}

	if _, err := newService(store).List(context.Background(), 1, want.Encode()); err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if store.gotListCursor == nil {
		t.Fatal("cursor = nil, want it forwarded to the store")
	}
	if store.gotListCursor.ID != want.ID || !store.gotListCursor.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("cursor = %+v, want %+v", *store.gotListCursor, want)
	}
}

func TestListRejectsBadToken(t *testing.T) {
	t.Parallel()

	store := &storeStub{}
	_, err := newService(store).List(context.Background(), 10, "!!!not-a-token")

	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("error = %v, want ErrInvalidArgument", err)
	}
	if store.calls != 0 {
		t.Errorf("store calls = %d, want 0", store.calls)
	}
}

func TestUpdateOnlyForwardsPresentFields(t *testing.T) {
	t.Parallel()

	fullName := "  Grace Hopper "
	store := &storeStub{}

	if _, err := newService(store).Update(context.Background(), uuid.NewString(), nil, &fullName); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	if store.gotUpdate.Email != nil {
		t.Errorf("email = %v, want nil so the stored value is kept", *store.gotUpdate.Email)
	}
	if store.gotUpdate.FullName == nil || *store.gotUpdate.FullName != "Grace Hopper" {
		t.Errorf("full name = %v, want normalized %q", store.gotUpdate.FullName, "Grace Hopper")
	}
}

func TestUpdateWithNoFieldsIsRejected(t *testing.T) {
	t.Parallel()

	store := &storeStub{}
	_, err := newService(store).Update(context.Background(), uuid.NewString(), nil, nil)

	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("error = %v, want ErrInvalidArgument", err)
	}
	if store.calls != 0 {
		t.Errorf("store calls = %d, want 0", store.calls)
	}
}

func TestDeletePropagatesNotFound(t *testing.T) {
	t.Parallel()

	store := &storeStub{deleteFn: func(context.Context, uuid.UUID) error {
		return domain.ErrNotFound
	}}

	if err := newService(store).Delete(context.Background(), uuid.NewString()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

// makeUsers returns n users ordered newest first, as the repository would.
func makeUsers(n int) []domain.User {
	now := time.Now().UTC()
	users := make([]domain.User, 0, n)
	for i := range n {
		users = append(users, domain.User{
			ID:        uuid.New(),
			Email:     "user@example.com",
			FullName:  "User",
			CreatedAt: now.Add(-time.Duration(i) * time.Minute),
			UpdatedAt: now.Add(-time.Duration(i) * time.Minute),
		})
	}
	return users
}
