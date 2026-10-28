package domain

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Page size bounds for list endpoints.
const (
	DefaultPageSize = 50
	MaxPageSize     = 200
)

// Cursor is a keyset pagination position. Keyset beats OFFSET here: the query
// stays O(page) regardless of depth and rows cannot shift between pages.
type Cursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// Encode renders the cursor as an opaque page token.
func (c Cursor) Encode() string {
	raw := fmt.Sprintf("%s|%s", c.CreatedAt.UTC().Format(time.RFC3339Nano), c.ID)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor parses a page token produced by Cursor.Encode.
func DecodeCursor(token string) (Cursor, error) {
	invalid := &ValidationError{Violations: []FieldViolation{{
		Field: "page_token", Description: "must be a token returned by a previous ListUsers call",
	}}}

	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return Cursor{}, invalid
	}
	createdAt, id, ok := strings.Cut(string(raw), "|")
	if !ok {
		return Cursor{}, invalid
	}
	ts, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return Cursor{}, invalid
	}
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return Cursor{}, invalid
	}
	return Cursor{CreatedAt: ts, ID: parsedID}, nil
}

// ListUsersInput is a validated, normalized page request.
type ListUsersInput struct {
	PageSize int32
	Cursor   *Cursor
}

// NormalizePageSize clamps the requested size into the supported range.
func NormalizePageSize(size int32) (int32, error) {
	switch {
	case size < 0:
		return 0, &ValidationError{Violations: []FieldViolation{{
			Field: "page_size", Description: "must not be negative",
		}}}
	case size == 0:
		return DefaultPageSize, nil
	case size > MaxPageSize:
		return MaxPageSize, nil
	default:
		return size, nil
	}
}

// UserPage is one page of users plus the cursor needed to fetch the next one.
// NextCursor is nil on the last page.
type UserPage struct {
	Users      []User
	NextCursor *Cursor
}
