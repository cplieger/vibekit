// Package bridge manages a single kiro-cli ACP subprocess.
package bridge

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"sync/atomic"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/kascap"
	"github.com/cplieger/vibekit/internal/version"
)

// Compile-time interface assertion.
var _ api.ACPBridge = (*Bridge)(nil)

// scannerLineCap is the per-frame content cap for the bridge's stdout.
// Must accommodate a full fsWriteCap (4 MiB) content payload inside a
// JSON envelope after worst-case escaping (non-ASCII, control chars).
// 16 MiB is the safe pick: 4 MiB content + ~100% worst-case JSON
// overhead + envelope slack.
//
// Exceeding it is survivable, not fatal: the frame is drained to its terminator
// and dropped, and the stream continues. See bridge_frame.go.
const scannerLineCap = 16 << 20

// stdoutBufSize is the ReadSlice window for the stdout frame reader. It is NOT
// the frame cap: a frame larger than this is assembled across several
// ErrBufferFull reads, up to scannerLineCap.
const stdoutBufSize = 64 * 1024

// stderrLineCap bounds a single kiro-cli stderr line before we drop
// the rest. 64 KiB is generous for panic traces and progress lines;
// anything longer is almost certainly binary garbage and not worth
// carrying into Loki.
const stderrLineCap = 64 * 1024

// errBridgeExited is the sentinel Call returns when a caller's waiter
// is unblocked by readLoop's post-exit drain or by the done-channel
// race-guard. Kept as a var-level errors.New rather than fmt.Errorf
// so callers can errors.Is against it without allocating per-call.
// errBridgeExited aliases the exported sentinel so a caller in another package
// can classify a dead bridge with errors.Is. It must stay an alias rather than
// its own errors.New: two distinct values with the same text would make the
// classification silently fail and the retry loop would spin on a corpse again.
var errBridgeExited = api.ErrBridgeExited

// ACP RPC method names — re-exported from api for package-local use.
// The canonical definitions live in api/methods.go so the full protocol
// vocabulary is discoverable in one place.
const (
	methodInitialize  = api.MethodInitialize
	methodSessionNew  = api.MethodSessionNew
	methodSessionLoad = api.MethodSessionLoad
	methodSetMode     = api.MethodSetMode
)

// metaKeyKiro is the vendor namespace inside an ACP `_meta` object. Every
// extension key on this wire hangs off it, on three different requests, and a
// block written one level up or down is IGNORED rather than rejected — so the one
// spelling is worth having in one place.
const metaKeyKiro = "kiro"

// session/set_config_option param keys. Named because three call sites spell
// them: the model, the reasoning effort and the supervised-mode autopilot.
const (
	keyConfigID    = "configId"
	keyConfigValue = "value"
)

// Bridge is one kiro-cli ACP subprocess tied to one chat.
type Bridge struct {
	// lifecycleCtx bounds the subprocess: the receiving half of
	// api.StartOpts.Lifetime, assigned by Start, which refuses a nil one. It is
	// a lifetime HANDLE rather than a stashed caller context — never a request
	// or turn context — which is why it is required at the method that runs the
	// process instead of defaulted anywhere.
	lifecycleCtx context.Context
	stdin        io.WriteCloser
	modes        atomic.Pointer[[]api.SessionMode]
	stdout       *frameReader
	pending      map[int64]chan *api.RPCResponse
	notifCh      chan *api.RPCResponse
	done         chan struct{}
	models       atomic.Pointer[[]api.SessionModel]
	// servedModels is every model id session/new advertised, UNFILTERED. models
	// above drops end-of-life entries for the picker; this one must not, because
	// it is the input to the entitlement check and a deprecated model the account
	// can still use has to pass it.
	servedModels atomic.Pointer[[]string]
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
	// $TOOLS/bin holds. Empty leaves the inherited environment (screened, see
	// bridge_env.go) as the whole environment.
	extraEnv []string
	// envAllow re-permits names the credential screen would otherwise drop
	// (bridge_env.go, EnvAllowVar). Nil is the shipped configuration.
	envAllow map[string]struct{}
	// extraArgs are the filtered operator launch flags for this spawn
	// (StartOpts.ExtraArgs). Immutable after Start, like agentEngine.
	extraArgs   []string
	nextID      atomic.Int64
	enableHooks bool
	// secretStorage gates the `_meta.kiro.secretStorage` declaration in
	// initialize (StartOpts.SecretStorage). Immutable after Start, like
	// enableHooks.
	secretStorage bool
	stopOnce      sync.Once
	mu            sync.Mutex
	writeMu       sync.Mutex
	pendingMu     sync.Mutex
}

// Option configures a Bridge at construction time.
type Option func(*Bridge)

// WithEnv appends extra environment variables to the kiro-cli process this
// bridge starts, on top of the inherited environment. Used to put the install
// manager's active version directory first on PATH.
func WithEnv(env []string) Option {
	return func(b *Bridge) { b.extraEnv = env }
}

// WithEnvAllow re-permits credential-shaped names the inherit screen would drop.
// Built by ParseEnvAllowlist from EnvAllowVar; see bridge_env.go for which
// direction the screen guards.
func WithEnvAllow(allowed map[string]struct{}) Option {
	return func(b *Bridge) { b.envAllow = allowed }
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

// ServedModels returns every model id this session advertised, including the
// end-of-life entries Models filters out. Empty when the session advertised no
// catalog, which callers must read as "entitlement unknowable" and allow.
// The returned slice is frozen; callers MUST NOT mutate it.
func (b *Bridge) ServedModels() []string {
	if p := b.servedModels.Load(); p != nil {
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
		keyConfigID:      api.ConfigOptionModel,
		keyConfigValue:   modelID,
	})
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.modelID = api.ModelID(modelID)
	b.mu.Unlock()
	return nil
}

// spawn packages this bridge's per-spawn facts for the kascap table's gated
// rows. SecretStorage decides the secretStorage key's VALUE (the key is present
// either way) and Hooks decides whether the hooks key is present AT ALL — two
// different mechanisms, which is why one boolean would not do.
//
// Both fields are immutable after Start, so this reads them without the mutex,
// and it is one method rather than a literal at each call site because the
// connection door and the session door must describe the SAME spawn: a bridge
// that declared a capability at initialize and then contradicted itself at
// session/new would be a defect no test looks for.
func (b *Bridge) spawn() kascap.Spawn {
	return kascap.Spawn{SecretStorage: b.secretStorage, Hooks: b.enableHooks}
}

func (b *Bridge) initialize(ctx context.Context) error {
	// The _meta.kiro block is DECLARED in internal/kascap rather than built
	// here: which call carries each key, how KAS resolves it, whether an ABSENT
	// key resolves true, whether vibekit sends it at all, and why. Every key's
	// rationale moved there verbatim, beside the declaration it belongs to,
	// along with the semanticReview row this literal had no way to express.
	//
	// This is the CONNECTION door. The session door is built from the same
	// table by withSessionMeta (bridge_session.go); a key belongs to whichever
	// one KAS reads it from, which is the table's door column.
	kiroMeta := kascap.Capabilities(b.spawn())

	// Advertise fs read/write and terminal capabilities. kiro-cli routes
	// file access and command execution through us when these are true.
	// elicitation advertises form-mode MCP elicitation support: kiro-cli
	// only forwards an MCP server's elicitation/create request to us when
	// this capability is present (verified against kiro-cli 2.6.0, which
	// gates forwarding on clientCapabilities.elicitation). Without it the
	// agent has nowhere to surface the prompt and the tool call stalls.
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
				"_meta": map[string]any{metaKeyKiro: map[string]any{
					"stat":          true,
					"readDirectory": true,
					"delete":        true,
				}},
			},
			// terminal:true is what routes every agent shell command through
			// vibekit's own terminal/* handlers (hub/agent_terminal.go) rather
			// than an in-process spawn inside KAS, so vibekit owns the pid, the
			// argv and the output ring for all agent shell work.
			//
			// It also decides WHICH ExecuteBash the agent gets, and that is not
			// obvious from here. KAS ships two: an ACP-origin one with no upper
			// bound, and a clamped one at min(input.timeout ?? 120000, 1800000).
			// vibekit gets the clamped one, so an agent command carries a 30
			// minute ceiling, because `hasClientIOTools` is false and
			// mergeTools' second argument wins.
			//
			// The trap: registering ANY client tool whose id is in KAS's
			// CORE_IO_TOOL_IDS set flips `hasClientIOTools` and silently
			// promotes the agent to the UNBOUNDED variant. Nothing logs it and
			// no test would catch it, so the 30 minute ceiling would disappear
			// as a side effect of an unrelated feature. If a future change wants
			// to register such a tool, the bound has to be reintroduced here
			// deliberately rather than lost by accident.
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
	// Developer-oriented intermediate signal; the user-facing
	// "bridge started" breadcrumb in Start() is the authoritative
	// "a bridge exists now" Info line.
	slog.Debug("ACP initialize RPC completed", "version", version.Build)
	return nil
}
