// Package hub shell subsystem provides a single global PTY session with server-side VT parsing.
//
// The shell runs in a real pseudo-terminal (creack/pty) so interactive
// programs (vim, htop, less, tab completion) work correctly. I/O flows
// over a WebSocket at /api/shell/ws using a compact binary wire protocol
// (see github.com/cplieger/vterm/terminal). The server maintains a VT500
// screen buffer (github.com/cplieger/vterm/vt) and sends only changed rows
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
	"net/http"

	"github.com/cplieger/vterm/terminal"
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

// kill shuts down the PTY process.
func (sm *ShellManager) kill() {
	sm.handler.Shutdown()
}
