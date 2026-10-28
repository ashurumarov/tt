package grpcserver

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ashur/ttr/internal/domain"
)

// errorToStatus is the single place where domain errors become gRPC statuses.
//
// Two rules hold here: a client gets a precise, actionable message for its own
// mistakes, and never anything about the server's internals. Unmapped errors
// are logged in full and reported as a bare Internal.
func errorToStatus(ctx context.Context, log *slog.Logger, err error) error {
	if err == nil {
		return nil
	}

	var validationErr *domain.ValidationError
	switch {
	case errors.As(err, &validationErr):
		return invalidArgument(validationErr)

	case errors.Is(err, domain.ErrNotFound):
		return status.Error(codes.NotFound, "user not found")

	case errors.Is(err, domain.ErrAlreadyExists):
		return status.Error(codes.AlreadyExists, "user with this email already exists")

	case errors.Is(err, domain.ErrInvalidArgument):
		return status.Error(codes.InvalidArgument, "invalid argument")

	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "request canceled")

	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "request deadline exceeded")

	default:
		log.ErrorContext(ctx, "unhandled error", slog.String("error", err.Error()))
		return status.Error(codes.Internal, "internal error")
	}
}

// invalidArgument attaches a google.rpc.BadRequest detail listing every
// rejected field, which is the convention gRPC clients already understand.
func invalidArgument(err *domain.ValidationError) error {
	st := status.New(codes.InvalidArgument, err.Error())

	violations := make([]*errdetails.BadRequest_FieldViolation, 0, len(err.Violations))
	for _, v := range err.Violations {
		violations = append(violations, &errdetails.BadRequest_FieldViolation{
			Field:       v.Field,
			Description: v.Description,
		})
	}

	withDetails, detailErr := st.WithDetails(&errdetails.BadRequest{FieldViolations: violations})
	if detailErr != nil {
		// Detail marshaling cannot realistically fail; the plain status is
		// still a correct answer if it ever does.
		return st.Err()
	}
	return withDetails.Err()
}
