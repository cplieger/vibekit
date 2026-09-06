package auth

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"

	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/procgroup"
	"github.com/cplieger/vibekit/internal/procout"
	"github.com/cplieger/vibekit/internal/sanitize"
	"github.com/cplieger/webhttp/v2"
)

// handleLogout shells out to `kiro-cli logout`, feeding "y\n" on stdin to acknowledge the
// confirmation prompt, and returns stdout+stderr as "output". The failure statuses tell the
// causes apart: 504 for the LogoutTimeout backstop, 503 when kiro-cli is missing, 502 otherwise.
func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	// Audit trail: every /api/logout POST is recorded; handleLogin owns the rationale.
	slog.Info("logout: request received",
		"client_ip", webhttp.ClientIP(r, h.trusted...),
		"user_agent", r.Header.Get("User-Agent"))
	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.LogoutTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, h.cliPath(), "logout") //nolint:gosec // G204: binary path from config
	// Honour LogoutTimeout rather than the child's lifetime; boundChild owns how.
	boundChild(cmd)
	cmd.Stdin = strings.NewReader("y\n")
	// Bounded capture so a runaway CLI cannot OOM the container. One Buffer for both streams:
	// os/exec drains them through one pipe with one goroutine when they are the same value.
	buf := procout.NewBuffer(logoutMaxOutput)
	cmd.Stdout = buf
	cmd.Stderr = buf
	err := cmd.Run()
	out := buf.Bytes()
	result := map[string]string{"output": sanitize.Output(string(out))}
	if err != nil {
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			// boundChild's Cancel has already killed the group by the time Run returns.
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
			// A generic sentinel to the client, so filesystem paths and OS messages do not leak.
			slog.Warn("logout: kiro-cli failed",
				"error", err, "output_bytes", len(out))
			result["error"] = "logout failed"
			webhttp.WriteJSONStatus(w, http.StatusBadGateway, result)
			return
		}
	}
	slog.Info("logout: completed", "output_bytes", len(out))
	// Published rather than re-read: a clean `kiro-cli logout` exit IS the answer, and forking a
	// second kiro-cli would only add a window in which the sidebar still shows the old identity.
	signedOut := signedOutIdentity()
	h.identity.publish(&signedOut)
	webhttp.WriteJSON(w, result)
}

// killProcessGroup SIGKILLs the whole process group of a kiro-cli subprocess: kiro-cli is a
// bun/Node wrapper that may spawn helper children, and killing only the parent leaves orphans
// pinning the stdout pipe open. Idempotent — a call after the process was reaped is a no-op.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	err := killGroup(cmd)
	if err == nil {
		return
	}
	// Both the group kill and the single-PID fallback can race with cmd.Wait reaping the
	// subprocess; procgroup.AlreadyGone owns which errors mean "already gone".
	if procgroup.AlreadyGone(err) {
		slog.Debug("auth: kill group no-op (already reaped)",
			"group_err", err)
		return
	}
	// Fallback: best-effort single-PID kill if the group kill failed.
	kerr := cmd.Process.Kill()
	if kerr == nil {
		return
	}
	if procgroup.AlreadyGone(kerr) {
		slog.Debug("auth: kill pid no-op (already reaped)",
			"group_err", err, "pid_err", kerr)
		return
	}
	slog.Error("auth: kill subprocess group failed",
		"group_err", err, "pid_err", kerr)
}
