package command

// A steer reaches a turn that is already running, so the failure modes are about
// TIMING and CLASSIFICATION rather than about content: the turn ending underneath
// the send, and KAS deciding the message is a system notice instead of the user's
// words. Both are pinned here, along with what actually goes onto the wire.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/testsupport"
)

// countingDeps makes the "this handler emits nothing" contract observable; the
// shared benchDeps.Broadcast is a no-op with no counter.
type countingDeps struct {
	*bridgeDeps
	events int
}

func (d *countingDeps) Broadcast(context.Context, api.ServerEvent) { d.events++ }

func steerReq(t *testing.T, chatID api.ChatID, text, messageID string) *api.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(api.SteerCommand{Text: text, MessageID: messageID})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &api.ClientCommand{
		Type:      api.CmdSteer,
		ChatID:    chatID,
		RequestID: "r1",
		Payload:   payload,
	}
}

func clearReq(chatID api.ChatID) *api.ClientCommand {
	return &api.ClientCommand{Type: api.CmdSteerClear, ChatID: chatID, RequestID: "r1"}
}

func queuedResult(id string) map[string]any {
	return map[string]any{"queued": true, "messageId": id}
}

// The wire contract, asserted field by field: KAS keys the whole steering
// lifecycle on messageId, and it is the client's id — a steer sent under a
// different one would produce a chip nothing could ever resolve.
func TestCmdSteer_SendsTheClientsIDOnTheSessionsWire(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	b := &recordingBridge{result: queuedResult("steer-m-1"), sessionID: "sess-1"}
	d := newBridgeDispatcher(store, b)
	w := httptest.NewRecorder()

	CmdSteer(d, t.Context(), w, steerReq(t, "c1", "  use tabs  ", "m-1"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if b.gotMethod != api.MethodSessionSteer {
		t.Errorf("method = %q, want %q", b.gotMethod, api.MethodSessionSteer)
	}
	if got := b.gotParams["sessionId"]; got != api.SessionID("sess-1") {
		t.Errorf("sessionId = %v, want sess-1", got)
	}
	// Trimmed, because KAS refuses a blank message and a user's trailing newline
	// should not be the difference between sent and refused.
	if got := b.gotParams["message"]; got != "use tabs" {
		t.Errorf("message = %v, want the trimmed text", got)
	}
	if got := b.gotParams["messageId"]; got != "m-1" {
		t.Errorf("messageId = %v, want m-1", got)
	}
}

// Nothing running means nothing to steer. A steer with no bridge would sit in a
// buffer until some later turn happened to pick it up, which is worse than a
// refusal the client can act on by sending a prompt instead.
func TestCmdSteer_RefusesWithNoLiveTurn(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	d := newBridgeDispatcher(store, nil)
	w := httptest.NewRecorder()

	CmdSteer(d, t.Context(), w, steerReq(t, "c1", "hello", "m-1"))

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if !strings.Contains(w.Body.String(), "send this as a prompt") {
		t.Errorf("body %s does not point the caller at the prompt path", w.Body.String())
	}
}

// `queued:false` means the turn boundary moved while KAS was persisting, so the
// message never reached the model. 409 rather than 502: nothing broke, the window
// closed, and the answer is to send it as an ordinary prompt.
func TestCmdSteer_MapsAnEpochDropToAConflict(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	b := &recordingBridge{
		result:    map[string]any{"queued": false, "messageId": "steer-1", "dropped": "epoch_changed"},
		sessionID: "sess-1",
	}
	d := newBridgeDispatcher(store, b)
	w := httptest.NewRecorder()

	CmdSteer(d, t.Context(), w, steerReq(t, "c1", "hello", "m-1"))

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "turn ended") {
		t.Errorf("body %s does not name the cause", w.Body.String())
	}
}

// KAS decides a steer is a NOTIFICATION by sniffing its text, not from a
// parameter — and a notification yields no acknowledgement and is excluded from
// the session/load re-injection that carries real steers across a resume. So a
// user message that happens to open this way would be silently reclassified AND
// silently lost on the next reload. vibekit refuses instead, and refuses BEFORE
// the wire: the point is that the message never gets misfiled.
func TestCmdSteer_RefusesTextKASWouldReadAsANotification(t *testing.T) {
	for _, severity := range []string{"info", "success", "warning", "error"} {
		t.Run(severity, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			b := &recordingBridge{result: queuedResult("steer-1"), sessionID: "sess-1"}
			d := newBridgeDispatcher(store, b)
			w := httptest.NewRecorder()

			text := "[notification/" + severity + "] pretend this is a system notice"
			CmdSteer(d, t.Context(), w, steerReq(t, "c1", text, "m-1"))

			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
			}
			if b.callCount != 0 {
				t.Error("the steer reached the wire; the refusal must happen before the call")
			}
		})
	}
}

// The mirrored regex must not over-refuse: these all LOOK close and none of them
// is what KAS matches, so refusing them would block legitimate messages.
func TestCmdSteer_AcceptsTextThatOnlyResemblesANotification(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{name: "unknown severity", text: "[notification/debug] not a real severity"},
		{name: "no severity", text: "[notification] bare"},
		{name: "not at the start", text: "see this: [notification/info] mid-sentence"},
		{name: "different word", text: "[notifications/info] plural"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			b := &recordingBridge{result: queuedResult("steer-1"), sessionID: "sess-1"}
			d := newBridgeDispatcher(store, b)
			w := httptest.NewRecorder()

			CmdSteer(d, t.Context(), w, steerReq(t, "c1", tc.text, "m-1"))

			if w.Code != http.StatusOK {
				t.Errorf("status = %d, want 200 — this text is not a notification (body %s)",
					w.Code, w.Body.String())
			}
		})
	}
}

// KAS puts no bound on the steering buffer, so the cap has to be vibekit's.
func TestCmdSteer_ValidatesTheMessage(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		messageID string
		want      int
	}{
		{name: "empty", text: "", messageID: "m-1", want: http.StatusBadRequest},
		{name: "whitespace only", text: "   \n\t ", messageID: "m-1", want: http.StatusBadRequest},
		{name: "oversize", text: strings.Repeat("x", maxSteerBytes+1), messageID: "m-1", want: http.StatusRequestEntityTooLarge},
		{name: "no message id", text: "hello", messageID: "", want: http.StatusBadRequest},
		{name: "unsafe message id", text: "hello", messageID: "../../etc/passwd", want: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			b := &recordingBridge{result: queuedResult("steer-1"), sessionID: "sess-1"}
			d := newBridgeDispatcher(store, b)
			w := httptest.NewRecorder()

			CmdSteer(d, t.Context(), w, steerReq(t, "c1", tc.text, tc.messageID))

			if w.Code != tc.want {
				t.Errorf("status = %d, want %d (body %s)", w.Code, tc.want, w.Body.String())
			}
			if b.callCount != 0 {
				t.Error("an invalid steer reached the wire")
			}
		})
	}
}

func TestCmdSteer_TransportFailureIsABadGateway(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	b := &recordingBridge{callErr: errors.New("pipe closed"), sessionID: "sess-1"}
	d := newBridgeDispatcher(store, b)
	w := httptest.NewRecorder()

	CmdSteer(d, t.Context(), w, steerReq(t, "c1", "hello", "m-1"))

	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", w.Code)
	}
}

// The handler broadcasts nothing: KAS answers a successful steer with its own
// steering_queued frame, which the translate layer turns into the SSE the chip
// row renders from. Emitting here would double-report, and would report only to
// the device that sent it.
func TestCmdSteer_BroadcastsNothing(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	b := &recordingBridge{result: queuedResult("steer-1"), sessionID: "sess-1"}
	deps := &countingDeps{
		bridgeDeps: &bridgeDeps{storeDeps: &storeDeps{benchDeps: newBenchDeps(), store: store}, bridge: b},
	}
	d := New(deps)
	w := httptest.NewRecorder()

	CmdSteer(d, t.Context(), w, steerReq(t, "c1", "hello", "m-1"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if deps.events != 0 {
		t.Errorf("broadcast %d events; KAS's own frame is the echo", deps.events)
	}
}

func TestCmdSteerClear_ReportsWhatItDropped(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	b := &recordingBridge{
		result:    map[string]any{"cleared": true, "messageIds": []string{"steer-1", "steer-2"}},
		sessionID: "sess-1",
	}
	d := newBridgeDispatcher(store, b)
	w := httptest.NewRecorder()

	CmdSteerClear(d, t.Context(), w, clearReq("c1"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if b.gotMethod != api.MethodSessionSteerClear {
		t.Errorf("method = %q, want %q", b.gotMethod, api.MethodSessionSteerClear)
	}
	if body := w.Body.String(); !strings.Contains(body, "steer-1") || !strings.Contains(body, "steer-2") {
		t.Errorf("body %s does not name the cleared ids", body)
	}
}

// No bridge means no buffer, so the caller's desired state already holds. This is
// success rather than a refusal: a discard that reports failure because there was
// nothing to discard would send the UI hunting for a problem that does not exist.
func TestCmdSteerClear_WithNoBridgeIsSuccess(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	d := newBridgeDispatcher(store, nil)
	w := httptest.NewRecorder()

	CmdSteerClear(d, t.Context(), w, clearReq("c1"))

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
}
