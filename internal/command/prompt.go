package command

// Prompt command handler: validates, persists user message, acquires
// bridge, sends to kiro-cli, handles empty-turn recovery.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/cplieger/vibekit/internal/chat"
	"github.com/cplieger/vibekit/internal/ids"
	"github.com/cplieger/vibekit/internal/rpcerr"
	"github.com/cplieger/vibekit/internal/settings"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// validatePromptPayload parses and validates the prompt command payload.
func validatePromptPayload(cmd *vibekit.ClientCommand) (vibekit.PromptCommand, int, error) {
	var p vibekit.PromptCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return p, http.StatusBadRequest, ErrInvalidPayload
	}
	if p.Text == "" {
		return p, http.StatusBadRequest, errEmptyPrompt
	}
	if len(p.Text) > maxPromptBytes {
		return p, http.StatusRequestEntityTooLarge, errPromptTooLong
	}
	if p.MessageID == "" {
		return p, http.StatusBadRequest, errMissingMessageID
	}
	if !ValidMessageID(p.MessageID) {
		return p, http.StatusBadRequest, ErrInvalidPayload
	}
	if !ValidIdent(p.Model) {
		return p, http.StatusBadRequest, ErrInvalidPayload
	}
	return p, 0, nil
}

// promptRetryDelay is the one wait this package retries at. A constant rather
// than a parameter because there is a single call site and a single value, and a
// knob nothing turns is a knob a reader has to check.
const promptRetryDelay = 2 * time.Second

// promptReply is a session/prompt response and the read loop position at which it
// arrived.
//
// The position travels with the response because the turn's local settle has to
// order itself against the notifications still queued behind it: a settle that
// reads the response alone decides the wire never closed the turn while the
// wire's own turn_end is three frames back in the channel.
type promptReply struct {
	resp *vibekit.RPCResponse
	seq  uint64
}

// retry re-invokes fn up to maxAttempts more times, promptRetryDelay apart, for
// as long as shouldRetry keeps saying yes. The delay is FIXED — there is no
// backoff, and the name used to claim one.
//
// A bare `time.After` in the loop rather than one reused timer: since Go 1.23 a
// timer is collected as soon as it is unreachable even if it never fired and
// Stop was never called, so the reuse this used to do (NewTimer, defer Stop,
// Reset per iteration) bought nothing but a redundant re-arm on the first pass.
// At maxAttempts of 2 against a 2s delay the allocation is not measurable.
func retry(ctx context.Context, maxAttempts int, shouldRetry func(error) bool, fn func() (promptReply, error)) (promptReply, error) {
	result, err := fn()
	if err == nil || !shouldRetry(err) {
		return result, err
	}
	for range maxAttempts {
		select {
		case <-time.After(promptRetryDelay):
		case <-ctx.Done():
			return result, err
		}
		result, err = fn()
		if err == nil || !shouldRetry(err) {
			break
		}
	}
	return result, err
}

// callPromptWithRetry sends the prompt to kiro-cli, retrying only the classes a
// second attempt can actually fix. The class is logged either way, because the
// old single-boolean version logged "prompt retry" for a dead bridge and nothing
// at all for a throttle, which is exactly backwards from what a reader needs.
func callPromptWithRetry(ctx context.Context, sb bridgeCaller, params map[string]any, chatID vibekit.ChatID) (promptReply, error) {
	return retry(ctx, 2, func(err error) bool {
		class := classifyPromptFailure(err)
		retry := class == classBusy || class == classTransient
		slog.Warn("prompt failure",
			"chat_id", chatID, "class", class.String(), "retry", retry, keyError, err)
		return retry
	}, func() (promptReply, error) {
		resp, seq, err := sb.CallAt(ctx, vibekit.MethodPrompt, params)
		return promptReply{resp: resp, seq: seq}, err
	})
}

// recoverEmptyTurn re-prompts a turn that ended having produced nothing:
// recreate the session, then send the same prompt once more.
//
// result is the finalized turn's CAPTURED outcome. The caller awaited it while
// still holding the turn's completion handle — the registry drops the retained
// result at the last release — and it keeps holding that handle until this
// returns, so the TurnOpenedAfter read below is against a live record. The
// decision comes off the finalized TURN, never off the live buffer: the buffer
// can still be withholding the turn's only text when this runs, so a
// measurement taken here recreates a session that had just answered. It stays
// on the prompt goroutine — see AwaitTurn.
//
// The caller released BOTH admission holds before this runs, so the retry
// competes like anything else and re-reserves with a TRY: a user prompt that
// won the slot in that window abandons the retry, which is the right
// direction — their turn is live, and the empty turn is already recorded.
func recoverEmptyTurn(ctx context.Context, bridges BridgeAccess, chats ChatStore, bus Broadcaster, outcome TurnOutcomeAccess, chatID vibekit.ChatID, epoch vibekit.TurnEpoch, result vibekit.TurnResult, p *vibekit.PromptCommand, params map[string]any) {
	// A verb KAS answers itself produces no content BY DESIGN, so an empty turn
	// is the correct outcome and recovery is pure damage: it would close the
	// bridge the launched run is parented on, detach the session, badge the turn
	// interrupted, and re-send the verb — a second run for one request. Checked
	// before the outcome because the turn is genuinely empty here; the prompt TEXT
	// is the only thing that distinguishes expected from broken.
	if kasClaimsPromptText(p.Text) {
		return
	}
	// WireEnded, because a locally-closed turn's outcome is the prompt response's —
	// nothing richer than end_turn or cancelled, and nothing at all on a fault — so
	// re-prompting on it is re-prompting on a guess.
	//
	// EmittedNothing, measured after the steer carry was settled back in, or a turn
	// whose only final text resembled a steering acknowledgement reads as empty.
	if !result.WireEnded || result.Stop != vibekit.StopReasonEndTurn || !result.EmittedNothing {
		return
	}
	if outcome.TurnOpenedAfter(chatID, epoch) {
		slog.Info("empty turn: a later turn opened on this chat, so the binding was not ours",
			"chat_id", chatID, "epoch", epoch)
		return
	}
	if !outcome.TryReserveTurn(chatID, vibekit.TurnSourceEmptyRetry) {
		slog.Warn("empty turn: another turn was admitted during recovery, abandoning retry",
			"chat_id", chatID)
		return
	}
	defer outcome.ReleaseTurnReservation(chatID)
	slog.Warn("empty turn detected, recreating session", "chat_id", chatID)
	refreshRetrySession(ctx, bridges, chats, chatID)
	retryEmptyTurnPrompt(ctx, bridges, bus, outcome, chatID, p, params)
}

// refreshRetrySession abandons the session that answered nothing: close its
// bridge, detach the chat from it, and record why on the transcript.
func refreshRetrySession(ctx context.Context, bridges BridgeAccess, chats ChatStore, chatID vibekit.ChatID) {
	bridges.CloseBridge(chatID)
	if err := chats.Mutate(ctx, chatID, func(c *vibekit.Chat, ex bool) bool {
		if !ex {
			return false
		}
		// Detach, don't forget: the abandoned session still holds this chat's
		// earlier transcript on disk, and it must stay in the chain or the
		// reaper sweeps it as an orphan.
		c.RecordSession("")
		return true
	}); err != nil {
		slog.Error("empty turn: clear session ID", "chat_id", chatID, keyError, err)
	}
	// Content is what the boundary divider RENDERS (messages-events.ts gives
	// `interrupted` a labelFn), so this is the user-facing account of why the
	// turn stopped. The other writer of this event, AbandonInFlightTurn, leaves
	// it empty on purpose: a failed prompt already sends its reason as an error
	// frame, and the divider falls back to the generic label.
	evt := vibekit.Message{
		ID: ids.NewMessageID(), Role: vibekit.RoleEvent, Ts: time.Now().UnixMilli(),
		EventKind: vibekit.EventInterrupted, Content: "Session refreshed, retrying",
	}
	if err := chats.AppendMessage(ctx, chatID, &evt); err != nil {
		slog.Error("empty turn: append event", "chat_id", chatID, keyError, err)
	}
}

// retryEmptyTurnPrompt respawns the bridge and re-sends the prompt as a turn of
// its own. The caller holds the retry's admission reservation.
func retryEmptyTurnPrompt(ctx context.Context, bridges BridgeAccess, bus Broadcaster, outcome TurnOutcomeAccess, chatID vibekit.ChatID, p *vibekit.PromptCommand, params map[string]any) {
	sb2, err2 := bridges.OpenBridge(ctx, chatID, p.Model)
	if err2 != nil {
		slog.Error("empty turn: respawn failed",
			"chat_id", chatID, keyError, err2)
		bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventError, chatID, vibekit.ErrorPayload{
			Code:    vibekit.ErrCodeRecoveryFailed,
			Message: "Session refresh failed: " + rpcerr.Text(err2),
		}))
		return
	}
	// Take the new bridge's prompt slot the same way the prompt goroutine takes
	// it, rather than asserting it with SetPrompting. OpenBridge leaves the
	// bridge registered and IDLE, so between that return and this line a
	// concurrent taker can win the slot -- and an unconditional SetPrompting
	// would then let the deferred release below flip that turn's slot to idle
	// while it is still streaming. Losing the race means abandoning the retry,
	// which is the right direction: the empty turn this would have replaced is
	// already recorded as interrupted.
	if !sb2.TryAcquireForPrompt() {
		slog.Warn("empty turn: another turn started during recovery, abandoning retry",
			"chat_id", chatID)
		return
	}
	defer sb2.ReleaseAfterPrompt()

	// The retry needs its own cancellable context registered as the in-flight
	// prompt, for the same reason the first send's does: session/cancel is a
	// NOTIFICATION nothing acks, so the 10s grace budget is what unblocks a turn
	// KAS never answers. Without BeginPromptCall, sb2.promptCancel is nil,
	// ArmCancelGrace refuses, and an unanswered retry holds this chat in
	// bridgePrompting until the bridge dies -- there is no prompt ceiling to
	// catch it, deliberately, because a real turn can run for hours.
	ctx, cancelRetry := context.WithCancel(ctx)
	defer cancelRetry()
	sb2.BeginPromptCall(cancelRetry)
	defer sb2.EndPromptCall()

	params[vibekit.KeySessionID] = sb2.SessionID()
	// The retry is a turn of its own — its own epoch, its own buffer, its own
	// bracket — and is closed on every path out of here. Its source says so: the
	// turn it replaces is already closed, so neither close can stand in for the
	// other, and the retry's reply must not extend the message of the turn it
	// replaced.
	retryEpoch := outcome.StartTurn(ctx, chatID, vibekit.TurnSourceEmptyRetry)
	if retryEpoch == 0 {
		// Dead ctx: shutdown, or the turn context died. With no epoch nothing
		// would finalize, so no ACP call is made.
		slog.Warn("empty turn: the retry turn could not start", "chat_id", chatID)
		return
	}
	defer outcome.ReleaseTurn(chatID, retryEpoch)
	reply, retryErr := callPromptWithRetry(ctx, sb2, params, chatID)
	if retryErr != nil {
		slog.Error("retry prompt failed", "chat_id", chatID, keyError, retryErr)
		reason := promptFailureReason(retryErr)
		outcome.AbandonInFlightTurn(ctx, chatID, retryEpoch, reason)
		bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventError, chatID, vibekit.ErrorPayload{
			Code:    vibekit.ErrCodeRecoveryFailed,
			Message: "Retry prompt failed: " + reason,
		}))
		return
	}
	outcome.SettleTurnOnResponse(ctx, chatID, retryEpoch, reply.seq, reply.resp)
}

// supervisedDefaultSetting reads the settings-wide Supervised default applied to
// newly auto-created chats. Fails CLOSED to false: supervised mode is opt-in, and
// a corrupt config.json must not suddenly gate every write on approval.
//
// Read here rather than through an internal/permissions package. That package was
// 29 lines wrapping one settings read with a single caller, and it was named for a
// responsibility vibekit no longer has — tool authorization is Cedar's and the
// write gate is KAS's, so a package called "permissions" holding one default was
// the last thing making it look otherwise.
func supervisedDefaultSetting(ctx context.Context, configDir string) bool {
	var b bool
	if !settings.FieldInto(ctx, configDir, settings.KeySupervisedDefault, &b) {
		return false
	}
	return b
}

// appendUserMessage adds the prompt's user message to the chat.
func appendUserMessage(ctx context.Context, chats ChatStore, bus Broadcaster, ws Workspace, chatID vibekit.ChatID, p *vibekit.PromptCommand) error {
	supervisedDefault := supervisedDefaultSetting(ctx, ws.ConfigDir)
	err := chats.Mutate(ctx, chatID, func(c *vibekit.Chat, exists bool) bool {
		// Idempotent by message id (the documented invariant): if this id
		// is already in the store — e.g. a 409-queued prompt whose first
		// attempt persisted the user message before the busy check, now
		// re-sent by the client's prompt queue — skip the append AND the
		// broadcast so no duplicate user bubble renders. The prompt itself
		// still proceeds (Mutate returns nil on a false mutator).
		if hasMessageID(c, p.MessageID) {
			return false
		}
		if !exists {
			c.Name = vibekit.DefaultChatName
			c.Model = p.Model
			c.SupervisedMode = supervisedDefault
		}
		userMsg := vibekit.Message{
			ID:      p.MessageID,
			Role:    vibekit.RoleUser,
			Ts:      time.Now().UnixMilli(),
			Content: p.Text,
			// The attachments belong on the RECORD, not only on the outbound
			// prompt: BuildPromptBlocks folds each one into a content block, and
			// for an image or a document the path never reaches Content, so a
			// turn read back later has no way to say what was attached.
			Attachments: p.Attachments,
		}
		c.Messages = append(c.Messages, userMsg)
		// The text just left the composer, so the draft holding it is spent.
		// Cleared HERE rather than only by the client's own set_draft: if that
		// POST is lost, a reload would put the sent message back in the box.
		hadComposer := c.Draft != "" || len(c.Attachments) > 0
		c.Draft = ""
		// Same argument for the files that went with it: the pill row emptied on
		// send, so a lost set_attachments would otherwise bring three already-sent
		// attachments back on the next open. They are recorded on the MESSAGE just
		// above, which is where a sent turn reads them from.
		c.Attachments = nil
		if c.Name == vibekit.DefaultChatName && len(c.Messages) == 1 {
			name := TruncateRunes(p.Text, 80)
			if name != p.Text {
				name += ellipsis
			}
			c.Name = name
		}
		bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventMessageAppended, chatID, &userMsg))
		// And SAY that the composer was cleared. CmdSetDraft is not the only writer
		// of this field, so it cannot be the only emitter of the frame: without
		// this, the clear above was silent and every other client — and this one
		// after a reload — kept serving the text that was already sent. The client's
		// own clearing set_draft is not a substitute, because it can be superseded
		// or lost, which is the whole reason the clear is duplicated here.
		//
		// Guarded, so the frame keeps meaning "something changed" (broadcastComposer
		// treats nil as no-change for the same reason): a prompt sent with an empty
		// composer is the common case and has nothing to announce.
		if hadComposer {
			cleared := c.Composer()
			broadcastComposer(ctx, bus, chatID, &cleared)
		}
		return true
	})
	return err
}

// hasMessageID reports whether the chat already contains a message with
// the given id. Scans backwards — a retried prompt's original append is
// almost always the most recent message.
func hasMessageID(c *vibekit.Chat, id string) bool {
	for i := range slices.Backward(c.Messages) {
		if c.Messages[i].ID == id {
			return true
		}
	}
	return false
}

// AdmissionWait is the design's ADMISSION_WAIT_MS: the longest a prompt waits
// for a contended admission slot before answering 409. It MUST stay below the
// client's API timeout (@cplieger/fetch API_TIMEOUT_MS, pinned cross-language
// via testdata/client_api_timeout.json) or the client aborts the POST before
// the refusal arrives; TestAdmissionWait_StaysUnderTheClientAPITimeout pins
// the margin. A var rather than a const only so tests — this package's and
// the agent package's integration suite — can shrink a deliberately
// contended wait; production never writes it.
var AdmissionWait = 20 * time.Second

// reasonStarting is the 409 refusal class whose holder cannot receive a steer:
// a cold spawn, a shell, or a prime. The client branches on the VALUE — it
// renders the busy face and retries instead of converting the 409 to a steer.
const reasonStarting = "starting"

// promptAck is the prompt's EARLY acknowledgement: admission is decided
// synchronously and the turn runs on its own goroutine, so the POST answers as
// soon as the user message is persisted and the admission slot is held.
type promptAck struct {
	MessageID string `json:"message_id"`
	Accepted  bool   `json:"accepted"`
}

// CmdPrompt handles the prompt command: validate → persist → admit → ack. The
// turn itself — spawn, prime, MCP wait, StartTurn, the ACP call, the finalize
// and the empty-turn recovery — runs on its own goroutine (runPromptTurn), so
// the POST answers in the time of a disk append rather than a turn.
func CmdPrompt(ctx context.Context, roles *promptRoles, cmd *vibekit.ClientCommand) (any, error) {
	if cmd.ChatID == "" {
		return nil, StatusError(http.StatusBadRequest, ErrMissingChatID)
	}
	p, code, vErr := validatePromptPayload(cmd)
	if vErr != nil {
		return nil, StatusError(code, vErr)
	}

	// Shell command interception.
	if strings.HasPrefix(p.Text, "!") {
		return HandleShellInterception(ctx, roles, cmd, &p)
	}

	// 1. Ensure the chat exists and append the user message, naming the chat
	// from its first prompt.
	//
	// A refused write answers HERE, before anything else runs, because every step
	// below depends on the record this one declined to create. Carrying on spawns
	// a bridge for a chat with no file (breaking the live-bridge invariant), sends
	// the prompt, spends the credits, lets the agent write to the workspace, and
	// then loses the whole turn at persist, since the same refusal applies there —
	// and every one of those steps reports success.
	//
	// Persist precedes ADMISSION too, deliberately: a 409'd prompt leaves a
	// persisted row exactly as an accepted one does, the idempotent-by-message-id
	// append dedupes the client's re-send of the same text, and the client's
	// post-persist failure discipline (no text restore) depends on the order.
	if err := appendUserMessage(ctx, roles.chats, roles.bus, roles.workspace, cmd.ChatID, &p); err != nil {
		if errors.Is(err, chat.ErrTombstoned) {
			return nil, StatusError(http.StatusConflict, ErrChatNotFound)
		}
		return nil, StatusError(http.StatusInternalServerError, err)
	}

	// 2. Admission: a bare per-chat reservation, synchronous and bridge-free.
	// Held → a bounded wait, then a refusal keyed on the holder's source.
	if err := reservePromptAdmission(ctx, roles, cmd.ChatID); err != nil {
		return nil, err
	}

	// 3. Register the turn in-flight BEFORE the ack goes out, so a shutdown
	// arriving between the ack and the goroutine's first step still waits for
	// it. The turn runs under a context detached from the prompt POST's
	// r.Context() (see LifecycleAccess.TurnContext): the POST returns at the
	// ack, and the turn must not die with it. The goroutine owns the cancel.
	roles.lifecycle.InflightAdd(1)
	turnCtx, cancel := roles.lifecycle.TurnContext(ctx)
	go runPromptTurn(turnCtx, cancel, roles, cmd.ChatID, &p)
	return promptAck{Accepted: true, MessageID: p.MessageID}, nil
}

// reservePromptAdmission takes the chat's admission slot for a prompt,
// translating a refusal into the two 409 variants the client branches on: a
// prompt-class holder with a live bridge answers the PLAIN 409 (the client's
// 409→steer conversion works — the steer lands in KAS's buffer for the
// in-flight or imminent turn), and every other holder answers 409 with the
// additive reason "starting", on which the client never attempts an
// undeliverable steer.
func reservePromptAdmission(ctx context.Context, roles *promptRoles, chatID vibekit.ChatID) error {
	switch roles.turnOutcome.ReserveTurnForPrompt(ctx, chatID, AdmissionWait) {
	case AdmissionAcquired:
		return nil
	case AdmissionBusy:
		return StatusError(http.StatusConflict, errBusy)
	default:
		return StatusErrorReason(http.StatusConflict, reasonStarting, errBusy)
	}
}

// runPromptTurn drives one ADMITTED prompt end to end, owning the reservation
// CmdPrompt took, the turn context's cancel, and the in-flight registration;
// every path out releases all three. Failures past the ack are SSE-only: the
// POST has already answered, so the error frame is the one surface left.
func runPromptTurn(ctx context.Context, cancel context.CancelFunc, roles *promptRoles, chatID vibekit.ChatID, p *vibekit.PromptCommand) {
	defer roles.lifecycle.InflightDone()
	defer cancel()
	sb, err := roles.bridges.OpenBridge(ctx, chatID, p.Model)
	if err != nil {
		roles.turnOutcome.ReleaseTurnReservation(chatID)
		roles.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventError, chatID, vibekit.ErrorPayload{Code: vibekit.ErrCodeBridgeStartFailed, Message: rpcerr.Text(err)}))
		return
	}
	// The reservation already excludes every prompt and shell, so a held bridge
	// slot here is a programming error — surfaced rather than silent, and the
	// reservation released so the chat is not wedged behind a phantom holder.
	if !sb.TryAcquireForPrompt() {
		roles.turnOutcome.ReleaseTurnReservation(chatID)
		slog.Error("prompt: bridge slot held despite an owned admission reservation", "chat_id", chatID)
		roles.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventError, chatID, vibekit.ErrorPayload{Code: vibekit.ErrCodePromptFailed, Message: "The prompt could not start. Send it again."}))
		return
	}
	promptAdmittedTurn(ctx, roles, sb, chatID, p)
}

// promptAdmittedTurn runs the turn with both holds owned: prime, MCP wait,
// StartTurn at bridge-ready, the ACP call, the settle, and the ordered handoff
// into the empty-turn recovery.
//
// The release ORDER on the settled path is the contract: the finalized result
// is captured via the still-held epoch handle FIRST (the registry drops the
// retained result at the last release), then the bridge slot, then the
// reservation — so a waiting prompt is admitted the moment the turn's effects
// are done — then recovery arbitrates on the CAPTURED result and re-acquires
// both holds with a try, and ReleaseTurn goes LAST so every epoch-based
// recovery predicate reads a live record.
func promptAdmittedTurn(ctx context.Context, roles *promptRoles, sb Bridge, chatID vibekit.ChatID, p *vibekit.PromptCommand) {
	// The prompt Call gets its own cancellable context so CmdCancel's grace
	// budget has something to trip when KAS never acks a session/cancel.
	// Without this the Call blocks until the bridge is torn down, and the
	// ReleaseAfterPrompt below never runs.
	ctx, cancelPrompt := context.WithCancel(ctx)
	defer cancelPrompt()
	sb.BeginPromptCall(cancelPrompt)
	defer sb.EndPromptCall()

	// Prime with history if this session has not had it yet. The "if needed"
	// is the callee's: it owns the flag. The priming turn's own open/finalize
	// runs HERE, between the reservation and StartTurn — the reservation is not
	// a Turn, so the prime's lifecycle is untouched by it.
	roles.bridges.PrimeIfNeeded(ctx, chatID)

	if !roles.mcp.WaitForReady(ctx, 30*time.Second) {
		// The registry distinguishes all three causes per server and holds the
		// enabled-name census, and this is the one moment the join is worth making:
		// a chat id on its own reports only that a wait expired, which is the one
		// fact the operator already has.
		pending := roles.mcp.PendingSummary(ctx)
		slog.Warn("MCP readiness timeout, proceeding anyway",
			"chat_id", chatID,
			"silent", pending.Silent,
			"failed", pending.Failed,
			"awaiting_auth", pending.AwaitingAuth)
	}
	// Open the turn at bridge-ready, immediately before dispatch. Everything
	// true of it for its whole life is captured here — the answering model, the
	// credit reading its spend is measured against, when it began — with the
	// bridge live, so the spawn, the prime and the MCP wait are excluded from
	// the turn's accounting. The epoch is this goroutine's completion handle on
	// that turn: it is what lets the recovery below read the turn's own outcome,
	// and releasing it is what lets the finalized record be dropped.
	epoch := roles.turnOutcome.StartTurn(ctx, chatID, vibekit.TurnSourcePrompt)
	if epoch == 0 {
		// Dead ctx: shutdown, or a cancel during the spawn/prime/MCP window. With
		// no epoch nothing would finalize and `thinking` would never clear, so the
		// failure is broadcast, both holds release, and no ACP call is made.
		sb.ReleaseAfterPrompt()
		roles.turnOutcome.ReleaseTurnReservation(chatID)
		roles.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventError, chatID, vibekit.ErrorPayload{
			Code: vibekit.ErrCodePromptFailed, Message: "The turn was cancelled before the agent answered.",
		}))
		return
	}
	slog.Info("prompt", "chat_id", chatID, "len", len(p.Text))
	start := time.Now()
	promptParams := BuildPromptParams(ctx, roles.workspace, sb, p)
	reply, err := callPromptWithRetry(ctx, sb, promptParams, chatID)
	elapsed := time.Since(start)
	if err != nil {
		reportPromptFailure(ctx, roles, chatID, epoch, err, elapsed)
		sb.ReleaseAfterPrompt()
		roles.turnOutcome.ReleaseTurnReservation(chatID)
		roles.turnOutcome.ReleaseTurn(chatID, epoch)
		return
	}
	slog.Info("prompt complete", "chat_id", chatID, "elapsed", elapsed)

	// Settle this turn BEFORE deciding whether it produced nothing: the close is
	// what settles the withheld steer carry and measures the turn, and the recovery
	// reads that measurement. Ordinarily the wire's turn_end has already closed it
	// by the time the folder reaches this position, and the call only waits.
	roles.turnOutcome.SettleTurnOnResponse(ctx, chatID, epoch, reply.seq, reply.resp)
	// Capture the result while the epoch handle is still held — the registry
	// drops it at the last release — then free both admission holds so a
	// waiting prompt is admitted before the recovery's own bookkeeping runs.
	result, aErr := roles.turnOutcome.AwaitTurn(ctx, chatID, epoch)
	sb.ReleaseAfterPrompt()
	roles.turnOutcome.ReleaseTurnReservation(chatID)
	if aErr != nil {
		slog.Warn("empty turn check: no turn outcome", "chat_id", chatID, keyError, aErr)
	} else {
		recoverEmptyTurn(ctx, roles.bridges, roles.chats, roles.bus, roles.turnOutcome, chatID, epoch, result, p, promptParams)
	}
	roles.turnOutcome.ReleaseTurn(chatID, epoch)
}

// reportPromptFailure finalizes a turn whose prompt Call failed and broadcasts
// the failure. The POST answered at the ack, so the error frame is the only
// live surface; post-ack failures are SSE-only by design.
//
// ONE rendering of the cause, on every surface that carries it. Handing the raw
// error to the broadcast instead meant RPCErrorText's fallback -- the machine
// triplet {"errorType":...,"retryErrorType":"THROTTLING",...} -- overwrote the
// prose promptFailureReason exists to produce. The chain is in the log line
// here; what travels to the user is the reason.
//
// Two surfaces take it, with two lifetimes: the SSE frame reaches the toast,
// and the transcript's interrupted divider keeps it for good. The divider is
// why the reason is computed BEFORE the finalize call -- AbandonInFlightTurn
// writes it.
func reportPromptFailure(ctx context.Context, roles *promptRoles, chatID vibekit.ChatID, epoch vibekit.TurnEpoch, err error, elapsed time.Duration) {
	slog.Error("prompt failed", "chat_id", chatID, keyError, err, "elapsed", elapsed)
	reason := promptFailureReason(err)
	// An auth failure is the one prompt failure whose remedy is not "send
	// again", so it does not travel as the generic prompt_failed code.
	// ErrCodeAuthTokenUnavailable already routes to a non-dismissible banner
	// with a Sign in CTA, which is why this is the whole client-side change:
	// no new wire enum, no decoder regeneration.
	code := vibekit.ErrCodePromptFailed
	if classifyPromptFailure(err) == classAuth {
		code = vibekit.ErrCodeAuthTokenUnavailable
		// The banner is only half the remedy. The token vibekit vended was
		// accepted at the vend and rejected at the backend, which is what an
		// account switch looks like — invalidated without being expired — so
		// without this the cache re-vends the same dead credential for up to
		// (expiry - reuseLeeway) and signing in again changes nothing until it
		// ages out. Withdrawing it from reuse makes the next callback re-ask
		// the CLI, which picks up the switched account with no restart.
		if roles.tokens != nil {
			roles.tokens.Invalidate()
		}
	}
	// Finalize the turn before returning. Without this the assistant buffer
	// survives with Started == true, so the NEXT prompt's ensureTurnStarted
	// no-ops, emits no message_created, and extends this dead turn's blocks
	// under this dead turn's message id: one persisted assistant message
	// holding two turns' replies. The partial is persisted rather than
	// dropped -- see AbandonInFlightTurn for why that direction.
	roles.turnOutcome.AbandonInFlightTurn(ctx, chatID, epoch, reason)
	roles.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventError, chatID,
		vibekit.ErrorPayload{Code: code, Message: reason}))
}

// BuildPromptParams constructs the full session/prompt parameter map. Takes
// sessionScoped, not Bridge: building a parameter map reads an id, nothing more.
func BuildPromptParams(ctx context.Context, ws Workspace, sb sessionScoped, p *vibekit.PromptCommand) map[string]any {
	params := SessionParams(sb, map[string]any{
		"prompt": BuildPromptBlocks(ctx, p.Text, p.Attachments, ws.ResolveInside),
	})
	// Forward the client-generated user message id so KAS stores this turn under
	// vibekit's own id. That shared id space is what makes rewind addressable:
	// _kiro/checkpoint/revertMultiple takes a messageId and requires it to name a
	// USER message, and a user turn's id is one KAS only knows because it was
	// sent here (an assistant turn carries KAS's own `<uuid>-say`). Drop this and
	// rewind loses its handle on the transcript — see CmdRewindChat.
	if p.MessageID != "" {
		params["messageId"] = p.MessageID
	}
	return params
}

// promptFailureClass names why a prompt failed, because the four causes want
// four different actions and one boolean could only ever pick one of them.
//
// The single `IsRetryablePromptError` this replaces got each cause wrong in a
// different direction: it retried a DEAD bridge (three attempts against a closed
// done channel, four seconds of wall-clock, same error), it gave a throttle the
// same fixed 2s as a busy session even though KAS had already spent five
// adaptive attempts of its own, and it retried an auth failure that no number of
// attempts can fix.
type promptFailureClass int

const (
	// classFatal is the default: surface it, do not retry.
	classFatal promptFailureClass = iota
	// classPipeDeath is a dead subprocess. Retrying the same bridge is provably
	// useless (readLoop closed done permanently), so this must not loop.
	classPipeDeath
	// classBusy is the session still finishing a prior turn: a real wait.
	classBusy
	// classTransient is a write failure or an internal error that a second
	// attempt can plausibly fix.
	classTransient
	// classThrottled is a backend rate limit. NOT retryable here: KAS's own
	// client already exhausted its adaptive attempts before handing this over.
	classThrottled
	// classRejected is a backend VALIDATION refusal, named in the closed
	// vocabulary of validationErrorNames. Non-retryable by construction, which
	// is the whole point of separating it from classTransient: the payload IS
	// what was refused, so resending the identical payload cannot succeed.
	classRejected
	// classAuth is the backend refusing the TOKEN rather than the request.
	// Terminal by OMISSION from callPromptWithRetry's retry set, like every class
	// but busy and transient; what it is FOR is the answer, not the retry
	// decision. CmdPrompt sends vibekit.ErrCodeAuthTokenUnavailable for it, which
	// is the only code the client routes to a Sign in CTA — so without a class
	// here the one remedy that works cannot be offered, whatever the message says.
	classAuth
)

// validationErrorNames are the backend's own names for a request it refused as
// malformed or oversized. Read off the string table of the `kiro-cli-chat`
// 2.19.0 binary (the sidecar `kiro-cli acp` re-execs), where they sit together
// in one enumeration; they are the model service's names, surfaced through KAS.
//
// Every member shares one property, and it is the property that makes the class:
// the refusal is a statement about the bytes that were sent, so a second attempt
// with the same bytes gets the same answer. Without this table they land on
// -32603 like everything else, classify as classTransient, and the prompt is
// resent twice more — on an oversized image, three uploads of the same refused
// payload before the user is told anything.
//
// Deliberately EXCLUDED, though they sit in the same enumeration: the quota and
// capacity names (`MonthlyRequestCount`, `DailyRequestCount`,
// `InsufficientModelCapacity`), the model names (`InvalidModelId`) and the `Kms*`
// family. None of them is a statement about the payload, so none of them shares
// the remedy this class carries into promptFailureReason, and a user told to
// shrink their prompt because their monthly allowance ran out is worse off than
// one told nothing. They are also normally MAPPED (-32000 with a CLIENT_ERROR
// classification), which already resolves to a non-retried class.
var validationErrorNames = []string{
	"ImageSizeExceeded",
	"ImageDimensionExceeded",
	"ImageCountExceeded",
	"ImageFormatUnsupported",
	"ImageMimeMismatch",
	"PromptTooLong",
	"ContentLengthExceedsThreshold",
	"DisallowedFileType",
	"DocumentSizeExceeded",
	"DocumentMaximumPagesExceeded",
	"DocumentCountExceeded",
}

// authErrorNames are the backend's own names for a request it refused because the
// CREDENTIAL was not usable — the classes it throws plus the name codes its own auth
// predicate keys on, measured off the KAS 2.20.0 bundle. A name survives a reworded
// message; the prose markers below matched none of the backend's eight auth messages.
//
// ModelRegistryAccessDeniedError is deliberately absent: upstream tells that user the
// ACCOUNT lacks model access, which no sign-in fixes, so a Sign in banner would be
// advice that cannot work.
var authErrorNames = []string{
	"TokenInvalidError",
	"TokenExpiredError",
	"AuthRefreshFailedError",
	"ModelRegistryUnauthenticatedError",
	"AccessDeniedError",
	"MISSING_TOKEN",
	"MALFORMED_TOKEN",
	"INVALID_AUTH",
	"INVALID_SSO_AUTH",
	"INVALID_IDC_AUTH",
}

// mappedErrorData is the shape KAS puts in `error.data` on a mapped backend
// error. Every KiroQError lands on -32000 with these fields, so the CLASS is
// what distinguishes them, not the presence of the data.
type mappedErrorData struct {
	ErrorType      string `json:"errorType"`
	RetryErrorType string `json:"retryErrorType"`
	RequestID      string `json:"requestId"`
}

// retryErrorType values KAS assigns. Counted off the 24 getters in the 2.16.1
// bundle: 16 classes return CLIENT_ERROR, 3 TRANSIENT, 3 THROTTLING, 2
// SERVER_ERROR. Only the THROTTLING three are a rate limit, which is why the
// classifier keys on this exact value and not on the mere presence of the data
// block -- doing the latter labelled 21 of 24 error classes a throttle, telling
// a user with a validation error to wait and try again.
const (
	kasRetryThrottling = "THROTTLING"
)

// classifyPromptFailure maps a prompt error onto its class.
func classifyPromptFailure(err error) promptFailureClass {
	if err == nil {
		return classFatal
	}
	// Order matters: a dead bridge arrives WRAPPED in a TransportError whose
	// Retryable is true, so the identity check has to win or the corpse gets
	// retried exactly as before.
	if errors.Is(err, vibekit.ErrBridgeExited) {
		return classPipeDeath
	}
	if errors.Is(err, vibekit.ErrNotIdle) {
		return classBusy
	}
	if te, ok := errors.AsType[*vibekit.TransportError](err); ok {
		if te.Retryable {
			return classTransient
		}
		return classFatal
	}
	if re, ok := errors.AsType[*vibekit.RPCError](err); ok {
		return classifyRPCFailure(re)
	}
	return classFatal
}

// classifyRPCFailure classifies an RPC error by its CODE. Split from its caller
// so each stays inside the complexity ceiling, and because the two answer
// different questions: which kind of failure is this, and what does this code
// mean.
func classifyRPCFailure(re *vibekit.RPCError) promptFailureClass {
	switch re.Code {
	case vibekit.RPCCodeNotIdle:
		return classBusy
	case vibekit.RPCCodeBridgeExited:
		// KAS's mapped-backend-error code, which happens to share a number with
		// vibekit's own bridge-exited constant (see its comment). A bridge exit
		// never arrives here as an RPCError, so this is KAS's.
		//
		// Every mapped error lands here, not just throttles, so the class has to
		// come from retryErrorType. Nothing on this code is retried either way:
		// KAS's own SDK ladder has already run (a real 429 on disk shows
		// `attempts: 5`), and CLIENT_ERROR is definitionally not retryable. The
		// class only decides what the user is told.
		d := mappedFromData(re)
		if d == nil {
			return classFatal
		}
		if d.RetryErrorType == kasRetryThrottling {
			return classThrottled
		}
		// errorType is the machine name, so it is read directly rather than
		// searched for in the haystack: on this code the name is the field.
		if slices.Contains(authErrorNames, d.ErrorType) {
			return classAuth
		}
		return classFatal
	case vibekit.RPCCodeInternal:
		// -32603 is KAS's catch-all: a genuine internal fault, and also every
		// validation failure and every auth failure. Retrying an auth failure is
		// pure latency, so it is excluded by name. So is a validation refusal,
		// for a stronger reason: the request is what was refused, so the retry is
		// not merely wasted latency but a second and third upload of the same
		// rejected payload.
		if isAuthShaped(re) {
			return classAuth
		}
		if isValidationShaped(re) {
			return classRejected
		}
		return classTransient
	}
	return classFatal
}

// mappedFromData decodes a mapped-error payload. Returns nil when the error
// carries none, which is every error that is not one of KAS's mapped backend
// classes.
func mappedFromData(re *vibekit.RPCError) *mappedErrorData {
	raw := re.ErrorData()
	if len(raw) == 0 {
		return nil
	}
	var d mappedErrorData
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil
	}
	if d.RetryErrorType == "" && d.ErrorType == "" {
		return nil
	}
	return &d
}

// isAuthShaped reports whether an internal error is really an authentication
// failure. Matched on the payload rather than the code because KAS collapses
// both onto -32603, and no amount of retrying fixes an expired token.
//
// authErrorNames comes first and carries the weight (see it for why a name beats
// prose); the markers remain for the errors that arrive with no class name at all.
//
// `credentials` is deliberately NOT among them: it matched an AWS SDK message
// that states a refresh WILL be attempted, so grading it terminal-auth suppressed
// the retry the message was asking for, plus an echoed
// access-control-allow-credentials header and a gRPC channel error. The two
// phrases are prefix-anchored rather than a bounded regex because this haystack
// is a JSON object, where bounding a two-token match across it means nothing.
func isAuthShaped(re *vibekit.RPCError) bool {
	hay := re.Message + string(re.ErrorData())
	for _, name := range authErrorNames {
		if strings.Contains(hay, name) {
			return true
		}
	}
	markers := []string{
		"Authentication failed",
		"Authentication token",
		"not logged in",
		"Unauthorized",
		"unauthorized",
		"ExpiredToken",
		"AccessDenied",
	}
	for _, marker := range markers {
		if strings.Contains(hay, marker) {
			return true
		}
	}
	return false
}

// isValidationShaped reports whether an internal error is really the backend
// refusing the request as sent. Matched on the payload for the same reason
// isAuthShaped is: KAS collapses this onto the same -32603 as a genuine fault,
// and the distinguishing token is in `error.data` rather than in the code.
//
// Matching a NAME rather than the surrounding prose is what makes this survive a
// reworded message, and the names are distinctive enough that a false positive
// would need the model service's own token to appear in unrelated text. A false
// positive costs one un-retried turn and some advice about attachment size; a
// false negative is the three-upload retry this exists to stop. That asymmetry is
// why the table stays a name list and not a looser pattern.
func isValidationShaped(re *vibekit.RPCError) bool {
	hay := re.Message + string(re.ErrorData())
	for _, name := range validationErrorNames {
		if strings.Contains(hay, name) {
			return true
		}
	}
	return false
}

// promptFailureReason renders a failure into something worth showing the user.
//
// KAS's own message on a mapped error is `userFacingSessionErrorMessage`, which
// is already written for a user, so this ADDS to it rather than replacing it.
// An earlier revision invented prose for every mapped error and told a user with
// a validation failure that they were being rate limited; the lesson is that the
// only thing worth adding is what KAS's message cannot know.
//
// Two things qualify. A throttle earns the note that retrying immediately will
// not help, because KAS has already spent its attempts and the user cannot see
// that. And a request id earns inclusion because it is the handle for an
// upstream report, and it is in `data` where nothing surfaces it.
func promptFailureReason(err error) string {
	// A cancelled prompt context is not a backend failure and must not read like
	// one. Two things reach here: the user pressed Cancel and KAS never answered
	// the pending call, so cancelGrace killed the context; or the HTTP request
	// went away. `rpcerr.Text` would render both as the literal "context
	// canceled", which was tolerable while this string only reached a tooltip and
	// is not now that it is persisted as the turn's own account of the stop.
	if errors.Is(err, context.Canceled) {
		return "The turn was cancelled before the agent answered."
	}
	re, ok := errors.AsType[*vibekit.RPCError](err)
	if !ok {
		return rpcerr.Text(err)
	}
	d := mappedFromData(re)
	if d == nil {
		// Not one of KAS's mapped backend classes, which is the overwhelming
		// majority: 127 of 137 measured engine errors are a -32603 whose message
		// is the literal "Internal error" and whose cause is in `error.data`.
		// This used to return err.Error(), i.e. that literal, so the real cause
		// was on the wire and the user was told nothing.
		text := rpcerr.Text(err)
		if isValidationShaped(re) {
			// rpcerr.Text already recovered the backend's own account of WHAT was
			// wrong. What it cannot know is that this failure is terminal for
			// these bytes, which is exactly the thing a user cannot infer: every
			// other prompt failure they have seen clears on a second Send.
			text += " The request was refused as sent. Resending it unchanged will fail the same way. Make the prompt or its attachments smaller, then send again."
		}
		return text
	}
	// A MAPPED error is the one case where `error.data` is NOT the text: it is the
	// machine triplet (errorType / retryErrorType / requestId), and the prose is
	// KAS's own userFacingSessionErrorMessage in `message`. So this branch keeps
	// the message and adds to it, while the unmapped branch above goes through
	// RPCErrorText to recover the details KAS puts in data. Reversing that is the
	// regression TestPromptFailureReason_NamesAThrottle catches: it renders the
	// triplet's raw JSON at the user.
	msg := re.Message
	if msg == "" {
		// NOT RPCErrorText here. On a mapped error `data` is the machine triplet,
		// which parses as neither of RPCDetails' two shapes, so it would fall
		// through to that function's raw-JSON fallback and print
		// {"errorType":…,"requestId":…} at the user — the exact rendering the
		// comment above names as the regression. errorType is the one token in the
		// triplet a person can read.
		msg = d.ErrorType
		if msg == "" {
			msg = "the model backend refused the request"
		}
	}
	if d.RetryErrorType == kasRetryThrottling {
		msg += " kiro-cli already retried; waiting a moment before resending is the only thing that helps."
	}
	if d.RequestID != "" {
		msg += " (request " + d.RequestID + ")"
	}
	return msg
}

// String names the class for logs. A number in a log line is a lookup the
// reader should not have to perform.
func (c promptFailureClass) String() string {
	switch c {
	case classPipeDeath:
		return "pipe_death"
	case classBusy:
		return "busy"
	case classTransient:
		return "transient"
	case classThrottled:
		return "throttled"
	case classRejected:
		return "rejected"
	case classAuth:
		return "auth"
	case classFatal:
		return "fatal"
	}
	return "unknown"
}
