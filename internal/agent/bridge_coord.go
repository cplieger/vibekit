package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/keyenc"
	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/command"
	"github.com/cplieger/vibekit/internal/durable"
	"github.com/cplieger/vibekit/internal/push"
	"github.com/cplieger/vibekit/internal/settings"
	"github.com/cplieger/vibekit/internal/translate"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// BridgeCoordinator owns bridge lifecycle: spawn, load, prime, notification
// forwarding, model switching and turn finalization.
type BridgeCoordinator struct {
	bridge    *bridges
	chatStore bridgeChatRecords
	// turns is the per-chat turn lifecycle and the exclusion every terminal step
	// claims through. See turn.go.
	turns          *turnRegistry
	broadcast      func(ctx context.Context, e vibekit.ServerEvent)
	translateEvent func(chatID vibekit.ChatID, msg *vibekit.RPCResponse)
	// push is optional; every send site nil-checks, so no push service means no
	// notification rather than a refusal to run.
	push        pushNotifier `wiring:"optional"`
	mcpRegistry *mcpRegistry
	lifecycle   *lifetime
	// installed later by SetPreBridgeSpawn from the composition root.
	preBridgeSpawn func(context.Context) `wiring:"optional"`
	// replayProjection is the session/load replay-projection lifecycle. Nil in
	// tests that do not exercise a load.
	replayProjection replayProjector
	// onSessionRehydrated fires after a successful session/load, off the spawn
	// path: the runtime heals the runs that chat's dying process paused.
	onSessionRehydrated func(vibekit.ChatID)
	// onTurnClosed fires from the WINNING closer once a turn has finalized; the
	// agent-terminal registry evicts that turn's retired output. A closure rather
	// than the collaborator, which is built after this literal. Nil in tests.
	onTurnClosed func(vibekit.ChatID, vibekit.TurnEpoch)
	// secretStorage reports whether the runtime holds a credential store, read at
	// SPAWN time: a bool captured here runs before NewHub opens the store, so it
	// would be false for every bridge this process ever starts.
	secretStorage func() bool `wiring:"optional"`
	// chatStatus reads a chat's last self-declared status, which is what the
	// agent-finished push body says instead of a fixed literal.
	chatStatus func(vibekit.ChatID) vibekit.ChatStatusPayload
	// unknownStops records the stop reasons already warned about, so an unmapped
	// wire value produces one line rather than one per turn.
	unknownStops sync.Map
	// primeFrom notes which chat's transcript primes a chat's FIRST session, for a
	// tangent whose session/fork was refused (command/fork.go). Claimed and deleted
	// by the next spawn, so it is a handoff rather than state anything reads twice.
	primeFrom map[vibekit.ChatID]vibekit.ChatID
	// agentEngine is the kiro-cli agent engine, hard-pinned to v3 by
	// resolveAgentEngine.
	agentEngine string
	// acpArgs are the filtered operator launch flags (VIBEKIT_KIRO_ACP_ARGS), set on
	// CHAT spawns only: an `--effort max` on the utility bridge would spend real
	// credits on a two-word title.
	acpArgs     []string `wiring:"optional"`
	primeFromMu sync.Mutex
}

// PrimeFromChat records that chatID's first session should be primed with
// sourceChatID's transcript. See BridgeCoordinator.primeFrom.
func (bc *BridgeCoordinator) PrimeFromChat(chatID, sourceChatID vibekit.ChatID) {
	if chatID == "" || sourceChatID == "" || chatID == sourceChatID {
		return
	}
	bc.primeFromMu.Lock()
	defer bc.primeFromMu.Unlock()
	if bc.primeFrom == nil {
		bc.primeFrom = make(map[vibekit.ChatID]vibekit.ChatID, 1)
	}
	bc.primeFrom[chatID] = sourceChatID
}

// takePrimeFrom claims and clears a chat's prime note: the note is spent by the
// session it primes, so a later bridge must not re-inject that history.
func (bc *BridgeCoordinator) takePrimeFrom(chatID vibekit.ChatID) vibekit.ChatID {
	bc.primeFromMu.Lock()
	defer bc.primeFromMu.Unlock()
	src, ok := bc.primeFrom[chatID]
	if !ok {
		return ""
	}
	delete(bc.primeFrom, chatID)
	return src
}

// newBridgeCoordinator constructs a BridgeCoordinator from the Runtime's fields,
// once, from NewHub after all options are applied.
func newBridgeCoordinator(h *Runtime) *BridgeCoordinator {
	return &BridgeCoordinator{
		bridge:         h.bridge,
		chatStore:      h.chatStore,
		turns:          newTurnRegistry(),
		broadcast:      h.bus.Broadcast,
		translateEvent: h.translateACPEvent,
		push:           h.push,
		mcpRegistry:    h.mcpRegistry,
		lifecycle:      h.lifecycle,
		// h implements replayProjector via load_projection.go.
		replayProjection: h.replay,
		chatStatus:       h.bus.chatStatus.Get,
		agentEngine:      resolveAgentEngine(),
		acpArgs:          h.acpArgs,
		secretStorage:    func() bool { return h.secrets != nil },
		onSessionRehydrated: func(chatID vibekit.ChatID) {
			ctx, cancel := h.lifecycle.derivedContext()
			defer cancel()
			h.runs.resumeInterruptedRuns(ctx, chatID)
		},
		onTurnClosed: func(chatID vibekit.ChatID, epoch vibekit.TurnEpoch) {
			h.agentTerms.CloseTurn(chatID, epoch)
		},
	}
}

// hasSecretStorage reports whether this process holds a credential store, and
// therefore whether a bridge may declare `_meta.kiro.secretStorage`. Nil-safe: no
// resolver declares the capability off.
func (bc *BridgeCoordinator) hasSecretStorage() bool {
	return bc.secretStorage != nil && bc.secretStorage()
}

// processLifetimeCtx returns the runtime's shutdown context, which bounds a spawned
// kiro-cli subprocess. Never the caller's ctx: CmdPrompt's is per-turn and cancels
// on return, so a bridge must outlive the turn that created it.
func (bc *BridgeCoordinator) processLifetimeCtx() context.Context {
	return bc.lifecycle.shutdownCtx
}

// resolveAgentEngine returns the kiro-cli agent engine, hard-pinned to v3 (KAS). A
// stray KIRO_AGENT_ENGINE=v1/v2 is ignored: vibekit cannot talk to a legacy engine
// at all, so honouring it stalls session/new and fails every turn.
func resolveAgentEngine() string {
	return vibekit.AgentEngineV3
}

// OpenBridge returns an existing bridge for chatID, or creates one. Concurrent
// callers for the same chatID coalesce via singleflight, keyed by bridgeSpawnKey.
//
//nolint:revive // unexported-return: sharedBridge is package-internal; callers within agent use the methods on it. Exporting would leak ACP wiring outside the runtime package.
func (bc *BridgeCoordinator) OpenBridge(ctx context.Context, chatID vibekit.ChatID, modelOverride string) (*sharedBridge, error) {
	if sb := bc.bridge.mgr.get(chatID); sb != nil {
		bc.repairEffort(ctx, chatID, sb)
		return sb, nil
	}

	// The model override is in the key, so callers with different model parameters
	// never coalesce onto the wrong bridge.
	sfKey := bridgeSpawnKey(chatID, modelOverride)
	v, err, _ := bc.bridge.mgr.spawnSF.Do(sfKey, func() (any, error) {
		return bc.spawnBridge(ctx, chatID, modelOverride)
	})
	if err != nil {
		return nil, err
	}
	b, _ := v.(*sharedBridge)
	// Wake prompts parked on the admission slot: their answer depends on the
	// holder's source, and the bridge going live changes it without moving any
	// registry state.
	bc.turns.wakeChat(chatID)
	return b, nil
}

// bridgeSpawnKey composes the bridge-spawn singleflight key over (chatID,
// modelOverride). keyenc keeps it injective even if either alphabet widens: a
// collision would hand a coalesced caller another chat's bridge.
func bridgeSpawnKey(chatID vibekit.ChatID, modelOverride string) string {
	return keyenc.Join(string(chatID), modelOverride)
}

// spawnBridge creates (or returns the just-created) bridge for chatID, inside the
// singleflight. On any start failure it rolls the half-registered bridge back out
// of the map and returns the error.
func (bc *BridgeCoordinator) spawnBridge(ctx context.Context, chatID vibekit.ChatID, modelOverride string) (*sharedBridge, error) {
	// Double-check after winning the singleflight race.
	sb, existed := bc.bridge.mgr.orInsert(chatID)
	if existed {
		return sb, nil
	}

	setupErr := func(err error) error {
		bc.bridge.mgr.removeIfSame(chatID, sb)
		sb.setState(bridgeIdle)
		return err
	}

	chat, exists := bc.chatStore.Get(ctx, chatID)
	if !exists {
		return nil, setupErr(fmt.Errorf("chat %s not found", chatID))
	}

	model := chat.Model
	if modelOverride != "" && modelOverride != modelAuto {
		model = modelOverride
	}
	// Withhold a model the account cannot run, SILENTLY: this is an inherited value
	// rather than a pick the user just made, and kiro-cli would reject it mid-prompt
	// on every later turn. An explicit pick is refused loudly, in cmdSwitchModel. An
	// empty advertised set means unknowable and allows.
	if !vibekit.ModelServed(model, chat.ServedModelIDs) {
		slog.Warn("withholding a model this account does not serve; using the backend default",
			"chat_id", chatID, "model", model)
		model = ""
	}

	// No mcpServers parameter: KAS reads its own hot-reloading config file, which
	// vibekit renders (internal/mcp/kasfile.go), and an inline copy would WIN over
	// the file and make every config edit look like a no-op.
	if bc.preBridgeSpawn != nil {
		bc.preBridgeSpawn(ctx)
	}

	// A model OVERRIDE differing from the record is the switch-by-restart path: the
	// chat's stored tier was chosen under the model being switched AWAY from, so
	// resolve effort against the target instead.
	effort := bc.effortFor(ctx, chat)
	if model != "" && model != chat.Model {
		effort = bc.EffortForSwitch(ctx, chat, model)
	}

	if chat.ACPSessionID != "" {
		if bc.tryLoadSession(ctx, chatID, sb, chat.ACPSessionID, model, effort) {
			return sb, nil
		}
	}

	// EnableHooks:true opts chat bridges into KAS's v2 hook engine, so workspace
	// hooks autofire without vibekit serving executeHook.
	//
	// Forward MUST drain NotifCh before Start: on v3 the agent sends
	// _kiro/auth/getAccessToken and _kiro/terminal/shell_type as server->client
	// REQUESTS on the session-creation path, so attaching Forward after Start
	// deadlocks every fresh session.
	go bc.Forward(chatID, sb.bridge)
	// Supervised is passed at creation only: KAS persists `autopilot` in its own
	// session metadata, so session/load need not repeat it.
	if err := sb.bridge.Start(ctx, &vibekit.StartOpts{Lifetime: bc.processLifetimeCtx(), Model: model, Mode: chat.CurrentModeID, Effort: effort, AgentEngine: bc.agentEngine, EnableHooks: true, ExtraArgs: bc.acpArgs, Supervised: chat.SupervisedMode, SecretStorage: bc.hasSecretStorage(), Presets: securityPresets(ctx, bc.lifecycle.configDir), ToolSearch: toolSearchEnabled(ctx, bc.lifecycle.configDir), Knowledge: knowledgeEnabled(ctx, bc.lifecycle.configDir), Memory: memoryEnabled(ctx, bc.lifecycle.configDir)}); err != nil {
		return nil, setupErr(err)
	}
	bc.persistNewSessionMetadata(ctx, chatID, sb.bridge)

	sb.primed = false
	// A tangent's refused fork needs the injection: this session has never seen the
	// conversation it was opened from, and that lives in another chat. Claimed here
	// rather than read at prime time, so exactly one session spends it.
	if src := bc.takePrimeFrom(chatID); src != "" {
		sb.primeReason = primeReasonFork
		sb.primeFrom = src
	}
	sb.setState(bridgeIdle)

	return sb, nil
}

// tryLoadSession attempts session/load against the stored ACP session id.
func (bc *BridgeCoordinator) tryLoadSession(
	ctx context.Context, chatID vibekit.ChatID, sb *sharedBridge, acpSessionID, model, effort string,
) bool {
	// Forward attaches BEFORE Start, as on the session/new path: session/load also
	// blocks on the host answering _kiro/auth/getAccessToken. On load failure the old
	// bridge is swapped out of sb before it is stopped, so this goroutine's
	// identity-compared exit cleanup cannot evict the replacement. Open the
	// projection first: KAS starts replaying inside Start below.
	if bc.replayProjection != nil {
		bc.replayProjection.OpenReplayProjection(chatID)
	}
	// The attachment is taken HERE, not inside the goroutine: the load's read-loop
	// position below is only comparable within one attachment. See replay_drain.go.
	gen := bc.turns.attachForward(chatID)
	go bc.forwardAt(chatID, sb.bridge, gen)
	if err := sb.bridge.Start(ctx, &vibekit.StartOpts{Lifetime: bc.processLifetimeCtx(), SessionID: acpSessionID, Model: model, Effort: effort, AgentEngine: bc.agentEngine, EnableHooks: true, ExtraArgs: bc.acpArgs, SecretStorage: bc.hasSecretStorage(), Presets: securityPresets(ctx, bc.lifecycle.configDir), ToolSearch: toolSearchEnabled(ctx, bc.lifecycle.configDir), Knowledge: knowledgeEnabled(ctx, bc.lifecycle.configDir), Memory: memoryEnabled(ctx, bc.lifecycle.configDir)}); err != nil {
		slog.Warn("session/load failed, starting new",
			"chat_id", chatID, "acp_session", acpSessionID, "error", err)
		// A failed load has no transcript to adopt, so a partial replay must not
		// survive into the fresh session.
		if bc.replayProjection != nil {
			bc.replayProjection.DiscardReplayProjection(chatID)
		}
		old := sb.bridge
		sb.bridge = bc.bridge.mgr.factory()
		old.Stop()
		// The replacement session has never seen this chat, so record why it needs
		// priming or the agent answers the next prompt with no history.
		sb.primeReason = primeReasonReload
		if mErr := bc.chatStore.Mutate(ctx, chatID, func(c *vibekit.Chat, ex bool) bool {
			if !ex {
				return false
			}
			// Detach but KEEP the session in the chain: its directory holds that
			// period's transcript, and blanking the id makes the reaper sweep it
			// as an orphan.
			c.RecordSession("")
			return true
		}); mErr != nil {
			slog.Error("clear stale acp_session_id", "chat_id", chatID, "error", mErr)
		}
		return false
	}
	// Record the load's read-loop position, the bound replay completion is measured
	// against, WITH one settle attempt: a replay Forward has already drained has no
	// later frame coming to notice it. See MarkReplayLoadedAt.
	if bc.replayProjection != nil {
		bc.replayProjection.MarkReplayLoadedAt(chatID, drainPoint{gen: gen, seq: sb.bridge.SessionLoadSeq()})
	}
	title := sb.bridge.SessionTitle()
	if mErr := bc.chatStore.Mutate(ctx, chatID, func(c *vibekit.Chat, ex bool) bool {
		if !ex {
			return false
		}
		applyLoadedSessionFacts(c, sb.bridge, title)
		return true
	}); mErr != nil {
		slog.Error("refresh session metadata", "chat_id", chatID, "error", mErr)
	}
	sb.primed = true
	sb.setState(bridgeIdle)
	// Heal the chat's restart-paused runs off the spawn path, so the user's prompt
	// never waits behind a run-list round trip, and AFTER the state flip, so the
	// resume's own bridge Call finds an idle bridge.
	if bc.onSessionRehydrated != nil {
		go bc.onSessionRehydrated(chatID)
	}
	return true
}

// kasDefaultSessionTitle is KAS's placeholder title (DEFAULT_SESSION_TITLE),
// returned by every session/new. Adopting it would swap vibekit's placeholder for a
// worse one and make the chat non-default-named, which then rejects the real title.
const kasDefaultSessionTitle = "New Session"

// adoptKASTitle names a chat from KAS's own session title, only while the chat has
// no name of its own and the title is real. Naming precedence is focus_update title
// > local first-prompt label > this; `titleIsPromptDerived` (translate/focus.go)
// implements the top of that ordering.
func adoptKASTitle(c *vibekit.Chat, title string) {
	if title == "" || title == kasDefaultSessionTitle || c.Name != vibekit.DefaultChatName {
		return
	}
	c.Name = title
}

// applyLoadedSessionFacts copies what a RESUMED session reported onto the chat
// record, writing each field only when the load result carried it: the bridge is
// freshly constructed, so an omitted field reads as the zero value, and
// `session/load` omits the model catalog routinely. Modes have no repair channel, so
// an emptied mode list stays empty for the rest of the session.
func applyLoadedSessionFacts(c *vibekit.Chat, facts acpSessionFacts, title string) {
	if mode := facts.CurrentMode(); mode != "" {
		c.CurrentModeID = mode
	}
	if modes := facts.Modes(); len(modes) > 0 {
		c.AvailableModes = modes
	}
	if models := facts.Models(); len(models) > 0 {
		c.AvailableModels = models
	}
	adoptKASTitle(c, title)
}

func (bc *BridgeCoordinator) persistNewSessionMetadata(ctx context.Context, chatID vibekit.ChatID, bridge acpSessionFacts) {
	newSessionID := bridge.SessionID()
	newModelID := bridge.ModelID()
	currentMode := bridge.CurrentMode()
	modes := bridge.Modes()
	models := bridge.Models()
	served := bridge.ServedModels()
	title := bridge.SessionTitle()
	// Read the requested mode before the mutation below overwrites it with the actual.
	var requestedMode string
	if err := bc.chatStore.Mutate(ctx, chatID, func(c *vibekit.Chat, ex bool) bool {
		if !ex {
			return false
		}
		requestedMode = c.CurrentModeID
		c.RecordSession(string(newSessionID))
		if newModelID != "" {
			c.Model = string(newModelID)
		}
		c.CurrentModeID = currentMode
		c.AvailableModes = modes
		c.AvailableModels = models
		c.ServedModelIDs = served
		adoptKASTitle(c, title)
		return true
	}); err != nil {
		slog.Error("persist new session metadata",
			"chat_id", chatID,
			"acp_session", newSessionID,
			"model", newModelID,
			"error", err)
	}
	bc.reportModeNotApplied(ctx, chatID, requestedMode, currentMode)
}

// reportModeNotApplied tells the user when the session did not get the mode the chat
// asked for. A banner rather than an automatic retry: the record keeps the ACTUAL
// mode, so the request is no longer stored and the next spawn's requested id equals
// the current one, which applyInitialMode's own guard reads as nothing to do.
func (bc *BridgeCoordinator) reportModeNotApplied(ctx context.Context, chatID vibekit.ChatID, requested, actual string) {
	if requested == "" || requested == actual {
		return
	}
	slog.Error("session mode not applied; the chat's mode was reset to the session's",
		"chat_id", chatID, "requested", requested, "actual", actual)
	bc.broadcast(ctx, vibekit.NewEvent(vibekit.EventError, chatID, vibekit.ErrorPayload{
		Code: vibekit.ErrCodeModeNotApplied,
		Message: "Could not start this chat in \"" + requested + "\" mode; it is running as \"" +
			actual + "\". Pick the mode again to retry.",
	}))
}

// Bridge returns the bridge for chatID, or nil.
//
//nolint:revive // unexported-return: see OpenBridge above.
func (bc *BridgeCoordinator) Bridge(chatID vibekit.ChatID) *sharedBridge {
	return bc.bridge.mgr.get(chatID)
}

// HasLiveBridge reports whether a chat currently has a bridge. Retention's exemption
// reads this: a chat with a live bridge is never purged, however old
// (archive.WithLiveChats).
func (rt *Runtime) HasLiveBridge(chatID vibekit.ChatID) bool {
	return rt.bridge.mgr.get(chatID) != nil
}

// HasOpenTurn reports whether a chat has a turn in flight. Composition injects it
// into the chat store (chat.WithTurnOpen) so `GET /api/chats/{id}` can state the fact
// instead of leaving the client to guess it from an absent carrier.
func (rt *Runtime) HasOpenTurn(chatID vibekit.ChatID) bool {
	return rt.coord.turns.hasOpenTurn(chatID)
}

// CloseBridge stops a bridge and removes it from the map.
func (bc *BridgeCoordinator) CloseBridge(chatID vibekit.ChatID) {
	bc.bridge.mgr.close(chatID)
}

// replayProjector is the slice of the Runtime's replay-projection lifecycle the
// coordinator drives. See agent/load_projection.go for the settle barrier.
type replayProjector interface {
	OpenReplayProjection(vibekit.ChatID)
	MarkReplayLoadedAt(chatID vibekit.ChatID, at drainPoint)
	DiscardReplayProjection(vibekit.ChatID)
	SettleReplayProjection(chatID vibekit.ChatID, at drainPoint, force bool)
	ReplaySettled(chatID vibekit.ChatID) <-chan struct{}
}

// Forward is the ACP notification → domain event translator, run as a
// goroutine per bridge. It takes the chat's forward attachment itself, which is
// every caller that has no local decision to order against that attachment.
func (bc *BridgeCoordinator) Forward(chatID vibekit.ChatID, bridge ACPBridge) {
	bc.forwardAt(chatID, bridge, bc.turns.attachForward(chatID))
}

// forwardAt is Forward on an attachment the CALLER already took, for the one
// caller that has to name it: tryLoadSession orders the load's own read-loop
// position against this goroutine's, so it cannot let the goroutine take the
// attachment asynchronously and then guess which one the position belongs to.
func (bc *BridgeCoordinator) forwardAt(chatID vibekit.ChatID, bridge ACPBridge, gen uint64) {
	ch := bridge.NotifCh()
	for n := range ch {
		bc.consumeFrame(chatID, gen, n)
		// Settle a session/load replay projection here rather than at Start's
		// return: this goroutine is the one folding the frames, so the position
		// it reports is the only one the completion condition can be measured
		// against. Rationale in agent/replay_drain.go.
		if bc.replayProjection != nil {
			bc.replayProjection.SettleReplayProjection(chatID, drainPoint{gen: gen, seq: n.Seq}, false)
		}
	}
	// The channel closed, so no further frame can advance the position. Seal the
	// settle so a load whose trailing catalog frames never came still completes
	// instead of leaking a projection.
	if bc.replayProjection != nil {
		bc.replayProjection.SettleReplayProjection(chatID, drainPoint{gen: gen}, true)
	}
	// No frame can advance the position now, so anything parked on one has to be
	// told rather than left to its context. Before the death closer, so a woken
	// settle has already deferred by the time that closer runs.
	bc.turns.sealPosition(chatID, gen)

	slog.Info("bridge exited", "chat_id", chatID)

	// Still registered means nobody removed it, so the process died on its own
	// rather than being torn down: the third actor closes whatever turn is still
	// open, because no other closer is coming for it.
	if bc.bridge.mgr.removeIfBridge(chatID, bridge) {
		bc.closeTurnOnBridgeDeath(bc.lifecycle.shutdownCtx, chatID)
	}

	// Flush staged writes for the chat. A bridge exit (crash, or a
	// model-switch CloseBridge) leaves the supervised fs-handler goroutine
	// parked on its resume channel and a phantom "awaiting approval"
	// pending op that would replay to reconnecting clients. Cancel, delete,
	// and mode-disable already flush; this is the bridge-exit sibling.
	lastBridge := bc.bridge.mgr.count() == 0

	if lastBridge {
		bc.mcpRegistry.clearAll(bc.lifecycle.shutdownCtx)
	}
	// A run chat has no record and no turn lifecycle of its own beyond the
	// position bookkeeping above, and nothing ever calls cleanupChatState for one,
	// so its lifecycle is dropped here or it outlives the run.
	if isRunChat(chatID) {
		bc.turns.forget(chatID)
	}
}

// consumeFrame translates one frame and then advances the chat's observed
// position, whatever the frame did.
//
// DEFERRED, and for every frame rather than for every fold: the advance
// acknowledges work that is done, and at least eight paths through the
// session-update cascade consume a frame without touching a turn. See observe.
func (bc *BridgeCoordinator) consumeFrame(chatID vibekit.ChatID, gen uint64, n vibekit.Notification) {
	defer bc.turns.observe(chatID, gen, n.Seq)
	bc.translateEvent(chatID, n.Msg)
}

// PrimeIfNeeded sends the chat history as an ephemeral priming prompt on the current
// bridge, unless this session already has it.
//
// The primed flag is claimed HERE rather than by the caller, so it has one owner and
// needs no interface assertion: a chat with no bridge is simply nothing to prime.
func (bc *BridgeCoordinator) PrimeIfNeeded(ctx context.Context, chatID vibekit.ChatID) {
	sb := bc.bridge.mgr.get(chatID)
	if sb == nil {
		return
	}
	if !sb.claimPriming() {
		return
	}
	// Preambles live in translate (PrimePreamble*) because the focus-title derivation
	// filter must recognise a title KAS derives from this text. The reason decides the
	// preamble AND whose history is read: a tangent whose fork was refused has no
	// transcript of its own, so the source is read off the bridge rather than assumed.
	var prime string
	source := chatID
	switch sb.primeReason {
	case primeReasonSwitch:
		prime = translate.PrimePreambleSwitch
	case primeReasonReload:
		prime = translate.PrimePreambleReload
	case primeReasonFork:
		prime = translate.PrimePreambleTangent
		if sb.primeFrom != "" {
			source = sb.primeFrom
		}
	default:
		return
	}

	history := bc.chatStore.BuildHistory(ctx, source)
	if history == "" {
		return
	}
	prime += history

	slog.Info("priming bridge", "chat_id", chatID, "reason", sb.primeReason, "history_from", source)
	// The prime is a real session/prompt, so it opens a real turn and closes it
	// like any other. Then it AWAITS its own epoch before returning, and that is
	// what keeps the unacknowledged set from ever holding two: the caller's own
	// pre-open cannot happen until this turn has finalized, so a wire turn_start
	// can only ever bind to one candidate.
	epoch := bc.StartTurn(ctx, chatID, vibekit.TurnSourcePrime)
	defer bc.ReleaseTurn(chatID, epoch)
	resp, seq, err := sb.bridge.CallAt(ctx, vibekit.MethodPrompt, command.SessionParams(sb, map[string]any{
		vibekit.KeyPrompt: []map[string]any{vibekit.TextBlock(prime)},
	}))
	if err != nil {
		slog.Error("prime failed", "chat_id", chatID, "error", err)
		bc.AbandonInFlightTurn(ctx, chatID, epoch, "The priming prompt failed.")
		return
	}
	bc.SettleTurnOnResponse(ctx, chatID, epoch, seq, resp)
	if _, aErr := bc.AwaitTurn(ctx, chatID, epoch); aErr != nil {
		slog.Warn("prime: no turn outcome", "chat_id", chatID, "error", aErr)
	}
}

// Effort is a field on the chat record (vibekit.Chat.Effort), read at spawn and
// written by CmdSetEffort — deliberately not a global setting keyed by the last
// model, which two chats could not disagree about.

// NotifyPush sends a push notification about one CHAT if the push service is
// configured. It keeps its chat-id parameter rather than taking a vibekit.PushSubject
// because every caller here is chat-scoped, so the conversion belongs at this one
// boundary; a notification with no chat behind it calls push.Send directly.
func (bc *BridgeCoordinator) NotifyPush(ctx context.Context, body string, kind vibekit.PushKind, chatID vibekit.ChatID) {
	if bc.push == nil || !bc.push.HasSubscribers() {
		return
	}
	bc.lifecycle.inflight.Go(func() {
		bc.push.Send(ctx, push.DefaultTitle, body, kind, vibekit.ChatSubject(chatID))
	})
}

// SettleTurnOnResponse closes the turn named by epoch on the response that settled
// it — once the folder has consumed everything queued behind that response, and
// only if the wire's own turn_end did not get there first.
//
// seq is the read loop position the response arrived at. Zero skips the wait,
// which is what the two paths that deliberately reach no bracket want: an oversize
// frame and a cancel-grace expiry fail the call while the bridge stays alive.
func (bc *BridgeCoordinator) SettleTurnOnResponse(ctx context.Context, chatID vibekit.ChatID, epoch vibekit.TurnEpoch, seq uint64, resp *vibekit.RPCResponse) {
	bc.finalizeTurn(ctx, chatID, turnClose{Closer: closerPromptResponse, Resp: resp, Epoch: epoch, Seq: seq})
}

// TurnOpenedAfter reports whether any turn on the chat opened after epoch — the
// structural half of the empty-turn gate. See turnRegistry.openedAfter.
func (bc *BridgeCoordinator) TurnOpenedAfter(chatID vibekit.ChatID, epoch vibekit.TurnEpoch) bool {
	return bc.turns.openedAfter(chatID, epoch)
}

// statusDescription reads the chat's self-declared status description (KAS's
// focus_update channel), for the push notification's body.
//
// Must be read BEFORE the turn_ended broadcast: the status cache is cleared at turn
// end, inside the same emit() that fires it, so a read after that always finds it
// gone. Empty is legitimate, so the caller's fallback literal is the honest default.
func (bc *BridgeCoordinator) statusDescription(chatID vibekit.ChatID) string {
	if bc.chatStatus == nil {
		return ""
	}
	return bc.chatStatus(chatID).Description
}

// defaultAgentFinishedBody is the body for a turn whose agent never declared what
// it was doing.
const defaultAgentFinishedBody = "Agent finished"

// agentFinishedBodyFrom picks the body from a chat's self-declared description.
// Split out from the cache read so the choice is testable without a Runtime.
func agentFinishedBodyFrom(description string) string {
	if d := strings.TrimSpace(description); d != "" {
		return d
	}
	return defaultAgentFinishedBody
}

// persistTurn commits the finalized assistant turn to the chat file.
//
// A failed append is survivable through KAS's own log — measured to flush each
// sub-message as it COMPLETES, so a session/load replay carries the turn and the
// projection rebuilds it. What neither covers is the final streaming fragment.
func (bc *BridgeCoordinator) persistTurn(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.Message) {
	if err := bc.chatStore.AppendMessage(ctx, chatID, msg); err != nil {
		slog.Error("persist assistant turn; the replay projection is the fallback",
			"chat_id", chatID, "error", err)
	}
}

// persistDisplacedTurn commits a turn a PROMPT displaced, ahead of the trailing
// user rows the file already carries.
//
// A prompt persists its user row before it asks for admission, and an
// engine-opened turn holds no reservation, so the prompt that ended this reply is
// already on disk. A plain append records the reply as FOLLOWING it, which
// projectTurns reads as a headerless turn below — while the client's array has it
// above, since the broadcast carries the streamed message's id and merges in place.
func (bc *BridgeCoordinator) persistDisplacedTurn(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.Message) {
	if msg.Ts == 0 {
		msg.Ts = time.Now().UnixMilli()
	}
	var inserted bool
	err := bc.chatStore.Mutate(ctx, chatID, func(c *vibekit.Chat, exists bool) bool {
		if !exists {
			return false
		}
		at := len(c.Messages)
		for at > 0 && c.Messages[at-1].Role == vibekit.RoleUser {
			at--
		}
		c.Messages = slices.Insert(c.Messages, at, *msg)
		inserted = true
		return true
	})
	if err != nil {
		slog.Error("persist displaced turn; the replay projection is the fallback",
			"chat_id", chatID, "error", err)
		return
	}
	if inserted {
		bc.broadcast(ctx, vibekit.NewEvent(vibekit.EventMessageAppended, chatID, msg))
	}
}

// TryFastModelSwitch attempts an in-session model swap via
// session/set_config_option (configId "model") on the running bridge, then
// re-applies effort so the swap does not carry the level away with it.
func (bc *BridgeCoordinator) TryFastModelSwitch(ctx context.Context, chatID vibekit.ChatID, model, effort string) bool {
	sb := bc.bridge.mgr.get(chatID)
	if sb == nil {
		return false
	}
	// Close an ENGINE-opened turn first, the guard the restart fallback gets from
	// its own flush: such a turn holds no admission reservation, so nothing refuses
	// a switch landing inside one, and the model_switched row this path persists is
	// not turn-terminal, so it would be written into that turn's body.
	//
	// Only an engine-opened one. The caller's OWN prompt turn keeps running, and
	// keeps the model it was dispatched under; the restart fallback discards it
	// because the bridge goes with it, which is not true here.
	if displaced, ok := bc.displaceEngineTurn(ctx, chatID); ok {
		slog.Info("a model switch displaced a live engine-opened turn",
			"chat_id", chatID, "displaced_epoch", displaced, "model", model)
	}
	return bc.applyModelSwitch(ctx, chatID, sb, model, effort)
}

// applyModelSwitch swaps the model on a bridge the caller ALREADY HOLDS, then
// re-applies the level.
//
// Takes the bridge rather than the chat id because only one of its callers can look
// it up safely: re-resolving by id on the restart path could answer with a DIFFERENT
// bridge, so the pick would land on a session the caller never loaded.
func (bc *BridgeCoordinator) applyModelSwitch(
	ctx context.Context, chatID vibekit.ChatID, sb *sharedBridge, model, effort string,
) bool {
	if err := sb.bridge.SetModel(ctx, model); err != nil {
		slog.Info("model switch: fast path failed, falling back to restart",
			"chat_id", chatID, "model", model, "error", err)
		return false
	}
	// Re-assert the level after the swap, or the swap can take it away: KAS reconciles
	// against the NEW model's tier list (measured on 2.19.1 — a swap to `auto` destroys
	// it). Best-effort, since the swap already landed and is what the user asked for.
	if effort != "" {
		if err := sb.bridge.EnsureEffort(ctx, effort); err != nil {
			slog.Warn("model switch: reasoning effort not re-applied after the swap",
				"chat_id", chatID, "model", model, "effort", effort, "error", err)
		}
	}
	slog.Info("model switch: fast path succeeded (session/set_config_option)",
		"chat_id", chatID, "model", model)
	return true
}

// repairEffort re-asserts the chat's reasoning-effort level on a bridge that is
// ALREADY OPEN — the one checkpoint that catches a level KAS changed on its own (its
// first-prompt model pin, or a switch made from the Kiro IDE or TUI).
//
// At the prompt rather than reactively: a Call from the Forward goroutine would block
// the drain it waits on. Best-effort and log-only.
func (bc *BridgeCoordinator) repairEffort(ctx context.Context, chatID vibekit.ChatID, sb *sharedBridge) {
	chat, ok := bc.chatStore.Get(ctx, chatID)
	if !ok {
		return
	}
	level := bc.effortFor(ctx, chat)
	if level == "" {
		return
	}
	if err := sb.bridge.EnsureEffort(ctx, level); err != nil {
		slog.Warn("reasoning effort not re-applied on the open session",
			"chat_id", chatID, "effort", level, "error", err)
	}
}

// healEffort re-asserts the chat's chosen level when a config_option_update reports
// the session running at a different one. It wraps the config-option handler.
//
// It covers repairEffort's hole: that runs on OpenBridge's ALREADY-OPEN path, so the
// turn that SPAWNS the bridge never takes it — and that is the one moment KAS's
// first-prompt model pin applies another tier. Reactive because the divergence appears
// DURING the turn. LATCHED once per bridge: the repair produces another
// config_option_update, so an unbounded reactive repair is a loop.
func (bc *BridgeCoordinator) healEffort(next sessionUpdateHandler) sessionUpdateHandler {
	return func(ctx context.Context, chatID vibekit.ChatID, raw json.RawMessage, attr translate.FrameAttribution) {
		next(ctx, chatID, raw, attr)
		// The chat's OWN frame only. A workflow step's session reports the level
		// IT runs at, and a subagent's frame would be attributed too; neither says
		// anything about the level this chat chose. Both fields are tested,
		// because an empty SubSessionID alone does not mean the chat owns the
		// frame — a step has one too.
		if attr.Step || attr.SubSessionID != "" {
			return
		}
		sb := bc.Bridge(chatID)
		if sb == nil {
			return
		}
		chat, ok := bc.chatStore.Get(ctx, chatID)
		if !ok {
			return
		}
		// The frame IS the session reporting its level, and the bridge forwards
		// this channel unread, so hand the report over before deciding anything:
		// it is what lets EnsureEffort assert here AND at the next prompt, rather
		// than comparing equal against the level the session door asked for.
		running := chat.EffortActive
		sb.bridge.ObserveEffort(running)
		want := bc.effortFor(ctx, chat)
		if want == "" || running == "" || want == running {
			return
		}
		if !sb.claimEffortHeal() {
			slog.Debug("reasoning effort still diverges after one repair; leaving it to the next prompt",
				"chat_id", chatID, "want", want, "running", running)
			return
		}
		slog.Info("re-asserting the chat's reasoning effort: the session reported a different level",
			"chat_id", chatID, "want", want, "running", running)
		// On inflight rather than untracked: Shutdown stops every bridge BEFORE it
		// waits on this group, which is the ordering a blocked bridge Call needs to
		// unblock through, and it is what the fs handlers beside it already do.
		bc.lifecycle.inflight.Go(func() {
			hctx, cancel := bc.lifecycle.derivedContext()
			defer cancel()
			if err := sb.bridge.EnsureEffort(hctx, want); err != nil {
				slog.Warn("reasoning effort not re-asserted after the session reported another level",
					"chat_id", chatID, "want", want, "running", running, "error", err)
			}
		})
	}
}

// effortFor resolves the level a chat's next session starts at: the chat's own
// choice, else the last level the user picked (settings.KeyLastEffort). Empty means
// send nothing and let the service apply the model's own default.
//
// A FALLBACK never written onto the chat record — stamping it would pin an unchosen
// chat to today's value. MODEL-SCOPED, because an explicit tier is a judgement about
// one model. Validated here because config.json is user-editable.
func (bc *BridgeCoordinator) effortFor(ctx context.Context, chat *vibekit.Chat) string {
	if chat.Effort != "" {
		return chat.Effort
	}
	return bc.effortSeedFor(ctx, chat.Model)
}

// effortSeedFor answers the remembered level for exactly one model: the
// KeyLastEffort/KeyLastEffortModel pair when the recorded model IS `model`,
// else "". The one seed read, shared by the session-start resolution above and
// the model-switch target below so the two cannot disagree about scope.
func (bc *BridgeCoordinator) effortSeedFor(ctx context.Context, model string) string {
	if model == "" {
		return ""
	}
	var level, seedModel string
	if !settings.FieldInto(ctx, bc.lifecycle.configDir, settings.KeyLastEffort, &level) {
		return ""
	}
	if !settings.FieldInto(ctx, bc.lifecycle.configDir, settings.KeyLastEffortModel, &seedModel) {
		return ""
	}
	if seedModel != model {
		return ""
	}
	if !vibekit.EffortLevel(level).Valid() {
		return ""
	}
	return level
}

// EffortForSwitch resolves the level a chat runs at AFTER a model switch: the seed
// when it was picked under the TARGET model, else the target model's own default,
// else "" (KAS reconciles on its own).
//
// Deliberately NOT effortFor: the chat's stored choice was made under the model being
// switched away from, so honouring it here carried `max` from one model onto the next.
// Explicit, because KAS KEEPS a fitting level across a swap.
func (bc *BridgeCoordinator) EffortForSwitch(ctx context.Context, chat *vibekit.Chat, model string) string {
	if level := bc.effortSeedFor(ctx, model); level != "" {
		return level
	}
	for _, m := range chat.AvailableModels {
		if m.ID == model {
			return m.DefaultEffortLevel
		}
	}
	return ""
}

// PersistModelSwitch records the switch event and updates the chat's
// model + resets usage counters.
func (bc *BridgeCoordinator) PersistModelSwitch(ctx context.Context, chatID vibekit.ChatID, model string, contextSize int) {
	// Both writes are one record of one switch: an event saying the model changed beside
	// a record still naming the old one is worse than neither.
	ctx = durable.Context(ctx)
	evt := vibekit.Message{
		ID:        newMessageID(),
		Role:      vibekit.RoleEvent,
		Ts:        time.Now().UnixMilli(),
		EventKind: vibekit.EventModelSwitched,
		Content:   model,
	}
	if err := bc.chatStore.AppendMessage(ctx, chatID, &evt); err != nil {
		slog.Error("switch_model: append event", "chat_id", chatID, "error", err)
	}
	if err := bc.chatStore.Mutate(ctx, chatID, func(c *vibekit.Chat, ex bool) bool {
		if !ex {
			return false
		}
		c.Model = model
		// The chosen tier was a judgement about the model being switched AWAY from, so it
		// does not survive: resolution falls to the model-scoped seed, else the new
		// model's own default. Switching back re-applies the seed.
		c.Effort = ""
		c.Usage = vibekit.Usage{ContextSize: contextSize}
		return true
	}); err != nil {
		slog.Error("switch_model: persist model", "chat_id", chatID, "error", err)
	}
}

// FlushInFlightTurnOnSwitch discards the chat's turn before a bridge restart,
// announcing the interruption when a turn was in flight.
func (bc *BridgeCoordinator) FlushInFlightTurnOnSwitch(ctx context.Context, chatID vibekit.ChatID) {
	bc.finalizeTurn(ctx, chatID, turnClose{Closer: closerModelSwitch, AnyOpen: true})
}

// assistantTurnMessage builds the persisted assistant message from a finished turn's
// content. Extracted so the interrupted and normal paths cannot drift: every field
// below is read by the client after a reload, so a second literal would lose one.
//
// It takes the SNAPSHOT rather than the buffer, so every field comes from ONE guarded
// read rather than eight off the dispatch goroutine.
func assistantTurnMessage(snap *buffer.TurnContent, stats turnStats, model string, c vibekit.TurnConclusion) vibekit.Message {
	return vibekit.Message{
		ID:        snap.MessageID,
		Role:      vibekit.RoleAssistant,
		Ts:        time.Now().UnixMilli(),
		Content:   snap.Content,
		Reasoning: snap.Reasoning,
		ToolCalls: snap.ToolCalls,
		// Blocks captures the chronological text/tool/thinking emission order; renderers
		// prefer it over Content+ToolCalls so a turn renders the way it was produced.
		Blocks: snap.Blocks,
		// CodeReferences persists the licensed-code attributions so the chip survives
		// reload (a streamed turn is never re-broadcast as message_appended).
		CodeReferences: snap.CodeReferences,
		// Refusal metadata, stamped from the refusal explanation chunk; persisted so the
		// callout survives reload.
		Refusal: snap.Refusal,
		// Turn summary (credits · elapsed · files changed), persisted so the footer
		// survives reload; omitempty drops the zero cases.
		TurnCredits:   stats.CreditsDelta,
		TurnElapsedMs: stats.ElapsedMs,
		ChangedFiles:  snap.ChangedFiles,
		// Which model answered, latched when the turn opened: the chat's Model is the
		// CURRENT one, so a footer derived from it would relabel history on a switch.
		TurnModel: model,
		// How the turn ENDED, durably. A live stop reason is broadcast and never stored,
		// so without these a reload read a failed turn as completed.
		TurnOutcome:       c.Outcome,
		TurnStopReasonRaw: c.RawStop,
		TurnTruncated:     c.Truncated,
		// WHY it ended badly, beside how: a `failed` close persisted a red mark and an
		// empty body, and the cause reached the user through a transient toast alone.
		TurnFailureReason: c.Reason,
	}
}

// AbandonInFlightTurn finalizes a turn whose prompt call could not finish it,
// PERSISTING the partial rather than dropping it: without a call that takes the
// buffer, the next prompt extended the dead turn's blocks under its message id.
//
// It waits for no read-loop position — the two failures that reach it settle locally
// with the bridge still alive, so no bracket is coming.
func (bc *BridgeCoordinator) AbandonInFlightTurn(ctx context.Context, chatID vibekit.ChatID, epoch vibekit.TurnEpoch, reason string) {
	bc.finalizeTurn(ctx, chatID, turnClose{Closer: closerPromptFailure, Reason: reason, Epoch: epoch})
}

// FinalizeLocalShellTurn closes a `!cmd` turn vibekit ran itself.
func (bc *BridgeCoordinator) FinalizeLocalShellTurn(ctx context.Context, chatID vibekit.ChatID, epoch vibekit.TurnEpoch) {
	bc.finalizeTurn(ctx, chatID, turnClose{Closer: closerLocalShell, Epoch: epoch})
}

// WireTurnStart is the engine's own turn_start bracket.
//
// It binds the single pending pre-open when there is one, PROVISIONALLY — the
// bracket cannot tell a prompted turn from an agent-initiated one. Otherwise the
// previous turn's end never arrived, so that turn closes `unknown` and a
// wireTurnStart turn opens in its place. An acknowledged start still passes
// through that branch rather than bypassing it: a wireTurnStart turn holds no
// prompt slot for admission control to have refused.
func (bc *BridgeCoordinator) WireTurnStart(ctx context.Context, chatID vibekit.ChatID) {
	bound, displaced := bc.turns.bindPending(chatID)
	if !bound && displaced != 0 {
		// A pre-open is owed this bracket while another turn is still folding, so
		// that turn's own end never arrived. Close it and bind on the retry rather
		// than binding over it: the pre-open's frames must not fold into the other
		// turn's buffer.
		bc.finalizeTurn(ctx, chatID, turnClose{Closer: closerWireDisplaced, Epoch: displaced})
		bound, _ = bc.turns.bindPending(chatID)
	}
	if bound {
		return
	}
	bc.finalizeTurn(ctx, chatID, turnClose{Closer: closerWireDisplaced, AnyOpen: true})
	model, credits := bc.turnOpenFacts(ctx, chatID, vibekit.TurnSourceWireTurnStart)
	bc.turns.openWire(ctx, chatID, vibekit.TurnSourceWireTurnStart, model, credits)
}

// WireTurnEnd is the engine's own turn_end bracket, and the closer whose outcome
// is the wire's rather than an inference.
//
// A turn_end for a chat with NO open turn is a no-op. Without that rule a
// cancel-grace expiry that closed its turn locally would meet the later wire
// bracket, and the fold-with-no-open-turn rule would manufacture a spurious
// empty persisted turn out of it. A replayed bracket is filtered upstream, so
// this is the live path only.
func (bc *BridgeCoordinator) WireTurnEnd(ctx context.Context, chatID vibekit.ChatID, stop vibekit.StopReason, details string) {
	bc.finalizeTurn(ctx, chatID, turnClose{Closer: closerWireEnd, Stop: stop, Reason: details, AnyOpen: true})
}

// CloseStepTurn closes a turn a workflow STEP's frames opened on chatID, because the
// run has reached a terminal state and nothing else will: the bracket path cannot,
// since the attribution gate drops a step's own turn_end. Idempotent (first-wins).
//
// EPOCH-scoped rather than AnyOpen, which is the difference that matters: AnyOpen
// describes the CHAT, so it would claim the chat's own live prompt turn if the user
// prompted between the step turn being displaced and the run's end.
func (bc *BridgeCoordinator) CloseStepTurn(ctx context.Context, chatID vibekit.ChatID) {
	epoch, ok := bc.turns.stepTurnEpoch(chatID)
	if !ok {
		return
	}
	bc.finalizeTurn(ctx, chatID, turnClose{Closer: closerRunComplete, Epoch: epoch})
}

// TurnFoldTarget returns the buffer this chat's frames fold into, opening a turn of
// the caller's stated source when none is open: a fold with no open turn is a turn
// vibekit did not prompt, and it needs a record like any other. The SOURCE comes from
// the frame and is used only on the open. The CHEAP question comes FIRST:
// turnOpenFacts is a whole chat-file read under the per-chat mutex, per delta, on the
// only consumer of a 256-slot channel. openWire re-checks the race under the lifecycle
// mutex, and the facts are read outside it — lock order is lifecycle first.
func (bc *BridgeCoordinator) TurnFoldTarget(ctx context.Context, chatID vibekit.ChatID, source vibekit.TurnOpenSource) *buffer.Buffer {
	if buf, ok := bc.turns.foldTarget(chatID); ok {
		return buf
	}
	model, credits := bc.turnOpenFacts(ctx, chatID, source)
	t := bc.turns.openWire(ctx, chatID, source, model, credits)
	if t == nil {
		// ctx died while the chat was finalizing. A throwaway buffer keeps the
		// handler's shape rather than making every fold site nil-check: the frame is
		// lost either way, and the process is shutting down.
		return buffer.New()
	}
	return t.Buf
}

// ReviseTurnBinding acts on a frame that PROVES the open turn is the agent's own
// rather than the prompt's: `agentInitiated` rides content frames and never the
// bracket, so this is the only discriminator there is. See
// turnRegistry.reclassify.
func (bc *BridgeCoordinator) ReviseTurnBinding(ctx context.Context, chatID vibekit.ChatID) {
	bc.turns.reclassify(ctx, chatID)
}

// SealTurnSegment persists the open turn's content so far as its own assistant
// message, so a boundary INSIDE a turn — a compaction point — is the sibling
// message every consumer already reads in array order. The rest of the turn
// accumulates into a fresh message.
//
// It never OPENS a turn: a chat with none has no point inside a turn to seal at.
// Declined-and-logged for an unsettled tool call, because an update resolves its
// call against the CURRENT buffer and splitting freezes that card mid-flight.
func (bc *BridgeCoordinator) SealTurnSegment(ctx context.Context, chatID vibekit.ChatID) bool {
	buf, ok := bc.turns.foldTarget(chatID)
	if !ok || buf == nil {
		return false
	}
	if !buf.ToolsSettled() {
		slog.Warn("turn segment not sealed: a tool call is still in flight, so the boundary lands after the turn",
			"chat_id", chatID)
		return false
	}
	// Settle a withheld steering-marker candidate into the segment that produced it,
	// for settleBuffer's reason: the carry can hold the segment's only final text, so
	// a content check taken before the flush reads that segment as empty.
	translate.FlushSteerCarry(buf)
	snap := buf.SplitSegment()
	// Content, not Started: a turn whose id was minted before any delta has nothing
	// to seal, and sealing it puts a blank assistant row above the boundary.
	if snap.EmittedNothing {
		return false
	}
	msg := segmentMessage(&snap)
	// The seal's own detach, not persistTurn's: that helper is shared with the
	// finalize path, so changing its body would change a closer's shutdown
	// behaviour nobody reviewed.
	bc.persistTurn(durable.Context(ctx), chatID, &msg)
	return true
}

// segmentMessage builds the persisted assistant message for a SEGMENT of a turn:
// assistantTurnMessage minus every field that describes the whole turn.
//
// The turn's credits, elapsed time, changed files, model and outcome belong to the
// turn rather than to a part of it, and each has exactly one carrier — the turn's
// closer stamps them on the last message it persists, or on an outcome marker when
// there is none. A segment claiming any of them would open a second turn for both
// projections and double the footer's numbers.
func segmentMessage(snap *buffer.TurnContent) vibekit.Message {
	return vibekit.Message{
		ID:             snap.MessageID,
		Role:           vibekit.RoleAssistant,
		Ts:             time.Now().UnixMilli(),
		Content:        snap.Content,
		Reasoning:      snap.Reasoning,
		ToolCalls:      snap.ToolCalls,
		Blocks:         snap.Blocks,
		CodeReferences: snap.CodeReferences,
		Refusal:        snap.Refusal,
	}
}

// closeTurnOnBridgeDeath is the third actor: after Forward has exited it closes
// any turn still open, because nothing else is going to.
//
// It fires only on an UNEXPECTED exit, and the discriminator is whether the
// bridge was still registered when it died. Every teardown vibekit performs
// itself -- CloseBridge for the model-switch fallback and the empty-turn
// recovery, drain at shutdown -- removes the bridge from the map first and has
// its own closer, so a deliberate stop must not also read as a death.
func (bc *BridgeCoordinator) closeTurnOnBridgeDeath(ctx context.Context, chatID vibekit.ChatID) {
	bc.finalizeTurn(ctx, chatID, turnClose{Closer: closerBridgeDeath, AnyOpen: true})
}

const stopReasonCancelled = vibekit.StopReasonCancelled

// InterruptTurn records why kiro-cli abandoned a turn without answering it, and trips
// that turn's prompt call so the ordinary failure path finalizes it. The cause lands
// on the TURN, epoch-scoped and first-wins.
//
// The bridge is left ALIVE and the session untouched: only the tool call was
// cancelled, so the chat is immediately promptable. No open turn, no bridge, or a
// cause already claimed is not a failure — a user cancel may have ended the turn.
func (bc *BridgeCoordinator) InterruptTurn(chatID vibekit.ChatID, reason string) {
	epoch, open := bc.turns.openEpoch(chatID)
	if !open {
		slog.Debug("interrupt turn: no turn open", "chat_id", chatID, "reason", reason)
		return
	}
	if !bc.turns.interrupt(chatID, epoch, vibekit.InterruptCause(reason)) {
		slog.Debug("interrupt turn: another cause already claimed this turn",
			"chat_id", chatID, "epoch", epoch, "reason", reason)
		return
	}
	sb := bc.bridge.mgr.get(chatID)
	if sb == nil {
		slog.Debug("interrupt turn: no bridge", "chat_id", chatID)
		return
	}
	if !sb.cancelPromptCall() {
		slog.Debug("interrupt turn: no prompt call in flight",
			"chat_id", chatID, "reason", reason)
	}
}

func extractStopReason(resp *vibekit.RPCResponse) vibekit.StopReason {
	if resp == nil || resp.Result == nil {
		return ""
	}
	var result struct {
		StopReason vibekit.StopReason `json:"stopReason"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		slog.Debug("turn_ended: parse result", "error", err)
		return ""
	}
	return result.StopReason
}

// BridgeRespond answers an ACP request on the chat's bridge, and is a no-op when
// that chat has none: a response to a request whose bridge already went away has
// nowhere to go and is not an error.
//
// It lives here rather than in translate_deps.go, where it sat until 2026-08-19,
// because no translate role declares it. Its three callers are all runtime's own
// (agent_terminal.go, translate.go, run_host.go), and this file already owns
// reaching a bridge by chat id.
func (rt *Runtime) BridgeRespond(ctx context.Context, chatID vibekit.ChatID, requestID int64, result any, err error) error {
	sb := rt.bridge.mgr.get(chatID)
	if sb == nil {
		return nil
	}
	return sb.bridge.Respond(ctx, requestID, result, err)
}

// ParentACPSession returns the ACP session id of the running bridge
// for chatID, or "" when no bridge exists. Translator helpers use this
// to short-circuit notifications whose top-level sessionId belongs to
// a subagent rather than the parent chat.
func (bc *BridgeCoordinator) ParentACPSession(chatID vibekit.ChatID) string {
	sb := bc.bridge.mgr.get(chatID)
	if sb == nil {
		return ""
	}
	return string(sb.bridge.SessionID())
}
