package command

import (
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

func TestValidRequestID(t *testing.T) {
	cases := []struct {
		name string
		id   string
		want bool
	}{
		{"empty is valid", "", true},
		{"simple alphanumeric", "abc123", true},
		{"with dots and dashes", "req-1.2_3:4", true},
		{"uuid format", "550e8400-e29b-41d4-a716-446655440000", true},
		{"too long", strings.Repeat("a", 129), false},
		{"max length", strings.Repeat("a", 128), true},
		{"contains space", "req id", false},
		{"contains slash", "req/id", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := validRequestID(tc.id)
			if got != tc.want {
				t.Errorf("validRequestID(%q) = %v, want %v", tc.id, got, tc.want)
			}
		})
	}
}

func TestValidMessageID(t *testing.T) {
	cases := []struct {
		name string
		id   string
		want bool
	}{
		{"empty is invalid", "", false},
		{"simple alphanumeric", "msg123", true},
		{"uuid format", "550e8400-e29b-41d4-a716-446655440000", true},
		{"with allowed chars", "msg_1.2-3:4", true},
		{"too long", strings.Repeat("x", 129), false},
		{"max length", strings.Repeat("x", 128), true},
		{"contains space", "msg id", false},
		{"contains slash", "msg/id", false},
		{"contains newline", "msg\nid", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ValidMessageID(tc.id)
			if got != tc.want {
				t.Errorf("ValidMessageID(%q) = %v, want %v", tc.id, got, tc.want)
			}
		})
	}
}

func TestValidChatID(t *testing.T) {
	cases := []struct {
		name string
		id   api.ChatID
		want bool
	}{
		{"empty is invalid", "", false},
		{"valid uuid", "550e8400-e29b-41d4-a716-446655440000", true},
		{"alphanumeric", "chat123", true},
		{"with dashes", "chat-id-1", true},
		{"too long", api.ChatID(strings.Repeat("a", 129)), false},
		{"contains space", "chat id", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := validChatID(tc.id)
			if got != tc.want {
				t.Errorf("validChatID(%q) = %v, want %v", tc.id, got, tc.want)
			}
		})
	}
}

func FuzzValidRequestID(f *testing.F) {
	f.Add("")
	f.Add("abc-123_456:789.0")
	f.Add(strings.Repeat("x", 128))
	f.Add(strings.Repeat("x", 129))
	f.Add("has space")
	f.Fuzz(func(t *testing.T, id string) {
		// Must not panic.
		_ = validRequestID(id)
	})
}

func FuzzValidMessageID(f *testing.F) {
	f.Add("")
	f.Add("msg-123_456:789.0")
	f.Add(strings.Repeat("x", 128))
	f.Add(strings.Repeat("x", 129))
	f.Add("has space")
	f.Fuzz(func(t *testing.T, id string) {
		// Must not panic.
		_ = ValidMessageID(id)
	})
}

func FuzzValidChatID(f *testing.F) {
	f.Add("")
	f.Add("abc-123_456")
	f.Add(strings.Repeat("a", 128))
	f.Add(strings.Repeat("a", 129))
	f.Add("has space")
	f.Add("has/slash")
	f.Add("../traversal")
	f.Fuzz(func(t *testing.T, id string) {
		result := validChatID(api.ChatID(id))
		// Path separators must always be rejected.
		if strings.ContainsAny(id, "/\\") && result {
			t.Errorf("validChatID(%q) = true, contains path separator", id)
		}
		// Over 128 bytes must be rejected.
		if len(id) > 128 && result {
			t.Errorf("validChatID(%q) = true, len=%d > 128", id, len(id))
		}
		// Empty must be rejected.
		if id == "" && result {
			t.Error("validChatID(\"\") = true, want false")
		}
	})
}
