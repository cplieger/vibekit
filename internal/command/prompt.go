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
	"github.com/cplieger/vibekit/internal/durable"
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

// promptRetryDelay is the one wait this package retries at.
const promptRetryDelay = 2 * time.Second

// promptReply is a session/prompt response and the read loop position at
// which it arrived. The position travels with the response so the turn's
// local settle can order itself against notifications still queued behind
// it — reading the response alone would decide the wire never closed the
// turn while turn_end is still a few frames back.
type promptReply struct {
	resp *vibekit.RPCResponse
	seq  uint64
}

// retry re-invokes fn up to maxAttempts more times, promptRetryDelay
// apart, for as long as shouldRetry keeps saying yes. The delay is fixed,
// no backoff.
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

// callPromptWithRetry sends the prompt to kiro-cli, retrying only the
// classes a second attempt can actually fix.
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
// result is the FINALIZED turn's captured outcome, never the live buffer,
// which can still be withholding the turn's only text when this runs.
// Both admission holds are already released, so the retry re-reserves
// with a try and a user prompt that won the slot abandons it.
func recoverEmptyTurn(ctx context.Context, bridges BridgeAccess, chats ChatStore, bus Broadcaster, outcome TurnOutcomeAccess, chatID vibekit.ChatID, epoch vibekit.TurnEpoch, result vibekit.TurnResult, p *vibekit.PromptCommand, params map[string]any) {
	// A verb KAS answers itself produces no content by design, so an
	// empty turn is the correct outcome and recovery is pure damage.
	if kasClaimsPromptText(p.Text) {
		return
	}
	// WireEnded: a locally-closed turn's outcome is only the prompt
	// response's, nothing richer, so re-prompting on it is a guess.
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
	retryEmptyTurnPrompt(ctx, bridges, chats, bus, outcome, chatID, p, params)
}

// refreshRetrySession abandons the session that answered nothing: close
// its bridge, detach the chat from it, and record why on the transcript.
func refreshRetrySession(ctx context.Context, bridges BridgeAccess, chats ChatStore, chatID vibekit.ChatID) {
	bridges.CloseBridge(chatID)
	if err := chats.Mutate(ctx, chatID, func(c *vibekit.Chat, ex bool) bool {
		if !ex {
			return false
		}
		// Detach, don't forget: the abandoned session still holds this
		// chat's earlier transcript on disk and must stay in the chain
		// or the reaper sweeps it as an orphan.
		c.RecordSession("")
		return true
	}); err != nil {
		slog.Error("empty turn: clear session ID", "chat_id", chatID, keyError, err)
	}
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
func retryEmptyTurnPrompt(ctx context.Context, bridges BridgeAccess, chats ChatStore, bus Broadcaster, outcome TurnOutcomeAccess, chatID vibekit.ChatID, p *vibekit.PromptCommand, params map[string]any) {
	sb2, err2 := bridges.OpenBridge(ctx, chatID, p.Model)
	if err2 != nil {
		slog.Error("empty turn: respawn failed",
			"chat_id", chatID, keyError, err2)
		reason := "Session refresh failed: " + rpcerr.Text(err2)
		// Correct the transcript before reporting: refreshRetrySession already wrote
		// "Session refreshed, retrying" onto this turn, so without this the record
		// claims a retry that will never happen. Appended rather than replacing it,
		// so the reader sees two consecutive dividers. The empty turn was finalized
		// before recovery began and the retry's epoch never opens, so no turn carries
		// this failure and the divider is its only durable surface.
		evt := vibekit.Message{
			ID: ids.NewMessageID(), Role: vibekit.RoleEvent, Ts: time.Now().UnixMilli(),
			EventKind: vibekit.EventInterrupted, Content: reason,
		}
		if err := chats.AppendMessage(durable.Context(ctx), chatID, &evt); err != nil {
			slog.Error("empty turn: append respawn-failure event", "chat_id", chatID, keyError, err)
		}
		bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventError, chatID, vibekit.ErrorPayload{
			Code:    vibekit.ErrCodeRecoveryFailed,
			Message: reason,
		}))
		return
	}
	// Take the new bridge's prompt slot the same way the prompt goroutine
	// does, rather than asserting it: OpenBridge leaves the bridge
	// registered and idle, so a concurrent taker can win the slot between
	// that return and this line. Losing the race abandons the retry — the
	// right direction, since the empty turn it would replace is already
	// recorded as interrupted.
	if !sb2.TryAcquireForPrompt() {
		slog.Warn("empty turn: another turn started during recovery, abandoning retry",
			"chat_id", chatID)
		return
	}
	defer sb2.ReleaseAfterPrompt()

	// The retry needs its own cancellable context registered as the
	// in-flight prompt: session/cancel is a notification nothing acks, so
	// the grace budget is what unblocks a turn KAS never answers.
	ctx, cancelRetry := context.WithCancel(ctx)
	defer cancelRetry()
	sb2.BeginPromptCall(cancelRetry)
	defer sb2.EndPromptCall()

	params[vibekit.KeySessionID] = sb2.SessionID()
	// The retry is a turn of its own — its own epoch, buffer and bracket
	// — closed on every path out of here, since the turn it replaces is
	// already closed.
	retryEpoch := outcome.StartTurn(ctx, chatID, vibekit.TurnSourceEmptyRetry)
	if retryEpoch == 0 {
		// Dead ctx: shutdown, or the turn context died. No ACP call made.
		slog.Warn("empty turn: the retry turn could not start", "chat_id", chatID)
		return
	}
	defer outcome.ReleaseTurn(chatID, retryEpoch)
	reply, retryErr := callPromptWithRetry(ctx, sb2, params, chatID)
	if retryErr != nil {
		slog.Error("retry prompt failed", "chat_id", chatID, keyError, retryErr)
		reason := promptFailureReason(retryErr)
		outcome.AbandonInFlightTurn(ctx, chatID, retryEpoch, reason)
		// Turn-scoped: the retry ran as a turn of its own and the abandon above
		// stamped this reason on it, so that card carries the cause.
		bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventError, chatID, vibekit.ErrorPayload{
			Code:       vibekit.ErrCodeRecoveryFailed,
			Message:    "Retry prompt failed: " + reason,
			TurnScoped: true,
		}))
		return
	}
	outcome.SettleTurnOnResponse(ctx, chatID, retryEpoch, reply.seq, reply.resp)
}

// supervisedDefaultSetting reads the settings-wide Supervised default
// applied to newly auto-created chats. Fails closed to false.
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
		// Idempotent by message id: a retried prompt whose first attempt
		// already persisted the user message skips the append and the
		// broadcast so no duplicate user bubble renders.
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
			// The attachments belong on the record too: for an image or
			// document the path never reaches Content, so a turn read
			// back later needs this to say what was attached.
			Attachments: p.Attachments,
		}
		c.Messages = append(c.Messages, userMsg)
		// The text just left the composer, so the draft holding it is
		// spent. Cleared here too: if the client's own set_draft POST is
		// lost, a reload would put the sent message back in the box.
		hadComposer := c.Draft != "" || len(c.Attachments) > 0
		c.Draft = ""
		c.Attachments = nil
		if c.Name == vibekit.DefaultChatName && len(c.Messages) == 1 {
			name := TruncateRunes(p.Text, 80)
			if name != p.Text {
				name += ellipsis
			}
			c.Name = name
		}
		bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventMessageAppended, chatID, &userMsg))
		// Say the composer was cleared, since CmdSetDraft is not the only
		// writer of this field: without this, every other client — and
		// this one after a reload — keeps serving the already-sent text.
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

// AdmissionWait is the design's ADMISSION_WAIT_MS: the longest a prompt
// waits for a contended admission slot before answering 409. Must stay
// below the client's API timeout — TestAdmissionWait_StaysUnderTheClientAPITimeout
// pins the margin. A var only so tests can shrink a deliberately contended
// wait; production never writes it.
var AdmissionWait = 20 * time.Second

// reasonStarting is the 409 refusal class whose holder cannot receive a
// steer: a cold spawn, a shell, or a prime. The client renders the busy
// face and retries instead of converting the 409 to a steer.
const reasonStarting = "starting"

// promptAck is the prompt's early acknowledgement: admission is decided
// synchronously and the turn runs on its own goroutine, so the POST
// answers as soon as the user message is persisted and the admission slot
// is held.
type promptAck struct {
	MessageID string `json:"message_id"`
	Accepted  bool   `json:"accepted"`
}

// CmdPrompt handles the prompt command: validate → persist → admit → ack.
// The turn itself runs on its own goroutine (runPromptTurn), so the POST
// answers in the time of a disk append rather than a turn.
func CmdPrompt(ctx context.Context, roles *promptRoles, cmd *vibekit.ClientCommand) (any, error) {
	if cmd.ChatID == "" {
		return nil, StatusError(http.StatusBadRequest, ErrMissingChatID)
	}
	p, code, vErr := validatePromptPayload(cmd)
	if vErr != nil {
		return nil, StatusError(code, vErr)
	}

	if strings.HasPrefix(p.Text, "!") {
		return HandleShellInterception(ctx, roles, cmd, &p)
	}

	// A refused write answers here, before anything else runs: every
	// later step depends on this record, and persist precedes admission
	// deliberately — the idempotent-by-message-id append dedupes a
	// client's re-send of the same text.
	if err := appendUserMessage(ctx, roles.chats, roles.bus, roles.workspace, cmd.ChatID, &p); err != nil {
		if errors.Is(err, chat.ErrTombstoned) {
			return nil, StatusError(http.StatusConflict, ErrChatNotFound)
		}
		return nil, StatusError(http.StatusInternalServerError, err)
	}

	if err := reservePromptAdmission(ctx, roles, cmd.ChatID); err != nil {
		return nil, err
	}

	// Register the turn in-flight before the ack goes out, so a shutdown
	// arriving between the ack and the goroutine's first step still waits
	// for it. The turn runs under a context detached from the POST's own,
	// which returns at the ack; the goroutine owns the cancel.
	roles.lifecycle.InflightAdd(1)
	turnCtx, cancel := roles.lifecycle.TurnContext(ctx)
	go runPromptTurn(turnCtx, cancel, roles, cmd.ChatID, &p)
	return promptAck{Accepted: true, MessageID: p.MessageID}, nil
}

// reservePromptAdmission takes the chat's admission slot for a prompt: a
// prompt-class holder with a live bridge answers the plain 409 (the
// client's 409→steer conversion works), and every other holder answers
// 409 with the additive reason "starting", on which the client never
// attempts an undeliverable steer.
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

// runPromptTurn drives one admitted prompt end to end, owning the
// reservation CmdPrompt took, the turn context's cancel, and the in-flight
// registration; every path out releases all three. Failures past the ack
// are SSE-only: the POST has already answered.
func runPromptTurn(ctx context.Context, cancel context.CancelFunc, roles *promptRoles, chatID vibekit.ChatID, p *vibekit.PromptCommand) {
	defer roles.lifecycle.InflightDone()
	defer cancel()
	sb, err := roles.bridges.OpenBridge(ctx, chatID, p.Model)
	if err != nil {
		roles.turnOutcome.ReleaseTurnReservation(chatID)
		roles.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventError, chatID, vibekit.ErrorPayload{Code: vibekit.ErrCodeBridgeStartFailed, Message: rpcerr.Text(err)}))
		return
	}
	// The reservation already excludes every prompt and shell, so a held
	// bridge slot here is a programming error.
	if !sb.TryAcquireForPrompt() {
		roles.turnOutcome.ReleaseTurnReservation(chatID)
		slog.Error("prompt: bridge slot held despite an owned admission reservation", "chat_id", chatID)
		roles.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventError, chatID, vibekit.ErrorPayload{Code: vibekit.ErrCodePromptFailed, Message: "The prompt could not start. Send it again."}))
		return
	}
	promptAdmittedTurn(ctx, roles, sb, chatID, p)
}

// promptAdmittedTurn runs the turn with both holds owned: prime, MCP wait,
// StartTurn at bridge-ready, the ACP call, the settle, and the ordered
// handoff into the empty-turn recovery.
//
// The release ORDER is the contract: capture the finalized result through
// the still-held epoch handle, then the bridge slot, then the reservation,
// and ReleaseTurn last so every epoch-based predicate reads a live record.
func promptAdmittedTurn(ctx context.Context, roles *promptRoles, sb Bridge, chatID vibekit.ChatID, p *vibekit.PromptCommand) {
	// The prompt Call gets its own cancellable context so CmdCancel's
	// grace budget has something to trip when KAS never acks a
	// session/cancel.
	ctx, cancelPrompt := context.WithCancel(ctx)
	defer cancelPrompt()
	sb.BeginPromptCall(cancelPrompt)
	defer sb.EndPromptCall()

	// Prime with history if this session has not had it yet — the
	// callee's own flag.
	roles.bridges.PrimeIfNeeded(ctx, chatID)

	if !roles.mcp.WaitForReady(ctx, 30*time.Second) {
		pending := roles.mcp.PendingSummary(ctx)
		slog.Warn("MCP readiness timeout, proceeding anyway",
			"chat_id", chatID,
			"silent", pending.Silent,
			"failed", pending.Failed,
			"awaiting_auth", pending.AwaitingAuth)
	}
	// Open the turn at bridge-ready, immediately before dispatch, so
	// everything true of it for its whole life is captured with the
	// bridge live — the spawn, the prime and the MCP wait are excluded.
	epoch := roles.turnOutcome.StartTurn(ctx, chatID, vibekit.TurnSourcePrompt)
	if epoch == 0 {
		// Dead ctx: shutdown, or a cancel during the spawn/prime/MCP
		// window. With no epoch nothing would finalize, so no ACP call.
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

	// Settle this turn before deciding whether it produced nothing: the
	// close is what settles the withheld steer carry and measures the
	// turn, and recovery reads that measurement.
	roles.turnOutcome.SettleTurnOnResponse(ctx, chatID, epoch, reply.seq, reply.resp)
	// Capture the result while the epoch handle is still held, then free
	// both admission holds so a waiting prompt is admitted before
	// recovery's own bookkeeping runs.
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

// reportPromptFailure finalizes a turn whose prompt Call failed and
// broadcasts the failure. The POST answered at the ack, so the error frame
// is the only live surface.
//
// One rendering of the cause on every surface that carries it: handing the
// raw error to the broadcast would let RPCErrorText's machine-triplet
// fallback overwrite the prose promptFailureReason produces.
func reportPromptFailure(ctx context.Context, roles *promptRoles, chatID vibekit.ChatID, epoch vibekit.TurnEpoch, err error, elapsed time.Duration) {
	slog.Error("prompt failed", "chat_id", chatID, keyError, err, "elapsed", elapsed)
	reason := promptFailureReason(err)
	// An auth failure is the one prompt failure whose remedy is not "send
	// again", so it routes through a different code.
	code := vibekit.ErrCodePromptFailed
	if classifyPromptFailure(err) == classAuth {
		code = vibekit.ErrCodeAuthTokenUnavailable
		// The token vibekit vended was accepted at the vend and rejected
		// at the backend, which is what an account switch looks like.
		// Withdrawing it from reuse makes the next callback re-ask the
		// CLI, which picks up the switched account with no restart.
		if roles.tokens != nil {
			roles.tokens.Invalidate()
		}
	}
	// Finalize the turn: without this the assistant buffer survives with
	// Started == true, so the next prompt's ensureTurnStarted no-ops and
	// extends this dead turn's blocks under this dead turn's message id.
	roles.turnOutcome.AbandonInFlightTurn(ctx, chatID, epoch, reason)
	// Turn-scoped: the abandon above stamps this same reason on the turn's
	// carrier, so the card says it durably and a toast for the chat on screen
	// would be a second copy of it.
	roles.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventError, chatID,
		vibekit.ErrorPayload{Code: code, Message: reason, TurnScoped: true}))
}

// BuildPromptParams constructs the full session/prompt parameter map.
// Takes sessionScoped, not Bridge: building a parameter map reads an id,
// nothing more.
func BuildPromptParams(ctx context.Context, ws Workspace, sb sessionScoped, p *vibekit.PromptCommand) map[string]any {
	params := SessionParams(sb, map[string]any{
		"prompt": BuildPromptBlocks(ctx, p.Text, p.Attachments, ws.ResolveInside),
	})
	// Forward the client-generated user message id so KAS stores this
	// turn under vibekit's own id — what makes rewind addressable:
	// revertMultiple requires a messageId naming a user message, one KAS
	// only knows because it was sent here.
	if p.MessageID != "" {
		params["messageId"] = p.MessageID
	}
	return params
}

// promptFailureClass names why a prompt failed: the four causes want four
// different actions, which one boolean could never express.
type promptFailureClass int

const (
	// classFatal is the default: surface it, do not retry.
	classFatal promptFailureClass = iota
	// classPipeDeath is a dead subprocess. Retrying the same bridge is
	// provably useless (readLoop closed done permanently).
	classPipeDeath
	// classBusy is the session still finishing a prior turn: a real wait.
	classBusy
	// classTransient is a write failure or internal error a second
	// attempt can plausibly fix.
	classTransient
	// classThrottled is a backend rate limit. Not retryable here: KAS's
	// own client already exhausted its adaptive attempts.
	classThrottled
	// classRejected is a backend validation refusal (validationErrorNames).
	// Non-retryable by construction: the payload is what was refused.
	classRejected
	// classAuth is the backend refusing the token rather than the
	// request. CmdPrompt sends vibekit.ErrCodeAuthTokenUnavailable for
	// it, the only code the client routes to a Sign in CTA.
	classAuth
)

// validationErrorNames are the backend's own names for a request refused
// as malformed or oversized, read off the kiro-cli-chat 2.19.0 binary's
// string table. Every member is a statement about the BYTES sent, so a
// retry with the same bytes gets the same answer; without this table they
// land on -32603 and are retried twice more. Quota, capacity, model and
// Kms* names are excluded: none describes the payload, so classifying one
// here would tell a user to shrink a prompt when their allowance ran out.
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

// authErrorNames are the backend's own names for a request refused because
// the credential was not usable, measured off the KAS 2.20.0 bundle.
//
// ModelRegistryAccessDeniedError is deliberately absent: upstream tells
// that user the account lacks model access, which no sign-in fixes.
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

// mappedErrorData is the shape KAS puts in `error.data` on a mapped
// backend error. Every KiroQError lands on -32000 with these fields, so
// the class is what distinguishes them, not the presence of the data.
type mappedErrorData struct {
	ErrorType      string `json:"errorType"`
	RetryErrorType string `json:"retryErrorType"`
	RequestID      string `json:"requestId"`
}

// retryErrorType values KAS assigns. Only THROTTLING is a rate limit —
// the classifier keys on this exact value rather than the mere presence
// of the data block, since most mapped classes are CLIENT_ERROR.
const (
	kasRetryThrottling = "THROTTLING"
)

// classifyPromptFailure maps a prompt error onto its class.
func classifyPromptFailure(err error) promptFailureClass {
	if err == nil {
		return classFatal
	}
	// A dead bridge arrives wrapped in a TransportError whose Retryable
	// is true, so the identity check has to win first.
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

// classifyRPCFailure classifies an RPC error by its code.
func classifyRPCFailure(re *vibekit.RPCError) promptFailureClass {
	switch re.Code {
	case vibekit.RPCCodeNotIdle:
		return classBusy
	case vibekit.RPCCodeBridgeExited:
		// KAS's mapped-backend-error code, which happens to share a
		// number with vibekit's own bridge-exited constant. A bridge
		// exit never arrives here as an RPCError, so this is KAS's.
		d := mappedFromData(re)
		if d == nil {
			return classFatal
		}
		if d.RetryErrorType == kasRetryThrottling {
			return classThrottled
		}
		if slices.Contains(authErrorNames, d.ErrorType) {
			return classAuth
		}
		return classFatal
	case vibekit.RPCCodeInternal:
		// -32603 is KAS's catch-all: a genuine internal fault, plus
		// every validation and auth failure. Both are excluded from
		// retry — auth is pure latency, validation is a second upload
		// of the same rejected payload.
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

// mappedFromData decodes a mapped-error payload. Returns nil for an error
// that is not one of KAS's mapped backend classes.
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

// isAuthShaped reports whether an internal error is really an
// authentication failure. Matched on the payload since KAS collapses both
// onto -32603.
//
// `credentials` is deliberately not among the markers: it matched an AWS
// SDK message stating a refresh will be attempted, so grading it
// terminal-auth suppressed the retry the message was asking for.
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

// isValidationShaped reports whether an internal error is really the
// backend refusing the request as sent. Matching a name rather than
// surrounding prose is what survives a reworded message.
func isValidationShaped(re *vibekit.RPCError) bool {
	hay := re.Message + string(re.ErrorData())
	for _, name := range validationErrorNames {
		if strings.Contains(hay, name) {
			return true
		}
	}
	return false
}

// promptFailureReason renders a failure into something worth showing the
// user. KAS's own message on a mapped error is already user-facing, so
// this adds to it rather than replacing it.
func promptFailureReason(err error) string {
	// A cancelled prompt context is not a backend failure and must not
	// read like one — either the user pressed Cancel and cancelGrace
	// killed the context, or the HTTP request went away.
	if errors.Is(err, context.Canceled) {
		return "The turn was cancelled before the agent answered."
	}
	re, ok := errors.AsType[*vibekit.RPCError](err)
	if !ok {
		return rpcerr.Text(err)
	}
	d := mappedFromData(re)
	if d == nil {
		// Not one of KAS's mapped backend classes — 127 of 137 measured
		// engine errors are a -32603 whose message is the literal
		// "Internal error" and whose cause is in error.data.
		text := rpcerr.Text(err)
		if isValidationShaped(re) {
			text += " The request was refused as sent. Resending it unchanged will fail the same way. Make the prompt or its attachments smaller, then send again."
		}
		return text
	}
	// A mapped error's `data` is the machine triplet, not the text; the
	// prose is KAS's own userFacingSessionErrorMessage in `message`.
	msg := re.Message
	if msg == "" {
		// Not RPCErrorText here: on a mapped error `data` parses as
		// neither of RPCDetails' two shapes and would fall through to
		// its raw-JSON fallback.
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
