package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/keyenc"
	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/command"
	"github.com/cplieger/vibekit/internal/push"
	"github.com/cplieger/vibekit/internal/settings"
	"github.com/cplieger/vibekit/internal/translate"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// BridgeCoordinator owns bridge lifecycle: spawn, session load, priming,
// notification forwarding, model switching and turn finalization.
type BridgeCoordinator struct {
	bridge    *bridges
	chatStore bridgeChatRecords
	// Workspace mode + model vocabulary, rather than a copy on every chat record.
	catalog *Catalog
	// Per-chat turn lifecycle; the exclusion every terminal step claims through.
	turns          *turnRegistry
	broadcast      func(ctx context.Context, e vibekit.ServerEvent)
	translateEvent func(chatID vibekit.ChatID, msg *vibekit.RPCResponse)
	// Optional; nil means no notification rather than a refusal to run.
	push        pushNotifier `wiring:"optional"`
	mcpRegistry *mcpRegistry
	lifecycle   *lifetime
	// installed later by SetPreBridgeSpawn from the composition root.
	preBridgeSpawn func(context.Context) `wiring:"optional"`
	// Session/load replay-projection lifecycle. Nil in tests that exercise no load.
	replayProjection replayProjector
	// Fires off the spawn path after a successful session/load. Nil in tests.
	onSessionRehydrated func(vibekit.ChatID)
	// Fires from the WINNING closer once a turn finalized. A closure over the
	// runtime, not the collaborator: terminals are built after the coordinator.
	onTurnClosed func(vibekit.ChatID, vibekit.TurnEpoch)
	// Gates `_meta.kiro.secretStorage`. A func, read at SPAWN time, because this
	// constructor runs before the store opens. Nil declares it off.
	secretStorage func() bool `wiring:"optional"`
	// A chat's last self-declared status, for the agent-finished push body.
	chatStatus func(vibekit.ChatID) vibekit.ChatStatusPayload
	// Stop reasons already warned about, so an unmapped wire value costs one line.
	unknownStops sync.Map
	// Which chat's transcript primes a chat's FIRST session, for a tangent whose
	// session/fork was refused. Spent by the next spawn; not persisted.
	primeFrom map[vibekit.ChatID]vibekit.ChatID
	// Agent engine for every bridge spawned here; resolveAgentEngine owns it.
	agentEngine string
	// Filtered operator launch flags. CHAT spawns only: on the utility bridge
	// an `--effort max` would spend real credits on a two-word summary.
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

// takePrimeFrom claims and clears a chat's prime note: it is spent by the session
// it primes, so a later bridge must not re-inject a history already read.
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

// newBridgeCoordinator constructs a BridgeCoordinator from the Runtime's fields.
func newBridgeCoordinator(h *Runtime) *BridgeCoordinator {
	return &BridgeCoordinator{
		bridge:         h.bridge,
		chatStore:      h.chatStore,
		catalog:        h.catalog,
		turns:          newTurnRegistry(),
		broadcast:      h.bus.Broadcast,
		translateEvent: h.translateACPEvent,
		push:           h.push,
		mcpRegistry:    h.mcpRegistry,
		lifecycle:      h.lifecycle,
		// preBridgeSpawn is installed by SetPreBridgeSpawn after New returns.
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

// hasSecretStorage reports whether this process holds a credential store, and so
// whether a bridge may declare `_meta.kiro.secretStorage`. A nil resolver is off.
func (bc *BridgeCoordinator) hasSecretStorage() bool {
	return bc.secretStorage != nil && bc.secretStorage()
}

// processLifetimeCtx returns the context bounding a spawned subprocess: the
// runtime's shutdown context, never the caller's, which cancels when a turn ends.
func (bc *BridgeCoordinator) processLifetimeCtx() context.Context {
	return bc.lifecycle.shutdownCtx
}

// resolveAgentEngine returns the agent engine, hard-pinned to v3. A stray
// KIRO_AGENT_ENGINE=v1/v2 is ignored: vibekit cannot talk to a legacy engine.
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

	// The override is in the key, so callers wanting different models cannot coalesce.
	sfKey := bridgeSpawnKey(chatID, modelOverride)
	v, err, _ := bc.bridge.mgr.spawnSF.Do(sfKey, func() (any, error) {
		return bc.spawnBridge(ctx, chatID, modelOverride)
	})
	if err != nil {
		return nil, err
	}
	b, _ := v.(*sharedBridge)
	// Wake prompts parked on the admission slot: a live bridge changes their answer.
	bc.turns.wakeChat(chatID)
	return b, nil
}

// bridgeSpawnKey composes the spawn singleflight key over (chatID, modelOverride).
// The join must stay injective: a collision would hand back another chat's bridge.
func bridgeSpawnKey(chatID vibekit.ChatID, modelOverride string) string {
	return keyenc.Join(string(chatID), modelOverride)
}

// spawnBridge creates the bridge for chatID from inside the singleflight. The
// override beats the chat's stored model; a start failure rolls the entry back out.
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
	// Withhold an unserved INHERITED model silently and take the backend default; an
	// explicit pick is refused loudly in cmdSwitchModel. Empty ServedModelIDs allows.
	if !vibekit.ModelServed(model, chat.ServedModelIDs) {
		slog.Warn("withholding a model this account does not serve; using the backend default",
			"chat_id", chatID, "model", model)
		model = ""
	}

	// No mcpServers parameter: the agent reads its own hot-reloading config file, and
	// an inline list WINS over it, making every config edit look like a no-op.
	if bc.preBridgeSpawn != nil {
		bc.preBridgeSpawn(ctx)
	}

	// An override differing from the record is the switch-by-restart path: the stored
	// tier was chosen under the model being left, so resolve against the TARGET.
	effort := bc.effortFor(ctx, chat)
	if model != "" && model != chat.Model {
		effort = bc.EffortForSwitch(ctx, model)
	}

	if chat.ACPSessionID != "" {
		if bc.tryLoadSession(ctx, chatID, sb, chat.ACPSessionID, model, effort) {
			return sb, nil
		}
	}

	// EnableHooks:true lets the workspace's own .kiro/hooks/*.json autofire, run by
	// the agent internally. Forward MUST drain NotifCh BEFORE Start: the agent issues
	// _kiro/auth/getAccessToken as a REQUEST on the session-creation critical path,
	// so attaching after Start deadlocks every fresh session.
	go bc.Forward(chatID, sb.bridge)
	// Supervised is passed at creation only: KAS persists autopilot in its own
	// session metadata.
	if err := sb.bridge.Start(ctx, &vibekit.StartOpts{Lifetime: bc.processLifetimeCtx(), Model: model, Mode: chat.CurrentModeID, Effort: effort, AgentEngine: bc.agentEngine, EnableHooks: true, ExtraArgs: bc.acpArgs, Supervised: chat.SupervisedMode, SecretStorage: bc.hasSecretStorage(), Presets: securityPresets(ctx, bc.lifecycle.configDir), ToolSearch: toolSearchEnabled(ctx, bc.lifecycle.configDir), Knowledge: knowledgeEnabled(ctx, bc.lifecycle.configDir), Memory: memoryEnabled(ctx, bc.lifecycle.configDir)}); err != nil {
		return nil, setupErr(err)
	}
	bc.persistNewSessionMetadata(ctx, chatID, sb.bridge)

	sb.primed = false
	// A tangent's refused fork needs the injection: this session never saw the
	// conversation it was opened from.
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
	// Open the projection BEFORE Forward attaches: replay starts as soon as
	// session/load is accepted, inside Start below. Forward before Start, as above.
	if bc.replayProjection != nil {
		bc.replayProjection.OpenReplayProjection(chatID)
	}
	go bc.Forward(chatID, sb.bridge)
	if err := sb.bridge.Start(ctx, &vibekit.StartOpts{Lifetime: bc.processLifetimeCtx(), SessionID: acpSessionID, Model: model, Effort: effort, AgentEngine: bc.agentEngine, EnableHooks: true, ExtraArgs: bc.acpArgs, SecretStorage: bc.hasSecretStorage(), Presets: securityPresets(ctx, bc.lifecycle.configDir), ToolSearch: toolSearchEnabled(ctx, bc.lifecycle.configDir), Knowledge: knowledgeEnabled(ctx, bc.lifecycle.configDir), Memory: memoryEnabled(ctx, bc.lifecycle.configDir)}); err != nil {
		slog.Warn("session/load failed, starting new",
			"chat_id", chatID, "acp_session", acpSessionID, "error", err)
		// A failed load has no transcript to adopt; drop the partial replay.
		if bc.replayProjection != nil {
			bc.replayProjection.DiscardReplayProjection(chatID)
		}
		old := sb.bridge
		sb.bridge = bc.bridge.mgr.factory()
		old.Stop()
		// The replacement session has never seen this chat. Record why it needs priming,
		// or the agent answers the next prompt with no idea what came before it.
		sb.primeReason = primeReasonReload
		if mErr := bc.chatStore.Mutate(ctx, chatID, func(c *vibekit.Chat, ex bool) bool {
			if !ex {
				return false
			}
			// Detach from the stale session but KEEP it in the chain: its directory still
			// holds that period's transcript, and blanking the id let the reaper sweep it.
			c.RecordSession("")
			return true
		}); mErr != nil {
			slog.Error("clear stale acp_session_id", "chat_id", chatID, "error", mErr)
		}
		return false
	}
	// The load returned, which is the other half of the settle condition. The settle
	// belongs to Forward — see load_projection.go for why this goroutine cannot.
	if bc.replayProjection != nil {
		bc.replayProjection.MarkReplayLoadDone(chatID)
	}
	title := sb.bridge.SessionTitle()
	bc.catalog.SetModes(sb.bridge.Modes())
	bc.catalog.SetModels(sb.bridge.Models())
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
	// The chat is back; heal its restart-paused runs. Off the spawn path, and AFTER
	// the state flip so the resume's own Call finds an idle bridge.
	if bc.onSessionRehydrated != nil {
		go bc.onSessionRehydrated(chatID)
	}
	return true
}

// kasDefaultSessionTitle is KAS's own placeholder title, spread onto every
// session/new result's _meta. It carries no information, so adopting it would swap
// vibekit's placeholder for a worse one AND make the chat non-default-named, which
// then rejects the real title that arrives later.
const kasDefaultSessionTitle = "New Session"

// adoptKASTitle names a chat from KAS's own session title, but only while the chat
// has no name of its own and only when the title is real.
//
// Precedence on chat naming is: agent-authored focus_update title > local
// first-prompt label > KAS's session title. So this fires only for a chat vibekit
// never named, and never overwrites; `titleIsPromptDerived` (translate/focus.go)
// implements the top of that ordering.
func adoptKASTitle(c *vibekit.Chat, title string) {
	if title == "" || title == kasDefaultSessionTitle || c.Name != vibekit.DefaultChatName {
		return
	}
	c.Name = title
}

// applyLoadedSessionFacts copies what a RESUMED session reported onto the chat
// record, writing each field only when the load result actually carried it.
//
// The guard belongs here rather than one layer down: applySessionResultLocked
// already keeps-on-absent, but that keep is worthless on a resume, because the
// bridge is FRESHLY constructed and every accessor answers the zero value for
// whatever the result omitted. session/new's path keeps unconditional writes on
// purpose: there the overwrite IS the check.
func applyLoadedSessionFacts(c *vibekit.Chat, facts acpSessionFacts, title string) {
	if mode := facts.CurrentMode(); mode != "" {
		c.CurrentModeID = mode
	}
	adoptKASTitle(c, title)
}

func (bc *BridgeCoordinator) persistNewSessionMetadata(ctx context.Context, chatID vibekit.ChatID, bridge acpSessionFacts) {
	newSessionID := bridge.SessionID()
	newModelID := bridge.ModelID()
	currentMode := bridge.CurrentMode()
	served := bridge.ServedModels()
	title := bridge.SessionTitle()
	// The vocabulary this session advertised is a workspace fact, so it goes to the
	// one holder. Outside the Mutate: holding the chat lock across it would order
	// two unrelated locks for nothing.
	bc.catalog.SetModes(bridge.Modes())
	bc.catalog.SetModels(bridge.Models())
	// requestedMode is read before the line below overwrites it with what landed.
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

// reportModeNotApplied tells the user when the session did not get the mode the
// chat asked for.
//
// Storing the ACTUAL mode is right — the pill must not claim a role the agent is
// not running under — but it was also the ONLY record of the request: one
// transient `session/set_mode` failure permanently converted a chat pinned to
// `spec` into a default-mode chat, because the next spawn's requested id then
// EQUALS the current one. A banner, not a retry, which needs no second field.
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

// HasLiveBridge reports whether a chat currently has a bridge, i.e. whether it is
// in active use. Retention's exemption reads this: an open chat is never purged.
func (rt *Runtime) HasLiveBridge(chatID vibekit.ChatID) bool {
	return rt.bridge.mgr.get(chatID) != nil
}

// CloseBridge stops a bridge and removes it from the map.
func (bc *BridgeCoordinator) CloseBridge(chatID vibekit.ChatID) {
	bc.bridge.mgr.close(chatID)
}

// replayProjector is the slice of the Runtime's replay-projection lifecycle the
// coordinator drives. See agent/load_projection.go for the settle barrier.
type replayProjector interface {
	OpenReplayProjection(vibekit.ChatID)
	MarkReplayLoadDone(vibekit.ChatID)
	DiscardReplayProjection(vibekit.ChatID)
	SettleReplayProjection(chatID vibekit.ChatID, buffered int, force bool)
}

// Forward is the ACP notification → domain event translator, run as a
// goroutine per bridge.
func (bc *BridgeCoordinator) Forward(chatID vibekit.ChatID, bridge ACPBridge) {
	ch := bridge.NotifCh()
	// This goroutine IS the folder, so the position it reports is the only one a
	// local settle can order itself against. The generation keeps a straggler from
	// the previous bridge from advancing a counter that restarted at zero.
	gen := bc.turns.attachForward(chatID)
	for n := range ch {
		bc.consumeFrame(chatID, gen, n)
		// Settle a session/load replay projection here rather than at Start's return:
		// this goroutine drains the frames, so its own view of the channel depth is the
		// only sound completion signal. Rationale in agent/load_projection.go.
		if bc.replayProjection != nil {
			bc.replayProjection.SettleReplayProjection(chatID, len(ch), false)
		}
	}
	// The channel closed, so no further frame can trigger the check above. Force the
	// settle, or a load whose trailing catalog frames never came leaks a projection.
	if bc.replayProjection != nil {
		bc.replayProjection.SettleReplayProjection(chatID, 0, true)
	}
	// No frame can advance the position now, so anything parked on one has to be
	// told. Before the death closer, so a woken settle has already deferred by then.
	bc.turns.sealPosition(chatID, gen)

	slog.Info("bridge exited", "chat_id", chatID)

	// Still registered means nobody removed it, so the process died on its own: the
	// third actor closes whatever turn is still open, because no other closer will.
	if bc.bridge.mgr.removeIfBridge(chatID, bridge) {
		bc.closeTurnOnBridgeDeath(bc.lifecycle.shutdownCtx, chatID)
	}

	// Flush staged writes for the chat. A bridge exit leaves the supervised
	// fs-handler goroutine parked on its resume channel and a phantom "awaiting
	// approval" pending op that would replay to reconnecting clients.
	lastBridge := bc.bridge.mgr.count() == 0

	if lastBridge {
		bc.mcpRegistry.clearAll(bc.lifecycle.shutdownCtx)
	}
	// A run chat has no record and no turn lifecycle beyond the position bookkeeping
	// above, and nothing calls cleanupChatState for one, so it is dropped here.
	if isRunChat(chatID) {
		bc.turns.forget(chatID)
	}
}

// consumeFrame translates one frame and then advances the chat's observed
// position, whatever the frame did. DEFERRED, and per FRAME rather than per fold:
// the advance acknowledges work that is done, and many paths through the
// session-update cascade consume a frame without touching a turn.
func (bc *BridgeCoordinator) consumeFrame(chatID vibekit.ChatID, gen uint64, n vibekit.Notification) {
	defer bc.turns.observe(chatID, gen, n.Seq)
	bc.translateEvent(chatID, n.Msg)
}

// PrimeIfNeeded sends the chat history as an ephemeral priming prompt on the
// current bridge, unless this session already has it.
//
// The primed flag is claimed HERE rather than by the caller, so there is one owner
// for it and no interface assertion back down to *sharedBridge: a chat with no
// bridge is simply nothing to prime.
func (bc *BridgeCoordinator) PrimeIfNeeded(ctx context.Context, chatID vibekit.ChatID) {
	sb := bc.bridge.mgr.get(chatID)
	if sb == nil {
		return
	}
	if !sb.claimPriming() {
		return
	}
	// Preambles live in translate (PrimePreamble*) because the focus-title derivation
	// filter must recognise a title KAS derives from this prime text — one definition
	// keeps the filter and the prime in lockstep.
	//
	// The reason decides the preamble AND whose history is read: a tangent whose fork
	// was refused has no transcript of its own and needs the parent's, which is why
	// the source is read off the bridge rather than assumed to be chatID.
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
	// The prime is a real session/prompt, so it opens and closes a real turn. It then
	// AWAITS its own epoch, which is what keeps the unacknowledged set from holding
	// two: a wire turn_start can only ever bind to one candidate.
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

// Effort lives on the chat record (vibekit.Chat.Effort), not in settings.

// NotifyPush sends a push notification about one CHAT if the push service is
// configured.
//
// It keeps its chat-id parameter rather than taking a vibekit.PushSubject: every
// caller here is chat-scoped, so the conversion belongs at this one boundary. A
// notification with no chat behind it calls push.Send with its own subject.
func (bc *BridgeCoordinator) NotifyPush(ctx context.Context, body string, kind vibekit.PushKind, chatID vibekit.ChatID) {
	if bc.push == nil || !bc.push.HasSubscribers() {
		return
	}
	bc.lifecycle.inflight.Go(func() {
		bc.push.Send(ctx, push.DefaultTitle, body, kind, vibekit.ChatSubject(chatID))
	})
}

// SettleTurnOnResponse closes the turn named by epoch on the response that
// settled it — once the folder has consumed everything queued behind that
// response, and only if the wire's own turn_end did not get there first.
//
// seq is the read loop position the response arrived at. Zero skips the wait,
// which is what the two paths that deliberately reach no bracket want.
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
// Must be read BEFORE the turn_ended broadcast, not at the push site: the status
// cache is cleared at turn end, inside the same emit() that fires turn_ended, so a
// read after that broadcast would always find it gone. Empty is legitimate — an
// agent need never call update_session_information.
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
func agentFinishedBodyFrom(description string) string {
	if d := strings.TrimSpace(description); d != "" {
		return d
	}
	return defaultAgentFinishedBody
}

// persistTurn commits the finalized assistant turn to the chat file.
//
// A failed append is survivable: KAS's own log flushes each sub-message as it
// COMPLETES, so a session/load replay carries the turn and the projection rebuilds
// it. What that does not cover is the final streaming fragment.
func (bc *BridgeCoordinator) persistTurn(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.Message) {
	if err := bc.chatStore.AppendMessage(ctx, chatID, msg); err != nil {
		slog.Error("persist assistant turn; the replay projection is the fallback",
			"chat_id", chatID, "error", err)
	}
}

// TryFastModelSwitch attempts an in-session model swap via
// session/set_config_option on the running bridge, then re-applies effort.
func (bc *BridgeCoordinator) TryFastModelSwitch(ctx context.Context, chatID vibekit.ChatID, model, effort string) bool {
	sb := bc.bridge.mgr.get(chatID)
	if sb == nil {
		return false
	}
	return bc.applyModelSwitch(ctx, chatID, sb, model, effort)
}

// applyModelSwitch swaps the model on a bridge the caller ALREADY HOLDS, then
// re-applies the level.
//
// Takes the bridge rather than the chat id because only one of the two callers can
// look it up safely: the restart fallback has just been handed a freshly-loaded
// bridge, and re-resolving by id could answer with a DIFFERENT bridge — a closing
// chat, or a spawn that raced the old bridge's exit — so the pick would land on a
// session the caller never loaded.
func (bc *BridgeCoordinator) applyModelSwitch(
	ctx context.Context, chatID vibekit.ChatID, sb *sharedBridge, model, effort string,
) bool {
	if err := sb.bridge.SetModel(ctx, model); err != nil {
		slog.Info("model switch: fast path failed, falling back to restart",
			"chat_id", chatID, "model", model, "error", err)
		return false
	}
	// Re-assert the level after the swap, or the swap can take it away: KAS reconciles
	// the session's effortLevel against the NEW model's tier list and replaces it with
	// that model's default whenever the current level is absent — measured on 2.19.1,
	// a swap to `auto`, which offers no tiers, destroys it. SetModel clears the
	// bridge's cached level for that reason, so EnsureEffort asserts here.
	//
	// Best-effort: the swap already landed and is the operation the user asked for.
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
// ALREADY OPEN — the one checkpoint that catches a level KAS changed on its own
// (pinSessionModelId settling an unset model, or a switch made from the IDE/TUI).
//
// At the prompt rather than reactively on the notification: a reactive Call from
// the Forward goroutine would block the drain it waits on. Bridge.EnsureEffort
// compares before it calls, so the normal case is one comparison and no round
// trip. Best-effort and log-only: a chat must not fail to answer over a preference.
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

// healEffort re-asserts the chat's chosen reasoning-effort level when a
// config_option_update reports the session running at a different one. It wraps
// the config-option handler in the dispatch table (see initDispatch).
//
// It covers repairEffort's hole: that runs on OpenBridge's ALREADY-OPEN path,
// which the turn that SPAWNS the bridge never takes, so a chat whose whole life is
// one turn keeps the wrong level. Reactive, because the divergence appears DURING
// the turn, and LATCHED once per bridge, or the repair's own update would loop.
func (bc *BridgeCoordinator) healEffort(next sessionUpdateHandler) sessionUpdateHandler {
	return func(ctx context.Context, chatID vibekit.ChatID, raw json.RawMessage, attr translate.FrameAttribution) {
		next(ctx, chatID, raw, attr)
		// The chat's OWN frame only: a step's session reports the level IT runs at, and
		// a subagent's frame is attributed too. Both fields are tested, because an empty
		// SubSessionID alone does not mean the chat owns the frame — a step has one too.
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
		// The frame IS the session reporting its level, and the bridge forwards this
		// channel unread, so hand the report over before deciding anything: it is what
		// lets EnsureEffort assert here rather than compare equal against the ask.
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
		// On inflight rather than untracked: Shutdown stops every bridge BEFORE it waits
		// on this group, which is the ordering a blocked bridge Call unblocks through.
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

// effortFor resolves the reasoning-effort level a chat's next session starts at:
// the chat's own choice, else the last level the user picked anywhere
// (settings.KeyLastEffort). Empty means send nothing and take the model's default.
//
// The seed is a FALLBACK never written onto the chat record — stamping it would
// pin an unchosen chat to today's value for every later session — and it is
// MODEL-SCOPED, because an explicit tier is a judgement about one model. Validated
// here because config.json is user-editable and an unknown level must not ship.
func (bc *BridgeCoordinator) effortFor(ctx context.Context, chat *vibekit.Chat) string {
	if chat.Effort != "" {
		return chat.Effort
	}
	return bc.effortSeedFor(ctx, chat.Model)
}

// effortSeedFor answers the remembered level for exactly one model: the
// KeyLastEffort/KeyLastEffortModel pair when the recorded model IS `model`, else
// "". The one seed read, so session start and model switch cannot disagree.
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
// when it was picked under the TARGET model, else the target's own default from
// the workspace catalog, else "" (KAS reconciles on its own).
//
// Deliberately NOT effortFor: the chat's stored choice was made under the model
// being left, so honouring it here is what carried `max` from one model onto the
// next (user report, 2026-08-31). Explicit, because KAS KEEPS a fitting level.
func (bc *BridgeCoordinator) EffortForSwitch(ctx context.Context, model string) string {
	if level := bc.effortSeedFor(ctx, model); level != "" {
		return level
	}
	return bc.catalog.DefaultEffortFor(model)
}

// PersistModelSwitch records the switch event and updates the chat's
// model + resets usage counters.
func (bc *BridgeCoordinator) PersistModelSwitch(ctx context.Context, chatID vibekit.ChatID, model string, contextSize int) {
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
		// The chat's chosen tier was a judgement about the model being switched AWAY
		// from, so it does not survive: resolution falls to EffortForSwitch. Switching
		// back re-applies the seed when the pick was made under that model.
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

// assistantTurnMessage builds the persisted assistant message from a finished
// turn's content. Extracted so the interrupted path and the normal path cannot
// drift: every field below exists because something in the client reads it after a
// reload, so a second hand-written literal would quietly lose one of them.
//
// It takes the SNAPSHOT rather than the buffer, so every field comes from ONE
// guarded read.
func assistantTurnMessage(snap *buffer.TurnContent, stats turnStats, model string, c vibekit.TurnConclusion) vibekit.Message {
	return vibekit.Message{
		ID:        snap.MessageID,
		Role:      vibekit.RoleAssistant,
		Ts:        time.Now().UnixMilli(),
		Content:   snap.Content,
		Reasoning: snap.Reasoning,
		ToolCalls: snap.ToolCalls,
		// Blocks captures the chronological text/tool/thinking emission order; client
		// renderers prefer it over Content+ToolCalls.
		Blocks: snap.Blocks,
		// CodeReferences persists the turn's licensed-code attributions so the chip
		// survives reload; the streamed assistant turn is never re-broadcast.
		CodeReferences: snap.CodeReferences,
		// Refusal metadata (kiro-cli 2.13), stamped from the refusal explanation
		// chunk; persisting it here is what makes the callout survive reload.
		Refusal: snap.Refusal,
		// Turn summary (credits · elapsed · files changed), persisted so the footer
		// survives reload; omitempty drops a read-only turn's zeros.
		TurnCredits:   stats.CreditsDelta,
		TurnElapsedMs: stats.ElapsedMs,
		ChangedFiles:  snap.ChangedFiles,
		// Which model answered, latched when the turn opened: the chat's Model is the
		// CURRENT one, and a footer derived from it would relabel history.
		TurnModel: model,
		// How the turn ENDED, durable at last: a live stop reason is broadcast and
		// never stored, so a reloaded transcript read a failed turn as completed.
		TurnOutcome:       c.Outcome,
		TurnStopReasonRaw: c.RawStop,
		TurnTruncated:     c.Truncated,
	}
}

// AbandonInFlightTurn finalizes a turn whose prompt call could not finish it,
// PERSISTING the partial rather than dropping it.
//
// Without a call that takes the assistant buffer, that buffer survived with
// Started == true and the next prompt extended the dead turn's blocks under the
// dead turn's message id — one message holding two turns' replies. It waits for no
// read loop position: the two failures that reach it settle with the bridge alive.
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
// wireTurnStart turn opens in its place, holding no prompt slot for admission
// control to have refused.
func (bc *BridgeCoordinator) WireTurnStart(ctx context.Context, chatID vibekit.ChatID) {
	bound, displaced := bc.turns.bindPending(chatID)
	if !bound && displaced != 0 {
		// A pre-open is owed this bracket while another turn is still folding, so
		// A pre-open is owed this bracket while another turn is still folding, so that
		// turn's end never arrived. Close it and bind on the retry, not over it.
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
// A turn_end for a chat with NO open turn is a no-op: without that rule a
// cancel-grace expiry that closed its turn locally would meet the later wire
// bracket and the fold-with-no-open-turn rule would manufacture a spurious empty
// persisted turn. A replayed bracket is filtered upstream.
func (bc *BridgeCoordinator) WireTurnEnd(ctx context.Context, chatID vibekit.ChatID, stop vibekit.StopReason) {
	bc.finalizeTurn(ctx, chatID, turnClose{Closer: closerWireEnd, Stop: stop, AnyOpen: true})
}

// TurnFoldTarget returns the buffer this chat's frames fold into, opening a turn
// of the caller's stated source when none is open: a fold with no open turn is a
// turn vibekit did not prompt, and it needs a record like any other. The SOURCE
// comes from the frame, because a step of a chat-parented run folds here and the
// turn opened for it belongs to the RUN.
//
// The CHEAP question comes first: the open facts cost a full chat-file read under
// the per-chat mutex, per delta. Lock order is lifecycle then chat store, never back.
func (bc *BridgeCoordinator) TurnFoldTarget(ctx context.Context, chatID vibekit.ChatID, source vibekit.TurnOpenSource) *buffer.Buffer {
	if buf, ok := bc.turns.foldTarget(chatID); ok {
		return buf
	}
	model, credits := bc.turnOpenFacts(ctx, chatID, source)
	t := bc.turns.openWire(ctx, chatID, source, model, credits)
	if t == nil {
		// ctx died while the chat was finalizing. A throwaway buffer keeps the handler's
		// shape rather than making every fold site nil-check; the frame is lost anyway.
		return buffer.New()
	}
	return t.Buf
}

// ReviseTurnBinding acts on a frame that PROVES the open turn is the agent's own
// rather than the prompt's: `agentInitiated` rides content frames and never the
// bracket, so this is the only discriminator there is. See turnRegistry.reclassify.
func (bc *BridgeCoordinator) ReviseTurnBinding(ctx context.Context, chatID vibekit.ChatID) {
	bc.turns.reclassify(ctx, chatID)
}

// closeTurnOnBridgeDeath is the third actor: after Forward has exited it closes
// any turn still open, because nothing else is going to.
//
// It fires only on an UNEXPECTED exit, and the discriminator is whether the bridge
// was still registered when it died: every teardown vibekit performs itself
// removes the bridge from the map first and has its own closer, so a deliberate
// stop must not also read as a death.
func (bc *BridgeCoordinator) closeTurnOnBridgeDeath(ctx context.Context, chatID vibekit.ChatID) {
	bc.finalizeTurn(ctx, chatID, turnClose{Closer: closerBridgeDeath, AnyOpen: true})
}

const stopReasonCancelled = vibekit.StopReasonCancelled

// InterruptTurn records why kiro-cli abandoned a turn without answering it, and
// trips that turn's prompt call so the ordinary failure path finalizes it.
// Satisfies translate.TurnInterruptAccess.
//
// The cause lands on the TURN, epoch-scoped and first-wins. The bridge is left
// ALIVE and the ACP session untouched: only the tool call was cancelled, so
// tripping the prompt context frees the slot. A chat with no open turn, no bridge,
// or a cause already claimed is not a failure — logged at Debug.
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
func (rt *Runtime) BridgeRespond(ctx context.Context, chatID vibekit.ChatID, requestID int64, result any, err error) error {
	sb := rt.bridge.mgr.get(chatID)
	if sb == nil {
		return nil
	}
	return sb.bridge.Respond(ctx, requestID, result, err)
}

// ParentACPSession returns the ACP session id of the running bridge for chatID, or
// "" when no bridge exists. Translator helpers use it to short-circuit
// notifications whose top-level sessionId belongs to a subagent.
func (bc *BridgeCoordinator) ParentACPSession(chatID vibekit.ChatID) string {
	sb := bc.bridge.mgr.get(chatID)
	if sb == nil {
		return ""
	}
	return string(sb.bridge.SessionID())
}
