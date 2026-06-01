package auth

import (
	"bufio"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"

	"vibekit/internal/api"
)

// classifyLoginStartErr maps a cmd.Start error to an HTTP status code
// for the "login unavailable" sentinel. fs.ErrNotExist catches
// fork/exec ENOENT (absolute cliPath); exec.ErrNotFound catches
// LookPath failures (bare names on PATH). Both surface as 503 so
// Grafana can distinguish "binary missing" (redeploy) from
// "transient fork failure" (retry — 500). Caller logs; we just
// classify.
func classifyLoginStartErr(err error, cliPath string) int {
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		slog.Error("login: kiro-cli binary not found",
			"cli_path", cliPath)
		return http.StatusServiceUnavailable
	}
	slog.Error("login: kiro-cli start failed",
		"error", err, "cli_path", cliPath)
	return http.StatusInternalServerError
}

// reapLoginProcess waits for the login subprocess to exit, escalates
// the process-group kill on hard-cap expiry, releases the semaphore,
// and closes waitDone. Runs in its own goroutine. Owns the semaphore
// release to preserve the "one login at a time for the full
// device-flow window" invariant — see Handler doc comment.
func (h *Handler) reapLoginProcess(r loginReap) {
	// Watch for ctx expiry and escalate to a process-group kill.
	// CommandContext's default cancel only SIGKILLs the parent
	// PID; any child bun/Node helper (or, in tests, a `sleep` in
	// the fake CLI script) survives and holds the stdout pipe
	// open, which blocks cmd.Wait.
	killOnDeadline := make(chan struct{})
	go func() {
		select {
		case <-r.ctx.Done():
			if errors.Is(r.ctx.Err(), context.DeadlineExceeded) {
				killLoginProcess(r.cmd)
			}
		case <-killOnDeadline:
		}
	}()
	// Wait for the scanner+drain goroutine to finish reading stdout
	// before calling cmd.Wait. Wait closes the pipe as it returns
	// (per Go's exec.Cmd contract), and a concurrent reader sees
	// "file already closed". The drain goroutine reads until EOF,
	// which only happens after the subprocess exits and closes its
	// write end — so this gate doesn't extend lifetime in the normal
	// path, just orders the FD close after the read completes.
	<-r.stdoutDone
	werr := r.cmd.Wait()
	close(killOnDeadline)
	// If we somehow raced the watcher goroutine (cmd.Wait returned
	// without ctx expiring but the deadline then fired),
	// belt-and-braces the group kill.
	if errors.Is(r.ctx.Err(), context.DeadlineExceeded) {
		killLoginProcess(r.cmd)
	}
	r.cancel()
	switch {
	case werr == nil:
		slog.Info("login: subprocess completed cleanly")
	case errors.Is(r.ctx.Err(), context.DeadlineExceeded):
		attrs := make([]any, 0, 4)
		attrs = append(attrs, "cap", h.cfg.LoginProcessCap)
		attrs = append(attrs, stderrAttr(r.stderrBuf)...)
		slog.Warn("login: subprocess hit hard cap", attrs...)
	default:
		slog.Debug("login: cmd wait returned", "error", werr)
	}
	// Release the semaphore AFTER the subprocess has fully
	// exited so a second POST during the browser-flow window
	// still gets 409. See Handler doc comment.
	<-h.loginSem
	close(r.waitDone)
}

// extractAuthURL pulls an auth URL out of a single already-stripped
// login-output line. Returns "" when the line carries no URL. Both
// branches apply the same discipline: the first https://-prefixed
// whitespace-separated token wins. An explicit "Open this URL:"
// prefix anchors the search to the tail after the prefix, so a
// legitimate CLI banner mentioning a secondary URL on the same line
// never shadows the primary. A non-https scheme (javascript:, http://)
// after the prefix yields "" — defense-in-depth against a compromised
// kiro-cli emitting scheme-injection payloads.
func extractAuthURL(line string) string {
	if after, found := strings.CutPrefix(line, "Open this URL:"); found {
		for word := range strings.FieldsSeq(after) {
			if strings.HasPrefix(word, "https://") {
				return word
			}
		}
		return ""
	}
	if strings.Contains(line, "https://") {
		for word := range strings.FieldsSeq(line) {
			if strings.HasPrefix(word, "https://") {
				return word
			}
		}
	}
	return ""
}

// scanLoginOutputWithDrain runs scanLoginOutput and then keeps reading
// stdout to io.Discard until the subprocess closes the pipe. The
// drain is what lets kiro-cli keep writing progress banners after we
// emitted the URL — without it, the pipe fills at 64 KiB and kiro-cli
// blocks on write(2) until the 16m hard cap fires. A Copy error here
// is normal (EPIPE after killLoginProcess, closed pipe on clean exit)
// and already surfaced via cmd.Wait in the reap goroutine; logged at
// Debug for completeness.
func scanLoginOutputWithDrain(stdout io.ReadCloser, urlCh chan<- map[string]string) {
	scanLoginOutput(stdout, urlCh)
	if _, err := io.Copy(io.Discard, stdout); err != nil {
		slog.Debug("login: stdout drain stopped", "error", err)
	}
}

// scanLoginOutput reads lines from r until it finds an auth URL (either
// in an explicit "Open this URL: …" line or as the first bare https://
// token). Sends the discovered URL + optional "Code:" into urlCh. On
// EOF with no URL, on a scanner error (buffer overflow, read failure),
// or when the line cap is hit, sends an error map. urlCh MUST be
// buffered (capacity ≥1) so this function never blocks after the
// caller's select has already moved on.
//
// Memory is bounded at O(maxScanLineBytes) — the scanner owns one
// buffer. Historical context: an earlier implementation retained every
// scanned line in a []string for logging, which pinned up to
// ~50 MiB worst-case even though callers only read len(). A small
// ring of the first/last 5 lines is kept for the line-cap log so
// operators can diagnose kiro-cli output-format drift.
func scanLoginOutput(stdout io.Reader, urlCh chan<- map[string]string) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), maxScanLineBytes)
	// Bounded ring: keep first-5 and last-5 lines for operator
	// diagnostics on the line-cap branch. Individual lines are
	// capped at 128 bytes before storage so an adversarial CLI
	// can't blow up the log event (worst case: 10 * 128 = 1.25 KiB
	// in a single structured attribute, comfortably within Loki's
	// narrow-field budget).
	ring := newLineRing(5, 128)
	var code, authURL string
	var lineCount int
	for scanner.Scan() {
		line := strings.TrimSpace(api.StripANSI(scanner.Text()))
		lineCount++
		ring.Push(line)
		// Fast path: kiro-cli refuses a fresh login when a session
		// already exists. Surface a dedicated error key the UI can
		// recognise instead of the generic "no auth URL" sentinel,
		// which read as "something broke" to users.
		if strings.Contains(strings.ToLower(line), "already logged in") {
			urlCh <- api.ErrorJSON("already_logged_in")
			return
		}
		if after, found := strings.CutPrefix(line, "Code:"); found {
			code = strings.TrimSpace(after)
		}
		if authURL == "" {
			authURL = extractAuthURL(line)
		}
		if authURL != "" {
			slog.Info("login: auth URL extracted",
				"has_code", code != "",
				"lines_before_url", lineCount)
			// urlCh is guaranteed buffered by handleLogin; this
			// send never blocks after the caller times out.
			urlCh <- map[string]string{"url": authURL, "code": code}
			return
		}
		if lineCount >= maxLoginLines {
			// Warn (not Error): kiro-cli output-format drift or
			// a user-cancel is a recoverable, user-visible
			// situation — not an SRE page. handleLogin surfaces
			// the failure to the caller regardless.
			slog.Warn("login: output line cap hit without auth URL",
				"lines", lineCount,
				"first_and_last_sample", ring.Sample())
			urlCh <- api.ErrorJSON("CLI produced too much output without auth URL")
			return
		}
	}
	if err := scanner.Err(); err != nil {
		// Warn (not Error): EOF after killLoginProcess (a normal
		// consequence of the URL-timeout branch in handleLogin)
		// and network flake both land here. Not a page-worthy
		// event on its own.
		slog.Warn("login: scanner failed before URL",
			"error", err, "lines_read", lineCount)
		urlCh <- api.ErrorJSON("scanner error: " + err.Error())
		return
	}
	// Clean EOF without a URL. No log at this layer — handleLogin's
	// URL-timeout branch and the scanner-reported-error breadcrumb
	// already surface the failure with richer context. Emitting a
	// second Warn here duplicated every timeout event on Loki's
	// level=warn stream without adding information.
	urlCh <- api.ErrorJSON("no auth URL found in CLI output")
}
