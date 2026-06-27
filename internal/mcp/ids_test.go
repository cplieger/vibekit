package mcp

import (
	"strings"
	"testing"
)

func TestNewID_FormatAndCharset(t *testing.T) {
	for range 100 {
		id := newID()
		if len(id) != 10 {
			t.Fatalf("newID() len = %d, want 10 (got %q)", len(id), id)
		}
		// base32 lowercase (RFC 4648): a-z and 2-7.
		const allowed = "abcdefghijklmnopqrstuvwxyz234567"
		for _, r := range id {
			if !strings.ContainsRune(allowed, r) {
				t.Errorf("newID() = %q contains %q outside base32-lower charset", id, r)
				break
			}
		}
	}
}

func FuzzParseServerID(f *testing.F) {
	f.Add("")
	f.Add("abc")
	f.Add(string(newID()))
	f.Add("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaX") // 33 chars

	f.Fuzz(func(t *testing.T, raw string) {
		id, err := ParseServerID(raw)
		if err == nil {
			if id == "" {
				t.Fatal("ParseServerID returned empty ID with nil error")
			}
			if len(id) > 32 {
				t.Fatalf("ParseServerID accepted %d-char input (max 32)", len(id))
			}
			// Idempotent: re-parsing a valid ID must succeed.
			id2, err2 := ParseServerID(string(id))
			if err2 != nil {
				t.Fatalf("ParseServerID not idempotent: %v", err2)
			}
			if id2 != id {
				t.Fatalf("ParseServerID not idempotent: %q != %q", id2, id)
			}
		} else {
			if raw == "" || len(raw) > 32 {
				return // expected rejection
			}
		}
	})
}

func TestNewID_UniqueOver10k(t *testing.T) {
	// ~48 bits of entropy means collisions across 10k IDs are
	// vanishingly unlikely (birthday bound ~1/2^28). If this ever
	// flakes, newID has silently lost entropy.
	seen := make(map[string]struct{}, 10000)
	for i := range 10000 {
		id := newID()
		if _, dup := seen[string(id)]; dup {
			t.Fatalf("newID() collision at i=%d: %q", i, id)
		}
		seen[string(id)] = struct{}{}
	}
}

// TestParseServerID_LengthBoundary pins the 32-char length cap: an id of
// exactly 32 chars is accepted (the cap is len > 32, not >=) and a
// 33-char id is rejected.
func TestParseServerID_LengthBoundary(t *testing.T) {
	atMax := strings.Repeat("a", 32)
	id, err := ParseServerID(atMax)
	if err != nil {
		t.Errorf("ParseServerID(32 chars) = err %v, want nil", err)
	}
	if string(id) != atMax {
		t.Errorf("ParseServerID(32 chars) id = %q, want %q", string(id), atMax)
	}
	if _, err := ParseServerID(strings.Repeat("a", 33)); err == nil {
		t.Error("ParseServerID(33 chars) = nil, want too-long error")
	}
}
