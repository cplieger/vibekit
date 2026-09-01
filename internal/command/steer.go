package command

// Mid-turn steering, via KAS's own buffer.
//
// A prompt typed while a turn is running used to go into a client-side
// queue and be sent as a fresh turn on turn_ended — the wrong shape, since
// the user is correcting work in progress and the queue guaranteed the
// correction arrived only after the work it was correcting had finished.
//
// `_session/steer` is KAS's verb: it appends to a per-session steering
// buffer, and the graph consumes it at the next node boundary as an
// ordinary human turn, resetting the agent's iteration counter. Nothing is
// cancelled and nothing is discarded.
//
// So vibekit keeps no queue: idle sends are prompts, mid-turn sends are
// steers, and the buffer that used to live in the browser is KAS's.

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

// maxSteerBytes caps a steer's text — deliberately the same cap as a
// prompt's, since a steer is a prompt as far as the user is concerned.
const maxSteerBytes = maxPromptBytes

// notificationPrefix mirrors KAS's NOTIFICATION_PREFIX_RE. KAS decides
// whether a steer is a user message or a system notification by sniffing
// the text against this pattern, not by a parameter, and a notification is
// skipped by the session/load re-injection that carries real steers across
// a resume — so the reclassification changes whether the message survives
// a reload.
var notificationPrefix = regexp.MustCompile(`^\s*\[notification/(info|success|warning|error)\]`)

var (
	// errSteerNoTurn is the refusal for a steer with no turn to join.
	errSteerNoTurn = errors.New("nothing is running to steer — send this as a prompt instead")
	// errSteerPriming refuses a steer aimed into the prime window. A prime
	// is vibekit's own transcript replay sent as a real session/prompt;
	// its frames are neither broadcast nor persisted, so a steer
	// delivered into it would be silently discarded.
	errSteerPriming = errors.New("the session is still loading its history — send this again in a moment")
	// errSteerDropped maps KAS's `{queued: false}`.
	errSteerDropped = errors.New("the turn ended before this could be delivered — send it as a prompt instead")
	// errSteerLooksLikeNotification refuses the sniffing collision above,
	// rather than escaping (would alter what the user wrote) or passing
	// through (would file it as a system notification and drop it on
	// resume).
	errSteerLooksLikeNotification = errors.New(
		`a message starting with "[notification/...]" is read as a system notice, not as your words — start it with anything else`,
	)
)

// reasonNoTurn is the 409 refusal class for a steer with no turn to join.
// The client branches on the value and retries the text as an ordinary
// prompt.
const reasonNoTurn = "no_turn"

// CmdSteer delivers a message into the running turn.
//
// Requires a live bridge and a holder whose turn actually drains the
// steering buffer: KAS queues a steer for any live session, so one
// delivered to an idle chat — or into a `!cmd` shell turn — would sit
// unread with the chip stuck "queued". A wire-started turn is steerable:
// the engine's own turn drains the buffer at its next node boundary.
func CmdSteer(ctx context.Context, bridges BridgeAccess, outcome TurnOutcomeAccess, cmd *vibekit.ClientCommand) (any, error) {
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
		return nil, StatusErrorReason(http.StatusConflict, reasonNoTurn, errSteerNoTurn)
	}
	source, held := outcome.AdmissionHolderSource(cmd.ChatID)
	switch {
	case !held, source == vibekit.TurnSourceLocalShell:
		return nil, StatusErrorReason(http.StatusConflict, reasonNoTurn, errSteerNoTurn)
	case source == vibekit.TurnSourcePrime:
		return nil, StatusError(http.StatusConflict, errSteerPriming)
	}

	resp, err := bridge.Call(ctx, vibekit.MethodSessionSteer, SessionParams(bridge, map[string]any{
		"message":   text,
		"messageId": p.MessageID,
	}))
	if err != nil {
		// KAS throws (rather than answering) for an unknown session and
		// an empty message, both already ruled out — so an error here is
		// a transport or session-liveness failure.
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
		// `dropped: "epoch_changed"` means the turn boundary moved while
		// KAS was persisting: the message never reached the model. 409
		// rather than 502 — the client's answer is to send it as an
		// ordinary prompt.
		slog.Info("steer dropped", "chat", cmd.ChatID, "reason", result.Dropped)
		return nil, StatusErrorReason(http.StatusConflict, reasonNoTurn, errSteerDropped)
	}

	// No event is broadcast here: KAS answers a successful steer with its
	// own `steering_queued` frame, which the translate layer turns into
	// the SSE the chip row renders from.
	slog.Info("steer queued", "chat", cmd.ChatID, "steer_id", result.MessageID)
	return responseWith(map[string]any{"steer_id": result.MessageID}), nil
}

// CmdSteerClear drops every steer still queued for the chat's session. Does
// not cancel the turn — only the unread steers go away.
func CmdSteerClear(ctx context.Context, bridges BridgeAccess, cmd *vibekit.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	bridge := bridges.Bridge(cmd.ChatID)
	if bridge == nil {
		// Nothing to clear without a session, so this is success rather
		// than a refusal: the caller's desired state holds.
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

	// As with a steer, the visible effect arrives as KAS's own
	// `steering_cleared` frame; the ids come back only so the caller knows
	// what it dropped.
	slog.Info("steers cleared", "chat", cmd.ChatID, "count", len(result.MessageIDs))
	return responseWith(map[string]any{"cleared": result.MessageIDs}), nil
}
