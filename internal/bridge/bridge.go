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
	sessionTitle string
	agentEngine  string
	// extraEnv is appended to the inherited environment of the kiro-cli
	// process this bridge starts. The install manager's active version
	// directory leads PATH through it, so `kiro-cli` resolves any sibling it
	// needs out of the same verified install rather than through whatever else
	// $TOOLS/bin holds. Empty leaves the environment fully inherited.
	extraEnv []string
	// extraArgs are the filtered operator launch flags for this spawn
	// (StartOpts.ExtraArgs). Immutable after Start, like agentEngine.
	extraArgs   []string
	nextID      atomic.Int64
	enableHooks bool
	stopOnce    sync.Once
	mu          sync.Mutex
	writeMu     sync.Mutex
	pendingMu   sync.Mutex
}

// Option configures a Bridge at construction time.
type Option func(*Bridge)

// WithEnv appends extra environment variables to the kiro-cli process this
// bridge starts, on top of the inherited environment. Used to put the install
// manager's active version directory first on PATH.
func WithEnv(env []string) Option {
	return func(b *Bridge) { b.extraEnv = env }
}

// New returns a fresh bridge that runs the kiro-cli binary at cliPath. Call
// Start before any other method.
//
// cliPath is resolved by the CALLER, once per bridge, which is what makes a
// version switch reach the next chat: the bridge is the long-lived consumer
// here, and one is constructed per chat rather than once per process.
func New(cliPath, workDir string, opts ...Option) *Bridge {
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

// SessionTitle returns KAS's own title for the live session, taken from the
// session/new or session/load result's flat `_meta.title`. On creation this is
// always the literal "New Session" placeholder; on load it is the real stored
// title. Empty when no session result has been applied.
//
// This is NOT the authoritative chat name. The caller adopts it only while the
// chat is still default-named (bridge_coord.go), and an agent-authored
// focus_update title outranks it — see translate/focus.go.
func (b *Bridge) SessionTitle() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessionTitle
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
	// from supervised mode, which is KAS's autopilot gate (vibekit-acp.md).
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
	// _meta.kiro.backgroundProcesses opts into KAS's background-process tools
	// (control_bash_process, list_processes, get_process_output). KAS serves
	// them from its own ACPBackgroundProcessManager over standard
	// terminal/create + terminal/output, which vibekit already implements
	// (hub/agent_terminal.go) — so the capability is the whole integration:
	// without it the agent has no way to run a dev server or a watcher
	// without blocking its turn on a foreground command.
	// _meta.kiro.knowledge gates getKnowledgeListing into the system prompt,
	// i.e. it tells the agent WHICH knowledge bases are indexed (four lines
	// per base, undefined when none). vibekit ships the knowledge UI and
	// seeds chat.enableKnowledge, so the index exists and /knowledge works —
	// the agent just could not see what was in it.
	// _meta.kiro.secretStorage opts into KAS's AcpSecretStorage: it then asks
	// the client to HOLD the MCP OAuth credentials it derives (the DCR result,
	// the token set, the PKCE verifier) via _kiro/secret/{get,store,delete}.
	// KAS keeps only an in-process memory copy and has no file of its own, so
	// without this flag the store is never constructed and every bridge spawn
	// re-runs discovery and a fresh POST /register — measured, and measured to
	// stop at zero DCRs once a stored blob is replayed. Answered by
	// hub/bridge_v3_secret.go against internal/secretstore. Declaring it is a
	// COMMITMENT: KAS rethrows a client-side store/delete failure into the MCP
	// connect path, so the handlers must answer on every bridge that declares
	// it, the utility bridge included.
	kiroMeta := map[string]any{
		"openExternalUrl":      true,
		"infrastructureSafety": true,
		"userInput":            true,
		"backgroundProcesses":  true,
		"knowledge":            true,
		"secretStorage":        true,
		"settings":             map[string]any{"codeIntelligence": map[string]any{"enabled": true}},
	}
	if b.enableHooks {
		kiroMeta["hooks"] = map[string]any{"enabled": true, "v2": true}
	}
	if _, err := b.Call(ctx, methodInitialize, map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs": map[string]any{
				"readTextFile":  true,
				"writeTextFile": true,
				// fs._meta.kiro.{stat,readDirectory,delete} claim KAS's own fs
				// verbs. Declaring them CONFINES rather than grants: the
				// else-branch is not a refusal, it is KAS's in-process
				// NodeFileSystem doing the same fs.stat / fs.readdir / fs.rm
				// with no vibekit path check at all. So an agent delete is an
				// unchecked unlink today, and readDirectory is an unfiltered
				// listing — which is how an agent discovers the file the
				// ignore-list read filter would then refuse to open. Handled by
				// hub/bridge_fs_kiro.go, which resolves inside the work dir,
				// filters the listing, and executes; it does not stage or gate
				// (KAS checkpoints before its own delete and restores a
				// rejected one through fs/write_text_file — a second gate here
				// would intercept that restore).
				//
				// readFile / writeFile are deliberately ABSENT. They are the
				// same ladder one rung up, and claiming them would move reads
				// and writes off fs/{read,write}_text_file — the rung that
				// carries the supervised staging path.
				"_meta": map[string]any{"kiro": map[string]any{
					"stat":          true,
					"readDirectory": true,
					"delete":        true,
				}},
			},
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
