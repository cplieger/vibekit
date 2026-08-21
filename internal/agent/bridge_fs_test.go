package agent

// Tests for bridge_fs.go: fs/read_text_file + fs/write_text_file
// handlers and the shared resolveInsideWorkDir boundary check.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// --- respondRecorder: captures Respond calls from the fakeBridge ---

type respondingBridge struct {
	*fakeBridge

	done     chan struct{}
	response struct {
		result any
		err    error
		id     int64
	}
	respMu sync.Mutex
}

func newRespondingBridge() *respondingBridge {
	return &respondingBridge{
		fakeBridge: newFakeBridge(),
		done:       make(chan struct{}, 1),
	}
}

func (b *respondingBridge) Respond(_ context.Context, id int64, result any, err error) error {
	b.respMu.Lock()
	b.response.id = id
	b.response.result = result
	b.response.err = err
	b.respMu.Unlock()
	select {
	case b.done <- struct{}{}:
	default:
	}
	return nil
}

// hubForFSTest returns a runtime wired with a respondingBridge so the fs
// handlers can complete through h.inbound.respondBridge.
func hubForFSTest(t *testing.T, workDir string) (*Runtime, *respondingBridge) {
	t.Helper()
	cs := newFakeChatStore()
	br := newRespondingBridge()
	factory := func() ACPBridge { return br }
	h := New(t.Context(), workDir, factory, cs)
	cs.Bus = h
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })
	sb, err := h.coord.OpenBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("getOrCreateBridge: %v", err)
	}
	sb.bridge = br // ensure the map entry uses our respondingBridge
	h.bridge.mgr.mu.Lock()
	h.bridge.mgr.bridges["c1"] = sb
	h.bridge.mgr.mu.Unlock()
	return h, br
}

// --- resolveInsideWorkDir ---

func TestResolveInsideWorkDir(t *testing.T) {
	t.Parallel()

	type tc struct {
		setupFn  func(t *testing.T, work string)
		wantPath func(work string) string
		name     string
		input    string
		wantErr  bool
		skipWin  bool
	}

	cases := []tc{
		{
			name:    "rejects empty path",
			input:   "",
			wantErr: true,
		},
		{
			name:    "rejects dot-dot escape",
			input:   "../etc/passwd",
			wantErr: true,
		},
		{
			name:  "accepts inside path",
			input: "sub/file.txt",
			wantPath: func(work string) string {
				return filepath.Join(work, "sub", "file.txt")
			},
		},
		{
			name:    "follows symlink inside workdir",
			skipWin: true,
			setupFn: func(t *testing.T, work string) {
				target := filepath.Join(work, "real.txt")
				if err := os.WriteFile(target, []byte("hello"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(work, "link.txt")); err != nil {
					t.Fatal(err)
				}
			},
			input: "link.txt",
			wantPath: func(work string) string {
				return filepath.Join(work, "real.txt")
			},
		},
		{
			name:    "rejects symlink escaping workdir",
			skipWin: true,
			setupFn: func(t *testing.T, work string) {
				outside := t.TempDir()
				outsideFile := filepath.Join(outside, "secret.txt")
				if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outsideFile, filepath.Join(work, "escape.txt")); err != nil {
					t.Fatal(err)
				}
			},
			input:   "escape.txt",
			wantErr: true,
		},
		{
			name:    "rejects symlinked parent escaping workdir",
			skipWin: true,
			setupFn: func(t *testing.T, work string) {
				outside := t.TempDir()
				if err := os.Symlink(outside, filepath.Join(work, "escape-dir")); err != nil {
					t.Fatal(err)
				}
			},
			input:   "escape-dir/new.txt",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.skipWin && runtime.GOOS == "windows" {
				t.Skip("symlinks need admin on windows")
			}
			work := t.TempDir()
			if tc.setupFn != nil {
				tc.setupFn(t, work)
			}
			h, _ := hubForFSTest(t, work)
			got, err := h.lifecycle.resolveInsideWorkDir(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantPath != nil {
				if want := tc.wantPath(work); got != want {
					t.Errorf("got %q, want %q", got, want)
				}
			}
		})
	}
}

// --- Read handler ---

func TestRespondFSRead_Success(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "hello.txt"), []byte("hi\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, work)
	id := int64(1)
	msg := &vibekit.RPCResponse{
		ID:     &id,
		Method: vibekit.MethodFSRead,
		Params: mustJSON(t, map[string]any{"path": "hello.txt"}),
	}
	h.inbound.respondFSRead(t.Context(), "c1", msg)
	<-br.done

	res, ok := br.response.result.(map[string]any)
	if !ok || res["content"] != "hi\nworld\n" {
		t.Errorf("unexpected response: %+v err=%v", br.response.result, br.response.err)
	}
}

func TestRespondFSRead_MissingPath(t *testing.T) {
	h, br := hubForFSTest(t, t.TempDir())
	id := int64(2)
	msg := &vibekit.RPCResponse{
		ID:     &id,
		Method: vibekit.MethodFSRead,
		Params: mustJSON(t, map[string]any{}),
	}
	h.inbound.respondFSRead(t.Context(), "c1", msg)
	<-br.done

	if br.response.err == nil {
		t.Error("expected error for missing path")
	}
}

func TestRespondFSRead_LineLimitWindow(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "file.txt"), []byte("a\nb\nc\nd\ne\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, work)
	id := int64(3)
	msg := &vibekit.RPCResponse{
		ID:     &id,
		Method: vibekit.MethodFSRead,
		Params: mustJSON(t, map[string]any{"path": "file.txt", "line": 2, "limit": 2}),
	}
	h.inbound.respondFSRead(t.Context(), "c1", msg)
	<-br.done

	res, ok := br.response.result.(map[string]any)
	if !ok || res["content"] != "b\nc\n" {
		t.Errorf("unexpected response: %+v", br.response.result)
	}
}

func TestRespondFSRead_SizeCapRejects(t *testing.T) {
	work := t.TempDir()
	huge := make([]byte, fsReadCap+1)
	if err := os.WriteFile(filepath.Join(work, "big.txt"), huge, 0o644); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, work)
	id := int64(4)
	msg := &vibekit.RPCResponse{
		ID:     &id,
		Method: vibekit.MethodFSRead,
		Params: mustJSON(t, map[string]any{"path": "big.txt"}),
	}
	h.inbound.respondFSRead(t.Context(), "c1", msg)
	<-br.done

	// The sentinel, not a substring of the message: errCapExceeded is what
	// respondFSError classifies as a routine denial, so matching on it is what
	// pins the log level too.
	if !errors.Is(br.response.err, errCapExceeded) {
		t.Errorf("respondFSWrite(%d bytes) response.err = %v, want %v", fsWriteCap+1, br.response.err, errCapExceeded)
	}
}

// The cap is the largest write ACCEPTED, not the first one refused. A file
// exactly at the limit is an ordinary write the agent has no way to shrink, so
// shaving a byte off the boundary refuses work that should have gone to disk.
func TestRespondFSWrite_AcceptsAWriteExactlyAtTheCap(t *testing.T) {
	work := t.TempDir()
	h, br := hubForFSTest(t, work)
	id := int64(9)
	msg := &vibekit.RPCResponse{
		ID:     &id,
		Method: vibekit.MethodFSWrite,
		Params: mustJSON(t, map[string]any{"path": "at-cap.txt", "content": strings.Repeat("x", fsWriteCap)}),
	}
	h.inbound.respondFSWrite(t.Context(), "c1", msg)
	select {
	case <-br.done:
	case <-time.After(10 * time.Second):
		t.Fatal("respondFSWrite did not respond")
	}

	br.respMu.Lock()
	gotErr := br.response.err
	br.respMu.Unlock()
	if gotErr != nil {
		t.Fatalf("respondFSWrite(%d bytes) response.err = %v, want nil at the cap", fsWriteCap, gotErr)
	}
	data, err := os.ReadFile(filepath.Join(work, "at-cap.txt"))
	if err != nil {
		t.Fatalf("read back the at-cap write: %v", err)
	}
	if len(data) != fsWriteCap {
		t.Errorf("wrote %d bytes, want %d", len(data), fsWriteCap)
	}
}

// --- Write handler ---

func TestRespondFSWrite_Success(t *testing.T) {
	work := t.TempDir()
	h, br := hubForFSTest(t, work)
	id := int64(5)
	msg := &vibekit.RPCResponse{
		ID:     &id,
		Method: vibekit.MethodFSWrite,
		Params: mustJSON(t, map[string]any{"path": "out.txt", "content": "written"}),
	}
	h.inbound.respondFSWrite(t.Context(), "c1", msg)
	<-br.done

	if br.response.err != nil {
		t.Fatalf("unexpected error: %v", br.response.err)
	}
	data, err := os.ReadFile(filepath.Join(work, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "written" {
		t.Errorf("got %q, want 'written'", string(data))
	}
}

func TestRespondFSWrite_CreatesParentDirs(t *testing.T) {
	work := t.TempDir()
	h, br := hubForFSTest(t, work)
	id := int64(6)
	msg := &vibekit.RPCResponse{
		ID:     &id,
		Method: vibekit.MethodFSWrite,
		Params: mustJSON(t, map[string]any{"path": "nested/deeply/out.txt", "content": "ok"}),
	}
	h.inbound.respondFSWrite(t.Context(), "c1", msg)
	<-br.done

	if br.response.err != nil {
		t.Fatalf("unexpected error: %v", br.response.err)
	}
	if _, err := os.Stat(filepath.Join(work, "nested", "deeply", "out.txt")); err != nil {
		t.Errorf("file not created: %v", err)
	}
}

func TestRespondFSWrite_RejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need admin on windows")
	}
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "target.txt")
	if err := os.WriteFile(outsideFile, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	link := filepath.Join(work, "escape.txt")
	if err := os.Symlink(outsideFile, link); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, work)
	id := int64(7)
	msg := &vibekit.RPCResponse{
		ID:     &id,
		Method: vibekit.MethodFSWrite,
		Params: mustJSON(t, map[string]any{"path": "escape.txt", "content": "HIJACKED"}),
	}
	h.inbound.respondFSWrite(t.Context(), "c1", msg)
	<-br.done

	if br.response.err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
	// The outside file must be unchanged.
	data, err := os.ReadFile(outsideFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Errorf("outside file was written through symlink: %q", string(data))
	}
}

func TestRespondFSWrite_CapRejects(t *testing.T) {
	work := t.TempDir()
	h, br := hubForFSTest(t, work)
	id := int64(8)
	huge := strings.Repeat("x", fsWriteCap+1)
	msg := &vibekit.RPCResponse{
		ID:     &id,
		Method: vibekit.MethodFSWrite,
		Params: mustJSON(t, map[string]any{"path": "out.txt", "content": huge}),
	}
	h.inbound.respondFSWrite(t.Context(), "c1", msg)
	<-br.done

	if br.response.err == nil || !strings.Contains(br.response.err.Error(), "cap") {
		t.Errorf("expected cap error, got %v", br.response.err)
	}
}

// --- Dispatch ---

func TestHandleFSRequest_ReturnsFalseForNonFSMethod(t *testing.T) {
	h, _ := hubForFSTest(t, t.TempDir())
	id := int64(9)
	msg := &vibekit.RPCResponse{ID: &id, Method: "session/update", Params: json.RawMessage(`{}`)}
	if h.inbound.handleFSRequest(t.Context(), "c1", msg) {
		t.Error("handleFSRequest claimed non-fs method")
	}
}

func TestHandleFSRequest_DispatchesFSRead(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "hi.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, work)
	id := int64(10)
	msg := &vibekit.RPCResponse{
		ID:     &id,
		Method: vibekit.MethodFSRead,
		Params: mustJSON(t, map[string]any{"path": "hi.txt"}),
	}
	if !h.inbound.handleFSRequest(t.Context(), "c1", msg) {
		t.Fatal("handleFSRequest returned false for fs/read_text_file")
	}
	<-br.done
	if br.response.err != nil {
		t.Errorf("unexpected error: %v", br.response.err)
	}
}

// --- Folded mutant-killing coverage (read/write boundary + respond) ---

// A read of a missing file must respond with a graceful not-found error
// rather than dereferencing the nil FileInfo from the failed stat.
func TestRespondFSRead_MissingFileRespondsGracefully(t *testing.T) {
	h, br := hubForFSTest(t, t.TempDir())
	id := int64(901)
	msg := &vibekit.RPCResponse{
		ID:     &id,
		Method: vibekit.MethodFSRead,
		Params: mustJSON(t, map[string]any{"path": "ghost.txt"}),
	}
	h.inbound.respondFSRead(t.Context(), "c1", msg)
	<-br.done
	if br.response.err == nil {
		t.Errorf("respondFSRead(missing file) err = nil, want a not-found error")
	}
}

// A file of exactly fsReadCap bytes reads successfully: the size guard
// is a strict `>`, so size==cap is allowed.
func TestRespondFSRead_ExactCapBoundarySucceeds(t *testing.T) {
	work := t.TempDir()
	data := bytes.Repeat([]byte("a"), fsReadCap)
	if err := os.WriteFile(filepath.Join(work, "exact.txt"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, work)
	id := int64(902)
	msg := &vibekit.RPCResponse{
		ID:     &id,
		Method: vibekit.MethodFSRead,
		Params: mustJSON(t, map[string]any{"path": "exact.txt"}),
	}
	h.inbound.respondFSRead(t.Context(), "c1", msg)
	<-br.done
	if br.response.err != nil {
		t.Fatalf("respondFSRead(exact cap) err = %v, want nil (boundary is strict >)", br.response.err)
	}
	res, ok := br.response.result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map[string]any", br.response.result)
	}
	content, _ := res["content"].(string)
	if len(content) != fsReadCap {
		t.Errorf("content length = %d, want %d", len(content), fsReadCap)
	}
}

// respondFSWrite reports the write error when the target can't be
// written (a directory at the target path) and otherwise responds with
// the success result map.
func TestRespondFSWrite_ErrCheck(t *testing.T) {
	t.Run("write_failure_reports_error", func(t *testing.T) {
		work := t.TempDir()
		if err := os.Mkdir(filepath.Join(work, "dir-target"), 0o755); err != nil {
			t.Fatal(err)
		}
		h, br := hubForFSTest(t, work)
		id := int64(8137)
		msg := &vibekit.RPCResponse{
			ID:     &id,
			Method: vibekit.MethodFSWrite,
			Params: mustJSON(t, map[string]any{"path": "dir-target", "content": "x"}),
		}
		h.inbound.respondFSWrite(t.Context(), "c1", msg)
		select {
		case <-br.done:
		case <-time.After(3 * time.Second):
			t.Fatal("respondFSWrite did not respond")
		}
		br.respMu.Lock()
		gotErr := br.response.err
		br.respMu.Unlock()
		if gotErr == nil {
			t.Errorf("respondFSWrite(dir target) response.err = nil, want non-nil")
		}
	})

	t.Run("write_success_reports_result_map", func(t *testing.T) {
		work := t.TempDir()
		h, br := hubForFSTest(t, work)
		id := int64(8138)
		msg := &vibekit.RPCResponse{
			ID:     &id,
			Method: vibekit.MethodFSWrite,
			Params: mustJSON(t, map[string]any{"path": "ok.txt", "content": "hello"}),
		}
		h.inbound.respondFSWrite(t.Context(), "c1", msg)
		select {
		case <-br.done:
		case <-time.After(3 * time.Second):
			t.Fatal("respondFSWrite did not respond")
		}
		br.respMu.Lock()
		gotErr := br.response.err
		gotRes := br.response.result
		br.respMu.Unlock()
		if gotErr != nil {
			t.Fatalf("respondFSWrite(success) response.err = %v, want nil", gotErr)
		}
		if _, ok := gotRes.(map[string]any); !ok {
			t.Errorf("respondFSWrite(success) result = %T, want map[string]any", gotRes)
		}
		data, rErr := os.ReadFile(filepath.Join(work, "ok.txt"))
		if rErr != nil || string(data) != "hello" {
			t.Errorf("file content = %q, err=%v, want %q", string(data), rErr, "hello")
		}
	})
}

// respondBridge stays log-silent when the bridge Respond succeeds.
func TestRespondBridge_NoErrorLogOnSuccess(t *testing.T) {
	h, _ := hubForFSTest(t, t.TempDir())
	id := int64(903)
	msg := &vibekit.RPCResponse{ID: &id, Method: vibekit.MethodFSRead, Params: mustJSON(t, map[string]any{})}

	logs := captureLogs(t)
	h.inbound.respondBridge(t.Context(), "c1", msg, map[string]any{"ok": true}, nil)
	if got := logs.String(); strings.Contains(got, "fs response write failed") {
		t.Errorf("unexpected respond-failure error log on success: %s", got)
	}
}

// droppingBridge refuses every Respond, standing in for a bridge whose stdin
// has already gone away.
type droppingBridge struct {
	*fakeBridge
}

func (b *droppingBridge) Respond(_ context.Context, _ int64, _ any, _ error) error {
	return errors.New("bridge stdin closed")
}

// TestRespondHelpersReportADroppedWrite pins the diagnostic on the two ACP
// response helpers the terminal handlers answer through. A response the bridge
// refused leaves the agent waiting until its own Call times out, and this log
// line is the only place that is visible — so it has to be emitted when the
// write fails, and stay absent when it succeeds.
//
// No t.Parallel: captureLogs swaps the process-global slog default.
func TestRespondHelpersReportADroppedWrite(t *testing.T) {
	id := int64(904)
	msg := &vibekit.RPCResponse{ID: &id, Method: methodTermOutput}

	t.Run("respondOK_write_refused", func(t *testing.T) {
		h := hubWithBridge(t, t.TempDir(), &droppingBridge{fakeBridge: newFakeBridge()})
		logs := captureLogs(t)
		respondOK(t.Context(), h.bridge.mgr, "c1", msg, map[string]any{"ok": true})
		if got := logs.String(); !strings.Contains(got, "respondOK: bridge respond failed") {
			t.Errorf("respondOK(refused write) logged %q, want a respond-failed line", got)
		}
	})

	t.Run("respondOK_write_accepted", func(t *testing.T) {
		h := hubWithBridge(t, t.TempDir(), newFakeBridge())
		logs := captureLogs(t)
		respondOK(t.Context(), h.bridge.mgr, "c1", msg, map[string]any{"ok": true})
		if got := logs.String(); strings.Contains(got, "respondOK: bridge respond failed") {
			t.Errorf("respondOK(accepted write) logged %q, want no respond-failed line", got)
		}
	})

	t.Run("respondErr_write_refused", func(t *testing.T) {
		h := hubWithBridge(t, t.TempDir(), &droppingBridge{fakeBridge: newFakeBridge()})
		logs := captureLogs(t)
		respondErr(t.Context(), h.bridge.mgr, "c1", msg, "terminal not found")
		if got := logs.String(); !strings.Contains(got, "respondErr: bridge respond failed") {
			t.Errorf("respondErr(refused write) logged %q, want a respond-failed line", got)
		}
	})

	t.Run("respondErr_write_accepted", func(t *testing.T) {
		h := hubWithBridge(t, t.TempDir(), newFakeBridge())
		logs := captureLogs(t)
		respondErr(t.Context(), h.bridge.mgr, "c1", msg, "terminal not found")
		if got := logs.String(); strings.Contains(got, "respondErr: bridge respond failed") {
			t.Errorf("respondErr(accepted write) logged %q, want no respond-failed line", got)
		}
	})
}
