package auth

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"vibekit/internal/api"
)

// validateProvider rejects anything that isn't a well-formed HTTPS URL.
// Empty strings are allowed (kiro-cli falls back to the default Builder
// ID flow). The check is a phishing guardrail: without validation, a
// LAN-reachable POST could forward an attacker-controlled start URL and
// hand the user's SSO session to the attacker's IdP.
func validateProvider(v string) error {
	if v == "" {
		return nil
	}
	if len(v) > maxProviderLen {
		return errors.New("provider too long")
	}
	u, perr := url.Parse(v)
	if perr != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("provider must be an https URL")
	}
	// Reject URLs embedding userinfo (https://user:[email protected]/).
	// Some HTTP clients dereference the credentials before
	// following redirects; rejecting them here is cheap
	// defense-in-depth against CWE-601 URL-credential-confusion
	// even though vibekit is LAN-only behind an origin check.
	if u.User != nil {
		return errors.New("provider must not contain credentials")
	}
	return nil
}

// validateRegion rejects anything that isn't a canonical AWS region id
// across all partitions (commercial, China, GovCloud, ISO). Empty
// strings pass through so kiro-cli picks its default.
func validateRegion(v string) error {
	if v == "" {
		return nil
	}
	if len(v) > maxRegionLen {
		return errors.New("region too long")
	}
	if !awsRegionRe.MatchString(v) {
		return errors.New("invalid region")
	}
	return nil
}

// buildLoginArgs returns the argv tail (after the binary path) for a
// `kiro-cli login` invocation with optional provider/region overrides.
// Empty strings are omitted so kiro-cli picks its defaults.
func buildLoginArgs(provider, region string) []string {
	args := []string{"login", "--use-device-flow"}
	if provider != "" {
		args = append(args, "--identity-provider", provider)
	}
	if region != "" {
		args = append(args, "--region", region)
	}
	return args
}

// parseLoginRequest decodes the POST body, enforces the MaxJSONBody
// cap, and validates provider+region. Writes the error response and
// returns ok=false on any failure so the caller just returns; the
// 413 MaxBytesError path is distinguished from generic decode errors
// so Grafana can tell "client attack" from "client bug". An empty
// body is legitimate (default Builder ID flow) and returns zero
// values with ok=true.
func parseLoginRequest(w http.ResponseWriter, r *http.Request) (provider, region string, ok bool) {
	api.LimitBody(w, r, api.MaxJSONBody)
	var body struct {
		Provider string `json:"provider"`
		Region   string `json:"region"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			slog.Warn("login: body exceeds limit",
				"limit_bytes", api.MaxJSONBody)
			api.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
				map[string]string{"error": "request too large"})
			return "", "", false
		}
		slog.Warn("login: decode body", "error", err)
		api.BadRequest(w, "invalid JSON body")
		return "", "", false
	}
	if err := validateProvider(body.Provider); err != nil {
		api.BadRequest(w, err.Error())
		return "", "", false
	}
	if err := validateRegion(body.Region); err != nil {
		api.BadRequest(w, err.Error())
		return "", "", false
	}
	return body.Provider, body.Region, true
}

// handleLogin spawns `kiro-cli login --use-device-flow` and streams its
// stdout looking for the first "Open this URL:" (or bare https://) token
// to return to the browser. The subprocess intentionally outlives the
// HTTP request (device-flow login takes minutes while the user completes
// the browser flow; r.Context() would cancel it as soon as the response
// is written) — see the context.WithTimeout below which caps the
// subprocess at the LoginProcessCap wall-clock budget.
//
// Only one login may be in flight at a time: a concurrent POST gets
// HTTP 409. vibekit is single-user, and a double-click/LAN-probe
// would otherwise pin two AWS device codes for 16 minutes each.
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		api.MethodNotAllowed(w)
		return
	}
	// Audit trail: record every /api/login POST so operators can
	// distinguish browser-initiated login from unexpected LAN
	// activity in Loki. Single-user, LAN-trusted deployment, so
	// cardinality on remote_addr + user_agent is bounded.
	slog.Info("login: request received",
		"remote_addr", r.RemoteAddr,
		"user_agent", r.Header.Get("User-Agent"))
	select {
	case h.loginSem <- struct{}{}:
		// Do NOT release here. Ownership transfers to the reap
		// goroutine below once cmd.Start succeeds; any pre-reap
		// error return (body decode, validation, StdoutPipe,
		// cmd.Start) releases via the semReleased guard before
		// returning. See Handler doc comment.
	default:
		api.Conflict(w, "login in progress")
		return
	}
	// Guarded release: only fires if we return before the reap
	// goroutine takes ownership. After cmd.Start succeeds and the
	// reap goroutine is launched, we set semReleased=true so the
	// defer is a no-op and the goroutine owns the release.
	semReleased := false
	defer func() {
		if !semReleased {
			<-h.loginSem
		}
	}()
	provider, region, ok := parseLoginRequest(w, r)
	if !ok {
		return
	}
	// Wall-clock cap on the subprocess — NOT r.Context(). The
	// device-flow login intentionally outlives the HTTP request:
	// the client disconnects immediately after we write the auth
	// URL and comes back minutes later (Builder ID flow). Request
	// cancellation here would SIGKILL the login before the user
	// completes the browser step. The outer hard cap bounds
	// abandoned flows (tab closed, network lost) at AWS's
	// device-code TTL + 1m grace. The select below separately
	// bounds the URL-discovery phase at LoginURLTimeout.
	ctx, cancel := context.WithTimeout(context.Background(), h.cfg.LoginProcessCap)
	cmd := exec.CommandContext(ctx, h.cliPath, buildLoginArgs(provider, region)...) //nolint:gosec // G204: binary path from config
	setLoginProcAttr(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		slog.Error("login: stdout pipe failed",
			"error", err, "cli_path", h.cliPath)
		api.WriteJSONStatus(w, http.StatusInternalServerError,
			map[string]string{"error": "login unavailable"})
		return
	}
	// Capture stderr into a bounded buffer separate from stdout so
	// stderr chatter (deprecation warnings, debug envs) doesn't
	// count against the 200-line maxLoginLines cap on the URL
	// scanner. Logged on timeout / error paths for diagnostics.
	var stderrBuf bytes.Buffer
	cmd.Stderr = &limitedWriter{W: &stderrBuf, N: stderrCap}
	if err := cmd.Start(); err != nil {
		cancel()
		status := classifyLoginStartErr(err, h.cliPath)
		api.WriteJSONStatus(w, status, map[string]string{"error": "login unavailable"})
		return
	}
	urlCh := make(chan map[string]string, 1)
	// scanLoginOutput returns the moment it finds a URL (or hits an
	// error). The subprocess keeps writing progress/banner lines to
	// stdout while the user completes the browser flow — if nothing
	// drains the pipe, kiro-cli blocks on a full 64 KiB buffer and
	// wedges until the 16-minute hard cap fires. Keep draining in
	// the background so the child can make forward progress.
	go scanLoginOutputWithDrain(stdout, urlCh)

	// Transfer loginSem ownership to the reap goroutine: any
	// pre-reap return path above this point releases via the
	// semReleased guard, but from here on the sem is held until
	// cmd.Wait returns so a second login attempt during the 16m
	// device-code window still gets 409.
	semReleased = true

	// Reap the child no matter which branch wins the select below
	// so both the success path and the timeout path collect the
	// exit status and release the subprocess resources. On success
	// this goroutine outlives the HTTP handler (the login runs
	// until the user finishes the browser flow or the outer
	// LoginProcessCap fires). On timeout we kill the process
	// group (CommandContext's default cancel only kills the PID,
	// which orphans bun/Node helper children that keep the stdout
	// pipe open and wedge cmd.Wait for the full sleep), and wait
	// here so FDs are reclaimed before the handler returns.
	waitDone := make(chan struct{})
	go h.reapLoginProcess(loginReap{
		ctx:       ctx,
		cancel:    cancel,
		cmd:       cmd,
		stderrBuf: &stderrBuf,
		waitDone:  waitDone,
	})

	select {
	case result := <-urlCh:
		if result["url"] == "" {
			// Scanner hit an error (line cap, buffer overflow,
			// reader error, already_logged_in) without a URL.
			// Log a handler-layer breadcrumb so operators can
			// correlate the scanLoginOutput Warn with this
			// specific HTTP request. result["error"] is one of
			// a fixed sentinel set so cardinality is bounded.
			slog.Warn("login: scanner reported error without URL",
				"error", result["error"])
			// Reap the child eagerly rather than waiting for
			// the 16-minute hard cap — no user completion is
			// coming and the device code is wasted either way.
			killLoginProcess(cmd)
			<-waitDone
		}
		api.WriteJSON(w, result)
	case <-time.After(h.cfg.LoginURLTimeout):
		killLoginProcess(cmd)
		<-waitDone // bounded: Kill → SIGKILL → reap
		attrs := make([]any, 0, 4)
		attrs = append(attrs, "timeout", h.cfg.LoginURLTimeout)
		attrs = append(attrs, stderrAttr(&stderrBuf)...)
		slog.Warn("login: timeout waiting for auth URL", attrs...)
		api.WriteJSONStatus(w, http.StatusGatewayTimeout,
			map[string]string{"error": "timeout waiting for auth URL"})
	}
}

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
			urlCh <- map[string]string{"error": "already_logged_in"}
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
			urlCh <- map[string]string{"error": "CLI produced too much output without auth URL"}
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
		urlCh <- map[string]string{"error": "scanner error: " + err.Error()}
		return
	}
	// Clean EOF without a URL. No log at this layer — handleLogin's
	// URL-timeout branch and the scanner-reported-error breadcrumb
	// already surface the failure with richer context. Emitting a
	// second Warn here duplicated every timeout event on Loki's
	// level=warn stream without adding information.
	urlCh <- map[string]string{"error": "no auth URL found in CLI output"}
}
