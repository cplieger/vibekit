package command

// Rewind is destructive and has no undo, so what is pinned here is the shape of
// the damage: which messages leave, that a refusal leaves the record untouched,
// and that the two id rules KAS enforces are honoured before the call is made.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/testsupport"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// recordingBridge is a CommandBridge that records the one call made through it
// and replies with a scripted result. Only Call is exercised; the rest satisfies
// the interface. Shared by every command test that needs to assert what went
// onto the wire.
type recordingBridge struct {
	callErr error
	result  any
	// order, when set, records each Call under the name "call" in a slice the
	// host double shares, so a test can assert a host-side step ran BEFORE the
	// wire call. Nil in every test that does not care.
	order     *[]string
	gotMethod string
	gotParams map[string]any
	callCount int
	sessionID vibekit.SessionID
}

func (b *recordingBridge) Call(_ context.Context, method string, params any) (*vibekit.RPCResponse, error) {
	b.callCount++
	if b.order != nil {
		*b.order = append(*b.order, "call")
	}
	b.gotMethod = method
	if m, ok := params.(map[string]any); ok {
		b.gotParams = m
	}
	if b.callErr != nil {
		return nil, b.callErr
	}
	raw, err := json.Marshal(b.result)
	if err != nil {
		return nil, err
	}
	return &vibekit.RPCResponse{Result: raw}, nil
}

// CallAt exists because the prompt paths order a local settle against the read
// loop position. This double is not on a prompt path, so it reports position zero
// — which is what "no ordering to wait for" means.
func (b *recordingBridge) CallAt(ctx context.Context, method string, params any) (*vibekit.RPCResponse, uint64, error) {
	resp, err := b.Call(ctx, method, params)
	return resp, 0, err
}

func (b *recordingBridge) Notify(context.Context, string, any) error        { return nil }
func (b *recordingBridge) Respond(context.Context, int64, any, error) error { return nil }
func (b *recordingBridge) SessionID() vibekit.SessionID                     { return b.sessionID }
func (b *recordingBridge) TryAcquireForPrompt() bool                        { return true }
func (b *recordingBridge) ReleaseAfterPrompt()                              {}
func (b *recordingBridge) BeginPromptCall(context.CancelFunc) uint64        { return 0 }
func (b *recordingBridge) EndPromptCall()                                   {}
func (b *recordingBridge) PromptGeneration() uint64                         { return 0 }
func (b *recordingBridge) ArmCancelGrace(uint64, time.Duration) bool        { return false }
func (b *recordingBridge) IsPrimed() bool                                   { return true }
func (b *recordingBridge) SetPrimed()                                       {}

// bridgeDeps adds a bridge to storeDeps so the outgoing call can be observed.
type bridgeDeps struct {
	*storeDeps
	// bridge is the LIVE bridge Bridge() reports.
	bridge Bridge
	// opened is what OpenBridge hands back, and it is a separate field because
	// the state rewind exists to serve is exactly nil live plus a non-nil
	// resume: a chat nobody has prompted since it was reopened.
	opened Bridge
	// order records the host-side steps a test wants ordered against the
	// bridge's own calls. Shared with recordingBridge.order; nil by default.
	order *[]string
	// awaitErr is what AwaitReplayAdopted reports. Nil is the adopted case, so
	// the barrier is transparent unless a test asks for the refusal.
	awaitErr error
}

func (d *bridgeDeps) Bridge(vibekit.ChatID) Bridge { return d.bridge }

func (d *bridgeDeps) OpenBridge(context.Context, vibekit.ChatID, string) (Bridge, error) {
	return d.opened, nil
}

func (d *bridgeDeps) AwaitReplayAdopted(context.Context, vibekit.ChatID) error {
	if d.order != nil {
		*d.order = append(*d.order, "await")
	}
	return d.awaitErr
}

// newBridgeHost builds the all-in-one double a handler is called with. It used
// to return a *Dispatcher beside it, because every handler took one to reach its
// response helpers; a handler that returns its outcome takes no dispatcher, so
// there is nothing to hand back.
//
// One bridge answers both lookups: a chat with a live bridge is what every
// caller but rewind's resume path sees.
func newBridgeHost(store ChatStore, bridge Bridge) hostDouble {
	return &bridgeDeps{
		storeDeps: &storeDeps{benchDeps: newBenchDeps(), store: store},
		bridge:    bridge,
		opened:    bridge,
	}
}

// newBridgelessHost is newBridgeHost for a chat with NO live bridge whose
// session a resume can still reach — the state every reopened chat and every
// chat after a container restart is in.
func newBridgelessHost(store ChatStore, resumed Bridge) hostDouble {
	return &bridgeDeps{
		storeDeps: &storeDeps{benchDeps: newBenchDeps(), store: store},
		opened:    resumed,
	}
}

func rewindReq(t *testing.T, chatID vibekit.ChatID, messageID string) *vibekit.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(vibekit.RewindChatCommand{MessageID: messageID})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &vibekit.ClientCommand{
		Type:    vibekit.CmdRewindChat,
		ChatID:  chatID,
		Payload: payload,
	}
}

// seedChat writes a four-message transcript: u1, a1, u2, a2.
//
// The session id matters as much as the messages: rewind captures it before it
// resumes and refuses when the resumed bridge reports a different one, so it has
// to match recordingBridge{sessionID: "sess-1"} or every rewind test refuses.
func seedChat(t *testing.T, store ChatStore, id vibekit.ChatID) {
	t.Helper()
	err := store.Mutate(t.Context(), id, func(c *vibekit.Chat, _ bool) bool {
		c.RecordSession("sess-1")
		c.Messages = []vibekit.Message{
			{ID: "u1", Role: vibekit.RoleUser, Content: "first", Ts: 100},
			{ID: "a1", Role: vibekit.RoleAssistant, Content: "reply one", Ts: 200},
			{ID: "u2", Role: vibekit.RoleUser, Content: "second", Ts: 300},
			{ID: "a2", Role: vibekit.RoleAssistant, Content: "reply two", Ts: 400},
		}
		c.MessageCount = len(c.Messages)
		return true
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func okResult() map[string]any {
	return map[string]any{"success": true, "affectedFiles": []string{"a.go"}, "totalFiles": 2}
}

// The target message goes WITH its successors. Reverting to u2 must leave u1 and
// a1 only — not u2 — because KAS slices from the target inclusive, and a record
// that kept u2 would disagree with the session about what the transcript is.
func TestCmdRewindChat_DropsTheTargetAndEverythingAfter(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedChat(t, store, "c1")
	b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
	host := newBridgeHost(store, b)

	_, err := CmdRewindChat(t.Context(), host, host, rewindReq(t, "c1", "u2"))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
	c, ok := store.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("chat vanished")
	}
	got := make([]string, 0, len(c.Messages))
	for i := range c.Messages {
		got = append(got, c.Messages[i].ID)
	}
	if len(got) != 2 || got[0] != "u1" || got[1] != "a1" {
		t.Errorf("messages = %v, want [u1 a1]", got)
	}
	if c.MessageCount != 2 {
		t.Errorf("message_count = %d, want 2", c.MessageCount)
	}
}

func TestCmdRewindChat_CallsTheRevertVerbWithTheSessionAndMessage(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedChat(t, store, "c1")
	b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
	host := newBridgeHost(store, b)

	_, _ = CmdRewindChat(t.Context(), host, host, rewindReq(t, "c1", "u1"))

	if b.gotMethod != vibekit.MethodCheckpointRevertMultiple {
		t.Errorf("method = %q, want %q", b.gotMethod, vibekit.MethodCheckpointRevertMultiple)
	}
	if b.gotParams["messageId"] != "u1" {
		t.Errorf("messageId = %v, want u1", b.gotParams["messageId"])
	}
	// SessionParams supplies sessionId; KAS rejects the call without it.
	if b.gotParams["sessionId"] != vibekit.SessionID("sess-1") {
		t.Errorf("sessionId = %v, want sess-1", b.gotParams["sessionId"])
	}
}

// KAS refuses a non-user target in-band, so vibekit checks first rather than
// spending a round trip to be told. It also cannot address an assistant turn at
// all: only user ids are shared with KAS (an assistant turn carries KAS's own
// `<uuid>-say`).
func TestCmdRewindChat_RefusesANonUserTarget(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedChat(t, store, "c1")
	b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
	host := newBridgeHost(store, b)

	_, err := CmdRewindChat(t.Context(), host, host, rewindReq(t, "c1", "a1"))

	if statusOf(err) != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", statusOf(err))
	}
	if b.callCount != 0 {
		t.Errorf("called the bridge %d times, want 0", b.callCount)
	}
	c, _ := store.Get(t.Context(), "c1")
	if len(c.Messages) != 4 {
		t.Errorf("transcript changed on a refused rewind: %d messages", len(c.Messages))
	}
}

func TestCmdRewindChat_RefusesAnUnknownTarget(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedChat(t, store, "c1")
	b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
	host := newBridgeHost(store, b)

	_, err := CmdRewindChat(t.Context(), host, host, rewindReq(t, "c1", "nope"))

	if statusOf(err) != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", statusOf(err))
	}
	if b.callCount != 0 {
		t.Errorf("called the bridge %d times, want 0", b.callCount)
	}
}

func TestCmdRewindChat_RejectsAnEmptyMessageID(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedChat(t, store, "c1")
	host := newBridgeHost(store, &recordingBridge{result: okResult()})

	_, err := CmdRewindChat(t.Context(), host, host, rewindReq(t, "c1", ""))

	if statusOf(err) != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", statusOf(err))
	}
}

// A chat with no live bridge is the NORMAL state — vibekit spawns one on the
// first prompt, not the first view — so a rewind resumes the session instead of
// refusing. Requiring a live bridge made Rewind fail on every reopened chat and
// after every restart, with the remedy "to undo the last two turns, first add a
// turn".
func TestCmdRewindChat_ResumesABridgelessChatAndReverts(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedChat(t, store, "c1")
	b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
	host := newBridgelessHost(store, b)

	_, err := CmdRewindChat(t.Context(), host, host, rewindReq(t, "c1", "u2"))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
	if b.gotMethod != vibekit.MethodCheckpointRevertMultiple {
		t.Errorf("method = %q, want the revert verb on the resumed bridge", b.gotMethod)
	}
	c, _ := store.Get(t.Context(), "c1")
	if len(c.Messages) != 2 {
		t.Errorf("messages = %d, want 2 (u1, a1)", len(c.Messages))
	}
}

// A resume that hands back nothing cannot revert anything, and the record must
// not be cut on the strength of a bridge that does not exist.
func TestCmdRewindChat_AFailedResumeIsA502(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedChat(t, store, "c1")
	host := newBridgelessHost(store, nil)

	_, err := CmdRewindChat(t.Context(), host, host, rewindReq(t, "c1", "u2"))

	if statusOf(err) != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", statusOf(err))
	}
	c, _ := store.Get(t.Context(), "c1")
	if len(c.Messages) != 4 {
		t.Errorf("transcript truncated with no bridge to revert on: %d messages", len(c.Messages))
	}
}

// A chat that has never run a turn has no KAS session, so there is no checkpoint
// to roll back to. Refused before anything is opened.
func TestCmdRewindChat_RefusesAChatWithNoSession(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedChat(t, store, "c1")
	if err := store.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.RecordSession("")
		return true
	}); err != nil {
		t.Fatalf("clear session: %v", err)
	}
	b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
	host := newBridgelessHost(store, b)

	_, err := CmdRewindChat(t.Context(), host, host, rewindReq(t, "c1", "u2"))

	if statusOf(err) != http.StatusConflict {
		t.Errorf("status = %d, want 409", statusOf(err))
	}
	if b.callCount != 0 {
		t.Errorf("called the bridge %d times, want 0", b.callCount)
	}
	c, _ := store.Get(t.Context(), "c1")
	if len(c.Messages) != 4 {
		t.Errorf("transcript changed on a refused rewind: %d messages", len(c.Messages))
	}
}

// A failed session/load falls through to session/new, so the bridge comes back on
// a FRESH session whose log never held the target. Refuse: reverting there would
// roll back the wrong thing, and truncating the record would leave it disagreeing
// with a session that still holds every turn.
func TestCmdRewindChat_RefusesWhenTheOriginalSessionWasNotResumed(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedChat(t, store, "c1")
	b := &recordingBridge{result: okResult(), sessionID: "sess-fresh"}
	host := newBridgelessHost(store, b)

	_, err := CmdRewindChat(t.Context(), host, host, rewindReq(t, "c1", "u2"))

	if statusOf(err) != http.StatusConflict {
		t.Errorf("status = %d, want 409", statusOf(err))
	}
	if b.callCount != 0 {
		t.Errorf("called the bridge %d times, want 0", b.callCount)
	}
	c, _ := store.Get(t.Context(), "c1")
	if len(c.Messages) != 4 {
		t.Errorf("transcript truncated against an unrelated session: %d messages", len(c.Messages))
	}
}

// recordingStore notes each Mutate in a shared order slice, so a test can place
// the record rewrite against the steps that must precede it.
type recordingStore struct {
	ChatStore
	order *[]string
}

func (s *recordingStore) Mutate(ctx context.Context, id vibekit.ChatID, fn func(*vibekit.Chat, bool) bool) error {
	*s.order = append(*s.order, "mutate")
	return s.ChatStore.Mutate(ctx, id, fn)
}

// The replay-adoption wait must come BEFORE the revert and the truncation. A
// resume replays the whole transcript into a staged projection that is swapped in
// on another goroutine, and mergeProjection returns the projection's messages
// wholesale — so a swap landing after the cut hands every reverted turn straight
// back, and the rewind silently did nothing.
func TestCmdRewindChat_WaitsForTheReplayBeforeItReverts(t *testing.T) {
	order := []string{}
	base := testsupport.NewInMemoryChatStore()
	seedChat(t, base, "c1")
	store := &recordingStore{ChatStore: base, order: &order}
	b := &recordingBridge{result: okResult(), sessionID: "sess-1", order: &order}
	host := &bridgeDeps{
		storeDeps: &storeDeps{benchDeps: newBenchDeps(), store: store},
		opened:    b,
		order:     &order,
	}

	if _, err := CmdRewindChat(t.Context(), host, host, rewindReq(t, "c1", "u2")); err != nil {
		t.Fatalf("CmdRewindChat = %v, want it to succeed", err)
	}

	// seedChat's own Mutate goes through the base store, so the recorded order is
	// the handler's alone.
	want := []string{"await", "call", "mutate"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v (the replay wait must precede the revert)", order, want)
		}
	}
}

// A replay-adoption wait that does NOT complete must refuse the whole rewind, not
// proceed. The barrier's own budget bounds it, because the settle it waits on is
// triggered by the bridge's frame loop rather than by anything this handler can
// reach — so "we waited and it never settled" is a reachable state, and the swap
// it was waiting for is still to come. Cutting the record there is the exact
// data loss the barrier exists to prevent, so nothing may be cut and the revert
// must not even be attempted.
func TestCmdRewindChat_ARefusedReplayWaitTruncatesNothing(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedChat(t, store, "c1")
	b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
	host := &bridgeDeps{
		storeDeps: &storeDeps{benchDeps: newBenchDeps(), store: store},
		opened:    b,
		awaitErr:  errors.New("replay not adopted"),
	}

	_, err := CmdRewindChat(t.Context(), host, host, rewindReq(t, "c1", "u2"))

	if statusOf(err) != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (a pending replay is worth retrying)", statusOf(err))
	}
	if b.gotMethod != "" {
		t.Errorf("revert was attempted as %q; a rewind that cannot be made durable must not touch KAS either", b.gotMethod)
	}
	c, _ := store.Get(t.Context(), "c1")
	if len(c.Messages) != 4 {
		t.Errorf("messages = %d, want 4: the record was cut into a pending swap", len(c.Messages))
	}
}

// KAS's in-band refusal (a live turn, a concurrent revert, an unreadable
// snapshot) must leave the record ALONE. A truncated transcript against an
// un-reverted session is the one outcome worse than a failed rewind.
func TestCmdRewindChat_InBandRefusalLeavesTheRecordIntact(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedChat(t, store, "c1")
	b := &recordingBridge{
		result: map[string]any{
			"success": false,
			"error":   "Cannot revert while the agent is still running. Stop the turn and try again.",
		},
		sessionID: "sess-1",
	}
	host := newBridgeHost(store, b)

	_, err := CmdRewindChat(t.Context(), host, host, rewindReq(t, "c1", "u2"))

	if statusOf(err) != http.StatusConflict {
		t.Errorf("status = %d, want 409", statusOf(err))
	}
	// KAS's reason reaches the client: it is more specific than anything vibekit
	// could infer, and mid-turn is the case a user can actually act on.
	if body := errText(err); !strings.Contains(body, "still running") {
		t.Errorf("response %s does not carry KAS's reason", body)
	}
	c, _ := store.Get(t.Context(), "c1")
	if len(c.Messages) != 4 {
		t.Errorf("transcript truncated after a refused revert: %d messages", len(c.Messages))
	}
}

func TestCmdRewindChat_TransportFailureLeavesTheRecordIntact(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedChat(t, store, "c1")
	b := &recordingBridge{callErr: errors.New("broken pipe"), sessionID: "sess-1"}
	host := newBridgeHost(store, b)

	_, err := CmdRewindChat(t.Context(), host, host, rewindReq(t, "c1", "u2"))

	if statusOf(err) != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", statusOf(err))
	}
	c, _ := store.Get(t.Context(), "c1")
	if len(c.Messages) != 4 {
		t.Errorf("transcript truncated after a failed call: %d messages", len(c.Messages))
	}
}

// Reverting to the FIRST message empties the transcript. Legal, and the chat
// survives — it is the same chat, back at the start, not a deleted one.
func TestCmdRewindChat_ToTheFirstMessageEmptiesTheTranscript(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedChat(t, store, "c1")
	b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
	host := newBridgeHost(store, b)

	_, err := CmdRewindChat(t.Context(), host, host, rewindReq(t, "c1", "u1"))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200", statusOf(err))
	}
	c, ok := store.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("chat was deleted; a rewind is not a delete")
	}
	if len(c.Messages) != 0 {
		t.Errorf("messages = %d, want 0", len(c.Messages))
	}
}

// seedLiveLayout writes the shape a real 12-message chat had when a rewind to the
// end of turn 3 failed: two interrupted turns, one completed turn, then two turns
// whose prompt failed outright and left only an outcome marker.
//
// A trimmed SYNTHETIC fixture — the structure is the snapshot's exactly (roles,
// event kinds, outcomes, and which rows carry no Content at all), the words are
// not. What makes it worth pinning is that four of the twelve rows carry no
// Content and five carry a turn outcome, so a projection or a boundary rule that
// treats either as a turn terminator moves the rewind target.
func seedLiveLayout(t *testing.T, store ChatStore, id vibekit.ChatID) {
	t.Helper()
	interrupted := func(msgID string, ts int64) vibekit.Message {
		return vibekit.Message{
			ID: msgID, Role: vibekit.RoleAssistant, Ts: ts,
			TurnOutcome:       vibekit.TurnOutcomeInterrupted,
			TurnStopReasonRaw: vibekit.StopReasonInterrupted,
		}
	}
	failedMarker := func(msgID string, ts int64) vibekit.Message {
		return vibekit.Message{
			ID: msgID, Role: vibekit.RoleEvent, Ts: ts,
			EventKind:         vibekit.EventTurnOutcome,
			TurnOutcome:       vibekit.TurnOutcomeFailed,
			TurnStopReasonRaw: vibekit.StopReasonError,
		}
	}
	err := store.Mutate(t.Context(), id, func(c *vibekit.Chat, _ bool) bool {
		c.RecordSession("sess-1")
		c.Messages = []vibekit.Message{
			{ID: "m-u1", Role: vibekit.RoleUser, Content: "first", Ts: 100},
			interrupted("a1", 200),
			{ID: "e1", Role: vibekit.RoleEvent, EventKind: vibekit.EventInterrupted, Content: "Interrupted", Ts: 201},
			{ID: "m-u2", Role: vibekit.RoleUser, Content: "resume", Ts: 300},
			interrupted("a2", 400),
			{ID: "e2", Role: vibekit.RoleEvent, EventKind: vibekit.EventInterrupted, Content: "Interrupted", Ts: 401},
			{ID: "m-u3", Role: vibekit.RoleUser, Content: "resume", Ts: 500},
			{
				ID: "a3", Role: vibekit.RoleAssistant, Content: "the reply", Ts: 600,
				TurnOutcome:       vibekit.TurnOutcomeCompleted,
				TurnStopReasonRaw: vibekit.StopReasonEndTurn,
			},
			{ID: "m-u4", Role: vibekit.RoleUser, Content: "carry on", Ts: 700},
			failedMarker("e3", 701),
			{ID: "m-u5", Role: vibekit.RoleUser, Content: "carry on", Ts: 800},
			failedMarker("e4", 801),
		}
		return true
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// Rewinding to the end of turn 3 on the live layout keeps messages 0..7 and
// nothing else.
//
// The record is checked by ID SEQUENCE, not just by length: a merge that put back
// eight of the wrong messages would satisfy a count.
func TestCmdRewindChat_KeepsTurnsOneToThreeOnTheLiveLayout(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedLiveLayout(t, store, "c1")

	// The client sends the NEXT turn's trigger, because KAS drops the addressed
	// message inclusive. Turn 4's user message is index 8.
	c, _ := store.Get(t.Context(), "c1")
	if got := userMessageIndex(c.Messages, "m-u4"); got != 8 {
		t.Fatalf("userMessageIndex(m-u4) = %d, want 8", got)
	}

	b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
	host := newBridgelessHost(store, b)

	_, err := CmdRewindChat(t.Context(), host, host, rewindReq(t, "c1", "m-u4"))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
	c, _ = store.Get(t.Context(), "c1")
	got := make([]string, 0, len(c.Messages))
	for i := range c.Messages {
		got = append(got, c.Messages[i].ID)
	}
	want := []string{"m-u1", "a1", "e1", "m-u2", "a2", "e2", "m-u3", "a3"}
	if !slices.Equal(got, want) {
		t.Errorf("messages = %v, want %v", got, want)
	}
}

func TestUserMessageIndex(t *testing.T) {
	msgs := []vibekit.Message{
		{ID: "u1", Role: vibekit.RoleUser},
		{ID: "a1", Role: vibekit.RoleAssistant},
		{ID: "e1", Role: vibekit.RoleEvent},
		{ID: "u2", Role: vibekit.RoleUser},
	}
	cases := map[string]int{"u1": 0, "u2": 3, "a1": -1, "e1": -1, "missing": -1, "": -1}
	for id, want := range cases {
		if got := userMessageIndex(msgs, id); got != want {
			t.Errorf("userMessageIndex(%q) = %d, want %d", id, got, want)
		}
	}
}

// How much history a rewind discarded is the one number in its log line that a
// reader cannot recover from anywhere else: the record has already been cut by
// the time anyone looks. Reverting to u2 of the four-message transcript drops
// u2 and a2, so the count is 2.
func TestCmdRewindChat_LogsHowManyMessagesItDropped(t *testing.T) {
	logs := captureLogs(t)
	store := testsupport.NewInMemoryChatStore()
	seedChat(t, store, "c1")
	b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
	host := newBridgeHost(store, b)

	if _, err := CmdRewindChat(t.Context(), host, host, rewindReq(t, "c1", "u2")); err != nil {
		t.Fatalf("CmdRewindChat = %v, want it to succeed", err)
	}

	if !strings.Contains(logs.String(), "dropped_messages=2") {
		t.Errorf("log does not report dropped_messages=2: %s", logs.String())
	}
}
