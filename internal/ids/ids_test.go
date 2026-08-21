package ids

import (
	"math"
	"testing"

	"pgregory.net/rapid"
)

func TestNew(t *testing.T) {
	tests := []struct {
		name    string
		byteLen int
		enc     Encoding
		wantLen int // expected output string length: ceil(byteLen*8/5)
	}{
		{"HexUpper 8 bytes", 8, HexUpper, 13},
		{"HexUpper 16 bytes", 16, HexUpper, 26},
		{"HexUpper 1 byte", 1, HexUpper, 2},
		{"StdLower 8 bytes", 8, StdLower, 13},
		{"StdLower 16 bytes", 16, StdLower, 26},
		{"StdLower 1 byte", 1, StdLower, 2},
		{"HexUpper 5 bytes exact", 5, HexUpper, 8},
		{"StdLower 10 bytes", 10, StdLower, 16},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := New(tt.byteLen, tt.enc)
			if len(got) != tt.wantLen {
				t.Errorf("New(%d, %d) length = %d, want %d", tt.byteLen, tt.enc, len(got), tt.wantLen)
			}
		})
	}
}

func TestNew_OutputLengthInvariant(t *testing.T) {
	for byteLen := 1; byteLen <= 32; byteLen++ {
		expectedLen := int(math.Ceil(float64(byteLen) * 8 / 5))
		for _, enc := range []Encoding{HexUpper, StdLower} {
			got := New(byteLen, enc)
			if len(got) != expectedLen {
				t.Errorf("byteLen=%d enc=%d: len=%d, want ceil(%d*8/5)=%d",
					byteLen, enc, len(got), byteLen, expectedLen)
			}
		}
	}
}

func TestNew_HexUpperCharset(t *testing.T) {
	id := New(16, HexUpper)
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'A' || c > 'V') {
			t.Errorf("HexUpper output contains invalid char %q in %q", string(c), id)
		}
	}
}

func TestNew_StdLowerCharset(t *testing.T) {
	id := New(16, StdLower)
	for _, c := range id {
		if (c < 'a' || c > 'z') && (c < '2' || c > '7') {
			t.Errorf("StdLower output contains invalid char %q in %q", string(c), id)
		}
	}
}

func TestNew_Uniqueness(t *testing.T) {
	seen := make(map[string]struct{}, 100)
	for i := range 100 {
		id := New(16, HexUpper)
		if _, ok := seen[id]; ok {
			t.Fatalf("duplicate id after %d iterations: %s", i, id)
		}
		seen[id] = struct{}{}
	}
}

func TestNew_PanicOnBadEncoding(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for unknown encoding, got none")
		}
	}()
	New(8, Encoding(99))
}

func TestNewMessageID_RapidInvariants(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		id := NewMessageID()

		// Length must be 36 (standard UUID format).
		if len(id) != 36 {
			t.Fatalf("len=%d, want 36", len(id))
		}

		// Dash positions: 8, 13, 18, 23.
		for _, pos := range []int{8, 13, 18, 23} {
			if id[pos] != '-' {
				t.Fatalf("id[%d]=%c, want '-'", pos, id[pos])
			}
		}

		// Version nibble (position 14) must be '7'.
		if id[14] != '7' {
			t.Fatalf("version nibble=%c, want '7'", id[14])
		}

		// Variant nibble (position 19) must be 8, 9, a, or b.
		v := id[19]
		if v != '8' && v != '9' && v != 'a' && v != 'b' {
			t.Fatalf("variant nibble=%c, want 8/9/a/b", v)
		}

		// All non-dash characters must be hex digits.
		for i, c := range id {
			if i == 8 || i == 13 || i == 18 || i == 23 {
				continue
			}
			//nolint:staticcheck // QF1001: explicit two-range check reads more naturally than the De Morgan form
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				t.Fatalf("non-hex char %c at position %d", c, i)
			}
		}
	})
}

func TestNewMessageID_TimeOrdering(t *testing.T) {
	// No sleep: uuid.NewV7 packs a 12-bit sub-millisecond fraction beside the
	// timestamp and takes a mutex to guarantee the order, so consecutive ids
	// are strictly increasing even inside one millisecond. The hand-rolled
	// generator this replaced had millisecond granularity and random low bits,
	// so 5007 of 9999 consecutive pairs were out of order and the only
	// assertion available was one across a 2ms sleep.
	const n = 1000
	prev := NewMessageID()
	for i := 1; i < n; i++ {
		id := NewMessageID()
		if id <= prev {
			t.Fatalf("ids not strictly increasing at call %d: %s <= %s", i, id, prev)
		}
		prev = id
	}
}

func TestNewMessageID_Uniqueness(t *testing.T) {
	seen := make(map[string]struct{}, 10000)
	for i := range 10000 {
		id := NewMessageID()
		if _, ok := seen[id]; ok {
			t.Fatalf("duplicate id after %d iterations: %s", i, id)
		}
		seen[id] = struct{}{}
	}
}
