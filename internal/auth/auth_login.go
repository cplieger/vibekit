package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os/exec"
	"time"

	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/procout"
	"github.com/cplieger/webhttp"
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
const flagDeviceFlow = "--use-device-flow"

func buildLoginArgs(provider, region string) []string {
	args := []string{"login", flagDeviceFlow}
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
	webhttp.LimitBody(w, r, webhttp.MaxJSONBody)
	var body struct {
		Provider string `json:"provider"`
		Region   string `json:"region"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			slog.Warn("login: body exceeds limit",
				"limit_bytes", webhttp.MaxJSONBody)
			webhttp.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
				httpreply.ErrorJSON("request too large"))
			return "", "", false
		}
		slog.Warn("login: decode body", "error", err)
		httpreply.BadRequest(w, "invalid JSON body")
		return "", "", false
	}
	if err := validateProvider(body.Provider); err != nil {
		httpreply.BadRequest(w, err.Error())
		return "", "", false
	}
	if err := validateRegion(body.Region); err != nil {
		httpreply.BadRequest(w, err.Error())
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
// subprocess at the LoginTimeout wall-clock budget.
//
// Only one login may be in flight at a time: a concurrent POST gets
// HTTP 409. vibekit is single-user, and a double-click/LAN-probe
// would otherwise pin two AWS device codes for 16 minutes each.
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	// Audit trail: record every /api/login POST so operators can
	// distinguish browser-initiated login from unexpected activity in
	// Loki. client_ip is the spoof-safe resolved client host from
	// webhttp.ClientIP: the unspoofable socket peer when directly
	// exposed, or the real client from a trusted X-Forwarded-For when
	// TRUSTED_PROXIES is set. Single-user deployment, so cardinality on
	// client_ip + user_agent is bounded.
	slog.Info("login: request received",
		"client_ip", webhttp.ClientIP(r, h.trusted...),
		"user_agent", r.Header.Get("User-Agent"))
	select {
	case h.loginSem <- struct{}{}:
		// Do NOT release here. Ownership transfers to the reap
		// goroutine below once cmd.Start succeeds; any pre-reap
		// error return (body decode, validation, StdoutPipe,
		// cmd.Start) releases via the semReleased guard before
		// returning. See Handler doc comment.
	default:
		httpreply.Conflict(w, "login in progress")
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
	ctx, cancel := context.WithTimeout(context.Background(), h.cfg.LoginTimeout)
	cmd := exec.CommandContext(ctx, h.cliPath(), buildLoginArgs(provider, region)...) //nolint:gosec // G204: binary path from config
	setLoginProcAttr(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		slog.Error("login: stdout pipe failed",
			"error", err, "cli_path", h.cliPath())
		webhttp.WriteJSONStatus(w, http.StatusInternalServerError,
			httpreply.ErrorJSON("login unavailable"))
		return
	}
	// Capture stderr into a bounded buffer separate from stdout so
	// stderr chatter (deprecation warnings, debug envs) doesn't
	// count against the 200-line maxLoginLines cap on the URL
	// scanner. Logged on timeout / error paths for diagnostics.
	stderrBuf := procout.NewBuffer(stderrCap)
	cmd.Stderr = stderrBuf
	if err := cmd.Start(); err != nil {
		cancel()
		status := classifyLoginStartErr(err, h.cliPath())
		webhttp.WriteJSONStatus(w, status, httpreply.ErrorJSON("login unavailable"))
		return
	}
	urlCh := make(chan map[string]string, 1)
	// scanLoginOutput returns the moment it finds a URL (or hits an
	// error). The subprocess keeps writing progress/banner lines to
	// stdout while the user completes the browser flow — if nothing
	// drains the pipe, kiro-cli blocks on a full 64 KiB buffer and
	// wedges until the 16-minute hard cap fires. Keep draining in
	// the background so the child can make forward progress.
	//
	// stdoutDone is closed when the scanner+drain goroutine has
	// returned (URL found + drain to EOF, or scanner error). The
	// reap goroutine waits on this before calling cmd.Wait so the
	// pipe isn't closed mid-read. See loginReap doc for the
	// underlying Go runtime requirement.
	stdoutDone := make(chan struct{})
	go func() {
		defer close(stdoutDone)
		scanLoginOutputWithDrain(stdout, urlCh)
	}()

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
	// LoginTimeout fires). On timeout we kill the process
	// group (CommandContext's default cancel only kills the PID,
	// which orphans bun/Node helper children that keep the stdout
	// pipe open and wedge cmd.Wait for the full sleep), and wait
	// here so FDs are reclaimed before the handler returns.
	waitDone := make(chan struct{})
	go h.reapLoginProcess(loginReap{
		ctx:        ctx,
		cancel:     cancel,
		cmd:        cmd,
		stderrBuf:  stderrBuf,
		stdoutDone: stdoutDone,
		waitDone:   waitDone,
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
		webhttp.WriteJSON(w, result)
	case <-time.After(h.cfg.LoginURLTimeout):
		killLoginProcess(cmd)
		<-waitDone // bounded: Kill → SIGKILL → reap
		attrs := make([]any, 0, 4)
		attrs = append(attrs, "timeout", h.cfg.LoginURLTimeout)
		attrs = append(attrs, stderrAttr(stderrBuf)...)
		slog.Warn("login: timeout waiting for auth URL", attrs...)
		webhttp.WriteJSONStatus(w, http.StatusGatewayTimeout,
			httpreply.ErrorJSON("timeout waiting for auth URL"))
	}
}
