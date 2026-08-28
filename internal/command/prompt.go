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

// retry re-invokes fn up to maxAttempts more times, promptRetryDelay apart, for
// as long as shouldRetry keeps saying yes. The delay is FIXED — there is no
// backoff, and the name used to claim one.
//
// A bare `time.After` in the loop rather than one reused timer: since Go 1.23 a
// timer is collected as soon as it is unreachable even if it never fired and
// Stop was never called, so the reuse this used to do (NewTimer, defer Stop,
// Reset per iteration) bought nothing but a redundant re-arm on the first pass.
// At maxAttempts of 2 against a 2s delay the allocation is not measurable.
func retry(ctx context.Context, maxAttempts int, shouldRetry func(error) bool, fn func() (*vibekit.RPCResponse, error)) (*vibekit.RPCResponse, error) {
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
func callPromptWithRetry(ctx context.Context, sb bridgeCaller, params map[string]any, chatID vibekit.ChatID) (*vibekit.RPCResponse, error) {
	return retry(ctx, 2, func(err error) bool {
		class := classifyPromptFailure(err)
		retry := class == classBusy || class == classTransient
		slog.Warn("prompt failure",
			"chat_id", chatID, "class", class.String(), "retry", retry, keyError, err)
		return retry
	}, func() (*vibekit.RPCResponse, error) {
		return sb.Call(ctx, vibekit.MethodPrompt, params)
	})
}

// recoverEmptyTurn handles empty turn recovery: recreate session and retry.
func recoverEmptyTurn(ctx context.Context, bridges BridgeAccess, chats ChatStore, bus Broadcaster, outcome TurnOutcomeAccess, chatID vibekit.ChatID, resp *vibekit.RPCResponse, p *vibekit.PromptCommand, params map[string]any) *vibekit.RPCResponse {
	// A verb KAS answers itself produces no content BY DESIGN, so an empty turn
	// is the correct outcome and recovery is pure damage: it would close the
	// bridge the launched run is parented on, detach the session, badge the turn
	// interrupted, and re-send the verb — a second run for one request. Checked
	// before IsEmptyTurn because the buffer state is genuinely empty here; the
	// prompt TEXT is the only thing that distinguishes expected from broken.
	if kasClaimsPromptText(p.Text) {
		return resp
	}
	if !outcome.IsEmptyTurn(resp, chatID) {
		return resp
	}
	slog.Warn("empty turn detected, recreating session", "chat_id", chatID)
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
	sb2, err2 := bridges.OpenBridge(ctx, chatID, p.Model)
	if err2 != nil {
		slog.Error("empty turn: respawn failed",
			"chat_id", chatID, keyError, err2)
		bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventError, chatID, vibekit.ErrorPayload{
			Code:    vibekit.ErrCodeRecoveryFailed,
			Message: "Session refresh failed: " + rpcerr.Text(err2),
		}))
		return resp
	}
	// Take the new bridge's prompt slot the same way CmdPrompt takes it, rather
	// than asserting it with SetPrompting. OpenBridge leaves the bridge
	// registered and IDLE, so between that return and this line a concurrent
	// prompt can win the slot -- and an unconditional SetPrompting would then let
	// the deferred release below flip that turn's slot to idle while it is still
	// streaming. Losing the race means abandoning the retry, which is the right
	// direction: the user's own new turn is live on this chat, and the empty turn
	// it would have replaced is already recorded as interrupted.
	if !sb2.TryAcquireForPrompt() {
		slog.Warn("empty turn: another turn started during recovery, abandoning retry",
			"chat_id", chatID)
		return resp
	}
	defer sb2.ReleaseAfterPrompt()

	// The retry needs its own cancellable context registered as the in-flight
	// prompt, for the same reason CmdPrompt's does: session/cancel is a
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
	retryResp, retryErr := callPromptWithRetry(ctx, sb2, params, chatID)
	if retryErr != nil {
		slog.Error("retry prompt failed", "chat_id", chatID, keyError, retryErr)
		bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventError, chatID, vibekit.ErrorPayload{
			Code:    vibekit.ErrCodeRecoveryFailed,
			Message: "Retry prompt failed: " + rpcerr.Text(retryErr),
		}))
		return resp
	}
	return retryResp
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

// CmdPrompt handles the prompt command.
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
	if err := appendUserMessage(ctx, roles.chats, roles.bus, roles.workspace, cmd.ChatID, &p); err != nil {
		if errors.Is(err, chat.ErrTombstoned) {
			return nil, StatusError(http.StatusConflict, ErrChatNotFound)
		}
		return nil, StatusError(http.StatusInternalServerError, err)
	}

	// 2. Ensure the bridge exists and serialize per-chat prompts. The turn
	// runs under a context detached from the prompt POST's r.Context()
	// (see LifecycleAccess.TurnContext): a mid-turn client disconnect must not
	// cancel the in-flight bridge Call, or the turn fails before it can
	// finalize and persist the assistant buffer.
	ctx, cancel := roles.lifecycle.TurnContext(ctx)
	defer cancel()
	sb, err := roles.bridges.OpenBridge(ctx, cmd.ChatID, p.Model)
	if err != nil {
		roles.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventError, cmd.ChatID, vibekit.ErrorPayload{Code: vibekit.ErrCodeBridgeStartFailed, Message: rpcerr.Text(err)}))
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	if !sb.TryAcquireForPrompt() {
		return nil, StatusError(http.StatusConflict, errBusy)
	}
	defer sb.ReleaseAfterPrompt()

	// The prompt Call gets its own cancellable context so CmdCancel's grace
	// budget has something to trip when KAS never acks a session/cancel.
	// Without this the Call blocks until the bridge is torn down, and the
	// deferred ReleaseAfterPrompt above never runs.
	ctx, cancelPrompt := context.WithCancel(ctx)
	defer cancelPrompt()
	sb.BeginPromptCall(cancelPrompt)
	defer sb.EndPromptCall()

	roles.lifecycle.InflightAdd(1)
	defer roles.lifecycle.InflightDone()

	// 3. Prime with history if this session has not had it yet. The "if needed"
	// is the callee's: it owns the flag.
	roles.bridges.PrimeIfNeeded(ctx, cmd.ChatID)

	// 4. Send the prompt to kiro-cli.
	if !roles.mcp.WaitForReady(ctx, 30*time.Second) {
		// The registry distinguishes all three causes per server and holds the
		// enabled-name census, and this is the one moment the join is worth making:
		// a chat id on its own reports only that a wait expired, which is the one
		// fact the operator already has.
		pending := roles.mcp.PendingSummary(ctx)
		slog.Warn("MCP readiness timeout, proceeding anyway",
			"chat_id", cmd.ChatID,
			"silent", pending.Silent,
			"failed", pending.Failed,
			"awaiting_auth", pending.AwaitingAuth)
	}
	var creditsBeforeTurn float64
	if ch, ok := roles.chats.Get(ctx, cmd.ChatID); ok {
		creditsBeforeTurn = ch.Usage.Credits
		// Latch the answering model HERE, at dispatch, not on the turn's first
		// frame. The bridge is up and its model persisted by this point, and
		// switch_model's fast path can land any time from now on — including
		// before the old model has emitted anything, which is precisely when the
		// first-frame read attributed one model's answer to another.
		roles.turnOutcome.LatchTurnModel(cmd.ChatID, ch.Model)
	}
	slog.Info("prompt", "chat_id", cmd.ChatID, "len", len(p.Text))
	start := time.Now()
	promptParams := BuildPromptParams(ctx, roles.workspace, sb, &p)
	resp, err := callPromptWithRetry(ctx, sb, promptParams, cmd.ChatID)
	elapsed := time.Since(start)
	if err != nil {
		return nil, reportPromptFailure(ctx, roles, cmd.ChatID, err, elapsed)
	}
	slog.Info("prompt complete", "chat_id", cmd.ChatID, "elapsed", elapsed)

	// Empty turn recovery.
	resp = recoverEmptyTurn(ctx, roles.bridges, roles.chats, roles.bus, roles.turnOutcome, cmd.ChatID, resp, &p, promptParams)

	// Compute credit delta for the turn summary.
	var creditsDelta float64
	if ch, ok := roles.chats.Get(ctx, cmd.ChatID); ok {
		creditsDelta = ch.Usage.Credits - creditsBeforeTurn
	}
	roles.turnOutcome.EmitTurnEndedWithStats(ctx, cmd.ChatID, resp, TurnStats{
		CreditsDelta: creditsDelta,
		ElapsedMs:    float64(elapsed.Milliseconds()),
	})
	return responseOK, nil
}

// reportPromptFailure finalizes a turn whose prompt Call failed and returns the
// error the POST answers with.
//
// ONE rendering of the cause, on every surface that carries it. Handing the raw
// error to RespondErr instead meant RPCErrorText's fallback -- the machine triplet
// {"errorType":...,"retryErrorType":"THROTTLING",...} -- overwrote the prose
// promptFailureReason exists to produce. The chain is in the log line here; what
// travels to the user is the reason.
//
// Three surfaces take it, and they have three different lifetimes: the POST's
// error body and the SSE frame both reach the toast (whichever arrives first wins,
// the other dedupes), and the transcript's interrupted divider keeps it for good.
// The divider is why the reason is computed BEFORE the finalize call --
// AbandonInFlightTurn writes it.
func reportPromptFailure(ctx context.Context, roles *promptRoles, chatID vibekit.ChatID, err error, elapsed time.Duration) error {
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
	roles.turnOutcome.AbandonInFlightTurn(ctx, chatID, reason)
	roles.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventError, chatID,
		vibekit.ErrorPayload{Code: code, Message: reason}))
	return StatusError(http.StatusInternalServerError, errors.New(reason))
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

// authErrorNames are the backend's own names for a request it refused because
// the CREDENTIAL was not usable. Measured off the KAS 2.20.0 bundle: the first
// four are the error classes it throws, the five upper-case entries are the name
// codes its own auth predicate keys on.
//
// A NAME rather than the surrounding prose, for the same reason
// validationErrorNames is a name list: it comes from a closed set the backend
// stamps as `error.data.errorType`, so it survives a reworded message. Measured
// against the bundle, the prose markers below matched NONE of the backend's eight
// auth messages — they were written against AWS SDK exception-name fragments
// ("ExpiredToken" against a class called TokenExpiredError, "unauthorized"
// against "not authorized").
//
// Deliberately EXCLUDED, though it sits in the same family:
// ModelRegistryAccessDeniedError. Its message is that the account does not have
// access, which is an entitlement fact rather than a sign-in problem — signing in
// again does not fix it, so a Sign in banner would be advice that cannot work.
// This narrows rather than widens: it is the one auth-shaped class the old
// AccessDenied marker could reach.
var authErrorNames = []string{
	"TokenInvalidError",
	"TokenExpiredError",
	"AuthRefreshFailedError",
	"ModelRegistryUnauthenticatedError",
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
