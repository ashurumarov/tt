package domain

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCursorRoundTrip(t *testing.T) {
	t.Parallel()

	want := Cursor{
		// Nanosecond precision must survive encoding, otherwise rows at the
		// page boundary would be served twice or skipped.
		CreatedAt: time.Date(2026, 10, 25, 11, 22, 33, 123456789, time.UTC),
		ID:        uuid.MustParse("3f2b1c4d-5e6f-4a8b-9c0d-1e2f3a4b5c6d"),
	}

	got, err := DecodeCursor(want.Encode())
	if err != nil {
		t.Fatalf("DecodeCursor() error = %v", err)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, want.CreatedAt)
	}
	if got.ID != want.ID {
		t.Errorf("ID = %v, want %v", got.ID, want.ID)
	}
}

func TestDecodeCursorRejectsGarbage(t *testing.T) {
	t.Parallel()

	for name, token := range map[string]string{
		"not base64":      "!!!!",
		"no separator":    "YWJjZGVm",
		"bad timestamp":   encode("nope|3f2b1c4d-5e6f-4a8b-9c0d-1e2f3a4b5c6d"),
		"bad uuid":        encode("2026-10-25T11:22:33Z|nope"),
		"empty but non-0": encode("|"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := DecodeCursor(token); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("DecodeCursor(%q) error = %v, want ErrInvalidArgument", token, err)
			}
		})
	}
}

func TestNormalizePageSize(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		in      int32
		want    int32
		wantErr bool
	}{
		"zero falls back to default": {in: 0, want: DefaultPageSize},
		"explicit size is kept":      {in: 10, want: 10},
		"oversized is clamped":       {in: MaxPageSize + 1, want: MaxPageSize},
		"negative is rejected":       {in: -1, wantErr: true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := NormalizePageSize(tc.in)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidArgument) {
					t.Fatalf("error = %v, want ErrInvalidArgument", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error = %v", err)
			}
			if got != tc.want {
				t.Errorf("NormalizePageSize(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// encode builds a token the same way Cursor.Encode does, so the test can feed
// deliberately malformed payloads through the real decoding path.
func encode(raw string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}
