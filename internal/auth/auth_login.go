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
	"github.com/cplieger/vibekit/internal/logsafe"
	"github.com/cplieger/vibekit/internal/procout"
	"github.com/cplieger/webhttp/v2"
)

// validateProvider rejects anything that is not a well-formed HTTPS URL; empty
// passes, so kiro-cli falls back to the default Builder ID flow. A phishing
// guardrail: an attacker-controlled start URL hands the user's SSO session away.
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
	// Some HTTP clients dereference embedded userinfo credentials before following a
	// redirect (CWE-601 URL-credential confusion).
	if u.User != nil {
		return errors.New("provider must not contain credentials")
	}
	return nil
}

// validateRegion rejects anything that is not a canonical AWS region id, in any
// partition. Empty strings pass through so kiro-cli picks its default.
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

// buildLoginArgs returns the argv tail after the binary path for `kiro-cli login`.
// An empty provider or region is omitted so kiro-cli picks its default.
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

// parseLoginRequest decodes and validates the POST body. It writes the error
// response itself and returns ok=false on any failure, so the caller just returns.
// An empty body is legitimate — the default Builder ID flow — and yields zero values
// with ok=true.
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

// handleLogin spawns `kiro-cli login --use-device-flow` and returns the first auth
// URL its stdout carries. The subprocess deliberately outlives the HTTP request,
// capped at LoginTimeout. Only one login is in flight at a time; a concurrent POST
// gets 409.
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	// Audit trail; client_ip is webhttp.ClientIP's spoof-safe resolved host.
	slog.Info("login: request received",
		"client_ip", webhttp.ClientIP(r, h.trusted...),
		"user_agent", logsafe.Field(r.Header.Get("User-Agent")))
	select {
	case h.loginSem <- struct{}{}:
		// Ownership transfers to the reap goroutine once cmd.Start succeeds; an
		// earlier return releases via the semReleased guard.
	default:
		httpreply.Conflict(w, "login in progress")
		return
	}
	// Only fires if we return before the reap goroutine takes ownership.
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
	// Wall-clock cap on the SUBPROCESS, not r.Context(): the client disconnects right
	// after we write the auth URL and comes back minutes later. This bounds an
	// abandoned flow; the select below bounds URL discovery at LoginURLTimeout.
	ctx, cancel := context.WithTimeout(context.Background(), h.cfg.LoginTimeout)
	cmd := exec.CommandContext(ctx, h.cliPath(), buildLoginArgs(provider, region)...) //nolint:gosec // G204: binary path from config
	setProcGroup(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		slog.Error("login: stdout pipe failed",
			"error", err, "cli_path", h.cliPath())
		webhttp.WriteJSONStatus(w, http.StatusInternalServerError,
			httpreply.ErrorJSON("login unavailable"))
		return
	}
	// Bounded, and separate from stdout so stderr chatter does not count against the
	// URL scanner's maxLoginLines cap.
	stderrBuf := procout.NewBuffer(stderrCap)
	cmd.Stderr = stderrBuf
	if err := cmd.Start(); err != nil {
		cancel()
		status := classifyLoginStartErr(err, h.cliPath())
		webhttp.WriteJSONStatus(w, status, httpreply.ErrorJSON("login unavailable"))
		return
	}
	urlCh := make(chan map[string]string, 1)
	// The subprocess keeps writing banners while the user completes the browser flow,
	// so the drain runs in the background or the child wedges on a full pipe.
	// stdoutDone is what the reap goroutine waits on before cmd.Wait closes the pipe.
	stdoutDone := make(chan struct{})
	go func() {
		defer close(stdoutDone)
		scanLoginOutputWithDrain(stdout, urlCh)
	}()

	// From here the sem is held until cmd.Wait returns, so a second attempt during
	// the device-code window still gets 409.
	semReleased = true

	// The browser polls /api/whoami every 3s while the modal is open; without this a
	// fresh cache entry keeps answering signed_out for its full TTL, so the login
	// that just succeeded looks like it never did.
	h.identity.invalidate()

	// Reaped whichever branch of the select wins, so both collect the exit status. On
	// timeout the process GROUP is killed — CommandContext kills only the PID, which
	// orphans helpers holding the stdout pipe — and waited for, to reclaim the FDs.
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
			// No user completion is coming and the device code is wasted either way,
			// so reap eagerly rather than waiting for the hard cap.
			killProcessGroup(cmd)
			<-waitDone
		}
		webhttp.WriteJSON(w, result)
	case <-time.After(h.cfg.LoginURLTimeout):
		killProcessGroup(cmd)
		<-waitDone // bounded: Kill → SIGKILL → reap
		attrs := make([]any, 0, 4)
		attrs = append(attrs, "timeout", h.cfg.LoginURLTimeout)
		attrs = append(attrs, stderrAttr(stderrBuf)...)
		slog.Warn("login: timeout waiting for auth URL", attrs...)
		webhttp.WriteJSONStatus(w, http.StatusGatewayTimeout,
			httpreply.ErrorJSON("timeout waiting for auth URL"))
	}
}
