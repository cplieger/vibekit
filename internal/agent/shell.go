// Shell subsystem: a single global PTY session with server-side VT parsing
// over a WebSocket at /api/shell/ws (github.com/cplieger/web-terminal-engine/v5).
//
// Client control messages are JSON prefixed with 0x00 (e.g. resize); the
// prefix distinguishes them from raw input, since no valid terminal input
// starts with NUL.

package agent

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/cplieger/web-terminal-engine/v5/terminal"
	"github.com/cplieger/webhttp/v2"
)

// shutdownBudget bounds one PTY teardown. Sized above the engine's 5s reap
// ceiling so an expiry means the teardown genuinely hung rather than that a
// child took its time dying.
const shutdownBudget = 10 * time.Second

// retireHandler shuts a handler down and WAITS, bounded. All three teardown
// paths go through it — lazy replacement, explicit restart, Runtime.Shutdown —
// because engine v4's Shutdown blocks until the child is reaped, so a caller
// that does not wait leaves a process alive past its panel.
//
// shutdownBudget caps ctx: Shutdown passes the one shutdown grace, while the
// two user actions have nothing to cancel them and pass Background. A spent
// ctx still SIGNALS — engine Shutdown closes first and waits second.
func retireHandler(ctx context.Context, h *terminal.Handler, why string) {
	ctx, cancel := context.WithTimeout(ctx, shutdownBudget)
	defer cancel()
	if err := h.Shutdown(ctx); err != nil {
		slog.Warn("shell teardown did not finish",
			"why", why, "budget", shutdownBudget, "error", err)
	}
}

// ShellManager wraps terminal.Handler to give the Runtime a stable interface;
// the terminal package owns PTY lifecycle, VT parsing, wire encoding, client
// fan-out and reconnect replay.
//
// The handler is REPLACEABLE: terminal.Handler is single-use, so vibekit
// swaps it on `exit` (spent, latched by the process-exit callback) or on
// restart() for a WEDGED shell (a stuck foreground process never fires
// process-exit, so the lazy path cannot reach it).
type ShellManager struct {
	handler *terminal.Handler
	workDir string
	mu      sync.Mutex
	// spent means the child exited, so this handler can never start another.
	spent bool
}

// NewShellManager creates a ShellManager backed by the terminal package.
// The command is always bash --login; workDir is the initial CWD. ctx is
// unused (the terminal package manages its own lifecycle via Shutdown).
func NewShellManager(_ context.Context, workDir string) *ShellManager {
	sm := &ShellManager{workDir: workDir}
	sm.handler = sm.newHandler()
	return sm
}

// newHandler builds a fresh PTY handler for this manager's workDir.
func (sm *ShellManager) newHandler() *terminal.Handler {
	return terminal.NewHandler([]string{"bash", "--login"},
		terminal.WithWorkDir(sm.workDir),
		// Latch spent rather than notify: the client reattaches on the socket
		// close and lands on a fresh handler via current(); no SSE needed.
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
	retireHandler(context.Background(), retire, "restart")
	slog.Info("shell: restarted")
}

// handleWS serves /api/shell/ws, the terminal handler's WebSocket endpoint.
func (sm *ShellManager) handleWS(w http.ResponseWriter, r *http.Request) {
	h, retire := sm.current()
	if retire != nil {
		retireHandler(context.Background(), retire, "spent")
	}
	h.ServeHTTP(w, r)
}

// handleRestart kills the PTY and installs a fresh one. POST because it destroys
// running processes; the client confirms before calling it.
func (sm *ShellManager) handleRestart(w http.ResponseWriter, _ *http.Request) {
	sm.restart()
	webhttp.Ok(w)
}

// kill ends the PTY session and waits for its teardown: reaping the child and
// telling attached clients the process is gone. Its one caller is Runtime.Shutdown,
// which runs at process exit, and work still in flight then is work that is
// lost, so this takes the engine's blocking form rather than a bare close.
//
// The handler is read under the mutex because restart() can swap it: killing
// the field directly would race a restart and could tear down the replacement
// while leaving the one it replaced running.
func (sm *ShellManager) kill(ctx context.Context) {
	sm.mu.Lock()
	h := sm.handler
	sm.mu.Unlock()
	retireHandler(ctx, h, "shutdown")
}
