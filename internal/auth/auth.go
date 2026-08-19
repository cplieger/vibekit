// Package auth wires the /api/whoami, /api/login, /api/logout endpoints
// that shell out to the bundled kiro-cli binary for identity operations.
// No state persists in the package beyond the CLI path resolved at
// construction time.
package auth

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/httpreply"
)

// Compile-time interface assertion.
var _ api.RouteHandler = (*Handler)(nil)

// Config holds per-instance timeout configuration. Tests construct
// a Handler with short timeouts directly via WithConfig; production
// passes a config built by the composition layer's ConfigFromEnv.
type Config struct {
	LoginURLTimeout time.Duration
	LoginTimeout    time.Duration
	LogoutTimeout   time.Duration
	WhoamiTimeout   time.Duration
}

// DefaultConfig is the production configuration.
var DefaultConfig = Config{
	LoginURLTimeout: 10 * time.Second,
	LoginTimeout:    16 * time.Minute,
	LogoutTimeout:   10 * time.Second,
	WhoamiTimeout:   5 * time.Second,
}

// Scanner caps for scanLoginOutput: a generous per-line limit guards
// against accidental 64 KiB overflows from debug dumps embedded in the
// login banner, and maxLoginLines bounds total memory at roughly
// maxScanLineBytes * maxLoginLines worst case.
const (
	maxScanLineBytes = 256 * 1024
	maxLoginLines    = 200
)

// Subprocess output caps. Legitimate whoami output is ~150 bytes of
// JSON; the 64 KiB cap is three orders of magnitude of headroom and
// catches a pathological/malicious kiro-cli replacement that tries to
// OOM the container via unbounded stdout. The logout cap matches in
// spirit: a confirmation banner fits easily in 1 MiB.
const (
	whoamiMaxOutput = buffer.DefaultOutputCap
	logoutMaxOutput = 1 << 20 // 1 MiB

	// stderrCap bounds subprocess stderr capture across every
	// handler (whoami, login, logout uses a combined stdout+stderr
	// cap via logoutMaxOutput). A hostile or runaway kiro-cli can
	// otherwise OOM the container via unbounded stderr on any
	// endpoint. 32 KiB fits every legitimate kiro-cli diagnostic
	// with several orders of magnitude of headroom.
	stderrCap = 32 * 1024
)

// Maximum byte lengths for login request fields. A well-formed start URL
// is under ~200 bytes; the 2048 ceiling matches browser URL limits.
const (
	maxProviderLen = 2048
	maxRegionLen   = 32
)

// awsRegionRe matches AWS region ids across all partitions: commercial
// (us-east-1), China (cn-north-1), GovCloud (us-gov-west-1), ISO
// (us-iso-east-1, us-isob-east-1, eu-isoe-west-1). Format is a
// two-letter partition, one or more lowercase-alpha groups joined by
// single hyphens, then a trailing digit. Each interior segment must be
// non-empty (the `+` quantifier on `[a-z]+`) so `us--east-1` still
// fails. Rejects flag-smuggling (`--help`), shell metacharacters,
// whitespace, uppercase, and empty interior segments.
var awsRegionRe = regexp.MustCompile(`^[a-z]{2}(?:-[a-z]+)+-\d+$`)

// Handler is the /api/whoami + /api/login + /api/logout endpoint bundle.
// It shells out to kiro-cli for identity operations; no state persists
// beyond the CLI path. `loginSem` serialises login subprocesses for the
// full device-flow lifetime: vibekit is single-user, and a browser
// double-click or LAN probe would otherwise spawn duplicate kiro-cli
// subprocesses each pinning their own AWS device code for the full
// LoginTimeout (16m). The semaphore is released by the reap
// goroutine when cmd.Wait returns (user completes flow, or hard cap
// fires), not when the HTTP handler returns — so a second POST that
// arrives after the first URL has been emitted but while the first
// subprocess is still alive still gets 409.
type Handler struct {
	loginSem chan struct{}
	// cliPath resolves the kiro-cli binary at CALL time. It is a function
	// because the install manager selects the active version after the listener
	// binds and can switch it later, so a path captured at construction would
	// pin whoami/login/logout to whatever was installed first — and on a first
	// boot that is nothing at all.
	cliPath func() string
	// trusted is the reverse-proxy network set passed to
	// webhttp.ClientIP when recording the client IP in the login/logout
	// audit logs. Nil (unconfigured) = log the unspoofable socket peer.
	trusted []*net.IPNet
	cfg     Config
}

// Option configures a Handler at construction time.
type Option func(*Handler)

// WithConfig overrides the default timeout configuration. Called
// from the composition layer with env-derived config and from tests
// with shorter timeouts. Having ONE construction-time config-injection
// path, exercised by both production and tests, keeps deadcode happy
// and ensures tests exercise the same wiring production uses.
func WithConfig(cfg Config) Option {
	return func(h *Handler) { h.cfg = cfg }
}

// WithTrustedProxies sets the reverse-proxy networks trusted when
// resolving the client IP for the login/logout audit logs via
// webhttp.ClientIP. Empty/nil trusts nothing, so the unspoofable
// socket-peer host is logged (the spoof-safe default for a
// directly-exposed deployment).
func WithTrustedProxies(trusted []*net.IPNet) Option {
	return func(h *Handler) { h.trusted = trusted }
}

// NewHandler returns an auth handler that shells out to the kiro-cli binary
// cliPath resolves to for whoami / login / logout operations. The resolver is
// consulted per call, never cached (see Handler.cliPath).
func NewHandler(cliPath func() string, opts ...Option) *Handler {
	h := &Handler{cliPath: cliPath, loginSem: make(chan struct{}, 1), cfg: DefaultConfig}
	for _, o := range opts {
		o(h)
	}
	return h
}

// RegisterRoutes wires the auth handler's HTTP endpoints onto mux:
//   - GET  /api/whoami — current kiro-cli identity
//   - POST /api/login  — device-code login flow
//   - POST /api/logout — clear local credentials
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/whoami", h.handleWhoami)
	mux.HandleFunc("/api/login", h.handleLogin)
	mux.HandleFunc("/api/logout", h.handleLogout)
}

// limitedWriter is a package-level alias for httpreply.LimitedWriter.
// Kept as a type alias so existing construction sites remain unchanged.
type limitedWriter = httpreply.LimitedWriter

// stderrAttr returns slog key/value attributes for a captured stderr
// buffer, omitting the "stderr" key entirely when the buffer is empty.
// The common timeout case is "subprocess stuck on read/sleep wrote
// nothing"; emitting `stderr=""` pads every such log line with a
// useless key-value pair and gives the false impression that stderr
// was empty-but-captured rather than empty-and-unavailable.
func stderrAttr(stderr *bytes.Buffer) []any {
	s := api.SanitizeOutput(strings.TrimSpace(stderr.String()))
	if s == "" {
		return nil
	}
	return []any{"stderr", s}
}

// loginReap bundles the state handed off from handleLogin to the reap
// goroutine. Ownership transfers atomically: after the go statement,
// the reap goroutine is the sole owner of ctx cancellation, cmd.Wait,
// stderrBuf reads, and the waitDone close.
//
// stdoutDone is closed by the scanner goroutine after it finishes
// reading the stdout pipe (URL found, scanner error, or EOF). The reap
// goroutine waits on this channel before calling cmd.Wait — per Go's
// exec.Cmd docs, "it is incorrect to call Wait before all reads from
// the pipe have completed" because Wait closes the pipe and races a
// concurrent reader. Without this gate, fast-exiting subprocesses
// (e.g. tests using a `cat + exit` fake CLI on a slow CI runner)
// could see Wait close the pipe before the scanner reads anything,
// surfacing as `read |0: file already closed` errors.
type loginReap struct {
	ctx        context.Context
	cancel     context.CancelFunc
	cmd        *exec.Cmd
	stderrBuf  *bytes.Buffer
	stdoutDone <-chan struct{}
	waitDone   chan struct{}
}

// lineRing holds the first N and last N lines pushed into it, capping
// each line at perLineCap bytes. Used for the line-cap diagnostic log
// in scanLoginOutput. Zero-alloc for the first 2*halfCap lines; after
// that, each Push allocates a new backing array of halfCap strings
// (bounded, not growing).
type lineRing struct {
	first      []string
	last       []string
	halfCap    int
	perLineCap int
}

func newLineRing(halfCap, perLineCap int) *lineRing {
	return &lineRing{
		first:      make([]string, 0, halfCap),
		last:       make([]string, 0, halfCap),
		halfCap:    halfCap,
		perLineCap: perLineCap,
	}
}

// Push appends line to the ring, truncating at perLineCap bytes so an
// adversarial CLI can't blow up a single structured log attribute.
func (r *lineRing) Push(line string) {
	if len(line) > r.perLineCap {
		line = line[:r.perLineCap]
	}
	switch {
	case len(r.first) < r.halfCap:
		r.first = append(r.first, line)
	case len(r.last) == r.halfCap:
		r.last = append(r.last[1:], line)
	default:
		r.last = append(r.last, line)
	}
}

// Sample returns the concatenation of the first-N and last-N slices
// for a single slog attribute.
func (r *lineRing) Sample() []string {
	return slices.Concat(r.first, r.last)
}
