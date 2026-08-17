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
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/web-terminal-engine/v4/terminal"
)

// shutdownBudget bounds one PTY teardown. Sized above the engine's 5s reap
// ceiling so an expiry means the teardown genuinely hung rather than that a
// child took its time dying.
const shutdownBudget = 10 * time.Second

// retireHandler shuts a handler down and WAITS, bounded. All three teardown
// paths go through it — the lazy replacement of a spent handler on connect, the
// explicit restart, and Hub.Shutdown — because engine v4's Shutdown blocks
// until the child is reaped and attached clients have been told, so a caller
// that does not wait leaves a process alive past the moment its panel stopped
// showing it. The budget is per-call rather than inherited: Hub.Shutdown
// carries no context, and the other two are user actions with nothing to
// cancel them.
func retireHandler(h *terminal.Handler, why string) {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownBudget)
	defer cancel()
	if err := h.Shutdown(ctx); err != nil {
		slog.Warn("shell teardown did not finish",
			"why", why, "budget", shutdownBudget, "error", err)
	}
}

// ShellManager wraps the terminal.Handler to provide the same interface
// the Hub expects (RegisterRoutes, Shutdown). The terminal package
// handles all PTY lifecycle, VT parsing, binary wire encoding, client
// fan-out, and reconnect replay internally.
//
// The handler is REPLACEABLE, and that is the whole point of this type
// beyond delegation. terminal.Handler is single-use by construction:
// ensureStarted returns early once its `started` flag is set, and nothing
// ever clears it — not Shutdown, not the child exiting. The sibling apps
// never meet that because they run terminal.SessionManager, which builds a
// fresh Handler per session, so a finished session is closed and a new one
// created. vibekit holds ONE handler for the process lifetime, so without a
// swap a user typing `exit` (or a shell that crashes) leaves a panel that can
// never start again until the container is recreated — the wrong failure mode
// for the interface that exists to be there when everything else has failed.
//
// Two paths replace it. `spent` is latched by the process-exit callback and
// consumed lazily on the next connect, which makes `exit` behave the way a web
// terminal should: the shell ends, and reopening the panel gets you a new one.
// restart() is the explicit path, for a shell that is WEDGED rather than
// exited — a stuck foreground process never fires process-exit, so the lazy
// path cannot help there.
type ShellManager struct {
	handler *terminal.Handler
	workDir string
	mu      sync.Mutex
	// spent means the child exited, so this handler can never start another.
	spent bool
}

// NewShellManager creates a ShellManager backed by the terminal package.
// The command is always bash --login; workDir is the initial CWD.
// ctx is accepted for interface compatibility but not used (the terminal
// package manages its own lifecycle via Shutdown).
func NewShellManager(_ context.Context, workDir string) *ShellManager {
	sm := &ShellManager{workDir: workDir}
	sm.handler = sm.newHandler()
	return sm
}

// newHandler builds a fresh PTY handler for this manager's workDir.
func (sm *ShellManager) newHandler() *terminal.Handler {
	return terminal.NewHandler([]string{"bash", "--login"},
		terminal.WithWorkDir(sm.workDir),
		// Latch the handler as spent. No client notification rides this: the
		// engine closes the socket when the child exits, the client's own
		// backoff loop reconnects, and that connect lands on a fresh handler
		// via current(). So `exit` self-heals into a new prompt, which is what
		// a browser terminal should do, and needs no new SSE event to say so.
		terminal.WithOnProcessExit(func(err error) {
			sm.mu.Lock()
			sm.spent = true
			sm.mu.Unlock()
			slog.Info("shell: child exited", "error", err)
		}),
	)
}

// current returns the handler to serve this connection with, replacing a spent
// one first. Returns the old handler for the caller to shut down after the lock
// is released: Shutdown can run the process-exit callback synchronously, which
// takes this same mutex.
func (sm *ShellManager) current() (h, retire *terminal.Handler) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.spent {
		retire = sm.handler
		sm.handler = sm.newHandler()
		sm.spent = false
	}
	return sm.handler, retire
}

// restart replaces the PTY unconditionally. For a wedged shell, where the child
// is alive and stuck so the process-exit latch never fires.
func (sm *ShellManager) restart() {
	sm.mu.Lock()
	retire := sm.handler
	sm.handler = sm.newHandler()
	sm.spent = false
	sm.mu.Unlock()
	retireHandler(retire, "restart")
	slog.Info("shell: restarted")
}

// handleShellWS delegates to the terminal handler's WebSocket endpoint.
func (sm *ShellManager) handleShellWS(w http.ResponseWriter, r *http.Request) {
	h, retire := sm.current()
	if retire != nil {
		retireHandler(retire, "spent")
	}
	h.ServeHTTP(w, r)
}

// handleShellWS is the Hub method registered at /api/shell/ws.
func (h *Hub) handleShellWS(w http.ResponseWriter, r *http.Request) {
	h.shellMgr.handleShellWS(w, r)
}

// handleShellRestart kills the PTY and installs a fresh one. POST because it
// destroys running processes; the client confirms before calling it.
func (h *Hub) handleShellRestart(w http.ResponseWriter, _ *http.Request) {
	h.shellMgr.restart()
	api.Ok(w)
}

// kill ends the PTY session and waits for its teardown: reaping the child and
// telling attached clients the process is gone. Its one caller is Hub.Shutdown,
// which runs at process exit, and work still in flight then is work that is
// lost, so this takes the engine's blocking form rather than a bare close.
//
// The handler is read under the mutex because restart() can swap it: killing
// the field directly would race a restart and could tear down the replacement
// while leaving the one it replaced running.
func (sm *ShellManager) kill() {
	sm.mu.Lock()
	h := sm.handler
	sm.mu.Unlock()
	retireHandler(h, "shutdown")
}
