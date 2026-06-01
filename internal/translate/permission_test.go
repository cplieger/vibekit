package translate

import (
	"testing"

	"vibekit/internal/api"
)

func TestFindAllowOnce(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		want    string
		options []api.PermissionOption
	}{
		{
			name: "MatchesByKind",
			options: []api.PermissionOption{
				{OptionID: "deny", Name: "Deny", Kind: "deny"},
				{OptionID: "opt-42", Name: "Allow once", Kind: "allow_once"},
			},
			want: "opt-42",
		},
		{
			name: "MatchesByOptionID",
			options: []api.PermissionOption{
				{OptionID: "deny", Name: "Deny", Kind: "deny"},
				{OptionID: "allow_once", Name: "Allow once", Kind: "other"},
			},
			want: "allow_once",
		},
		{
			name:    "EmptySliceReturnsEmpty",
			options: nil,
			want:    "",
		},
		{
			name: "NoMatchReturnsEmpty",
			options: []api.PermissionOption{
				{OptionID: "deny", Name: "Deny", Kind: "deny"},
				{OptionID: "always", Name: "Always allow", Kind: "always_allow"},
			},
			want: "",
		},
		{
			name: "PrefersFirstMatch",
			options: []api.PermissionOption{
				{OptionID: "first-allow", Name: "A", Kind: "allow_once"},
				{OptionID: "second-allow", Name: "B", Kind: "allow_once"},
			},
			want: "first-allow",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := FindAllowOnce(tt.options)
			if got != tt.want {
				t.Errorf("FindAllowOnce() = %q, want %q", got, tt.want)
			}
		})
	}
}
