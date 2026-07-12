package mcp

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Package-level errors. All HTTP handlers map these to 4xx responses;
// anything else is a 500. ErrPersist wraps the underlying filesystem
// error from SaveJSON's temp+rename so writeErr can route mutator
// failures to 500 with a generic body (no filesystem path leaked to
// the browser) while the full error still shows up in slog.
var (
	ErrNotFound     = errors.New("server not found")
	ErrNameConflict = errors.New("server name already exists")
	ErrPersist      = errors.New("persist failed")

	// ErrPersistMarshal and ErrPersistWrite are typed sub-sentinels for
	// ErrPersist. Callers can still match the parent via
	// errors.Is(err, ErrPersist); the sub-sentinels let the HTTP layer
	// log at different levels (marshal = programmer bug, write = transient infra).
	ErrPersistMarshal = fmt.Errorf("%w: marshal", ErrPersist)
	ErrPersistWrite   = fmt.Errorf("%w: write", ErrPersist)
)

// nameRe is the character set for a server's display name. Matches the
// MCP tool-name prefix convention (mcp_<server>_<tool>) where <server>
// must be a valid identifier segment.
var nameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

// keyRe is the character set for env var names and HTTP header names.
// Permissive enough for both (env disallows "-", headers disallow "_"
// on paper; in practice both fly everywhere and we let the server/
// kiro-cli be the final judge).
var keyRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,127}$`)

// Length caps on user-supplied fields. These are defense-in-depth
// bounds: api.MaxJSONBody caps the whole PUT payload, but a single
// 500 KB env value still slows every masked read and bloats mcp.json.
// Values are generous enough to accept any realistic MCP config.
const (
	commandMax       = 512
	urlMax           = 2048
	argMax           = 4096
	maxArgs          = 64
	envValueMax      = 32 * 1024 // 32 KiB
	maxEnvEntries    = 64
	headerValueMax   = 8 * 1024 // 8 KiB
	maxHeaderEntries = 32
	disabledToolMax  = 128
	maxDisabledTools = 256
	// oauthClientIDMax bounds the OAuth 2.0 client_id length. Real-world
	// client IDs are typically 20–80 chars (UUID-ish or app-id-ish);
	// 256 leaves headroom and rejects clearly-malformed input.
	oauthClientIDMax = 256
	// oauthClientSecretMax bounds the OAuth 2.0 client_secret length.
	// Secrets are typically 40-128 chars; 512 leaves ample headroom.
	oauthClientSecretMax = 512
)

// transportValidators maps each supported transport to its validation
// function. Adding a new transport requires only a map entry, not a
// control-flow change. The init() below validates that every known
// transport has a registered validator, preventing nil-call panics.
// TransportSSE shares validateRemote with TransportHTTP: both are remote
// transports whose wire shape is url + headers (+ optional oauth), differing
// only in the ACP `type` discriminator emitted at export time.
var transportValidators = map[Transport]func(*Server) error{
	TransportStdio: validateStdio,
	TransportHTTP:  validateRemote,
	TransportSSE:   validateRemote,
}

func init() {
	for _, t := range []Transport{TransportStdio, TransportHTTP, TransportSSE} {
		if _, ok := transportValidators[t]; !ok {
			panic("mcp: no validator registered for transport " + string(t))
		}
	}
}

// Validate checks a fully-populated Server record. Called on every
// create/update before persist. Callers do not run their own checks;
// this is the single source of truth.
func Validate(s *Server) error {
	if !nameRe.MatchString(s.Name) {
		return fmt.Errorf("name must be [A-Za-z][A-Za-z0-9_-]{0,63}: %q", s.Name)
	}
	if s.Transport == "" {
		return errors.New("transport required")
	}
	if !s.Transport.Valid() {
		return fmt.Errorf("unknown transport: %q", s.Transport)
	}
	fn, ok := transportValidators[s.Transport]
	if !ok {
		return fmt.Errorf("no validator registered for transport %q", s.Transport)
	}
	if err := fn(s); err != nil {
		return err
	}
	if err := validateToolNames("disabled_tools", s.DisabledTools); err != nil {
		return err
	}
	return validateToolNames("auto_approve", s.AutoApprove)
}

// hasCtl reports whether s contains any C0 control character
// (U+0000..U+001F) or DEL (U+007F). Intentionally rejects \t — none of
// the fields validated here (command, url, args, env value, header
// value, disabled-tool name) has a legitimate need for tabs, and RFC
// 7230 forbids non-VCHAR/SP/HTAB in header values specifically, so we
// err on the strict side. The byte-wise scan is correct: every byte of
// a UTF-8 continuation (0x80..0xBF) is > 0x7F, so multi-byte runes
// can never trigger a false positive.
func hasCtl(s string) bool {
	for i := range len(s) {
		c := s[i]
		if c < 0x20 || c == 0x7F {
			return true
		}
	}
	return false
}

// validateToolNames enforces the shared shape rules for a list of MCP
// tool names (disabled_tools, auto_approve): bounded count, no control
// characters, per-entry length cap. field names the list in errors.
func validateToolNames(field string, tools []string) error {
	if len(tools) > maxDisabledTools {
		return fmt.Errorf("%s: too many entries (%d, max %d)",
			field, len(tools), maxDisabledTools)
	}
	for i, t := range tools {
		if hasCtl(t) {
			return fmt.Errorf("%s[%d]: control character", field, i)
		}
		if len(t) > disabledToolMax {
			return fmt.Errorf("%s[%d]: too long (%d bytes, max %d)",
				field, i, len(t), disabledToolMax)
		}
	}
	return nil
}

func validateStdio(s *Server) error {
	if strings.TrimSpace(s.Command) == "" {
		return errors.New("command required for stdio transport")
	}
	if hasCtl(s.Command) {
		return errors.New("command contains a control character")
	}
	if len(s.Command) > commandMax {
		return fmt.Errorf("command too long: %d bytes (max %d)", len(s.Command), commandMax)
	}
	if s.URL != "" || len(s.Headers) > 0 {
		return errors.New("stdio transport cannot have url or headers")
	}
	if s.OAuthClientID != "" {
		return errors.New("stdio transport cannot have oauth_client_id")
	}
	if s.OAuthClientSecret != "" {
		return errors.New("stdio transport cannot have oauth_client_secret")
	}
	if len(s.Args) > maxArgs {
		return fmt.Errorf("args: too many entries (%d, max %d)", len(s.Args), maxArgs)
	}
	for i, a := range s.Args {
		if hasCtl(a) {
			return fmt.Errorf("args[%d] contains a control character", i)
		}
		if len(a) > argMax {
			return fmt.Errorf("args[%d] too long: %d bytes (max %d)", i, len(a), argMax)
		}
	}
	return validateKeyPairs("env", s.Env, maxEnvEntries, envValueMax, false)
}

func validateRemote(s *Server) error {
	if s.Command != "" || len(s.Args) > 0 || len(s.Env) > 0 {
		return errors.New("remote transport cannot have command, args or env")
	}
	if len(s.URL) > urlMax {
		return fmt.Errorf("url too long: %d bytes (max %d)", len(s.URL), urlMax)
	}
	if hasCtl(s.URL) {
		return errors.New("url contains a control character")
	}
	u, err := url.Parse(s.URL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("url must be an absolute http(s) URL: %q", s.URL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("url scheme must be http or https: %q", u.Scheme)
	}
	// Userinfo in URL would stash a credential outside the Headers
	// secret-masking path — List() returns URL verbatim, so the
	// browser and anyone dumping mcp.json would see the token. Reject
	// at the boundary; users get a clean 400 pointing them at Headers.
	if u.User != nil {
		return errors.New("url must not contain userinfo; use Headers for auth")
	}
	if err := validateOAuthField("oauth_client_id", s.OAuthClientID, oauthClientIDMax); err != nil {
		return err
	}
	if err := validateOAuthField("oauth_client_secret", s.OAuthClientSecret, oauthClientSecretMax); err != nil {
		return err
	}
	return validateKeyPairs("headers", s.Headers, maxHeaderEntries, headerValueMax, true)
}

// validateOAuthField enforces the shared length-cap + control-character
// rules for the optional oauth_client_id / oauth_client_secret fields. An
// empty value is allowed (both are optional). The error wording matches
// what the two inline call sites used, so validate_test.go substring
// assertions still match.
func validateOAuthField(field, value string, maxLen int) error {
	if value == "" {
		return nil
	}
	if len(value) > maxLen {
		return fmt.Errorf("%s too long: %d bytes (max %d)", field, len(value), maxLen)
	}
	if hasCtl(value) {
		return fmt.Errorf("%s contains a control character", field)
	}
	return nil
}

// validateKeyPairs enforces the shared shape rules for env entries and
// HTTP header entries: bounded entry count, regex-valid names, unique
// names (case-insensitive for headers, case-sensitive for env), control-
// character-free values, and a length cap per value. Error messages
// follow the "<kind>[i]: ..." format both call sites relied on, so the
// existing validate_test.go assertions continue to match substring-
// wise. See validateStdio / validateRemote for the call sites.
func validateKeyPairs(kind string, pairs []KeyPair, maxEntries, maxValue int, caseInsensitiveDedup bool) error {
	if len(pairs) > maxEntries {
		return fmt.Errorf("%s: too many entries (%d, max %d)",
			kind, len(pairs), maxEntries)
	}
	seen := make(map[string]struct{}, len(pairs))
	for i, kv := range pairs {
		if !keyRe.MatchString(kv.Name) {
			return fmt.Errorf("%s[%d]: bad name %q", kind, i, kv.Name)
		}
		key := kv.Name
		if caseInsensitiveDedup {
			key = strings.ToLower(kv.Name)
		}
		if _, dup := seen[key]; dup {
			return fmt.Errorf("%s[%d]: duplicate name %q", kind, i, kv.Name)
		}
		seen[key] = struct{}{}
		if hasCtl(kv.Value) {
			return fmt.Errorf("%s[%d]: value contains a control character",
				kind, i)
		}
		if len(kv.Value) > maxValue {
			return fmt.Errorf("%s[%d]: value too long (%d bytes, max %d)",
				kind, i, len(kv.Value), maxValue)
		}
	}
	return nil
}
