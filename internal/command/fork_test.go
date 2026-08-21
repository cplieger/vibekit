package command

// The tangent has TWO paths and the fallback is the interesting one, so these
// tests drive a real REFUSAL rather than mocking the decision: every case here
// hands the handler a bridge that answers the way KAS would, and the handler
// picks its own path from that answer. A test that stubbed "the fork failed"
// would assert that the branch exists without pinning what triggers it, which is
// exactly the half that has to keep working.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/testsupport"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// primeRecorder is bridgeDeps plus the prime-note channel, which is the only way
// the fallback is observable from here: the note is what makes the tangent's
// FIRST session carry the parent's transcript, and nothing else in the reply
// distinguishes the two paths for the next spawn.
type primeRecorder struct {
	*bridgeDeps
	primed map[vibekit.ChatID]vibekit.ChatID
}

func (d *primeRecorder) PrimeFromChat(chatID, sourceChatID vibekit.ChatID) {
	if d.primed == nil {
		d.primed = make(map[vibekit.ChatID]vibekit.ChatID, 1)
	}
	d.primed[chatID] = sourceChatID
}

func newForkHost(store ChatStore, bridge Bridge) *primeRecorder {
	return &primeRecorder{bridgeDeps: &bridgeDeps{
		storeDeps: &storeDeps{benchDeps: newBenchDeps(), store: store},
		bridge:    bridge,
	}}
}

func forkReq(t *testing.T, newChat, parent vibekit.ChatID, title string) *vibekit.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(vibekit.ForkChatCommand{ParentChatID: parent, Title: title})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &vibekit.ClientCommand{
		Type:    vibekit.CmdForkChat,
		ChatID:  newChat,
		Payload: payload,
	}
}

// seedParent writes a parent chat with a transcript, a model, a mode and a live
// session id — everything the tangent inherits.
func seedParent(t *testing.T, store ChatStore, id vibekit.ChatID) {
	t.Helper()
	if err := store.Mutate(t.Context(), id, func(c *vibekit.Chat, _ bool) bool {
		c.Name = "Parent conversation"
		c.Model = "parent-model"
		c.CurrentModeID = "plan"
		c.Effort = string(vibekit.EffortHigh)
		c.RecordSession("sess_parent")
		c.Messages = []vibekit.Message{
			{ID: "u1", Role: vibekit.RoleUser, Content: "how does the reaper work", Ts: 100},
			{ID: "a1", Role: vibekit.RoleAssistant, Content: "it keeps the session chain", Ts: 200},
		}
		return true
	}); err != nil {
		t.Fatalf("seed parent: %v", err)
	}
}

// TestCmdForkChat_BindsTheForkedSession is the primary path and the whole point:
// KAS returns a NEW session id carrying the parent's context, and the tangent is
// created already bound to it — so the transcript arrives from the session/load
// replay and vibekit copies no messages.
func TestCmdForkChat_BindsTheForkedSession(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedParent(t, store, "c-parent")
	br := &recordingBridge{
		sessionID: "sess_parent",
		result:    map[string]any{"sessionId": "sess_tangent"},
	}
	host := newForkHost(store, br)

	_, err := CmdForkChat(t.Context(), host, host, testWorkspace(t), forkReq(t, "c-tangent", "c-parent", "Reaper detour"))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
	if br.gotMethod != vibekit.MethodSessionFork {
		t.Errorf("called %q, want %q", br.gotMethod, vibekit.MethodSessionFork)
	}
	if got := br.gotParams["sessionId"]; got != vibekit.SessionID("sess_parent") {
		t.Errorf("forked sessionId = %v, want the PARENT's sess_parent", got)
	}
	c, ok := store.Get(t.Context(), "c-tangent")
	if !ok {
		t.Fatal("the tangent chat was not created")
	}
	if c.ACPSessionID != "sess_tangent" {
		t.Errorf("acp_session_id = %q, want the forked sess_tangent", c.ACPSessionID)
	}
	// Bound means the replay supplies the transcript. Copying messages here would
	// duplicate what the session already carries.
	if len(c.Messages) != 0 {
		t.Errorf("tangent carries %d messages, want 0: the replay supplies them", len(c.Messages))
	}
	// The chain is what the reaper's keep-list reads, so a forked session must be
	// IN it or the next sweep deletes the transcript the tangent is reading.
	if chain := c.SessionChain(); len(chain) != 1 || chain[0] != "sess_tangent" {
		t.Errorf("session chain = %v, want [sess_tangent]", chain)
	}
	// A forked tangent needs no prime: it HAS the context.
	if len(host.primed) != 0 {
		t.Errorf("a forked tangent was also marked for priming: %v", host.primed)
	}
}

// TestCmdForkChat_SendsTangentMeta pins the _meta.kiro block, which is entirely
// caller-supplied on this verb. `createdReason` is KAS's own spelling for a
// tangent (measured against the 2.18.0 sidecar) and is what a later session/load
// reports back beside parentSessionId.
//
// It also pins the absence of `messageId`: KAS's own /tangent sends none, and
// adding one would make the fork addressable to a user message that a tangent has
// no reason to name.
func TestCmdForkChat_SendsTangentMeta(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedParent(t, store, "c-parent")
	br := &recordingBridge{sessionID: "sess_parent", result: map[string]any{"sessionId": "sess_t"}}
	host := newForkHost(store, br)

	_, _ = CmdForkChat(t.Context(), host, host, testWorkspace(t), forkReq(t, "c-tangent", "c-parent", "Reaper detour"))

	if _, ok := br.gotParams["messageId"]; ok {
		t.Error("session/fork carried a messageId; a tangent fork names no message")
	}
	if br.gotParams["cwd"] == nil {
		t.Error("session/fork carried no cwd")
	}
	meta, ok := br.gotParams["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("_meta = %T, want a map", br.gotParams["_meta"])
	}
	kiro, ok := meta["kiro"].(map[string]any)
	if !ok {
		t.Fatalf("_meta.kiro = %T, want a map", meta["kiro"])
	}
	if kiro["createdReason"] != vibekit.CreatedReasonTangent {
		t.Errorf("createdReason = %v, want %q", kiro["createdReason"], vibekit.CreatedReasonTangent)
	}
	if kiro["title"] != "Reaper detour" {
		t.Errorf("title = %v, want the supplied one", kiro["title"])
	}
}

// TestCmdForkChat_OmitsAnEmptyTitle: an absent title must not become an empty
// one. KAS stores the block verbatim, so sending `title: ""` would name the
// session the empty string rather than leaving it unnamed.
func TestCmdForkChat_OmitsAnEmptyTitle(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedParent(t, store, "c-parent")
	br := &recordingBridge{sessionID: "sess_parent", result: map[string]any{"sessionId": "sess_t"}}
	host := newForkHost(store, br)

	_, _ = CmdForkChat(t.Context(), host, host, testWorkspace(t), forkReq(t, "c-tangent", "c-parent", ""))

	meta := br.gotParams["_meta"].(map[string]any) //nolint:forcetypeassert // shape pinned by TestCmdForkChat_SendsTangentMeta
	kiro := meta["kiro"].(map[string]any)          //nolint:forcetypeassert // shape pinned by TestCmdForkChat_SendsTangentMeta
	if _, ok := kiro["title"]; ok {
		t.Errorf("an empty title was sent as a key: %v", kiro["title"])
	}
}

// TestCmdForkChat_InheritsTheParentsAgent: the tangent's answers must come from
// the same agent that produced the conversation it inherited. Read off the
// parent's RECORD rather than sent by the client, because the record is the truth
// about all three and a tab's projection can be stale.
func TestCmdForkChat_InheritsTheParentsAgent(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedParent(t, store, "c-parent")
	br := &recordingBridge{sessionID: "sess_parent", result: map[string]any{"sessionId": "sess_t"}}
	host := newForkHost(store, br)

	_, _ = CmdForkChat(t.Context(), host, host, testWorkspace(t), forkReq(t, "c-tangent", "c-parent", ""))

	c, _ := store.Get(t.Context(), "c-tangent")
	if c.Model != "parent-model" {
		t.Errorf("model = %q, want the parent's", c.Model)
	}
	if c.CurrentModeID != "plan" {
		t.Errorf("current_mode_id = %q, want the parent's", c.CurrentModeID)
	}
	if c.Effort != string(vibekit.EffortHigh) {
		t.Errorf("effort = %q, want the parent's", c.Effort)
	}
	// The NAME is deliberately not inherited: it stays the ordinary precedence
	// (the agent's focus title, else the first prompt's truncation). Copying the
	// parent's would give two tabs the same label with no way to tell them apart.
	if c.Name != vibekit.DefaultChatName {
		t.Errorf("name = %q, want the default; the parent's name is not inherited", c.Name)
	}
}

// TestCmdForkChat_FallsBackToPrimingOnRefusal is the fallback, driven by a real
// refusal in each of the three shapes a refusal actually takes. The tangent opens
// in all of them — unbound, and marked so its first session gets the parent's
// transcript — because a feature that vanishes on a refusal is one the user
// cannot rely on.
func TestCmdForkChat_FallsBackToPrimingOnRefusal(t *testing.T) {
	cases := map[string]*recordingBridge{
		// A transport or JSON-RPC failure: KAS threw.
		"call error": {sessionID: "sess_parent", callErr: errors.New("-32601 method not found")},
		// A reply with no session id at all. KAS's own fork wrapper reads
		// `.sessionId` off the result, so this is what a refusal it can explain
		// looks like from here.
		"no session id": {sessionID: "sess_parent", result: map[string]any{"error": "cannot fork"}},
		// A session id that is not path-safe. Validated rather than trusted,
		// because the value reaches a filesystem path inside KAS and vibekit's own
		// reaper keep-list.
		"unsafe session id": {sessionID: "sess_parent", result: map[string]any{"sessionId": "../../etc/passwd"}},
	}
	for name, br := range cases {
		t.Run(name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			seedParent(t, store, "c-parent")
			host := newForkHost(store, br)

			body, err := CmdForkChat(t.Context(), host, host, testWorkspace(t), forkReq(t, "c-tangent", "c-parent", ""))

			if statusOf(err) != http.StatusOK {
				t.Fatalf("status = %d, want 200: a refused fork still opens the tangent (err %v)",
					statusOf(err), err)
			}
			// The handler RETURNS its body, so the assertion reads the value
			// instead of decoding the JSON the dispatcher would have written.
			reply, ok := body.(map[string]any)
			if !ok {
				t.Fatalf("body = %T, want map[string]any", body)
			}
			if reply["outcome"] != vibekit.ForkOutcomePrimed {
				t.Errorf("outcome = %v, want %q", reply["outcome"], vibekit.ForkOutcomePrimed)
			}
			if reply["session_id"] != "" {
				t.Errorf("session_id = %v, want empty on the primed path", reply["session_id"])
			}
			c, ok := store.Get(t.Context(), "c-tangent")
			if !ok {
				t.Fatal("the tangent chat was not created on the primed path")
			}
			if c.ACPSessionID != "" {
				t.Errorf("acp_session_id = %q, want empty: no session was forked", c.ACPSessionID)
			}
			// The prime note is what carries the parent's history into the
			// tangent's first session. Without it that session starts blind on the
			// conversation the user opened it FROM.
			if got := host.primed["c-tangent"]; got != "c-parent" {
				t.Errorf("prime note = %q, want c-parent", got)
			}
			// The inheritance that does not depend on the fork still happens.
			if c.Model != "parent-model" || c.CurrentModeID != "plan" {
				t.Errorf("primed tangent lost the parent's agent: model=%q mode=%q",
					c.Model, c.CurrentModeID)
			}
		})
	}
}

// TestCmdForkChat_PrimesWhenTheParentHasNoLiveSession: the other fallback
// trigger, and it is the common one — a chat whose bridge was never started, or
// whose process is gone.
//
// The parent's bridge is deliberately NOT started here as a side effect of
// opening a tangent: that would resume a conversation the user did not ask to
// resume. So no call is made at all.
func TestCmdForkChat_PrimesWhenTheParentHasNoLiveSession(t *testing.T) {
	cases := map[string]Bridge{
		"no bridge":  nil,
		"no session": &recordingBridge{sessionID: ""},
	}
	for name, br := range cases {
		t.Run(name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			seedParent(t, store, "c-parent")
			host := newForkHost(store, br)

			_, err := CmdForkChat(t.Context(), host, host, testWorkspace(t), forkReq(t, "c-tangent", "c-parent", ""))

			if statusOf(err) != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
			}
			if rb, ok := br.(*recordingBridge); ok && rb.callCount != 0 {
				t.Errorf("made %d calls, want 0: a bridgeless parent is not started to be forked",
					rb.callCount)
			}
			if got := host.primed["c-tangent"]; got != "c-parent" {
				t.Errorf("prime note = %q, want c-parent", got)
			}
		})
	}
}

// TestCmdForkChat_RefusesToReshapeAnExistingChat pins the guard, for
// CmdResumeSession's reason: binding a live chat to another session strands its
// own (the transcript stays on disk unreferenced, so the reaper sweeps it) and
// silently changes the history under a conversation someone is reading.
func TestCmdForkChat_RefusesToReshapeAnExistingChat(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedParent(t, store, "c-parent")
	if err := store.Mutate(t.Context(), "c-tangent", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "Existing work"
		c.RecordSession("sess_existing")
		return true
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	br := &recordingBridge{sessionID: "sess_parent", result: map[string]any{"sessionId": "sess_new"}}
	host := newForkHost(store, br)

	_, _ = CmdForkChat(t.Context(), host, host, testWorkspace(t), forkReq(t, "c-tangent", "c-parent", ""))

	c, _ := store.Get(t.Context(), "c-tangent")
	if c.ACPSessionID != "sess_existing" {
		t.Errorf("acp_session_id = %q, want sess_existing — an existing chat was rebound",
			c.ACPSessionID)
	}
	if c.Name != "Existing work" {
		t.Errorf("name = %q, want Existing work", c.Name)
	}
}

// TestCmdForkChat_Rejects covers the refusals that are the CLIENT's mistake
// rather than KAS's, and which must not create a chat.
func TestCmdForkChat_Rejects(t *testing.T) {
	cases := map[string]struct {
		newChat vibekit.ChatID
		parent  vibekit.ChatID
		title   string
		want    int
		seed    bool
	}{
		// A tangent of itself would rebind the chat's own session through
		// RecordSession and retire the session it is still using. The one shape
		// here that corrupts rather than merely fails.
		"self fork":       {newChat: "c-parent", parent: "c-parent", want: http.StatusBadRequest, seed: true},
		"empty parent":    {newChat: "c-tangent", parent: "", want: http.StatusBadRequest, seed: true},
		"unsafe parent":   {newChat: "c-tangent", parent: "../etc", want: http.StatusBadRequest, seed: true},
		"unknown parent":  {newChat: "c-tangent", parent: "c-missing", want: http.StatusNotFound, seed: false},
		"oversized title": {newChat: "c-tangent", parent: "c-parent", title: strings.Repeat("t", vibekit.MaxChatNameBytes+1), want: http.StatusBadRequest, seed: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			if tc.seed {
				seedParent(t, store, "c-parent")
			}
			br := &recordingBridge{sessionID: "sess_parent", result: map[string]any{"sessionId": "sess_t"}}
			host := newForkHost(store, br)

			_, err := CmdForkChat(t.Context(), host, host, testWorkspace(t), forkReq(t, tc.newChat, tc.parent, tc.title))

			if statusOf(err) != tc.want {
				t.Errorf("status = %d, want %d (body %s)", statusOf(err), tc.want, errText(err))
			}
			if tc.newChat != "c-parent" {
				if _, ok := store.Get(t.Context(), tc.newChat); ok {
					t.Errorf("a chat was created for a refused fork")
				}
			}
			if br.callCount != 0 {
				t.Errorf("made %d calls, want 0: a refused request must not reach KAS", br.callCount)
			}
		})
	}
}

// TestCmdForkChat_RejectsAMalformedPayload: the envelope's own failure mode.
func TestCmdForkChat_RejectsAMalformedPayload(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	host := newForkHost(store, nil)

	_, err := CmdForkChat(t.Context(), host, host, testWorkspace(t), &vibekit.ClientCommand{
		Type:    vibekit.CmdForkChat,
		ChatID:  "c-tangent",
		Payload: json.RawMessage(`{"parent_chat_id":`),
	})

	if statusOf(err) != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", statusOf(err))
	}
}

// TestCmdForkChat_TheRecordSurvivesAClose is the History half of the tangent's
// contract: closing the tab kills the WORK, not the record, so a tangent (like
// any chat) is still there to reopen afterwards.
//
// It matters here specifically because a tangent is a SUB-tab: the parent's close
// cascade closes it, so this is the ordinary way a tangent ends rather than an
// edge case.
func TestCmdForkChat_TheRecordSurvivesAClose(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedParent(t, store, "c-parent")
	br := &recordingBridge{sessionID: "sess_parent", result: map[string]any{"sessionId": "sess_t"}}
	host := newForkHost(store, br)

	_, _ = CmdForkChat(t.Context(), host, host, testWorkspace(t), forkReq(t, "c-tangent", "c-parent", ""))
	_, _ = CmdCloseChat(t.Context(), host, host, host, &vibekit.ClientCommand{Type: vibekit.CmdCloseChat, ChatID: "c-tangent", Payload: json.RawMessage(`{}`)})

	c, ok := store.Get(t.Context(), "c-tangent")
	if !ok {
		t.Fatal("close_chat deleted the tangent's record; History would not list it")
	}
	// The session stays in the chain so the reaper's keep-list still protects the
	// transcript a reopen would load.
	if chain := c.SessionChain(); len(chain) == 0 {
		t.Error("the tangent lost its session chain on close")
	}
}

// testWorkspace is a Workspace value over throwaway dirs. It replaced a double
// method set when command.Workspace stopped being an interface.
func testWorkspace(t *testing.T) Workspace {
	t.Helper()
	return Workspace{Dir: t.TempDir(), ConfigDir: t.TempDir()}
}

// A title of exactly MaxChatNameBytes is legal: the oversized-title row in
// TestCmdForkChat_Rejects pins the refusal one byte above it, and this pins
// that the last accepted length really is accepted and reaches KAS verbatim.
func TestCmdForkChat_AcceptsATitleAtTheCap(t *testing.T) {
	atCap := strings.Repeat("t", vibekit.MaxChatNameBytes)
	store := testsupport.NewInMemoryChatStore()
	seedParent(t, store, "c-parent")
	br := &recordingBridge{sessionID: "sess_parent", result: map[string]any{"sessionId": "sess_t"}}
	host := newForkHost(store, br)

	_, err := CmdForkChat(t.Context(), host, host, testWorkspace(t), forkReq(t, "c-tangent", "c-parent", atCap))
	if err != nil {
		t.Fatalf("CmdForkChat with a %d-byte title = %v, want it accepted", len(atCap), err)
	}
	if _, ok := store.Get(t.Context(), "c-tangent"); !ok {
		t.Fatal("no tangent was created for an accepted title")
	}
	meta, ok := br.gotParams["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("_meta = %T, want a map", br.gotParams["_meta"])
	}
	kiro, ok := meta["kiro"].(map[string]any)
	if !ok {
		t.Fatalf("_meta.kiro = %T, want a map", meta["kiro"])
	}
	title, _ := kiro["title"].(string)
	if title != atCap {
		t.Errorf("title reached KAS as %d bytes, want the %d-byte title as given",
			len(title), len(atCap))
	}
}
