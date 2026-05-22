package hub

import (
	"context"
	"runtime"
	"testing"
)

func TestParseSignal_Table(t *testing.T) {
	// Exercise the unix parseSignal directly. The non-unix build of this
	// file has its own parseSignal that returns (0, false) for everything;
	// covered implicitly by the shell-level tests.
	tests := []struct {
		name string
		ok   bool
	}{
		{"SIGINT", true},
		{"SIGTERM", true},
		{"SIGQUIT", true},
		{"SIGKILL", false}, // not allowlisted (would be unblockable)
		{"SIGHUP", false},
		{"", false},
		{"sigint", false}, // case-sensitive on purpose
		{"INT", false},    // without SIG prefix
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := parseSignal(tt.name)
			if ok != tt.ok {
				t.Errorf("parseSignal(%q) ok = %v, want %v", tt.name, ok, tt.ok)
			}
		})
	}
}

func TestScrollback_Empty(t *testing.T) {
	sess := &shellSession{
		scrollback: newByteRing(64),
	}
	got := sess.getScrollback()
	if len(got) != 0 {
		t.Errorf("empty scrollback returned %d bytes", len(got))
	}
}

func TestScrollback_PartialFill(t *testing.T) {
	sess := &shellSession{
		scrollback: newByteRing(64),
	}
	sess.appendScrollback([]byte("hello"))
	got := sess.getScrollback()
	if string(got) != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestScrollback_Wrap(t *testing.T) {
	sess := &shellSession{
		scrollback: newByteRing(8),
	}
	sess.appendScrollback([]byte("ABCDEFGH")) // fills exactly
	sess.appendScrollback([]byte("IJ"))       // wraps: overwrites A,B
	got := sess.getScrollback()
	if string(got) != "CDEFGHIJ" {
		t.Errorf("got %q, want %q", got, "CDEFGHIJ")
	}
}

func TestScrollback_MultiWrap(t *testing.T) {
	sess := &shellSession{
		scrollback: newByteRing(4),
	}
	// Write more than 2x the buffer size.
	sess.appendScrollback([]byte("ABCDEFGHIJ"))
	got := sess.getScrollback()
	// Only the last 4 bytes survive.
	if string(got) != "GHIJ" {
		t.Errorf("got %q, want %q", got, "GHIJ")
	}
}

func TestKillShell_NoShellIsOK(t *testing.T) {
	h, _, _ := newTestHub()
	// Should not panic.
	h.shellMgr.kill()
}

func TestStartShell_Unix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PTY not supported on windows")
	}
	h, _, _ := newTestHub()
	h.shellMgr = NewShellManager(context.Background(), "/tmp")
	sess := h.shellMgr.start()
	if sess == nil {
		// PTY may fail in restricted environments (CI, WSL, containers
		// without /dev/ptmx). The scrollback and signal tests cover the
		// logic; this test is for integration only.
		t.Skip("startShell returned nil (PTY unavailable in this environment)")
	}
	defer h.shellMgr.kill()

	h.shellMgr.mu.Lock()
	alive := h.shellMgr.session != nil
	h.shellMgr.mu.Unlock()
	if !alive {
		t.Error("shell not stored on hub")
	}
}

func FuzzAppendScrollback(f *testing.F) {
	f.Add([]byte("hello"))
	f.Add([]byte("ABCDEFGHIJ"))
	f.Add(make([]byte, 128))

	f.Fuzz(func(t *testing.T, data []byte) {
		const bufSize = 64
		sess := &shellSession{scrollback: newByteRing(bufSize)}

		// Write in random-sized chunks.
		chunkSize := 7
		var totalWritten []byte
		for i := 0; i < len(data); i += chunkSize {
			end := min(i+chunkSize, len(data))
			sess.appendScrollback(data[i:end])
			totalWritten = append(totalWritten, data[i:end]...)
		}

		got := sess.getScrollback()
		// Invariant: getScrollback returns the last min(total_written, bufSize) bytes.
		wantLen := min(len(totalWritten), bufSize)
		if len(got) != wantLen {
			t.Fatalf("getScrollback len=%d, want %d", len(got), wantLen)
		}
		// Content must be the tail of totalWritten.
		tail := totalWritten[len(totalWritten)-wantLen:]
		if string(got) != string(tail) {
			t.Fatalf("getScrollback content mismatch:\n got: %q\nwant: %q", got, tail)
		}
	})
}
