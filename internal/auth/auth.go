// Package auth serves /api/whoami, /api/login and /api/logout by shelling out to
// the bundled kiro-cli binary. It persists no state of its own.
package auth

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/procout"
	"github.com/cplieger/vibekit/internal/sanitize"
)

// Config holds per-instance timeouts, supplied through WithConfig.
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

// Scanner caps for scanLoginOutput: the per-line limit absorbs debug dumps in
// the login banner, and maxLoginLines bounds total memory.
const (
	maxScanLineBytes = 256 * 1024
	maxLoginLines    = 200
)

// Subprocess stdout caps: legitimate whoami output is ~150 bytes of JSON, so
// these exist to stop a hostile kiro-cli replacement OOMing the container.
const (
	whoamiMaxOutput = buffer.DefaultOutputCap
	logoutMaxOutput = 1 << 20 // 1 MiB

	// stderrCap bounds stderr capture on every endpoint, for the same reason.
	stderrCap = 32 * 1024
)

// Maximum byte lengths for login request fields; 2048 matches browser URL limits.
const (
	maxProviderLen = 2048
	maxRegionLen   = 32
)

// childWaitDelay bounds Wait past the context's deadline. A BACKSTOP only:
// boundChild's Cancel kills the process group, so this is reached solely by a
// descendant that escaped the group or sits in uninterruptible sleep.
const childWaitDelay = time.Second

// awsRegionRe matches AWS region ids in every partition (us-east-1,
// cn-north-1, us-gov-west-1, us-isob-east-1). Interior segments must be
// non-empty, so it also rejects flag smuggling, shell metacharacters,
// whitespace, uppercase and `us--east-1`.
var awsRegionRe = regexp.MustCompile(`^[a-z]{2}(?:-[a-z]+)+-\d+$`)

// Handler is the /api/whoami + /api/login + /api/logout endpoint bundle.
//
// loginSem serialises login subprocesses for the whole device-flow lifetime, so
// a double-click cannot pin two AWS device codes at once. The reap goroutine
// releases it when cmd.Wait returns, not when the handler returns, so a second
// POST arriving after the first URL was emitted still gets 409.
type Handler struct {
	loginSem chan struct{}
	// identity is what /api/whoami answers from, so the endpoint's page-load and
	// SSE-reconnect traffic never triggers a read (see identityCache).
	identity *identityCache
	// cliPath resolves the binary at CALL time: the install manager picks the
	// active version after the listener binds and can switch it later, and on a
	// first boot there is nothing installed yet.
	cliPath func() string
	// trusted is the reverse-proxy set handed to webhttp.ClientIP for the
	// login/logout audit logs. Nil = log the unspoofable socket peer.
	trusted []*net.IPNet
	cfg     Config
}

// Option configures a Handler at construction time.
type Option func(*Handler)

// WithConfig overrides the default timeout configuration.
func WithConfig(cfg Config) Option {
	return func(h *Handler) { h.cfg = cfg }
}

// WithTrustedProxies sets the reverse-proxy networks trusted when resolving the
// client IP for the login/logout audit logs. Empty or nil trusts nothing, so the
// unspoofable socket peer is logged.
func WithTrustedProxies(trusted []*net.IPNet) Option {
	return func(h *Handler) { h.trusted = trusted }
}

// NewHandler returns an auth handler that shells out to whatever binary cliPath
// resolves to. The resolver is consulted per call, never cached.
func NewHandler(cliPath func() string, opts ...Option) *Handler {
	h := &Handler{cliPath: cliPath, loginSem: make(chan struct{}, 1), cfg: DefaultConfig}
	for _, o := range opts {
		o(h)
	}
	// After the options: the cache captures the read budget WithConfig sets.
	h.identity = newIdentityCache(h.readIdentity, h.cfg.WhoamiTimeout)
	return h
}

// RegisterRoutes wires GET /api/whoami, POST /api/login and POST /api/logout
// onto mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/whoami", h.handleWhoami)
	mux.HandleFunc("/api/login", h.handleLogin)
	mux.HandleFunc("/api/logout", h.handleLogout)
}

// stderrAttr returns slog attributes for captured stderr, omitting the key when
// the buffer is empty: `stderr=""` would read as captured-and-empty rather than
// unavailable.
func stderrAttr(stderr *procout.Buffer) []any {
	s := sanitize.Output(strings.TrimSpace(stderr.String()))
	if s == "" {
		return nil
	}
	return []any{"stderr", s}
}

// boundChild makes a subprocess honour ITS CONTEXT'S deadline rather than the
// child's own lifetime. exec.CommandContext's cancellation SIGKILLs the parent
// PID only, and every kiro-cli invocation forks helpers inheriting the pipe write
// ends, so Wait blocks until the LAST descendant exits. Group-killing Cancel and
// WaitDelay are both load-bearing: under a 50ms context against a child running
// `sleep 10`, either alone returned in 10.0s and 1.05s, the pair in 50ms.
func boundChild(cmd *exec.Cmd) {
	setProcGroup(cmd)
	cmd.Cancel = func() error {
		err := killGroup(cmd)
		// exec treats os.ErrProcessDone from Cancel as nothing to report, so
		// mirroring os.Process.Kill's mapping keeps Wait's error untouched for an
		// already-reaped child.
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = childWaitDelay
}

// loginReap is the state handleLogin hands to the reap goroutine, which becomes
// sole owner of ctx cancellation, cmd.Wait, stderrBuf and the waitDone close.
//
// It must wait for stdoutDone (closed by the scanner) before calling cmd.Wait:
// exec.Cmd forbids Wait before every pipe read has completed, because Wait
// closes the pipe under the reader.
type loginReap struct {
	ctx        context.Context
	cancel     context.CancelFunc
	cmd        *exec.Cmd
	stderrBuf  *procout.Buffer
	stdoutDone <-chan struct{}
	waitDone   chan struct{}
}

// lineRing holds the first N and last N lines pushed into it, each capped at
// perLineCap bytes.
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

// Push appends line, truncating at perLineCap bytes so an adversarial CLI cannot
// blow up one log attribute.
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

// Sample returns the first-N and last-N lines concatenated, for one slog
// attribute.
func (r *lineRing) Sample() []string {
	return slices.Concat(r.first, r.last)
}
