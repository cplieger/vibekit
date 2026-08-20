package command

// Mid-turn steering, via KAS's own buffer.
//
// A prompt typed while a turn is running used to go into a CLIENT-side queue and
// be sent as a fresh turn on `turn_ended`. That is the wrong shape for what the
// user is doing: they are correcting work in progress, and the queue guaranteed
// the correction arrived only after the work it was correcting had finished. The
// escape hatch was a "send now" button that CANCELLED the turn to get ahead of
// it, throwing away everything since the last durable step.
//
// `_session/steer` is the verb for this. KAS appends the message to a
// per-session steering buffer; the graph consumes it at the next node boundary
// as an ordinary human turn and resets the agent's iteration counter so it has
// budget to act. Nothing is cancelled and nothing is discarded. A steer that
// arrives too late for the current node still lands — the end-of-turn drain
// loops the turn back through the model rather than dropping it.
//
// So vibekit keeps NO queue. Idle sends are prompts, mid-turn sends are steers,
// and the buffer that used to live in the browser is KAS's.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// maxSteerBytes caps a steer's text. Deliberately the same cap as a prompt's:
// a steer IS a prompt as far as the user is concerned, and a different limit
// would mean the same typed text is accepted or refused depending on whether a
// turn happens to be running.
const maxSteerBytes = maxPromptBytes

// notificationPrefix mirrors KAS's NOTIFICATION_PREFIX_RE.
//
// KAS decides whether a steer is a user message or a system notification by
// SNIFFING the text against this pattern — it is not a parameter, so a caller
// cannot say "this is from the user, treat it as such". A message that opens
// this way is filed as a notification with a parsed severity, and notifications
// are skipped by the `session/load` re-injection that carries real steers across
// a resume. So the reclassification is not cosmetic: it changes whether the
// message survives a reload.
//
// Keep this in sync with the bundle if KAS's pattern changes; the vibekit-side
// copy exists to REFUSE the ambiguous case, not to reimplement the routing.
var notificationPrefix = regexp.MustCompile(`^\s*\[notification/(info|success|warning|error)\]`)

var (
	// errSteerNoTurn is the refusal for a steer with no turn to join.
	errSteerNoTurn = errors.New("nothing is running to steer — send this as a prompt instead")
	// errSteerDropped maps KAS's `{queued: false}`.
	errSteerDropped = errors.New("the turn ended before this could be delivered — send it as a prompt instead")
	// errSteerLooksLikeNotification refuses the sniffing collision above.
	//
	// A refusal rather than an escape or a silent pass-through. Escaping would
	// alter text the user wrote and the agent reads; passing it through would file
	// their message as a system notification and quietly drop it on the next
	// resume. Both are invisible. This is rare, and it is fixable by the user in
	// one edit, so it is the one case worth stating out loud.
	errSteerLooksLikeNotification = errors.New(
		`a message starting with "[notification/...]" is read as a system notice, not as your words — start it with anything else`,
	)
)

// CmdSteer delivers a message into the RUNNING turn.
//
// Requires a live bridge, and unlike most session verbs that is not merely a
// precondition — a steer with no turn to join would sit in KAS's buffer until
// some later turn happened to pick it up, which is a worse outcome than a clear
// refusal. The client only reaches this path while a turn is streaming.
func CmdSteer(ctx context.Context, bridges BridgeAccess, cmd *vibekit.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p vibekit.SteerCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	text := strings.TrimSpace(p.Text)
	switch {
	case text == "":
		return nil, StatusError(http.StatusBadRequest, errEmptyPrompt)
	case len(text) > maxSteerBytes:
		return nil, StatusError(http.StatusRequestEntityTooLarge, errPromptTooLong)
	case !ValidMessageID(p.MessageID):
		return nil, StatusError(http.StatusBadRequest, errMissingMessageID)
	case notificationPrefix.MatchString(text):
		return nil, StatusError(http.StatusBadRequest, errSteerLooksLikeNotification)
	}

	bridge := bridges.Bridge(cmd.ChatID)
	if bridge == nil {
		return nil, StatusError(http.StatusConflict, errSteerNoTurn)
	}

	resp, err := bridge.Call(ctx, vibekit.MethodSessionSteer, SessionParams(bridge, map[string]any{
		"message":   text,
		"messageId": p.MessageID,
	}))
	if err != nil {
		// KAS throws (rather than answering) for an unknown session and an empty
		// message, both of which this handler has already ruled out — so an error
		// here is a transport or session-liveness failure, not a validation one.
		slog.Warn("steer: bridge call failed", "chat", cmd.ChatID, keyError, err)
		return nil, StatusError(http.StatusBadGateway, err)
	}

	var result struct {
		MessageID string `json:"messageId"`
		Dropped   string `json:"dropped"`
		Queued    bool   `json:"queued"`
	}
	if resp != nil && resp.Result != nil {
		_ = json.Unmarshal(resp.Result, &result)
	}
	if !result.Queued {
		// `dropped: "epoch_changed"` means the turn boundary moved while KAS was
		// persisting: the turn this was meant for has ended, so the message never
		// reached the model. 409 rather than 502 — nothing broke, the window closed,
		// and the client's answer is to send it as an ordinary prompt.
		slog.Info("steer dropped", "chat", cmd.ChatID, "reason", result.Dropped)
		return nil, StatusError(http.StatusConflict, errSteerDropped)
	}

	// No event is broadcast here. KAS answers a successful steer with its own
	// `steering_queued` frame on the session wire, and the translate layer turns
	// that into the SSE the chip row renders from — so emitting one here would
	// double-report, and would report it to only the device that sent it.
	slog.Info("steer queued", "chat", cmd.ChatID, "steer_id", result.MessageID)
	return responseWith(map[string]any{"steer_id": result.MessageID}), nil
}

// CmdSteerClear drops every steer still queued for the chat's session.
//
// Does NOT cancel the turn — the running execution continues, only the unread
// steers go away. That is KAS's semantic and it is the right one for this
// command: discarding a message you changed your mind about should not also
// throw away the work in flight.
func CmdSteerClear(ctx context.Context, bridges BridgeAccess, cmd *vibekit.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	bridge := bridges.Bridge(cmd.ChatID)
	if bridge == nil {
		// Nothing to clear without a session, and no buffer survives a bridge, so
		// this is success rather than a refusal: the caller's desired state holds.
		return responseWith(map[string]any{"cleared": []string{}}), nil
	}

	resp, err := bridge.Call(ctx, vibekit.MethodSessionSteerClear, SessionParams(bridge))
	if err != nil {
		slog.Warn("steer_clear: bridge call failed", "chat", cmd.ChatID, keyError, err)
		return nil, StatusError(http.StatusBadGateway, err)
	}
	var result struct {
		MessageIDs []string `json:"messageIds"`
	}
	if resp != nil && resp.Result != nil {
		_ = json.Unmarshal(resp.Result, &result)
	}

	// As with a steer, the visible effect arrives as KAS's own `steering_cleared`
	// frame; the ids come back here only so the caller knows what it dropped.
	slog.Info("steers cleared", "chat", cmd.ChatID, "count", len(result.MessageIDs))
	return responseWith(map[string]any{"cleared": result.MessageIDs}), nil
}
