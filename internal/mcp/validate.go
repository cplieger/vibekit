package mcp

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
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

// NameMaxLen is the byte bound on a server name.
//
// The name becomes the agent's tool prefix (mcp_<name>_<tool>), so the bound is
// a property of that namespace rather than of any one admission door — which is
// why it is exported beside the validator instead of appearing as a literal in
// each caller.
const NameMaxLen = 64

// NameLeadRune reports whether r may open a name. Deliberately ASCII-only: the
// name becomes the agent's tool prefix, and a non-ASCII prefix is not something
// the tool namespace accepts.
//
// This and NameAllowedRune are the ONLY executable statement of the charset in the
// package. There used to be three — a regexp, this pair, and a hard-coded grammar
// string carrying its own copy of the bound — and a change to any one of them could
// leave the others behind.
func NameLeadRune(r rune) bool {
	return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z'
}

// NameAllowedRune reports whether r may appear anywhere in a name (position 2
// onward). The leading position is narrower — see NameLeadRune.
func NameAllowedRune(r rune) bool {
	return NameLeadRune(r) || r >= '0' && r <= '9' || r == '_' || r == '-'
}

// nameGrammar describes the rule for the person reading the rejection.
//
// PROSE, deliberately, built from NameMaxLen. It replaced a regex literal
// that was a second executable grammar in all but enforcement, restating
// the charset and hard-coding the bound.
func nameGrammar() string {
	return "a letter, then letters, digits, underscores or hyphens, up to " +
		strconv.Itoa(NameMaxLen) + " characters"
}

// ValidateName is the ONE admission rule for a server name, implemented
// directly from the rune predicates and the length constant.
//
// Three doors reach a name and they must agree: Validate, ParseServerID,
// and paste.go's sanitizeName (which REPAIRS rather than rejects). All
// three read the same two predicates, so agreement is structural.
func ValidateName(name string) error {
	if err := checkName(name); err != nil {
		return &FieldError{Field: fieldName, Msg: err.Error()}
	}
	return nil
}

// checkName is the rule itself, returning a plain error so the attribution wrapper
// above is the only place that knows about form fields.
func checkName(name string) error {
	if name == "" {
		return fmt.Errorf("name must be %s: %q", nameGrammar(), name)
	}
	if len(name) > NameMaxLen {
		return fmt.Errorf("name too long: %d bytes (max %d)", len(name), NameMaxLen)
	}
	for i, r := range name {
		ok := NameAllowedRune(r)
		if i == 0 {
			ok = NameLeadRune(r)
		}
		if !ok {
			return fmt.Errorf("name must be %s: %q", nameGrammar(), name)
		}
	}
	return nil
}

// keyRe is the character set for env var names and HTTP header names.
// Permissive enough for both (env disallows "-", headers disallow "_"
// on paper; in practice both fly everywhere and we let the server/
// kiro-cli be the final judge).
var keyRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,127}$`)

// Length caps on user-supplied fields. These are defense-in-depth
// bounds: webhttp.MaxJSONBody caps the whole PUT payload, but a single
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

// Wire field names, used for error attribution.
//
// Named rather than spelled at each site because a field name is a CONTRACT with
// the form: the client's field-to-input map keys on exactly these strings, so a
// typo here does not fail a build, it silently stops one input being marked.
const (
	fieldName          = "name"
	fieldTransport     = "transport"
	fieldCommand       = "command"
	fieldArgs          = "args"
	fieldURL           = "url"
	fieldEnvPairs      = "env"
	fieldHeaderPairs   = "headers"
	fieldDisabledTools = "disabled_tools"
	fieldAutoApprove   = "auto_approve"
	fieldOAuthClientID = "oauth_client_id"
	//nolint:gosec // G101: this is the NAME of a wire field, not a credential. The
	// value it names is masked on read and merged from disk on write
	// (see SecretMask / mergeSecret); nothing here holds a secret.
	fieldOAuthClientSecret = "oauth_client_secret"
)

// FieldError is one validation failure, attributed to the wire field it
// came from. Msg is unchanged from what the check always said — an
// indexed message like `headers[1]: duplicate name "X"` keeps its
// index.
type FieldError struct {
	Field string `json:"field"`
	Msg   string `json:"message"`
}

func (e *FieldError) Error() string { return e.Msg }

// maxFieldErrors bounds one response's error list: accumulation turns a
// per-entry check into a per-entry ALLOCATION, so a paste naming
// thousands of bad tool names would otherwise build thousands of
// messages.
const maxFieldErrors = 32

// fieldErrs accumulates independent validation failures.
//
// Two verbs, and the difference is the whole point. addf() attributes a LEAF
// check to its field. merge() splices an error that is already attributed —
// including a joined one from a sub-validator — so nesting keeps every inner
// field instead of collapsing the group under one outer name.
type fieldErrs struct {
	errs []error
}

func (c *fieldErrs) addf(field, format string, args ...any) {
	if len(c.errs) >= maxFieldErrors {
		return
	}
	c.errs = append(c.errs, &FieldError{Field: field, Msg: fmt.Sprintf(format, args...)})
}

func (c *fieldErrs) merge(err error) {
	if err == nil || len(c.errs) >= maxFieldErrors {
		return
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range joined.Unwrap() {
			c.merge(e)
		}
		return
	}
	c.errs = append(c.errs, err)
}

func (c *fieldErrs) any() bool { return len(c.errs) > 0 }

// join returns the accumulated failures as one error, or nil.
//
// errors.Join is the idiomatic fit and needs no dependency: its Error() is the
// newline-joined messages (so every existing substring assertion still matches)
// and errors.Is/As walk into it (so the sentinels the HTTP layer routes on keep
// working through a wrap).
func (c *fieldErrs) join() error { return errors.Join(c.errs...) }

// FieldErrors flattens an error tree into the field failures it carries,
// for a caller that wants to mark inputs rather than print a paragraph.
//
// It walks BOTH wrap shapes: errors.Join's Unwrap() []error, and
// fmt.Errorf("%w")'s Unwrap() error.
func FieldErrors(err error) []FieldError {
	var out []FieldError
	var walk func(error)
	walk = func(e error) {
		if e == nil || len(out) >= maxFieldErrors {
			return
		}
		// The concrete type, deliberately, and not errors.As: As descends the
		// whole tree and would return the FIRST FieldError under a join, so the
		// walk below would never run and a three-field failure would report one.
		// A FieldError wraps nothing, so this branch terminates.
		if fe, ok := e.(*FieldError); ok {
			out = append(out, *fe)
			return
		}
		switch u := e.(type) {
		case interface{ Unwrap() []error }:
			for _, inner := range u.Unwrap() {
				walk(inner)
			}
		case interface{ Unwrap() error }:
			walk(u.Unwrap())
		}
	}
	walk(err)
	return out
}

// Validate checks a fully-populated Server record. Called on every
// create/update before persist. Callers do not run their own checks;
// this is the single source of truth.
//
// It ACCUMULATES rather than short-circuits, so a record with three
// problems is answered once instead of over three round trips.
//
// One short-circuit stays: the transport chain. An unknown transport
// means transportValidators has no entry, so the per-transport check
// CANNOT run.
func Validate(s *Server) error {
	var errs fieldErrs
	errs.merge(ValidateName(s.Name))
	errs.merge(validateTransportChain(s))
	errs.merge(validateToolNames(fieldDisabledTools, s.DisabledTools))
	errs.merge(validateToolNames(fieldAutoApprove, s.AutoApprove))
	return errs.join()
}

// validateTransportChain is the dependent run: each step's input is the previous
// step's verdict, so a failure ends the chain instead of joining a list.
func validateTransportChain(s *Server) error {
	if s.Transport == "" {
		return &FieldError{Field: fieldTransport, Msg: "transport required"}
	}
	if !s.Transport.Valid() {
		return &FieldError{Field: fieldTransport, Msg: fmt.Sprintf("unknown transport: %q", s.Transport)}
	}
	fn, ok := transportValidators[s.Transport]
	if !ok {
		// Unreachable in practice: init() panics at boot if any known transport
		// lacks a validator, and the check above has already refused anything
		// unknown. Kept as the belt to that braces — it is not an accumulation
		// candidate, because it describes a state the process cannot reach.
		return &FieldError{
			Field: fieldTransport,
			Msg:   fmt.Sprintf("no validator registered for transport %q", s.Transport),
		}
	}
	return fn(s)
}

// hasCtl reports whether s contains any C0 control character
// (U+0000..U+001F) or DEL (U+007F). Intentionally rejects \t — RFC 7230
// forbids non-VCHAR/SP/HTAB in header values, so this errs strict. The
// byte-wise scan is correct: every byte of a UTF-8 continuation is >
// 0x7F, so multi-byte runes can never trigger a false positive.
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
//
// The count cap and the per-entry checks are independent, and so is each entry
// from the next, so all of them accumulate. Within one entry the two checks are
// independent too — a name can be both control-bearing and oversize, and saying
// so once per problem is the point.
func validateToolNames(field string, tools []string) error {
	var errs fieldErrs
	if len(tools) > maxDisabledTools {
		errs.addf(field, "%s: too many entries (%d, max %d)",
			field, len(tools), maxDisabledTools)
	}
	for i, t := range tools {
		if hasCtl(t) {
			errs.addf(field, "%s[%d]: control character", field, i)
		}
		if len(t) > disabledToolMax {
			errs.addf(field, "%s[%d]: too long (%d bytes, max %d)",
				field, i, len(t), disabledToolMax)
		}
	}
	return errs.join()
}

func validateStdio(s *Server) error {
	var errs fieldErrs
	errs.merge(validateCommand(s.Command))
	// ONE ERROR PER PRESENT FIELD. These are independent presence checks — the
	// transport is already known, so nothing sequences them — and the whole reason
	// attribution exists is to mark the input that is wrong. Grouping them named
	// `url` and left `headers` unmarked, so a record carrying both got one message
	// and one highlighted box out of two mistakes.
	if s.URL != "" {
		errs.addf(fieldURL, "stdio transport cannot have url")
	}
	if len(s.Headers) > 0 {
		errs.addf(fieldHeaderPairs, "stdio transport cannot have headers")
	}
	if s.OAuthClientID != "" {
		errs.addf(fieldOAuthClientID, "stdio transport cannot have oauth_client_id")
	}
	if s.OAuthClientSecret != "" {
		errs.addf(fieldOAuthClientSecret, "stdio transport cannot have oauth_client_secret")
	}
	errs.merge(validateArgs(s.Args))
	errs.merge(validateKeyPairs(fieldEnvPairs, s.Env, maxEnvEntries, envValueMax, false))
	return errs.join()
}

// validateCommand is a DEPENDENT chain within one field: "command required"
// precedes the control-character and length checks on the same value, because
// those two have nothing to say about a value that is not there.
func validateCommand(command string) error {
	if strings.TrimSpace(command) == "" {
		return &FieldError{Field: fieldCommand, Msg: "command required for stdio transport"}
	}
	var errs fieldErrs
	if hasCtl(command) {
		errs.addf(fieldCommand, "command contains a control character")
	}
	if len(command) > commandMax {
		errs.addf(fieldCommand, "command too long: %d bytes (max %d)", len(command), commandMax)
	}
	return errs.join()
}

// validateArgs accumulates the count cap and every per-entry failure: one arg
// being wrong says nothing about the next.
func validateArgs(args []string) error {
	var errs fieldErrs
	if len(args) > maxArgs {
		errs.addf(fieldArgs, "args: too many entries (%d, max %d)", len(args), maxArgs)
	}
	for i, a := range args {
		if hasCtl(a) {
			errs.addf(fieldArgs, "args[%d] contains a control character", i)
		}
		if len(a) > argMax {
			errs.addf(fieldArgs, "args[%d] too long: %d bytes (max %d)", i, len(a), argMax)
		}
	}
	return errs.join()
}

func validateRemote(s *Server) error {
	var errs fieldErrs
	// Three independent presence checks, three attributions — same reason as the
	// stdio pair above. The grouped form named `command` and left `args` and `env`
	// unmarked, which is exactly the case a pasted stdio block hits when its
	// transport is switched to remote.
	if s.Command != "" {
		errs.addf(fieldCommand, "remote transport cannot have command")
	}
	if len(s.Args) > 0 {
		errs.addf(fieldArgs, "remote transport cannot have args")
	}
	if len(s.Env) > 0 {
		errs.addf(fieldEnvPairs, "remote transport cannot have env")
	}
	errs.merge(validateRemoteURL(s.URL))
	errs.merge(validateOAuthField(fieldOAuthClientID, s.OAuthClientID, oauthClientIDMax))
	errs.merge(validateOAuthField(fieldOAuthClientSecret, s.OAuthClientSecret, oauthClientSecretMax))
	errs.merge(validateKeyPairs(fieldHeaderPairs, s.Headers, maxHeaderEntries, headerValueMax, true))
	return errs.join()
}

// validateRemoteURL is the url field's own chain. The length and control-char
// checks are independent of each other and accumulate; everything after them is
// SEQUENTIAL by necessity — url.Parse has to succeed before Scheme, Host and User
// can be read, and a control character makes Parse fail with a message about
// syntax rather than about the character.
func validateRemoteURL(raw string) error {
	var errs fieldErrs
	if len(raw) > urlMax {
		errs.addf(fieldURL, "url too long: %d bytes (max %d)", len(raw), urlMax)
	}
	if hasCtl(raw) {
		errs.addf(fieldURL, "url contains a control character")
	}
	if errs.any() {
		return errs.join()
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return &FieldError{
			Field: fieldURL,
			Msg:   fmt.Sprintf("url must be an absolute http(s) URL: %q", raw),
		}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return &FieldError{
			Field: fieldURL,
			Msg:   fmt.Sprintf("url scheme must be http or https: %q", u.Scheme),
		}
	}
	// Userinfo in URL would stash a credential outside the Headers
	// secret-masking path — List() returns URL verbatim, so the
	// browser and anyone dumping mcp.json would see the token. Reject
	// at the boundary; users get a clean 400 pointing them at Headers.
	if u.User != nil {
		return &FieldError{
			Field: fieldURL,
			Msg:   "url must not contain userinfo; use Headers for auth",
		}
	}
	return nil
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
	var errs fieldErrs
	if len(value) > maxLen {
		errs.addf(field, "%s too long: %d bytes (max %d)", field, len(value), maxLen)
	}
	if hasCtl(value) {
		errs.addf(field, "%s contains a control character", field)
	}
	return errs.join()
}

// validateKeyPairs enforces the shared shape rules for env entries and
// HTTP header entries: bounded entry count, regex-valid names, unique
// names (case-insensitive for headers, case-sensitive for env), control-
// character-free values, and a length cap per value. Error messages
// follow the "<kind>[i]: ..." format both call sites relied on, so the
// existing validate_test.go assertions continue to match substring-
// wise. See validateStdio / validateRemote for the call sites.
func validateKeyPairs(kind string, pairs []KeyPair, maxEntries, maxValue int, caseInsensitiveDedup bool) error {
	var errs fieldErrs
	if len(pairs) > maxEntries {
		errs.addf(kind, "%s: too many entries (%d, max %d)",
			kind, len(pairs), maxEntries)
	}
	seen := make(map[string]struct{}, len(pairs))
	for i, kv := range pairs {
		// Per-entry accumulation, and the duplicate check accumulates WITH it:
		// duplicate detection is per index, so entry 3 being a repeat of entry 1
		// says nothing about entry 4. A bad name is still recorded in `seen`
		// under its own spelling, so a repeated bad name reports both problems
		// rather than hiding the second behind the first.
		if !keyRe.MatchString(kv.Name) {
			errs.addf(kind, "%s[%d]: bad name %q", kind, i, kv.Name)
		}
		key := kv.Name
		if caseInsensitiveDedup {
			key = strings.ToLower(kv.Name)
		}
		if _, dup := seen[key]; dup {
			errs.addf(kind, "%s[%d]: duplicate name %q", kind, i, kv.Name)
		}
		seen[key] = struct{}{}
		if hasCtl(kv.Value) {
			errs.addf(kind, "%s[%d]: value contains a control character", kind, i)
		}
		if len(kv.Value) > maxValue {
			errs.addf(kind, "%s[%d]: value too long (%d bytes, max %d)",
				kind, i, len(kv.Value), maxValue)
		}
	}
	return errs.join()
}
