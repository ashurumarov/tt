package domain

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestCreateUserInputNormalize(t *testing.T) {
	t.Parallel()

	in := CreateUserInput{Email: "  Ada@Example.COM ", FullName: "  Ada Lovelace  "}
	in.Normalize()

	if in.Email != "ada@example.com" {
		t.Errorf("Email = %q, want %q", in.Email, "ada@example.com")
	}
	if in.FullName != "Ada Lovelace" {
		t.Errorf("FullName = %q, want %q", in.FullName, "Ada Lovelace")
	}
}

func TestCreateUserInputValidate(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		in         CreateUserInput
		wantFields []string
	}{
		"valid": {
			in: CreateUserInput{Email: "ada@example.com", FullName: "Ada Lovelace"},
		},
		"empty email": {
			in:         CreateUserInput{Email: "", FullName: "Ada"},
			wantFields: []string{"email"},
		},
		"malformed email": {
			in:         CreateUserInput{Email: "not-an-email", FullName: "Ada"},
			wantFields: []string{"email"},
		},
		"email with display name is rejected": {
			in:         CreateUserInput{Email: "Ada <ada@example.com>", FullName: "Ada"},
			wantFields: []string{"email"},
		},
		"email too long": {
			in:         CreateUserInput{Email: strings.Repeat("a", 320) + "@example.com", FullName: "Ada"},
			wantFields: []string{"email"},
		},
		"empty full name": {
			in:         CreateUserInput{Email: "ada@example.com", FullName: ""},
			wantFields: []string{"full_name"},
		},
		"full name too long": {
			in:         CreateUserInput{Email: "ada@example.com", FullName: strings.Repeat("я", 256)},
			wantFields: []string{"full_name"},
		},
		"every violation is reported at once": {
			in:         CreateUserInput{Email: "bad", FullName: ""},
			wantFields: []string{"email", "full_name"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := tc.in.Validate()
			if len(tc.wantFields) == 0 {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}

			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("errors.Is(err, ErrInvalidArgument) = false, err = %v", err)
			}

			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("errors.As(%v, *ValidationError) = false", err)
			}
			if got := fields(ve); !equalUnordered(got, tc.wantFields) {
				t.Errorf("violating fields = %v, want %v", got, tc.wantFields)
			}
		})
	}
}

func TestUpdateUserInputValidate(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	email := "ada@example.com"
	empty := ""

	tests := map[string]struct {
		in         UpdateUserInput
		wantFields []string
	}{
		"single field update is valid": {
			in: UpdateUserInput{ID: id, Email: &email},
		},
		"no fields selected": {
			in:         UpdateUserInput{ID: id},
			wantFields: []string{"update_mask"},
		},
		"zero id": {
			in:         UpdateUserInput{Email: &email},
			wantFields: []string{"id"},
		},
		"masked field cleared to empty is rejected": {
			in:         UpdateUserInput{ID: id, FullName: &empty},
			wantFields: []string{"full_name"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := tc.in.Validate()
			if len(tc.wantFields) == 0 {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}

			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("errors.As(%v, *ValidationError) = false", err)
			}
			if got := fields(ve); !equalUnordered(got, tc.wantFields) {
				t.Errorf("violating fields = %v, want %v", got, tc.wantFields)
			}
		})
	}
}

func fields(err *ValidationError) []string {
	out := make([]string, 0, len(err.Violations))
	for _, v := range err.Violations {
		out = append(out, v.Field)
	}
	return out
}

func equalUnordered(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := make(map[string]int, len(got))
	for _, g := range got {
		seen[g]++
	}
	for _, w := range want {
		if seen[w] == 0 {
			return false
		}
		seen[w]--
	}
	return true
}
