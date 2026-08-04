// Package kiroauth answers the v3 (KAS) host-mediated auth callback
// (_kiro/auth/getAccessToken) by asking kiro-cli itself for a fresh access
// token.
//
// kiro-cli acp --agent-engine v3 runs with --auth=acp-callback: the agent
// asks the CLIENT for the bearer token instead of reading its own store.
// Since kiro-cli 2.16 the CLI keeps sole custody of the login (its sqlite
// state store; `kiro-cli login` no longer writes the ~/.aws/sso/cache token
// file), and the endorsed host interface is the internal subcommand the
// CLI's own TUI host shells for exactly this callback:
//
//	kiro-cli chat _ get-kas-token
//	→ {"kind":"getKasToken","data":{accessToken, expiresAt, profileArn?,
//	   authMethod?, provider?}}
//	→ {"kind":"error","data":...} when logged out / refresh failed
//
// The CLI performs any SSO-OIDC refresh itself and persists the rotated
// refresh token in its own store. vibekit deliberately does NOT refresh
// tokens any more: the previous file-based reader ran its own SSO-OIDC
// refresh chain, and two independent refreshers of one rotating refresh
// token fork the chain — whichever side refreshes second holds a dead
// token ("invalid_grant: Invalid refresh token provided").
package kiroauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

// cliTimeout bounds one get-kas-token invocation. Matches the reference
// host (kiro-cli's own TUI shells the subcommand with a 30s cap): the call
// may perform a network token refresh, so a bare exec timeout would be too
// tight and no timeout would wedge the auth callback.
const cliTimeout = 30 * time.Second

// reuseLeeway is how far from expiry a cached token may still be vended
// without re-invoking the CLI. KAS itself refreshes through the callback
// only when INSIDE its own ~3 minute pre-expiry buffer, so anything we
// cache longer than that risks vending a token KAS immediately rejects;
// 5 minutes mirrors KAS's buffer with margin and keeps a burst of bridge
// spawns from stampeding N subprocess invocations.
const reuseLeeway = 5 * time.Minute

// errCLIUnavailable reports that no kiro-cli binary is resolvable (the
// install manager has no active version yet).
var errCLIUnavailable = errors.New("kiro-cli is not available yet (install pending or failed)")

// ErrNoSource reports that no token source was wired at all (a hub built
// without WithKiroCLIPath — tests, or a mis-assembled composition).
var ErrNoSource = errors.New("no kiro-cli token source configured")

// tokenEnvelope is the get-kas-token stdout shape: a kind discriminator
// plus a kind-specific data payload.
type tokenEnvelope struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

// tokenData is the getKasToken payload. AuthMethod and Provider ride the
// callback reply when present (KAS's middleware maps authMethod onto the
// upstream TokenType header), so they are preserved, not dropped.
type tokenData struct {
	AccessToken string `json:"accessToken"`
	ExpiresAt   string `json:"expiresAt"`
	ProfileArn  string `json:"profileArn,omitempty"`
	AuthMethod  string `json:"authMethod,omitempty"`
	Provider    string `json:"provider,omitempty"`
}

// CLISource vends access tokens for the v3 auth callback by shelling
// kiro-cli's get-kas-token. One instance is shared by every bridge: the
// mutex serializes invocations so N bridges spawning at once produce one
// subprocess, and the short cache answers the rest.
type CLISource struct {
	// resolve returns the absolute path of the active kiro-cli binary, or
	// "" when none is installed yet. A function rather than a string so a
	// version switch reaches the next callback (same contract as the
	// bridge factory's cliPath).
	resolve func() string

	// runCommand is the exec seam for tests; production uses execGetToken.
	runCommand func(ctx context.Context, cliPath string) ([]byte, error)

	cached *tokenData
	expiry time.Time
	mu     sync.Mutex
}

// NewCLISource builds a CLISource over the given binary resolver.
func NewCLISource(resolve func() string) *CLISource {
	return &CLISource{resolve: resolve, runCommand: execGetToken}
}

// Token returns the current access token result for the auth callback:
// {accessToken, expiresAt, profileArn?, authMethod?, provider?}. It invokes
// the CLI at most once per reuseLeeway window; concurrent callers share one
// invocation via the mutex.
func (s *CLISource) Token(ctx context.Context) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil && time.Until(s.expiry) > reuseLeeway {
		return s.cached.result(), nil
	}
	if s.resolve == nil {
		return nil, errCLIUnavailable
	}
	cliPath := s.resolve()
	if cliPath == "" {
		return nil, errCLIUnavailable
	}
	cctx, cancel := context.WithTimeout(ctx, cliTimeout)
	defer cancel()
	out, err := s.runCommand(cctx, cliPath)
	if err != nil {
		// stdout is NOT echoed into the error: on success it holds the
		// token, and a partial failure could still carry one.
		return nil, fmt.Errorf("kiro-cli get-kas-token: %w", err)
	}
	data, err := parseTokenEnvelope(out)
	if err != nil {
		return nil, err
	}
	exp, tErr := time.Parse(time.RFC3339Nano, data.ExpiresAt)
	if tErr != nil {
		// Vend it anyway (KAS parses expiresAt itself) but never cache a
		// token whose expiry we cannot judge.
		s.cached, s.expiry = nil, time.Time{}
		return data.result(), nil
	}
	s.cached, s.expiry = data, exp
	return data.result(), nil
}

// Invalidate drops the cached token so the next Token call re-invokes the
// CLI. Called when KAS rejects a vended token: the CLI may have re-logged
// in (new sqlite state) while a still-unexpired-looking cache lingers here.
func (s *CLISource) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cached, s.expiry = nil, time.Time{}
}

// result renders the callback reply map. Optional fields are omitted when
// empty — the reference host does the same, and KAS treats a present-but-
// empty authMethod differently from an absent one.
func (d *tokenData) result() map[string]any {
	res := map[string]any{
		"accessToken": d.AccessToken,
		"expiresAt":   d.ExpiresAt,
	}
	if d.ProfileArn != "" {
		res["profileArn"] = d.ProfileArn
	}
	if d.AuthMethod != "" {
		res["authMethod"] = d.AuthMethod
	}
	if d.Provider != "" {
		res["provider"] = d.Provider
	}
	return res
}

// parseTokenEnvelope decodes get-kas-token stdout. A kind:"error" envelope
// is the CLI's logged-out / refresh-failed verdict; its data is a reason
// object safe to fold into the error (it never carries token material).
func parseTokenEnvelope(out []byte) (*tokenData, error) {
	var env tokenEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, fmt.Errorf("kiro-cli get-kas-token: unparseable output (%d bytes): %w", len(out), err)
	}
	switch env.Kind {
	case "getKasToken":
		var data tokenData
		if err := json.Unmarshal(env.Data, &data); err != nil {
			return nil, fmt.Errorf("kiro-cli get-kas-token: bad data payload: %w", err)
		}
		if data.AccessToken == "" {
			return nil, errors.New("kiro-cli get-kas-token: empty access token")
		}
		return &data, nil
	case "error":
		return nil, fmt.Errorf("kiro-cli reports auth unavailable (log in again): %s", truncate(string(env.Data), 200))
	default:
		return nil, fmt.Errorf("kiro-cli get-kas-token: unexpected envelope kind %q", env.Kind)
	}
}

// execGetToken runs `<cliPath> chat _ get-kas-token` and returns stdout.
// stderr is discarded: the CLI logs progress there, and folding it into
// errors risks echoing sensitive material into logs.
func execGetToken(ctx context.Context, cliPath string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, cliPath, "chat", "_", "get-kas-token")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("exec: %w", err)
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
