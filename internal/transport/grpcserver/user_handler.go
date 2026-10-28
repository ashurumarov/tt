// Package grpcserver adapts the gRPC API to the service layer.
//
// Handlers do three things and nothing else: decode the request into domain
// types, call the service, encode the result. Every rule lives in the service
// or the domain, so the API surface can change without touching logic.
package grpcserver

import (
	"context"
	"log/slog"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ashur/ttr/internal/domain"
	userv1 "github.com/ashur/ttr/pkg/pb/user/v1"
)

// UserUseCase is the application contract this handler serves.
type UserUseCase interface {
	Create(ctx context.Context, in domain.CreateUserInput) (domain.User, error)
	Get(ctx context.Context, id string) (domain.User, error)
	List(ctx context.Context, pageSize int32, pageToken string) (domain.UserPage, error)
	Update(ctx context.Context, id string, email, fullName *string) (domain.User, error)
	Delete(ctx context.Context, id string) error
}

// UserHandler implements userv1.UserServiceServer.
type UserHandler struct {
	userv1.UnimplementedUserServiceServer

	users UserUseCase
	log   *slog.Logger
}

// NewUserHandler wires a handler to its use case.
func NewUserHandler(users UserUseCase, log *slog.Logger) *UserHandler {
	return &UserHandler{users: users, log: log}
}

// CreateUser implements userv1.UserServiceServer.
func (h *UserHandler) CreateUser(ctx context.Context, req *userv1.CreateUserRequest) (*userv1.CreateUserResponse, error) {
	user, err := h.users.Create(ctx, domain.CreateUserInput{
		Email:    req.GetEmail(),
		FullName: req.GetFullName(),
	})
	if err != nil {
		return nil, errorToStatus(ctx, h.log, err)
	}
	return &userv1.CreateUserResponse{User: userToProto(user)}, nil
}

// GetUser implements userv1.UserServiceServer.
func (h *UserHandler) GetUser(ctx context.Context, req *userv1.GetUserRequest) (*userv1.GetUserResponse, error) {
	user, err := h.users.Get(ctx, req.GetId())
	if err != nil {
		return nil, errorToStatus(ctx, h.log, err)
	}
	return &userv1.GetUserResponse{User: userToProto(user)}, nil
}

// ListUsers implements userv1.UserServiceServer.
func (h *UserHandler) ListUsers(ctx context.Context, req *userv1.ListUsersRequest) (*userv1.ListUsersResponse, error) {
	page, err := h.users.List(ctx, req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, errorToStatus(ctx, h.log, err)
	}

	resp := &userv1.ListUsersResponse{Users: make([]*userv1.User, 0, len(page.Users))}
	for _, user := range page.Users {
		resp.Users = append(resp.Users, userToProto(user))
	}
	if page.NextCursor != nil {
		resp.NextPageToken = page.NextCursor.Encode()
	}
	return resp, nil
}

// UpdateUser implements userv1.UserServiceServer.
func (h *UserHandler) UpdateUser(ctx context.Context, req *userv1.UpdateUserRequest) (*userv1.UpdateUserResponse, error) {
	email, fullName, err := resolveUpdateMask(req)
	if err != nil {
		return nil, errorToStatus(ctx, h.log, err)
	}

	user, err := h.users.Update(ctx, req.GetId(), email, fullName)
	if err != nil {
		return nil, errorToStatus(ctx, h.log, err)
	}
	return &userv1.UpdateUserResponse{User: userToProto(user)}, nil
}

// DeleteUser implements userv1.UserServiceServer.
func (h *UserHandler) DeleteUser(ctx context.Context, req *userv1.DeleteUserRequest) (*userv1.DeleteUserResponse, error) {
	if err := h.users.Delete(ctx, req.GetId()); err != nil {
		return nil, errorToStatus(ctx, h.log, err)
	}
	return &userv1.DeleteUserResponse{}, nil
}

// resolveUpdateMask decides which fields the update touches.
//
// With a mask, the listed paths are updated even if the value is empty, so a
// client can rely on the mask alone. Without one, non-empty fields are
// updated, which is the behavior proto3 scalars allow without wrapper types.
func resolveUpdateMask(req *userv1.UpdateUserRequest) (email, fullName *string, err error) {
	paths := req.GetUpdateMask().GetPaths()
	if len(paths) == 0 {
		if v := req.GetEmail(); v != "" {
			email = &v
		}
		if v := req.GetFullName(); v != "" {
			fullName = &v
		}
		return email, fullName, nil
	}

	var violations []domain.FieldViolation
	for _, path := range paths {
		switch path {
		case "email":
			v := req.GetEmail()
			email = &v
		case "full_name":
			v := req.GetFullName()
			fullName = &v
		default:
			violations = append(violations, domain.FieldViolation{
				Field:       "update_mask",
				Description: "unknown path " + path + "; want \"email\" or \"full_name\"",
			})
		}
	}
	if len(violations) > 0 {
		return nil, nil, &domain.ValidationError{Violations: violations}
	}
	return email, fullName, nil
}

// userToProto encodes a domain user for the wire.
func userToProto(u domain.User) *userv1.User {
	return &userv1.User{
		Id:        u.ID.String(),
		Email:     u.Email,
		FullName:  u.FullName,
		CreatedAt: timestamppb.New(u.CreatedAt),
		UpdatedAt: timestamppb.New(u.UpdatedAt),
	}
}
