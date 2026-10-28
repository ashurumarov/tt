package domain

import (
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Field limits are mirrored by the database schema; keeping them here lets the
// service reject oversized input before it reaches PostgreSQL.
const (
	MaxEmailLen    = 320
	MaxFullNameLen = 255
)

// User is the domain representation of a user record.
type User struct {
	ID        uuid.UUID
	Email     string
	FullName  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreateUserInput carries the caller-supplied data for a new user.
type CreateUserInput struct {
	Email    string
	FullName string
}

// Normalize trims whitespace and lowercases the email so that uniqueness is
// case-insensitive and stored data stays canonical.
func (in *CreateUserInput) Normalize() {
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	in.FullName = strings.TrimSpace(in.FullName)
}

// Validate reports every problem with the input at once.
func (in CreateUserInput) Validate() error {
	var v []FieldViolation
	v = appendEmailViolations(v, "email", in.Email)
	v = appendFullNameViolations(v, "full_name", in.FullName)
	return newValidationError(v)
}

// UpdateUserInput carries a partial update. A nil field is left untouched,
// which is how the field mask from the API surfaces in the domain.
type UpdateUserInput struct {
	ID       uuid.UUID
	Email    *string
	FullName *string
}

// Normalize applies the same canonicalization as CreateUserInput to the fields
// that are actually present.
func (in *UpdateUserInput) Normalize() {
	if in.Email != nil {
		normalized := strings.ToLower(strings.TrimSpace(*in.Email))
		in.Email = &normalized
	}
	if in.FullName != nil {
		normalized := strings.TrimSpace(*in.FullName)
		in.FullName = &normalized
	}
}

// IsEmpty reports whether the update would change nothing.
func (in UpdateUserInput) IsEmpty() bool {
	return in.Email == nil && in.FullName == nil
}

// Validate reports every problem with the present fields at once.
func (in UpdateUserInput) Validate() error {
	var v []FieldViolation
	if in.ID == uuid.Nil {
		v = append(v, FieldViolation{Field: "id", Description: "must be a valid UUID"})
	}
	if in.Email != nil {
		v = appendEmailViolations(v, "email", *in.Email)
	}
	if in.FullName != nil {
		v = appendFullNameViolations(v, "full_name", *in.FullName)
	}
	if in.IsEmpty() {
		v = append(v, FieldViolation{Field: "update_mask", Description: "must select at least one updatable field"})
	}
	return newValidationError(v)
}

func appendEmailViolations(v []FieldViolation, field, email string) []FieldViolation {
	switch {
	case email == "":
		return append(v, FieldViolation{Field: field, Description: "must not be empty"})
	case len(email) > MaxEmailLen:
		return append(v, FieldViolation{Field: field, Description: "must be at most 320 bytes"})
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return append(v, FieldViolation{Field: field, Description: "must be a valid email address"})
	}
	return v
}

func appendFullNameViolations(v []FieldViolation, field, name string) []FieldViolation {
	switch {
	case name == "":
		return append(v, FieldViolation{Field: field, Description: "must not be empty"})
	case utf8.RuneCountInString(name) > MaxFullNameLen:
		return append(v, FieldViolation{Field: field, Description: "must be at most 255 characters"})
	case !utf8.ValidString(name):
		return append(v, FieldViolation{Field: field, Description: "must be valid UTF-8"})
	}
	return v
}
