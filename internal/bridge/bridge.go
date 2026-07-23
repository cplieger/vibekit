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
	methodSetMode     = api.MethodSetMode
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
	modelID      api.ModelID
	sessionID    api.SessionID
	workDir      string
	cliPath      string
	currentMode  string
	agentEngine  string
	nextID       atomic.Int64
	enableHooks  bool
	stopOnce     sync.Once
	mu           sync.Mutex
	writeMu      sync.Mutex
	pendingMu    sync.Mutex
}

// New returns a fresh bridge. Call Start before any other method.
func New(cliPath, workDir string) *Bridge {
	return &Bridge{
		cliPath: cliPath,
		workDir: workDir,
		pending: make(map[int64]chan *api.RPCResponse),
		notifCh: make(chan *api.RPCResponse, 256),
		done:    make(chan struct{}),
	}
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

// NotifCh returns the channel of incoming ACP notifications from the bridge subprocess.
func (b *Bridge) NotifCh() <-chan *api.RPCResponse { return b.notifCh }

// SetModel performs an in-session model swap via session/set_config_option
// (configId "model") — the v3 (KAS) replacement for the removed
// session/set_model. On success, updates the bridge's internal model id
// and returns nil. On failure (context too large, model unavailable,
// InvalidModelError), returns the error and leaves the model id unchanged.
func (b *Bridge) SetModel(ctx context.Context, modelID string) error {
	b.mu.Lock()
	sessionID := b.sessionID
	b.mu.Unlock()
	_, err := b.Call(ctx, api.MethodSetConfigOption, map[string]any{
		api.KeySessionID: sessionID,
		"configId":       api.ConfigOptionModel,
		"value":          modelID,
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
	// _meta.kiro.openExternalUrl opts into KAS's _kiro/openExternalUrl
	// request (open a URL for the user, e.g. an MCP OAuth page).
	// _meta.kiro capabilities. openExternalUrl advertises that we can open
	// a URL for the user; KAS (v3) gates its _kiro/openExternalUrl request
	// on it (proactively opening an MCP server's OAuth page — the client
	// surfaces a clickable banner, no auto-open; see hub/bridge_v3_auth.go).
	// hooks opts into KAS's v2 hook engine. Set on the utility bridge (so
	// _kiro/hooks/list|setEnabled|triggerHook are available for the
	// hooks-management dashboard) AND on chat bridges (so the workspace's
	// user-authored .kiro/hooks/*.json hooks autofire on their triggers
	// during an agent turn). In v2 mode KAS loads the hook files and runs
	// runCommand hooks internally — it does not call back the client to run
	// autofired hooks. See hub/hooks.go and hub/bridge_coord.go.
	// _meta.kiro.infrastructureSafety opts into KAS's Infrastructure-Safety
	// gate for infrastructure-as-code tool calls. Safe-by-default: KAS installs
	// the gate only when this capability AND an AWS governance flag
	// (infraSafetyMonitor|infraSafetyEnforce) are both set, and that flag is off
	// by default on individual/Builder-ID accounts — so declaring it has zero
	// effect there (verified on a live probe: no gate, getProperties returns []).
	// It is required for the gate's statusChanged/propertiesChanged notifications
	// to ever surface (translate/safety.go); on an enterprise account with the
	// flag on, enforce mode can block infra-as-code writes remotely. Distinct
	// from vibekit's own Supervised write-gate (vibekit-supervised.md).
	// _meta.kiro.settings.codeIntelligence opts every session into KAS's
	// native code tool (tree-sitter symbol navigation always; LSP-backed
	// rename/references/diagnostics once the workspace is initialized —
	// hub/code_intel.go). This is the client-owned settings channel KAS
	// reads into clientMeta (the sqlite chat.enableCodeIntelligence
	// setting does NOT apply to acp mode); lab-verified against 2.13.0.
	// Costs nothing when unused: with no lsp.json and no servers on
	// PATH the LSP operations degrade gracefully and tree-sitter still
	// works.
	// _meta.kiro.userInput opts into KAS's _kiro/userInput request (2.14+):
	// the agent's structured questions (plan-mode clarifications, spec
	// gates) arrive as answerable A→C requests with full option metadata
	// (descriptions, recommended, sub-options) instead of being flattened
	// into permission prompts — and free-form questions are SURFACED
	// instead of silently skipped (without the capability KAS advances
	// past them). Handled by translate/user_input.go; answered via the
	// user_input_response command.
	kiroMeta := map[string]any{
		"openExternalUrl":      true,
		"infrastructureSafety": true,
		"userInput":            true,
		"settings":             map[string]any{"codeIntelligence": map[string]any{"enabled": true}},
	}
	if b.enableHooks {
		kiroMeta["hooks"] = map[string]any{"enabled": true, "v2": true}
	}
	if _, err := b.Call(ctx, methodInitialize, map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs":          map[string]any{"readTextFile": true, "writeTextFile": true},
			"terminal":    true,
			"elicitation": map[string]any{"form": map[string]any{}},
			"_meta":       map[string]any{"kiro": kiroMeta},
		},
		"clientInfo": map[string]any{
			"name": "vibekit", "title": "Vibekit for Kiro", "version": version.Build,
		},
	}); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	// Developer-oriented intermediate signal; the user-facing
	// "bridge started" breadcrumb in Start() is the authoritative
	// "a bridge exists now" Info line.
	slog.Debug("ACP initialize RPC completed", "version", version.Build)
	return nil
}
