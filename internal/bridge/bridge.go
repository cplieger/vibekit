// Package bridge manages a single kiro-cli ACP subprocess.
package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cplieger/vibekit/internal/kascap"
	"github.com/cplieger/vibekit/internal/version"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// scannerLineCap is the per-frame content cap for the bridge's stdout: a full
// fsWriteCap (4 MiB) payload plus worst-case JSON escaping and envelope slack.
// Exceeding it is survivable — the frame is drained and dropped (bridge_frame.go).
const scannerLineCap = 16 << 20

// stdoutBufSize is the ReadSlice window for the stdout frame reader, NOT the frame
// cap: a larger frame is assembled across several ErrBufferFull reads.
const stdoutBufSize = 64 * 1024

// stderrLineCap bounds a single kiro-cli stderr line. 64 KiB is generous for panic
// traces; anything longer is almost certainly binary garbage.
const stderrLineCap = 64 * 1024

// errBridgeExited aliases the exported sentinel Call returns when a waiter is
// unblocked by readLoop's post-exit drain. It must stay an ALIAS: two distinct values
// with the same text would make errors.Is fail and the retry loop spin on a corpse.
var errBridgeExited = vibekit.ErrBridgeExited

// ACP RPC method names, re-exported for package-local use. The canonical definitions
// live in vibekit/methods.go so the protocol vocabulary is discoverable in one place.
const (
	methodInitialize  = vibekit.MethodInitialize
	methodSessionNew  = vibekit.MethodSessionNew
	methodSessionLoad = vibekit.MethodSessionLoad
	methodSetMode     = vibekit.MethodSetMode
)

// metaKeyKiro is the vendor namespace inside an ACP `_meta` object. Every extension
// key on this wire hangs off it, on three different requests, and a block written one
// level up or down is IGNORED rather than rejected.
const metaKeyKiro = "kiro"

// The two per-session composer choices vibekit sends inside _meta.kiro on session/new.
// KAS reads them as `kiroMeta.modelId` and `kiroMeta.effortLevel`.
const (
	metaKeyModelID     = "modelId"
	metaKeyEffortLevel = "effortLevel"
)

// session/set_config_option param keys, named because three call sites spell them.
const (
	keyConfigID    = "configId"
	keyConfigValue = "value"
)

// Bridge is one kiro-cli ACP subprocess tied to one chat.
type Bridge struct {
	// lifecycleCtx bounds the subprocess: the receiving half of StartOpts.Lifetime,
	// assigned by Start, which refuses a nil one. A lifetime HANDLE rather than a
	// stashed caller context — never a request or turn context.
	lifecycleCtx context.Context
	stdin        io.WriteCloser
	modes        atomic.Pointer[[]vibekit.SessionMode]
	stdout       *frameReader
	pending      map[int64]chan pendingReply
	notifCh      chan vibekit.Notification
	done         chan struct{}
	models       atomic.Pointer[[]vibekit.SessionModel]
	// servedModels is every model id session/new advertised, UNFILTERED. models above
	// drops end-of-life entries for the picker; this one must not, because a deprecated
	// model the account can still use has to pass the entitlement check.
	servedModels atomic.Pointer[[]string]
	cmd          *exec.Cmd
	// envAllow re-permits names the credential screen would drop (bridge_env.go).
	envAllow     map[string]struct{}
	cliPath      string
	modelID      vibekit.ModelID
	workDir      string
	sessionID    vibekit.SessionID
	currentMode  string
	sessionTitle string
	// effortLevel is the tier the session last REPORTED, off the `effortLevel` option's
	// currentValue — observed rather than requested, which makes applyInitialEffort a
	// repair. Empty means unknown and must assert. ObserveEffort feeds the other channel.
	effortLevel string
	// extraArgs are the filtered operator launch flags for this spawn
	// (StartOpts.ExtraArgs). Immutable after Start.
	extraArgs []string
	// extraEnv is appended to the inherited environment of the kiro-cli process, so the
	// install manager's active version directory leads PATH and `kiro-cli` resolves a
	// sibling out of the same verified install. Empty leaves the screened inherited env.
	extraEnv []string
	// presets are the KAS policy-preset ids this session opens with. Immutable after
	// Start for a stronger reason than symmetry: KAS does not persist the ids, so both
	// session doors must send the SAME set or a resumed chat changes posture silently.
	presets []string
	// deliveredSeq counts the notifications readLoop has pushed onto notifCh and is the
	// sequence stamped on each. Touched only by readLoop's goroutine — published on the
	// frame and on the reply to a pending request, never read across the boundary.
	deliveredSeq uint64
	// loadSeq is the read-loop position the `session/load` response arrived at, published
	// by SessionLoadSeq. Guarded by b.mu, unlike deliveredSeq: written on the goroutine
	// that issued the load, read wherever a decision is ordered against the replay.
	loadSeq     uint64
	nextID      atomic.Int64
	stopOnce    sync.Once
	mu          sync.Mutex
	writeMu     sync.Mutex
	pendingMu   sync.Mutex
	enableHooks bool
	// secretStorage gates the `_meta.kiro.secretStorage` declaration in initialize.
	secretStorage bool
	// toolSearch and knowledge gate the `settings.toolSearch` row and the two
	// `knowledge` rows. Immutable after Start, like presets: KAS resolves both at
	// session creation and freezes them, so both doors must describe one spawn.
	toolSearch bool
	knowledge  bool
	// memory gates the `userMemoryOptIn` row's VALUE (never its presence) and contributes
	// KIRO_FEATURE_MEMORY_EXTERNAL_ENABLED to the child environment. Immutable after
	// Start, and the environment is fixed when the subprocess starts anyway.
	memory bool
}

// Option configures a Bridge at construction time.
type Option func(*Bridge)

// WithEnv appends extra environment variables to the kiro-cli process this bridge
// starts. Used to put the install manager's active version directory first on PATH.
func WithEnv(env []string) Option {
	return func(b *Bridge) { b.extraEnv = env }
}

// WithEnvAllow re-permits credential-shaped names the inherit screen would drop.
// Built by ParseEnvAllowlist from EnvAllowVar; see bridge_env.go.
func WithEnvAllow(allowed map[string]struct{}) Option {
	return func(b *Bridge) { b.envAllow = allowed }
}

// New returns a fresh bridge that runs the kiro-cli binary at cliPath. Call Start
// before any other method. cliPath is resolved by the CALLER, once per bridge, which
// is what makes a version switch reach the next chat.
func New(cliPath, workDir string, opts ...Option) *Bridge {
	b := &Bridge{
		cliPath: cliPath,
		workDir: workDir,
		pending: make(map[int64]chan pendingReply),
		notifCh: make(chan vibekit.Notification, 256),
		done:    make(chan struct{}),
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// SessionID returns the bridge's ACP session id. Safe from any goroutine.
func (b *Bridge) SessionID() vibekit.SessionID {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessionID
}

// SessionLoadSeq returns the read-loop position the `session/load` response arrived at,
// which a consumer must have folded up to before treating that replay as complete.
// Set by `session/load` ONLY, so session/new and a failed load answer 0 — and 0 is
// also a legal position, so pair this with the fact that the load returned.
func (b *Bridge) SessionLoadSeq() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.loadSeq
}

// ModelID returns the currently-selected model id. Safe from any goroutine.
func (b *Bridge) ModelID() vibekit.ModelID {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.modelID
}

// CurrentMode returns the currently-selected session mode. Safe from any goroutine.
func (b *Bridge) CurrentMode() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.currentMode
}

// SessionTitle returns KAS's own title for the live session, from the session result's
// flat `_meta.title` — always the "New Session" placeholder on creation, the real
// stored title on load. NOT the authoritative chat name: the caller adopts it only
// while the chat is default-named, and a focus_update title outranks it.
func (b *Bridge) SessionTitle() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessionTitle
}

// Modes returns the available session modes as declared on session/new or
// session/load. The returned slice is frozen; callers MUST NOT mutate it.
func (b *Bridge) Modes() []vibekit.SessionMode {
	if p := b.modes.Load(); p != nil {
		return *p
	}
	return nil
}

// Models returns the available model catalog with [Deprecated] / [Legacy] entries
// filtered out (modeltext.Hidden). Frozen; callers MUST NOT mutate it.
func (b *Bridge) Models() []vibekit.SessionModel {
	if p := b.models.Load(); p != nil {
		return *p
	}
	return nil
}

// ServedModels returns every model id this session advertised, including the
// end-of-life entries Models filters out. Empty means "entitlement unknowable", which
// callers must read as allow. Frozen; callers MUST NOT mutate it.
func (b *Bridge) ServedModels() []string {
	if p := b.servedModels.Load(); p != nil {
		return *p
	}
	return nil
}

// NotifCh returns incoming ACP notifications, each carrying the read loop's sequence.
func (b *Bridge) NotifCh() <-chan vibekit.Notification { return b.notifCh }

// SetModel performs an in-session model swap via session/set_config_option (configId
// "model") — the v3 replacement for the removed session/set_model. On failure the
// bridge's model id is left unchanged.
func (b *Bridge) SetModel(ctx context.Context, modelID string) error {
	b.mu.Lock()
	sessionID := b.sessionID
	b.mu.Unlock()
	_, err := b.Call(ctx, vibekit.MethodSetConfigOption, map[string]any{
		vibekit.KeySessionID: sessionID,
		keyConfigID:          vibekit.ConfigOptionModel,
		keyConfigValue:       modelID,
	})
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.modelID = vibekit.ModelID(modelID)
	// A swap can reset the session's effort level and the bridge cannot see that it did:
	// KAS reconciles against the NEW model's tier list (measured on 2.19.1 — a swap to
	// `auto`, which offers none, destroys it). Clearing makes the next call assert.
	b.effortLevel = ""
	b.mu.Unlock()
	return nil
}

// EnsureEffort makes the live session run at `level` via session/set_config_option
// (configId "effortLevel"). The ONE spelling of that call. Differs-only against what
// the session last REPORTED, which is safe rather than optimistic because SetModel
// CLEARS the cache. An invalid level is dropped rather than sent, and the cache is
// written from the REPLY: KAS silently ignores a level the current model lacks.
func (b *Bridge) EnsureEffort(ctx context.Context, level string) error {
	if level == "" || !vibekit.EffortLevel(level).Valid() {
		return nil
	}
	b.mu.Lock()
	sessionID := b.sessionID
	current := b.effortLevel
	b.mu.Unlock()
	if level == current {
		return nil
	}
	resp, err := b.Call(ctx, vibekit.MethodSetConfigOption, map[string]any{
		vibekit.KeySessionID: sessionID,
		keyConfigID:          vibekit.ConfigOptionEffort,
		keyConfigValue:       level,
	})
	if err != nil {
		return err
	}
	// Record what the session now REPORTS, never what was asked for. Probed on 2.19.1:
	// setting a level on a session at the `auto` model returns ok with no effortLevel
	// option at all, so storing the request would leave the bridge believing a tier.
	var out struct {
		ConfigOptions []sessionConfigOption `json:"configOptions"`
	}
	if resp != nil && len(resp.Result) > 0 && json.Unmarshal(resp.Result, &out) == nil {
		b.mu.Lock()
		b.applyEffortConfigOptionLocked(out.ConfigOptions)
		b.mu.Unlock()
	}
	return nil
}

// ObserveEffort records a level the SESSION reported on the one channel this bridge
// does not read: the `config_option_update` notification it forwards unread. Without
// it the cache goes stale-OPTIMISTIC where it matters — KAS moves the level on its own
// (first-prompt model pin, a swap from the IDE or TUI) and a bridge still believing its
// own request compares equal and skips the repair. An empty level is ignored.
func (b *Bridge) ObserveEffort(level string) {
	if level == "" {
		return
	}
	b.mu.Lock()
	b.effortLevel = level
	b.mu.Unlock()
}

// spawn packages this bridge's per-spawn facts for the kascap table's gated rows,
// which gate several different ways — presence, value, presence-from-emptiness — so one
// boolean would not do. All are immutable after Start, so this reads them without the
// mutex, and it is one method because both doors must describe the SAME spawn.
func (b *Bridge) spawn() kascap.Spawn {
	return kascap.Spawn{
		SecretStorage: b.secretStorage,
		Hooks:         b.enableHooks,
		Presets:       b.presets,
		ToolSearch:    b.toolSearch,
		Knowledge:     b.knowledge,
		Memory:        b.memory,
	}
}

func (b *Bridge) initialize(ctx context.Context) error {
	initStart := time.Now()
	// The _meta.kiro block is DECLARED in internal/kascap rather than built here. This
	// is the CONNECTION door; the session door is built from the same table by
	// withSessionMeta, and a key belongs to whichever one KAS reads it from.
	kiroMeta := kascap.Capabilities(b.spawn())

	// Advertise fs read/write and terminal: kiro-cli routes file access and command
	// execution through us when these are true. elicitation is what makes kiro-cli
	// forward an MCP server's elicitation/create; without it the tool call stalls.
	if _, err := b.Call(ctx, methodInitialize, map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs": map[string]any{
				"readTextFile":  true,
				"writeTextFile": true,
				// fs._meta.kiro.{stat,readDirectory,delete} claim KAS's own fs verbs, and
				// declaring them CONFINES rather than grants: the else-branch is KAS's own
				// NodeFileSystem with no vibekit path check. readFile / writeFile are
				// deliberately ABSENT — claiming them moves writes off the guarded rung.
				"_meta": map[string]any{metaKeyKiro: map[string]any{
					"stat":          true,
					"readDirectory": true,
					"delete":        true,
				}},
			},
			// terminal:true routes every agent shell command through vibekit's own
			// terminal/* handlers, so vibekit owns the pid, argv and output ring. THE
			// TRAP: registering any client tool whose id is in KAS's CORE_IO_TOOL_IDS
			// flips `hasClientIOTools` and silently unbounds the agent's ExecuteBash.
			"terminal":    true,
			"elicitation": map[string]any{"form": map[string]any{}},
			"_meta":       map[string]any{metaKeyKiro: kiroMeta},
		},
		"clientInfo": map[string]any{
			"name": "vibekit", "title": "Vibekit for Kiro", "version": version.Build,
		},
	}); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	// Developer-oriented; Start()'s "bridge started" is the authoritative line.
	// elapsed_ms isolates the initialize round trip so a change to it is attributable.
	slog.Debug("ACP initialize RPC completed",
		"version", version.Build,
		"elapsed_ms", time.Since(initStart).Milliseconds(),
	)
	return nil
}
