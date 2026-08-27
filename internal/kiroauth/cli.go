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
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/runesafe/v2"
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

// ErrNoSource reports that no token source was wired at all (a runtime built
// without WithKiroCLIPath — tests, or a mis-assembled composition).
var ErrNoSource = errors.New("no kiro-cli token source configured")

// diagCap bounds one piece of upstream CLI text folded into an error. The
// error reaches a slog attribute (runtime's "v3 auth: token unavailable") and a
// JSON-RPC error frame back to KAS, so the bound and the sanitizing belong
// at construction rather than at either sink.
const diagCap = 200

// tokenMask replaces a credential in text bound for a log line.
const tokenMask = "[redacted]"

// tokenEnvelope is the get-kas-token stdout shape: a kind discriminator
// plus a kind-specific data payload.
type tokenEnvelope struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

// accessToken best-effort reads the payload's accessToken value, whatever
// kind the envelope claims to be. "" when the payload is not an object or
// carries no such field.
//
// Read independently of kind on purpose: the value is a credential wherever
// it appears, so what must be kept out of an error message cannot be
// decided by trusting the discriminator that sits beside it.
func (e *tokenEnvelope) accessToken() string {
	var d struct {
		AccessToken string `json:"accessToken"`
	}
	if json.Unmarshal(e.Data, &d) != nil {
		return ""
	}
	return d.AccessToken
}

// diagnostic renders untrusted CLI text for an error message: the
// envelope's own access-token value masked out, then sanitized and bounded.
//
// The mask is exact rather than pattern-based — the value comes from the
// same payload being quoted, so no guess about token shape is involved.
// Both quoted fields can carry it. The kind discriminator is arbitrary
// upstream text, and the error payload is echoed whole, which the fuzz
// target refuses to accept on the say-so of a comment about what the CLI
// emits.
//
// Masking runs on BOTH sides of the sanitizer, because the raw payload and
// the decoded token are different representations of the same bytes and
// each pass closes a gap the other cannot reach:
//
//   - Before. A token carrying a rune the sanitizer rewrites (a bidi
//     override, U+2028) stops matching once that rune becomes a space, so
//     the tail of the credential would survive in the text.
//   - After. encoding/json reads an invalid UTF-8 byte as U+FFFD, and the
//     sanitizer normalizes the raw byte the same way, so sanitizing can
//     CONSTRUCT the decoded token out of raw bytes that never contained it.
//     A mask that ran only before it then finds nothing to mask. Pinned by
//     testdata/fuzz/FuzzParseTokenEnvelope/c0be6b2e9525c1d8 and by
//     TestParseTokenEnvelope_ErrorTextIsSafeForALog's ordering case.
//
// Capping comes last for the same ordering reason: a token straddling the
// cap boundary must be gone before the cut, not sliced in half by it.
// Sanitizing at all is the other half of the job — this text lands in a log
// line, where a raw newline forges a record and a C0/C1 introducer writes
// terminal escapes.
func (e *tokenEnvelope) diagnostic(s string) string {
	tok := e.accessToken()
	mask := func(in string) string {
		if tok == "" {
			return in
		}
		return strings.ReplaceAll(in, tok, tokenMask)
	}
	safe := mask(runesafe.SanitizeSingleLine(mask(s)))
	if len(safe) <= diagCap {
		return safe
	}
	return runesafe.CapBytes(safe, diagCap) + "..."
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

	// env returns the environment overlay for the invocation — the install
	// manager's PATH overlay leading with the active version directory. It
	// is REQUIRED for correctness, not hygiene: `kiro-cli chat` delegates
	// to the kiro-cli-chat sidecar by BARE NAME on PATH (it does not look
	// beside its own executable), so without the overlay every invocation
	// fails with ENOENT even though the sidecar sits next to the binary.
	// nil = no overlay (tests).
	env func() []string

	// runCommand is the exec seam for tests; production uses execGetToken.
	runCommand func(ctx context.Context, cliPath string, env []string) ([]byte, error)

	cached *tokenData
	expiry time.Time
	// fetched is when the cached token came back from the CLI, and it is what
	// makes a burst of bridge spawns cost ONE subprocess instead of N.
	//
	// The mutex alone does not do that. It serializes, so N callers run one at a
	// time — but inside the reuseLeeway window the cache check fails for each of
	// them in turn, so every one spends its own 30s-bounded invocation on an
	// answer the caller ahead of it already has, with the lock held throughout.
	// That is every chat's auth callback queued behind the same question.
	fetched time.Time
	mu      sync.Mutex
}

// NewCLISource builds a CLISource over the given binary resolver and
// environment-overlay source (both from the install manager).
func NewCLISource(resolve func() string, env func() []string) *CLISource {
	return &CLISource{resolve: resolve, env: env, runCommand: execGetToken}
}

// Token returns the current access token result for the auth callback:
// {accessToken, expiresAt, profileArn?, authMethod?, provider?}. It invokes
// the CLI at most once per reuseLeeway window; concurrent callers share one
// invocation via the mutex.
func (s *CLISource) Token(ctx context.Context) (map[string]any, error) {
	// Read BEFORE the lock, because the whole question below is whether this
	// caller was already waiting when the token it is about to ask for arrived.
	arrived := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil && time.Until(s.expiry) > reuseLeeway {
		return s.cached.result(), nil
	}
	// Inside the leeway window the CLI is re-asked, but only once per burst: a
	// caller that arrived before the current token was fetched adopts it rather
	// than spending a second subprocess on the same answer. Exact rather than a
	// tuned coalescing window — it never serves a token fetched before the caller
	// asked, so a caller that genuinely wants a newer one still gets one, which is
	// what keeps the sequential near-expiry re-ask (its own test) intact.
	//
	// The expiry re-check is not redundant with it: this branch is reached only
	// when the token is inside the leeway window, and a hard-expired one must be
	// re-asked however recently it arrived.
	if s.cached != nil && s.fetched.After(arrived) && time.Now().Before(s.expiry) {
		return s.cached.result(), nil
	}
	if s.resolve == nil {
		return nil, errCLIUnavailable
	}
	cliPath := s.resolve()
	if cliPath == "" {
		return nil, errCLIUnavailable
	}
	var overlay []string
	if s.env != nil {
		overlay = s.env()
	}
	cctx, cancel := context.WithTimeout(ctx, cliTimeout)
	defer cancel()
	out, err := s.runCommand(cctx, cliPath, overlay)
	if err != nil {
		// stdout is NOT echoed into the error: on success it holds the
		// token, and a partial failure could still carry one.
		return s.vendCachedOrFail(fmt.Errorf("kiro-cli get-kas-token: %w", err))
	}
	data, err := parseTokenEnvelope(out)
	if err != nil {
		return s.vendCachedOrFail(err)
	}
	if data.ProfileArn == "" {
		// A hard turn failure that reads as a service outage, so it is logged
		// rather than left to be inferred. KAS derives its region from segment 4
		// of this ARN and feeds it to the service-client factory, so without it
		// initialize and session/new both succeed and then the FIRST prompt dies
		// with -32000 ModelRegistryUnavailableError, user-facing text "Kiro could
		// not load the available models. Please try again." — which invites
		// retrying forever. _kiro/account/getUsage fails the same way.
		//
		// Vended anyway rather than refused: the token is valid, KAS tolerates the
		// absence on accounts where it can resolve a region another way, and
		// turning a working session into a client-side refusal would be worse than
		// the failure this warns about.
		slog.Warn("kiro-cli vended a token with no CodeWhisperer profile ARN; "+
			"the model registry and account usage will fail for this session",
			"remedy", "kiro-cli login")
	}
	exp, tErr := time.Parse(time.RFC3339Nano, data.ExpiresAt)
	if tErr != nil {
		// Vend it anyway (KAS parses expiresAt itself) but never cache a
		// token whose expiry we cannot judge. That also withdraws it from the
		// coalescing branch above, which is right: a token nothing can judge must
		// not be handed to a caller that did not ask for it.
		s.cached, s.expiry, s.fetched = nil, time.Time{}, time.Time{}
		return data.result(), nil
	}
	s.cached, s.expiry, s.fetched = data, exp, time.Now()
	return data.result(), nil
}

// vendCachedOrFail answers a failed invocation with the cached token when that
// token is still valid, and with the error otherwise. MUST be called with s.mu
// held.
//
// The window this covers is the only one there is: the cache is read in exactly
// one place, gated on more than reuseLeeway remaining, so between T-5min and T-0
// every callback re-invokes the CLI and ANY blip there — a network hiccup during
// the CLI's own refresh, or cliTimeout firing — used to become a hard auth
// failure. The user saw the sign-in banner and a failed turn on a session that
// was working, and readiness flipped unready, while a token good for up to five
// more minutes sat in this struct.
//
// It mirrors the reference host's own contract: a background refresh failure is
// logged and swallowed precisely because the cached token is still valid. The
// cache is deliberately NOT cleared — the next callback retries the CLI, and
// discarding a live credential to record a transient failure is the defect.
func (s *CLISource) vendCachedOrFail(err error) (map[string]any, error) {
	if s.cached != nil && time.Now().Before(s.expiry) {
		slog.Warn("kiro-cli token refresh failed; vending the cached token, which is still valid",
			"expires_in", time.Until(s.expiry).Round(time.Second), "error", err)
		return s.cached.result(), nil
	}
	return nil, err
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
// object, echoed through diagnostic so that a payload carrying token
// material cannot put a credential in a log line.
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
		return nil, fmt.Errorf("kiro-cli reports auth unavailable (log in again): %s", env.diagnostic(string(env.Data)))
	default:
		return nil, fmt.Errorf("kiro-cli get-kas-token: unexpected envelope kind %q", env.diagnostic(env.Kind))
	}
}

// execGetToken runs `<cliPath> chat _ get-kas-token` and returns stdout.
// stderr is discarded: the CLI logs progress there, and folding it into
// errors risks echoing sensitive material into logs. env is the PATH
// overlay; exec.Cmd deduplicates duplicate keys taking the LAST entry, so
// appending it to the ambient environment makes the overlay win.
func execGetToken(ctx context.Context, cliPath string, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, cliPath, "chat", "_", "get-kas-token")
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("exec: %w", err)
	}
	return out, nil
}
