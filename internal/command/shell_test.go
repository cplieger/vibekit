package command

import (
	"bytes"
	"testing"
)

func TestShellCappedBuffer(t *testing.T) {
	tests := []struct {
		name      string
		writes    [][]byte
		wantLen   int
		wantTrunc bool
	}{
		{
			name:      "under cap",
			writes:    [][]byte{[]byte("hello")},
			wantLen:   5,
			wantTrunc: false,
		},
		{
			name:      "exactly at cap",
			writes:    [][]byte{bytes.Repeat([]byte("x"), ShellOutputCap)},
			wantLen:   ShellOutputCap,
			wantTrunc: false,
		},
		{
			name:      "single write over cap",
			writes:    [][]byte{bytes.Repeat([]byte("x"), ShellOutputCap+100)},
			wantLen:   ShellOutputCap,
			wantTrunc: true,
		},
		{
			name: "multiple writes accumulating past cap",
			writes: [][]byte{
				bytes.Repeat([]byte("a"), ShellOutputCap-10),
				bytes.Repeat([]byte("b"), 20),
			},
			wantLen:   ShellOutputCap,
			wantTrunc: true,
		},
		{
			name: "write after cap already reached",
			writes: [][]byte{
				bytes.Repeat([]byte("a"), ShellOutputCap),
				[]byte("extra"),
			},
			wantLen:   ShellOutputCap,
			wantTrunc: true,
		},
		{
			// An empty write when the buffer is exactly full must still
			// mark Truncated (remaining == 0 takes the cap-reached branch,
			// not the len(p) <= remaining fast path).
			name: "empty write at exactly-full buffer",
			writes: [][]byte{
				bytes.Repeat([]byte("x"), ShellOutputCap),
				{},
			},
			wantLen:   ShellOutputCap,
			wantTrunc: true,
		},
		{
			name:      "empty write",
			writes:    [][]byte{{}},
			wantLen:   0,
			wantTrunc: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf ShellCappedBuffer
			for _, w := range tc.writes {
				n, err := buf.Write(w)
				if err != nil {
					t.Fatalf("Write returned error: %v", err)
				}
				if n != len(w) {
					t.Fatalf("Write returned n=%d, want %d", n, len(w))
				}
			}
			if buf.Buf.Len() != tc.wantLen {
				t.Errorf("Buf.Len() = %d, want %d", buf.Buf.Len(), tc.wantLen)
			}
			if buf.Truncated != tc.wantTrunc {
				t.Errorf("Truncated = %v, want %v", buf.Truncated, tc.wantTrunc)
			}
		})
	}
}
