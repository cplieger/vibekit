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

// retryWithBackoff retries fn up to maxAttempts times with a fixed delay.
func retryWithBackoff(ctx context.Context, maxAttempts int, delay time.Duration, shouldRetry func(error) bool, fn func() (*vibekit.RPCResponse, error)) (*vibekit.RPCResponse, error) {
	result, err := fn()
	if err == nil || !shouldRetry(err) {
		return result, err
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		timer.Reset(delay)
		select {
		case <-timer.C:
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
	return retryWithBackoff(ctx, 2, 2*time.Second, func(err error) bool {
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
func recoverEmptyTurn(bridges BridgeAccess, chats ChatAccess, outcome TurnOutcomeAccess, ctx context.Context, chatID vibekit.ChatID, resp *vibekit.RPCResponse, p *vibekit.PromptCommand, params map[string]any) *vibekit.RPCResponse { //nolint:revive // context-as-argument: dispatcher handler signature
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
	if err := chats.ChatStore().Mutate(ctx, chatID, func(c *vibekit.Chat, ex bool) bool {
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
	if err := chats.ChatStore().AppendMessage(ctx, chatID, &evt); err != nil {
		slog.Error("empty turn: append event", "chat_id", chatID, keyError, err)
	}
	sb2, err2 := bridges.GetOrCreateBridge(ctx, chatID, p.Model)
	if err2 != nil {
		slog.Error("empty turn: respawn failed",
			"chat_id", chatID, keyError, err2)
		chats.Broadcast(ctx, vibekit.NewEvent(vibekit.EventError, chatID, vibekit.ErrorPayload{
			Code:    vibekit.ErrCodeRecoveryFailed,
			Message: "Session refresh failed: " + rpcerr.Text(err2),
		}))
		return resp
	}
	// Take the new bridge's prompt slot the same way CmdPrompt takes it, rather
	// than asserting it with SetPrompting. GetOrCreateBridge leaves the bridge
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
		chats.Broadcast(ctx, vibekit.NewEvent(vibekit.EventError, chatID, vibekit.ErrorPayload{
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
func appendUserMessage(chats ChatAccess, ws Workspace, ctx context.Context, chatID vibekit.ChatID, p *vibekit.PromptCommand) error { //nolint:revive // context-as-argument: dispatcher handler signature
	supervisedDefault := supervisedDefaultSetting(ctx, ws.ConfigDir)
	err := chats.ChatStore().Mutate(ctx, chatID, func(c *vibekit.Chat, exists bool) bool {
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
		c.Draft = ""
		if c.Name == vibekit.DefaultChatName && len(c.Messages) == 1 {
			name := TruncateRunes(p.Text, 80)
			if name != p.Text {
				name += ellipsis
			}
			c.Name = name
		}
		chats.Broadcast(ctx, vibekit.NewEvent(vibekit.EventMessageAppended, chatID, &userMsg))
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
func CmdPrompt(d *Dispatcher, roles *promptRoles, ctx context.Context, w http.ResponseWriter, cmd *vibekit.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	if cmd.ChatID == "" {
		d.RespondErr(w, http.StatusBadRequest, ErrMissingChatID)
		return
	}
	p, code, vErr := validatePromptPayload(cmd)
	if vErr != nil {
		d.RespondErr(w, code, vErr)
		return
	}

	// Shell command interception.
	if strings.HasPrefix(p.Text, "!") {
		HandleShellInterception(d, roles, ctx, w, cmd, &p)
		return
	}

	// 1. Ensure the chat exists and append the user message, naming the chat
	// from its first prompt.
	if err := appendUserMessage(roles.chats, roles.workspace, ctx, cmd.ChatID, &p); err != nil {
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}

	// 2. Ensure the bridge exists and serialize per-chat prompts. The turn
	// runs under a context detached from the prompt POST's r.Context()
	// (see LifecycleAccess.TurnContext): a mid-turn client disconnect must not
	// cancel the in-flight bridge Call, or the turn fails before it can
	// finalize and persist the assistant buffer.
	ctx, cancel := roles.lifecycle.TurnContext(ctx)
	defer cancel()
	sb, err := roles.bridges.GetOrCreateBridge(ctx, cmd.ChatID, p.Model)
	if err != nil {
		roles.chats.Broadcast(ctx, vibekit.NewEvent(vibekit.EventError, cmd.ChatID, vibekit.ErrorPayload{Code: vibekit.ErrCodeBridgeStartFailed, Message: rpcerr.Text(err)}))
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}
	if !sb.TryAcquireForPrompt() {
		d.RespondErr(w, http.StatusConflict, errBusy)
		return
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

	// 3. Prime with history if the bridge needs it.
	if !sb.IsPrimed() {
		sb.SetPrimed()
		roles.bridges.PrimeIfNeeded(ctx, cmd.ChatID, sb)
	}

	// 4. Send the prompt to kiro-cli.
	if !roles.mcp.MCPWaitForReady(ctx, 30*time.Second) {
		slog.Warn("MCP readiness timeout, proceeding anyway", "chat_id", cmd.ChatID)
	}
	var creditsBeforeTurn float64
	if chat, ok := roles.chats.ChatStore().Get(ctx, cmd.ChatID); ok {
		creditsBeforeTurn = chat.Usage.Credits
		// Latch the answering model HERE, at dispatch, not on the turn's first
		// frame. The bridge is up and its model persisted by this point, and
		// switch_model's fast path can land any time from now on — including
		// before the old model has emitted anything, which is precisely when the
		// first-frame read attributed one model's answer to another.
		roles.turnOutcome.LatchTurnModel(cmd.ChatID, chat.Model)
	}
	slog.Info("prompt", "chat_id", cmd.ChatID, "len", len(p.Text))
	start := time.Now()
	promptParams := BuildPromptParams(ctx, roles.workspace, sb, &p)
	resp, err := callPromptWithRetry(ctx, sb, promptParams, cmd.ChatID)
	elapsed := time.Since(start)
	if err != nil {
		slog.Error("prompt failed", "chat_id", cmd.ChatID, keyError, err, "elapsed", elapsed)
		// Finalize the turn before returning. Without this the assistant buffer
		// survives with Started == true, so the NEXT prompt's ensureTurnStarted
		// no-ops, emits no message_created, and extends this dead turn's blocks
		// under this dead turn's message id: one persisted assistant message
		// holding two turns' replies. The partial is persisted rather than
		// dropped -- see AbandonInFlightTurn for why that direction.
		roles.turnOutcome.AbandonInFlightTurn(ctx, cmd.ChatID)
		// ONE rendering of the cause on both channels. The SSE frame and this
		// POST's error body land on the same send-button tooltip and the client
		// paints whichever arrives last, so handing the raw error to RespondErr
		// meant RPCErrorText's fallback -- the machine triplet
		// {"errorType":...,"retryErrorType":"THROTTLING",...} -- overwrote the
		// prose promptFailureReason exists to produce. The chain is already in the
		// log line above; what travels to the user is the reason.
		reason := promptFailureReason(err)
		roles.chats.Broadcast(ctx, vibekit.NewEvent(vibekit.EventError, cmd.ChatID,
			vibekit.ErrorPayload{Code: vibekit.ErrCodePromptFailed, Message: reason}))
		d.RespondErr(w, http.StatusInternalServerError, errors.New(reason))
		return
	}
	slog.Info("prompt complete", "chat_id", cmd.ChatID, "elapsed", elapsed)

	// Empty turn recovery.
	resp = recoverEmptyTurn(roles.bridges, roles.chats, roles.turnOutcome, ctx, cmd.ChatID, resp, &p, promptParams)

	// Compute credit delta for the turn summary.
	var creditsDelta float64
	if chat, ok := roles.chats.ChatStore().Get(ctx, cmd.ChatID); ok {
		creditsDelta = chat.Usage.Credits - creditsBeforeTurn
	}
	roles.turnOutcome.EmitTurnEndedWithStats(ctx, cmd.ChatID, resp, TurnStats{
		CreditsDelta: creditsDelta,
		ElapsedMs:    float64(elapsed.Milliseconds()),
	})
	d.RespondOK(w, cmd.RequestID)
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
)

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
		if d := mappedFromData(re); d != nil && d.RetryErrorType == kasRetryThrottling {
			return classThrottled
		}
		return classFatal
	case vibekit.RPCCodeInternal:
		// -32603 is KAS's catch-all: a genuine internal fault, and also every
		// validation failure and every auth failure. Retrying an auth failure is
		// pure latency, so it is excluded by name.
		if isAuthShaped(re) {
			return classFatal
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
func isAuthShaped(re *vibekit.RPCError) bool {
	hay := re.Message + string(re.ErrorData())
	markers := []string{
		"not logged in",
		"Unauthorized",
		"unauthorized",
		"ExpiredToken",
		"AccessDenied",
		"credentials",
	}
	for _, marker := range markers {
		if strings.Contains(hay, marker) {
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
		return rpcerr.Text(err)
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
	case classFatal:
		return "fatal"
	}
	return "unknown"
}
