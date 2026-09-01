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

	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/sanitize"
)

// classifyLoginStartErr maps a cmd.Start error to an HTTP status code for
// the "login unavailable" sentinel. fs.ErrNotExist catches fork/exec ENOENT;
// exec.ErrNotFound catches LookPath failures. Both surface as 503 so
// Grafana can distinguish "binary missing" from a transient fork failure
// (500).
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

// reapLoginProcess waits for the login subprocess to exit, escalates the
// process-group kill on hard-cap expiry, releases the semaphore, and
// closes waitDone. Owns the semaphore release to preserve the "one login
// at a time for the full device-flow window" invariant.
func (h *Handler) reapLoginProcess(r loginReap) {
	// Watch for ctx expiry and escalate to a process-group kill.
	// CommandContext's default cancel only SIGKILLs the parent PID; any
	// child helper survives and holds the stdout pipe open, blocking
	// cmd.Wait.
	killOnDeadline := make(chan struct{})
	go func() {
		select {
		case <-r.ctx.Done():
			if errors.Is(r.ctx.Err(), context.DeadlineExceeded) {
				killProcessGroup(r.cmd)
			}
		case <-killOnDeadline:
		}
	}()
	// Wait for the scanner+drain goroutine to finish reading stdout before
	// calling cmd.Wait, which closes the pipe as it returns; a concurrent
	// reader would see "file already closed".
	<-r.stdoutDone
	werr := r.cmd.Wait()
	close(killOnDeadline)
	// If we somehow raced the watcher goroutine, belt-and-braces the
	// group kill.
	if errors.Is(r.ctx.Err(), context.DeadlineExceeded) {
		killProcessGroup(r.cmd)
	}
	r.cancel()
	switch {
	case werr == nil:
		slog.Info("login: subprocess completed cleanly")
	case errors.Is(r.ctx.Err(), context.DeadlineExceeded):
		attrs := make([]any, 0, 4)
		attrs = append(attrs, "cap", h.cfg.LoginTimeout)
		attrs = append(attrs, stderrAttr(r.stderrBuf)...)
		slog.Warn("login: subprocess hit hard cap", attrs...)
	default:
		slog.Debug("login: cmd wait returned", "error", werr)
	}
	// Release the semaphore AFTER the subprocess has fully exited so a
	// second POST during the browser-flow window still gets 409.
	<-h.loginSem
	close(r.waitDone)
}

// extractAuthURL pulls an auth URL out of a single already-stripped
// login-output line. Returns "" when the line carries no URL. An explicit
// "Open this URL:" prefix anchors the search to the tail after the prefix,
// so a legitimate CLI banner mentioning a secondary URL on the same line
// never shadows the primary. A non-https scheme after the prefix yields ""
// — defense-in-depth against a compromised kiro-cli emitting
// scheme-injection payloads.
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
// stdout to io.Discard until the subprocess closes the pipe. The drain is
// what lets kiro-cli keep writing progress banners after we emitted the
// URL — without it the pipe fills and kiro-cli blocks on write(2) until
// the hard cap fires.
func scanLoginOutputWithDrain(stdout io.ReadCloser, urlCh chan<- map[string]string) {
	scanLoginOutput(stdout, urlCh)
	if _, err := io.Copy(io.Discard, stdout); err != nil {
		slog.Debug("login: stdout drain stopped", "error", err)
	}
}

// scanLoginOutput reads lines from r until it finds an auth URL (either in
// an explicit "Open this URL: …" line or as the first bare https:// token).
// Sends the discovered URL + optional "Code:" into urlCh. On EOF with no
// URL, on a scanner error, or when the line cap is hit, sends an error map.
// urlCh MUST be buffered so this function never blocks after the caller's
// select has already moved on.
//
// Memory is bounded at O(maxScanLineBytes); a small ring of the first/last
// 5 lines is kept for the line-cap log so operators can diagnose kiro-cli
// output-format drift.
func scanLoginOutput(stdout io.Reader, urlCh chan<- map[string]string) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), maxScanLineBytes)
	// Bounded ring: keep first-5 and last-5 lines for operator
	// diagnostics on the line-cap branch. Lines capped at 128 bytes
	// before storage so an adversarial CLI can't blow up the log event.
	ring := newLineRing(5, 128)
	var code, authURL string
	var lineCount int
	for scanner.Scan() {
		line := strings.TrimSpace(sanitize.StripANSI(scanner.Text()))
		lineCount++
		ring.Push(line)
		// Fast path: kiro-cli refuses a fresh login when a session
		// already exists. Surface a dedicated error key rather than
		// the generic "no auth URL" sentinel.
		if strings.Contains(strings.ToLower(line), "already logged in") {
			urlCh <- httpreply.ErrorJSON("already_logged_in")
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
			urlCh <- map[string]string{"url": authURL, "code": code}
			return
		}
		if lineCount >= maxLoginLines {
			// Warn, not Error: output-format drift or a user-cancel is
			// recoverable and user-visible, not an SRE page.
			slog.Warn("login: output line cap hit without auth URL",
				"lines", lineCount,
				"first_and_last_sample", ring.Sample())
			urlCh <- httpreply.ErrorJSON("CLI produced too much output without auth URL")
			return
		}
	}
	if err := scanner.Err(); err != nil {
		// Warn, not Error: EOF after killProcessGroup and a network flake
		// both land here.
		slog.Warn("login: scanner failed before URL",
			"error", err, "lines_read", lineCount)
		urlCh <- httpreply.ErrorJSON("scanner error: " + err.Error())
		return
	}
	// Clean EOF without a URL. handleLogin's URL-timeout branch already
	// surfaces the failure with richer context.
	urlCh <- httpreply.ErrorJSON("no auth URL found in CLI output")
}
