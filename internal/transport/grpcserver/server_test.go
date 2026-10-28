package grpcserver_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/ashur/ttr/internal/config"
	"github.com/ashur/ttr/internal/domain"
	"github.com/ashur/ttr/internal/transport/grpcserver"
	"github.com/ashur/ttr/internal/transport/grpcserver/interceptor"
	userv1 "github.com/ashur/ttr/pkg/pb/user/v1"
)

// useCaseStub stands in for the service layer so these tests cover exactly the
// transport concerns: decoding, status mapping and the interceptor chain.
type useCaseStub struct {
	createFn func(context.Context, domain.CreateUserInput) (domain.User, error)
	getFn    func(context.Context, string) (domain.User, error)
	listFn   func(context.Context, int32, string) (domain.UserPage, error)
	updateFn func(context.Context, string, *string, *string) (domain.User, error)
	deleteFn func(context.Context, string) error

	gotEmail    *string
	gotFullName *string
}

func (s *useCaseStub) Create(ctx context.Context, in domain.CreateUserInput) (domain.User, error) {
	if s.createFn != nil {
		return s.createFn(ctx, in)
	}
	return domain.User{ID: uuid.New(), Email: in.Email, FullName: in.FullName}, nil
}

func (s *useCaseStub) Get(ctx context.Context, id string) (domain.User, error) {
	if s.getFn != nil {
		return s.getFn(ctx, id)
	}
	return domain.User{ID: uuid.MustParse(id)}, nil
}

func (s *useCaseStub) List(ctx context.Context, pageSize int32, pageToken string) (domain.UserPage, error) {
	if s.listFn != nil {
		return s.listFn(ctx, pageSize, pageToken)
	}
	return domain.UserPage{}, nil
}

func (s *useCaseStub) Update(ctx context.Context, id string, email, fullName *string) (domain.User, error) {
	s.gotEmail, s.gotFullName = email, fullName
	if s.updateFn != nil {
		return s.updateFn(ctx, id, email, fullName)
	}
	return domain.User{ID: uuid.MustParse(id)}, nil
}

func (s *useCaseStub) Delete(ctx context.Context, id string) error {
	if s.deleteFn != nil {
		return s.deleteFn(ctx, id)
	}
	return nil
}

// startServer runs the production server wiring over an in-memory listener and
// returns a connected client. Everything is torn down via t.Cleanup.
func startServer(t *testing.T, uc grpcserver.UserUseCase) userv1.UserServiceClient {
	t.Helper()

	listener := bufconn.Listen(1 << 20)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.GRPC{
		Addr:              "bufconn",
		ShutdownTimeout:   5 * time.Second,
		HandlerTimeout:    2 * time.Second,
		MaxRecvMsgBytes:   4 << 20,
		MaxConnectionIdle: time.Minute,
		MaxConnectionAge:  time.Minute,
	}

	server := grpcserver.New(cfg, log, grpcserver.NewUserHandler(uc, log))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	t.Cleanup(func() {
		_ = conn.Close()
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve() error = %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("server did not shut down in time")
		}
	})

	return userv1.NewUserServiceClient(conn)
}

func TestCreateUserOK(t *testing.T) {
	t.Parallel()

	client := startServer(t, &useCaseStub{})
	resp, err := client.CreateUser(context.Background(), &userv1.CreateUserRequest{
		Email: "ada@example.com", FullName: "Ada Lovelace",
	})
	if err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}

	if resp.GetUser().GetEmail() != "ada@example.com" {
		t.Errorf("email = %q, want %q", resp.GetUser().GetEmail(), "ada@example.com")
	}
	if _, err := uuid.Parse(resp.GetUser().GetId()); err != nil {
		t.Errorf("id = %q, want a UUID", resp.GetUser().GetId())
	}
}

func TestErrorMapping(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		err      error
		wantCode codes.Code
	}{
		"not found":       {err: domain.ErrNotFound, wantCode: codes.NotFound},
		"already exists":  {err: domain.ErrAlreadyExists, wantCode: codes.AlreadyExists},
		"validation":      {err: &domain.ValidationError{Violations: []domain.FieldViolation{{Field: "email", Description: "bad"}}}, wantCode: codes.InvalidArgument},
		"canceled":        {err: context.Canceled, wantCode: codes.Canceled},
		"deadline":        {err: context.DeadlineExceeded, wantCode: codes.DeadlineExceeded},
		"unknown is 13":   {err: errors.New("boom"), wantCode: codes.Internal},
		"wrapped unknown": {err: errors.New("select user: connection refused"), wantCode: codes.Internal},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			client := startServer(t, &useCaseStub{
				createFn: func(context.Context, domain.CreateUserInput) (domain.User, error) {
					return domain.User{}, tc.err
				},
			})

			_, err := client.CreateUser(context.Background(), &userv1.CreateUserRequest{
				Email: "ada@example.com", FullName: "Ada",
			})
			if got := status.Code(err); got != tc.wantCode {
				t.Fatalf("code = %s, want %s (err = %v)", got, tc.wantCode, err)
			}
		})
	}
}

func TestInternalErrorsDoNotLeakDetails(t *testing.T) {
	t.Parallel()

	client := startServer(t, &useCaseStub{
		createFn: func(context.Context, domain.CreateUserInput) (domain.User, error) {
			return domain.User{}, errors.New(`insert user: dial tcp 10.0.0.5:5432: password authentication failed for user "app"`)
		},
	})

	_, err := client.CreateUser(context.Background(), &userv1.CreateUserRequest{
		Email: "ada@example.com", FullName: "Ada",
	})
	st, _ := status.FromError(err)
	if st.Message() != "internal error" {
		t.Errorf("message = %q, want a generic %q", st.Message(), "internal error")
	}
}

func TestValidationErrorCarriesFieldViolations(t *testing.T) {
	t.Parallel()

	client := startServer(t, &useCaseStub{
		createFn: func(context.Context, domain.CreateUserInput) (domain.User, error) {
			return domain.User{}, &domain.ValidationError{Violations: []domain.FieldViolation{
				{Field: "email", Description: "must be a valid email address"},
				{Field: "full_name", Description: "must not be empty"},
			}}
		},
	})

	_, err := client.CreateUser(context.Background(), &userv1.CreateUserRequest{Email: "x"})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("error = %v, want a status", err)
	}

	var badRequest *errdetails.BadRequest
	for _, detail := range st.Details() {
		if br, isBR := detail.(*errdetails.BadRequest); isBR {
			badRequest = br
		}
	}
	if badRequest == nil {
		t.Fatal("no google.rpc.BadRequest detail attached")
	}
	if len(badRequest.GetFieldViolations()) != 2 {
		t.Fatalf("violations = %d, want 2", len(badRequest.GetFieldViolations()))
	}
	if got := badRequest.GetFieldViolations()[0].GetField(); got != "email" {
		t.Errorf("first violating field = %q, want %q", got, "email")
	}
}

func TestPanicIsRecoveredAsInternal(t *testing.T) {
	t.Parallel()

	client := startServer(t, &useCaseStub{
		getFn: func(context.Context, string) (domain.User, error) {
			panic("handler exploded")
		},
	})

	_, err := client.GetUser(context.Background(), &userv1.GetUserRequest{Id: uuid.NewString()})
	if got := status.Code(err); got != codes.Internal {
		t.Fatalf("code = %s, want Internal", got)
	}

	// The server must still answer the next call.
	if _, err := client.ListUsers(context.Background(), &userv1.ListUsersRequest{}); err != nil {
		t.Fatalf("server did not survive the panic: %v", err)
	}
}

func TestRequestIDIsEchoedBack(t *testing.T) {
	t.Parallel()

	client := startServer(t, &useCaseStub{})
	ctx := metadata.AppendToOutgoingContext(context.Background(), interceptor.RequestIDHeader, "test-request-id")

	var header metadata.MD
	if _, err := client.ListUsers(ctx, &userv1.ListUsersRequest{}, grpc.Header(&header)); err != nil {
		t.Fatalf("ListUsers() error = %v", err)
	}

	if got := header.Get(interceptor.RequestIDHeader); len(got) == 0 || got[0] != "test-request-id" {
		t.Errorf("%s header = %v, want [test-request-id]", interceptor.RequestIDHeader, got)
	}
}

func TestRequestIDIsGeneratedWhenAbsent(t *testing.T) {
	t.Parallel()

	client := startServer(t, &useCaseStub{})

	var header metadata.MD
	if _, err := client.ListUsers(context.Background(), &userv1.ListUsersRequest{}, grpc.Header(&header)); err != nil {
		t.Fatalf("ListUsers() error = %v", err)
	}

	got := header.Get(interceptor.RequestIDHeader)
	if len(got) == 0 {
		t.Fatal("no request id header returned")
	}
	if _, err := uuid.Parse(got[0]); err != nil {
		t.Errorf("request id = %q, want a generated UUID", got[0])
	}
}

func TestListUsersReturnsNextPageToken(t *testing.T) {
	t.Parallel()

	cursor := domain.Cursor{CreatedAt: time.Now().UTC(), ID: uuid.New()}
	client := startServer(t, &useCaseStub{
		listFn: func(context.Context, int32, string) (domain.UserPage, error) {
			return domain.UserPage{
				Users:      []domain.User{{ID: uuid.New(), Email: "a@example.com", FullName: "A"}},
				NextCursor: &cursor,
			}, nil
		},
	})

	resp, err := client.ListUsers(context.Background(), &userv1.ListUsersRequest{PageSize: 1})
	if err != nil {
		t.Fatalf("ListUsers() error = %v", err)
	}
	if resp.GetNextPageToken() != cursor.Encode() {
		t.Errorf("next_page_token = %q, want %q", resp.GetNextPageToken(), cursor.Encode())
	}
	if len(resp.GetUsers()) != 1 {
		t.Errorf("len(users) = %d, want 1", len(resp.GetUsers()))
	}
}

func TestUpdateMaskSelectsFields(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		req          *userv1.UpdateUserRequest
		wantEmail    *string
		wantFullName *string
		wantCode     codes.Code
	}{
		"mask selects one field": {
			req: &userv1.UpdateUserRequest{
				Id:         uuid.NewString(),
				Email:      "new@example.com",
				FullName:   "ignored",
				UpdateMask: fieldMask("email"),
			},
			wantEmail: ptr("new@example.com"),
		},
		"no mask falls back to non-empty fields": {
			req: &userv1.UpdateUserRequest{
				Id:       uuid.NewString(),
				FullName: "Grace Hopper",
			},
			wantFullName: ptr("Grace Hopper"),
		},
		"unknown path is rejected": {
			req: &userv1.UpdateUserRequest{
				Id:         uuid.NewString(),
				UpdateMask: fieldMask("nickname"),
			},
			wantCode: codes.InvalidArgument,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			uc := &useCaseStub{}
			client := startServer(t, uc)

			_, err := client.UpdateUser(context.Background(), tc.req)
			if tc.wantCode != codes.OK {
				if got := status.Code(err); got != tc.wantCode {
					t.Fatalf("code = %s, want %s", got, tc.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("UpdateUser() error = %v", err)
			}

			assertPtrEqual(t, "email", uc.gotEmail, tc.wantEmail)
			assertPtrEqual(t, "full_name", uc.gotFullName, tc.wantFullName)
		})
	}
}

func TestHealthReportsServing(t *testing.T) {
	t.Parallel()

	listener := bufconn.Listen(1 << 20)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.GRPC{
		ShutdownTimeout: 5 * time.Second,
		HandlerTimeout:  2 * time.Second,
		MaxRecvMsgBytes: 4 << 20,
	}
	server := grpcserver.New(cfg, log, grpcserver.NewUserHandler(&useCaseStub{}, log))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Serve(ctx, listener) }()

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	resp, err := healthpb.NewHealthClient(conn).Check(context.Background(), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Errorf("status = %s, want SERVING", resp.GetStatus())
	}
}

func ptr(s string) *string { return &s }

func fieldMask(paths ...string) *fieldmaskpb.FieldMask {
	return &fieldmaskpb.FieldMask{Paths: paths}
}

func assertPtrEqual(t *testing.T, name string, got, want *string) {
	t.Helper()

	switch {
	case want == nil && got != nil:
		t.Errorf("%s = %q, want nil", name, *got)
	case want != nil && got == nil:
		t.Errorf("%s = nil, want %q", name, *want)
	case want != nil && *got != *want:
		t.Errorf("%s = %q, want %q", name, *got, *want)
	}
}
