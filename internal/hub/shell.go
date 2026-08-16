// Package hub shell subsystem provides a single global PTY session with server-side VT parsing.
//
// The shell runs in a real pseudo-terminal (creack/pty) so interactive
// programs (vim, htop, less, tab completion) work correctly. I/O flows
// over a WebSocket at /api/shell/ws using a compact binary wire protocol
// (see github.com/cplieger/web-terminal-engine/v4/terminal). The server maintains a VT500
// screen buffer (github.com/cplieger/web-terminal-engine/v4/vt) and sends only changed rows
// to the client on each flush tick — dramatically reducing bandwidth vs.
// raw-byte streaming, and enabling a lightweight DOM-based renderer on the
// client (no xterm.js dependency).
//
// On reconnect the server replays the full screen snapshot + scrollback
// history so the client can reconstruct terminal state without needing
// the raw byte stream.
//
// Wire protocol (binary WebSocket frames):
//
//	client → server: raw terminal input bytes
//	server → client: binary frames (screen/scroll/resumeAck/modes)
//	client → server: JSON control messages prefixed with 0x00:
//	  {"type":"resize","cols":N,"rows":N}
//
// The 0x00 prefix byte distinguishes control messages from raw input;
// no valid terminal input starts with NUL.
package hub

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/cplieger/web-terminal-engine/v4/terminal"
)

// ShellManager wraps the terminal.Handler to provide the same interface
// the Hub expects (RegisterRoutes, Shutdown). The terminal package
// handles all PTY lifecycle, VT parsing, binary wire encoding, client
// fan-out, and reconnect replay internally.
type ShellManager struct {
	handler *terminal.Handler
}

// NewShellManager creates a ShellManager backed by the terminal package.
// The command is always bash --login; workDir is the initial CWD.
// ctx is accepted for interface compatibility but not used (the terminal
// package manages its own lifecycle via Shutdown).
func NewShellManager(_ context.Context, workDir string) *ShellManager {
	h := terminal.NewHandler([]string{"bash", "--login"}, terminal.WithWorkDir(workDir))
	return &ShellManager{handler: h}
}

// handleShellWS delegates to the terminal handler's WebSocket endpoint.
func (sm *ShellManager) handleShellWS(w http.ResponseWriter, r *http.Request) {
	sm.handler.ServeHTTP(w, r)
}

// handleShellWS is the Hub method registered at /api/shell/ws.
func (h *Hub) handleShellWS(w http.ResponseWriter, r *http.Request) {
	h.shellMgr.handleShellWS(w, r)
}

// kill ends the PTY session and waits for its teardown: reaping the child and
// telling attached clients the process is gone. Its one caller is Hub.Shutdown,
// which runs at process exit, and work still in flight when the process goes is
// work that is lost — so this takes the engine's blocking form rather than Close.
//
// The budget is its own because Hub.Shutdown carries no context (the HTTP
// server's grace is the outer bound on everything around it). It is sized above
// the engine's 5s reap ceiling so an expiry means the teardown genuinely hung.
func (sm *ShellManager) kill() {
	const budget = 10 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	if err := sm.handler.Shutdown(ctx); err != nil {
		slog.Warn("shell teardown did not finish", "budget", budget, "error", err)
	}
}
