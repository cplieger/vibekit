package ids

import (
	"math"
	"testing"
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
