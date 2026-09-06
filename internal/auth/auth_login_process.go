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

// classifyLoginStartErr maps a cmd.Start error to an HTTP status: 503 when the
// binary is missing (fork/exec ENOENT or a LookPath failure), 500 otherwise.
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

// reapLoginProcess waits for the login subprocess to exit, then releases the
// semaphore and closes waitDone. Owning that release is what preserves the "one
// login at a time for the full device-flow window" invariant.
func (h *Handler) reapLoginProcess(r loginReap) {
	// CommandContext's cancel only SIGKILLs the parent PID; a surviving child
	// helper holds the stdout pipe open and blocks cmd.Wait.
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
	// cmd.Wait closes the stdout pipe as it returns; a concurrent reader would
	// see "file already closed".
	<-r.stdoutDone
	werr := r.cmd.Wait()
	close(killOnDeadline)
	// Redundant unless we raced the watcher goroutine above.
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
	// AFTER the subprocess has fully exited, so a second POST during the
	// browser-flow window still gets 409.
	<-h.loginSem
	close(r.waitDone)
	// A clean exit is not proof of a sign-in and a dirty one is not proof of a
	// failure, so the identity is re-read rather than inferred. LAST because the
	// read forks kiro-cli for up to WhoamiTimeout: above here it would hold the
	// login semaphore across that fork and hold waitDone shut, which
	// handleLogin's timeout branch waits on.
	h.identity.refresh()
}

// extractAuthURL returns the auth URL in one already-stripped login-output line,
// or "" when there is none. An "Open this URL:" prefix anchors the search to the
// tail after it, so a banner naming a secondary URL never shadows the primary.
// Only https:// tokens qualify, so a scheme-injection payload yields "".
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

// scanLoginOutputWithDrain runs scanLoginOutput, then drains stdout to
// io.Discard until the pipe closes. Without the drain, the progress banners
// kiro-cli writes after the URL fill the pipe and block it on write(2).
func scanLoginOutputWithDrain(stdout io.ReadCloser, urlCh chan<- map[string]string) {
	scanLoginOutput(stdout, urlCh)
	if _, err := io.Copy(io.Discard, stdout); err != nil {
		slog.Debug("login: stdout drain stopped", "error", err)
	}
}

// scanLoginOutput reads lines from r until it finds an auth URL and sends it,
// with any "Code:", into urlCh; on EOF without one, on a scanner error, or at the
// line cap it sends an error map instead. urlCh MUST be buffered so this never
// blocks after the caller's select has moved on. Memory is bounded at
// O(maxScanLineBytes).
func scanLoginOutput(stdout io.Reader, urlCh chan<- map[string]string) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), maxScanLineBytes)
	// Lines are capped before storage so an adversarial CLI cannot blow up the
	// line-cap log event.
	ring := newLineRing(5, 128)
	var code, authURL string
	var lineCount int
	for scanner.Scan() {
		line := strings.TrimSpace(sanitize.StripANSI(scanner.Text()))
		lineCount++
		ring.Push(line)
		// kiro-cli refuses a fresh login when a session exists; that gets its own
		// error key rather than the generic "no auth URL" sentinel.
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
			// Warn, not Error: format drift or a user cancel is recoverable and
			// user-visible.
			slog.Warn("login: output line cap hit without auth URL",
				"lines", lineCount,
				"first_and_last_sample", ring.Sample())
			urlCh <- httpreply.ErrorJSON("CLI produced too much output without auth URL")
			return
		}
	}
	if err := scanner.Err(); err != nil {
		// Warn, not Error: EOF after killProcessGroup lands here too.
		slog.Warn("login: scanner failed before URL",
			"error", err, "lines_read", lineCount)
		urlCh <- httpreply.ErrorJSON("scanner error: " + err.Error())
		return
	}
	// handleLogin's URL-timeout branch surfaces this with richer context.
	urlCh <- httpreply.ErrorJSON("no auth URL found in CLI output")
}
