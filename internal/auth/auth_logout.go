package auth

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/procout"
	"github.com/cplieger/vibekit/internal/sanitize"
	"github.com/cplieger/webhttp"
)

// handleLogout shells out to `kiro-cli logout`, feeding "y\n" on stdin
// to acknowledge the confirmation prompt, and returns the stdout+stderr
// as "output". Command failures surface with status codes that let
// Grafana distinguish timeout vs CLI failure vs missing binary: 504
// for the LogoutTimeout backstop, 503 when kiro-cli is missing, 502
// for any other non-zero exit. The timeout backstops the case where a
// future kiro-cli changes its prompt and our "y\n" no longer advances
// the flow.
func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	// Audit trail: record every /api/logout POST. See handleLogin
	// for the rationale (client_ip is the spoof-safe resolved client
	// host from webhttp.ClientIP); whoami is intentionally skipped
	// because it fires on every page load and SSE reconnect.
	slog.Info("logout: request received",
		"client_ip", webhttp.ClientIP(r, h.trusted...),
		"user_agent", r.Header.Get("User-Agent"))
	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.LogoutTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, h.cliPath(), "logout") //nolint:gosec // G204: binary path from config
	// Put the logout subprocess in its own process group so the
	// timeout branch can reap the whole tree (bun + Node helper
	// children) via killLoginProcess. CommandContext's default
	// cancel only SIGKILLs the parent PID, which leaves orphans
	// holding stdout/stderr pipes open when a future kiro-cli
	// changes its confirmation prompt and our "y\n" no longer
	// advances the flow. Mirror the login path.
	setLoginProcAttr(cmd)
	cmd.Stdin = strings.NewReader("y\n")
	// Bounded combined stdout+stderr capture so a runaway CLI
	// can't OOM the container. Use a single procout.Buffer for
	// both streams: os/exec compares the two writers and drains
	// them through one pipe with one goroutine when they are the
	// same value, so this is the documented way to merge the
	// streams — two separate writers over one buffer would race.
	buf := procout.NewBuffer(logoutMaxOutput)
	cmd.Stdout = buf
	cmd.Stderr = buf
	err := cmd.Run()
	out := buf.Bytes()
	// SanitizeOutput strips ANSI + hidden Unicode (matches the
	// convention used elsewhere in vibekit for all subprocess
	// output before it reaches clients).
	result := map[string]string{"output": sanitize.Output(string(out))}
	if err != nil {
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			// Reap the whole process group eagerly so orphan
			// bun/Node helpers don't survive the timeout
			// holding pipes open.
			killLoginProcess(cmd)
			slog.Warn("logout: kiro-cli timed out",
				"timeout", h.cfg.LogoutTimeout, "output_bytes", len(out))
			result["error"] = "logout timed out"
			webhttp.WriteJSONStatus(w, http.StatusGatewayTimeout, result)
			return
		case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist):
			slog.Error("logout: kiro-cli binary not found",
				"cli_path", h.cliPath())
			result["error"] = "logout unavailable"
			webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, result)
			return
		default:
			// Log err details server-side; return a generic
			// sentinel so filesystem paths / OS messages don't
			// leak to the client.
			slog.Warn("logout: kiro-cli failed",
				"error", err, "output_bytes", len(out))
			result["error"] = "logout failed"
			webhttp.WriteJSONStatus(w, http.StatusBadGateway, result)
			return
		}
	}
	slog.Info("logout: completed", "output_bytes", len(out))
	webhttp.WriteJSON(w, result)
}

// killLoginProcess sends SIGKILL to the entire process group of the
// login subprocess. kiro-cli is a bun/Node wrapper that may spawn
// helper children; killing only the parent leaves orphans pinning the
// stdout pipe open and preventing scanner teardown. See
// login_proc_unix.go / login_proc_other.go for the platform split.
// Idempotent: calling after the process has already been reaped is a
// no-op (ESRCH and os.ErrProcessDone suppressed at Debug level).
func killLoginProcess(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	err := loginKill(cmd)
	if err == nil {
		return
	}
	// Both the group kill and single-PID fallback can race with
	// cmd.Wait reaping the subprocess. ESRCH and os.ErrProcessDone
	// mean the process is already gone — expected on the
	// belt-and-braces second call in the reap goroutine.
	if errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone) {
		slog.Debug("login: kill group no-op (already reaped)",
			"group_err", err)
		return
	}
	// Fallback: best-effort single-PID kill if the group kill
	// failed (e.g. the process group wasn't set on a non-unix
	// platform).
	kerr := cmd.Process.Kill()
	if kerr == nil {
		return
	}
	if errors.Is(kerr, syscall.ESRCH) || errors.Is(kerr, os.ErrProcessDone) {
		slog.Debug("login: kill pid no-op (already reaped)",
			"group_err", err, "pid_err", kerr)
		return
	}
	slog.Error("login: kill timeout process",
		"group_err", err, "pid_err", kerr)
}
