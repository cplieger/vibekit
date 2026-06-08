// Package bridge manages a single kiro-cli ACP subprocess.
package bridge

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"sync/atomic"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/sessions"
	"github.com/cplieger/vibekit/internal/version"
)

// Compile-time interface assertion.
var _ api.ACPBridge = (*Bridge)(nil)

// scannerLineCap is the bufio.Scanner line cap for the bridge's stdout.
// Must accommodate a full fsWriteCap (4 MiB) content payload inside a
// JSON envelope after worst-case escaping (non-ASCII, control chars).
// 16 MiB is the safe pick: 4 MiB content + ~100% worst-case JSON
// overhead + envelope slack.
const scannerLineCap = 16 << 20

// stderrLineCap bounds a single kiro-cli stderr line before we drop
// the rest. 64 KiB is generous for panic traces and progress lines;
// anything longer is almost certainly binary garbage and not worth
// carrying into Loki.
const stderrLineCap = 64 * 1024

// errBridgeExited is the sentinel Call returns when a caller's waiter
// is unblocked by readLoop's post-exit drain or by the done-channel
// race-guard. Kept as a var-level errors.New rather than fmt.Errorf
// so callers can errors.Is against it without allocating per-call.
var errBridgeExited = errors.New("ACP bridge exited")

// ACP RPC method names — re-exported from api for package-local use.
// The canonical definitions live in api/methods.go so the full protocol
// vocabulary is discoverable in one place.
const (
	methodInitialize  = api.MethodInitialize
	methodSessionNew  = api.MethodSessionNew
	methodSessionLoad = api.MethodSessionLoad
	methodSetModel    = api.MethodSetModel
)

// Bridge is one kiro-cli ACP subprocess tied to one chat.
type Bridge struct {
	lifecycleCtx context.Context
	stdin        io.WriteCloser
	modes        atomic.Pointer[[]api.SessionMode]
	stdout       *bufio.Scanner
	pending      map[int64]chan *api.RPCResponse
	notifCh      chan *api.RPCResponse
	done         chan struct{}
	models       atomic.Pointer[[]api.SessionModel]
	cmd          *exec.Cmd
	lockMgr      *sessions.Manager
	modelID      api.ModelID
	sessionID    api.SessionID
	workDir      string
	cliPath      string
	currentMode  string
	nextID       atomic.Int64
	stopOnce     sync.Once
	mu           sync.Mutex
	writeMu      sync.Mutex
	pendingMu    sync.Mutex
}

// New returns a fresh bridge. Call Start before any other method.
// lockMgr may be nil if session lock management is not needed.
func New(cliPath, workDir string, opts ...BridgeOption) *Bridge {
	b := &Bridge{
		cliPath: cliPath,
		workDir: workDir,
		pending: make(map[int64]chan *api.RPCResponse),
		notifCh: make(chan *api.RPCResponse, 256),
		done:    make(chan struct{}),
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// BridgeOption configures a Bridge at construction time.
//
//nolint:revive // BridgeOption: name kept for clarity at call sites; bridge.Option would conflict with other package-level Option names callers commonly use.
type BridgeOption func(*Bridge)

// WithSessionManager sets the sessions.Manager used for stale-lock
// removal during session load.
func WithSessionManager(mgr *sessions.Manager) BridgeOption {
	return func(b *Bridge) { b.lockMgr = mgr }
}

// SessionID returns the bridge's ACP session id. Safe to call from
// any goroutine.
func (b *Bridge) SessionID() api.SessionID {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessionID
}

// ModelID returns the currently-selected model id. Safe to call from
// any goroutine.
func (b *Bridge) ModelID() api.ModelID {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.modelID
}

// CurrentMode returns the currently-selected session mode. Safe to
// call from any goroutine.
func (b *Bridge) CurrentMode() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.currentMode
}

// Modes returns the available session modes as declared by the agent
// on session/new or session/load. The returned slice is frozen
// (never mutated after construction); callers MUST NOT mutate it.
func (b *Bridge) Modes() []api.SessionMode {
	if p := b.modes.Load(); p != nil {
		return *p
	}
	return nil
}

// Models returns the available model catalog as declared by the agent
// on session/new or session/load, with [Deprecated] / [Legacy] entries
// filtered out (see api.TagExcluded). The returned slice is frozen
// (never mutated after construction); callers MUST NOT mutate it.
func (b *Bridge) Models() []api.SessionModel {
	if p := b.models.Load(); p != nil {
		return *p
	}
	return nil
}

func (b *Bridge) NotifCh() <-chan *api.RPCResponse { return b.notifCh }

// SetModel performs an in-session model swap via session/set_model.
// On success, updates the bridge's internal model id and returns nil.
// On failure (context too large, model unavailable), returns the error
// and leaves the bridge's model id unchanged.
func (b *Bridge) SetModel(ctx context.Context, modelID string) error {
	b.mu.Lock()
	sessionID := b.sessionID
	b.mu.Unlock()
	_, err := b.Call(ctx, methodSetModel, map[string]any{
		api.KeySessionID: sessionID,
		"modelId":        modelID,
	})
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.modelID = api.ModelID(modelID)
	b.mu.Unlock()
	return nil
}

func (b *Bridge) initialize(ctx context.Context) error {
	// Advertise fs read/write and terminal capabilities. kiro-cli routes
	// file access and command execution through us when these are true.
	// elicitation advertises form-mode MCP elicitation support: kiro-cli
	// only forwards an MCP server's elicitation/create request to us when
	// this capability is present (verified against kiro-cli 2.6.0, which
	// gates forwarding on clientCapabilities.elicitation). Without it the
	// agent has nowhere to surface the prompt and the tool call stalls.
	_, err := b.Call(ctx, methodInitialize, map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs":          map[string]any{"readTextFile": true, "writeTextFile": true},
			"terminal":    true,
			"elicitation": map[string]any{"form": map[string]any{}},
		},
		"clientInfo": map[string]any{
			"name": "vibekit", "title": "Vibekit for Kiro", "version": version.Build,
		},
	})
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	// Developer-oriented intermediate signal; the user-facing
	// "bridge started" breadcrumb in Start() is the authoritative
	// "a bridge exists now" Info line.
	slog.Debug("ACP initialize RPC completed", "version", version.Build)
	return nil
}
