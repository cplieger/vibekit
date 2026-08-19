package command

// Reasoning effort is PER-CHAT: a field on the chat record beside model, mode
// and supervised. It used to be one global `model_effort` setting keyed by the
// LAST model, so two chats could not disagree and switching models discarded the
// previous model's level. What is pinned here is that the level lands on the
// chat, that a bridgeless chat is no longer a 409, and that a refused live
// switch is not persisted as a level the session never took.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cplieger/vibekit/internal/testsupport"
	"github.com/cplieger/vibekit/internal/vibekit"
)

func effortReq(t *testing.T, chatID vibekit.ChatID, level string) *vibekit.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"level": level})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &vibekit.ClientCommand{
		Type:      vibekit.CmdSetEffort,
		ChatID:    chatID,
		RequestID: "r1",
		Payload:   payload,
	}
}

func TestCmdSetEffort_PersistsOnTheChatRecord(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedEmptyChat(t, store, "c1")
	b := &recordingBridge{result: map[string]any{}, sessionID: "sess-1"}
	d := newBridgeDispatcher(store, b)
	w := httptest.NewRecorder()

	CmdSetEffort(d, t.Context(), w, effortReq(t, "c1", "high"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	c, ok := store.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("chat vanished")
	}
	if c.Effort != "high" {
		t.Errorf("Effort = %q, want %q; the level has to survive a restart to reach StartOpts.Effort", c.Effort, "high")
	}
	if b.gotMethod != vibekit.MethodSetConfigOption {
		t.Errorf("method = %q, want %q", b.gotMethod, vibekit.MethodSetConfigOption)
	}
	if b.gotParams["configId"] != vibekit.ConfigOptionEffort {
		t.Errorf("configId = %v, want %q", b.gotParams["configId"], vibekit.ConfigOptionEffort)
	}
	if b.gotParams["value"] != "high" {
		t.Errorf("value = %v, want high", b.gotParams["value"])
	}
}

// Two chats disagreeing is the whole point of the move. The old global setting
// could not express it.
func TestCmdSetEffort_TwoChatsHoldDifferentLevels(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedEmptyChat(t, store, "c1")
	seedEmptyChat(t, store, "c2")
	d := newBridgeDispatcher(store, &recordingBridge{result: map[string]any{}, sessionID: "s"})

	CmdSetEffort(d, t.Context(), httptest.NewRecorder(), effortReq(t, "c1", "low"))
	CmdSetEffort(d, t.Context(), httptest.NewRecorder(), effortReq(t, "c2", "max"))

	c1, _ := store.Get(t.Context(), "c1")
	c2, _ := store.Get(t.Context(), "c2")
	if c1.Effort != "low" || c2.Effort != "max" {
		t.Errorf("efforts = %q / %q, want low / max", c1.Effort, c2.Effort)
	}
}

// noBridgeDeps reports no live bridge, the state of every chat before its first
// prompt.
type noBridgeDeps struct{ *storeDeps }

func (d *noBridgeDeps) GetBridge(vibekit.ChatID) Bridge { return nil }

// A bridgeless chat used to answer 409, which is why the client had a second
// path that wrote a GLOBAL setting instead — a different store and a different
// scope reached by the same click. The persisted level is enough now: spawnBridge
// applies it at session/new.
func TestCmdSetEffort_NoBridgeIsNotAConflict(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedEmptyChat(t, store, "c1")
	d := New(&noBridgeDeps{storeDeps: &storeDeps{benchDeps: newBenchDeps(), store: store}})
	w := httptest.NewRecorder()

	CmdSetEffort(d, t.Context(), w, effortReq(t, "c1", "medium"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	c, _ := store.Get(t.Context(), "c1")
	if c.Effort != "medium" {
		t.Errorf("Effort = %q, want medium", c.Effort)
	}
}

// Mirrors CmdSetMode: a fresh chat is client-side only until its first prompt, so
// without auto-create every pick before the first message 404'd and the control
// rolled back.
func TestCmdSetEffort_AutoCreatesTheRecordLikeSetMode(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	d := New(&noBridgeDeps{storeDeps: &storeDeps{benchDeps: newBenchDeps(), store: store}})
	w := httptest.NewRecorder()

	CmdSetEffort(d, t.Context(), w, effortReq(t, "c-brand-new", "xhigh"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	c, ok := store.Get(t.Context(), "c-brand-new")
	if !ok {
		t.Fatal("no chat record created; the pick would 404 and the control would roll back")
	}
	if c.Effort != "xhigh" {
		t.Errorf("Effort = %q, want xhigh", c.Effort)
	}
	if c.Name != vibekit.DefaultChatName {
		t.Errorf("Name = %q, want the default so the row is not blank", c.Name)
	}
}

// Switch live FIRST, persist second: a level the running session refused must not
// be stored, or the chat would advertise an effort that never applied and would
// re-apply it at the next session/new.
func TestCmdSetEffort_ARefusedLiveSwitchIsNotPersisted(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedEmptyChat(t, store, "c1")
	b := &recordingBridge{callErr: errors.New("no such config option"), sessionID: "sess-1"}
	d := newBridgeDispatcher(store, b)
	w := httptest.NewRecorder()

	CmdSetEffort(d, t.Context(), w, effortReq(t, "c1", "max"))

	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", w.Code)
	}
	c, _ := store.Get(t.Context(), "c1")
	if c.Effort != "" {
		t.Errorf("Effort = %q, want it unset after a refused switch", c.Effort)
	}
}

func TestCmdSetEffort_RejectsAnUnknownLevel(t *testing.T) {
	for _, level := range []string{"", "ludicrous", "LOW"} {
		t.Run(level, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			seedEmptyChat(t, store, "c1")
			b := &recordingBridge{result: map[string]any{}, sessionID: "s"}
			d := newBridgeDispatcher(store, b)
			w := httptest.NewRecorder()

			CmdSetEffort(d, t.Context(), w, effortReq(t, "c1", level))

			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", w.Code)
			}
			if b.callCount != 0 {
				t.Error("an invalid level reached the bridge")
			}
		})
	}
}

// vibekit.Chat.Effort is what spawnBridge reads for StartOpts.Effort, so the header
// has to carry it too: the effort control renders the ACTIVE chat's level, and an
// empty chat never fetches its full record.
func TestChatHeader_CarriesEffort(t *testing.T) {
	c := &vibekit.Chat{ID: "c1", Effort: "high"}
	if got := c.Header().Effort; got != "high" {
		t.Errorf("Header().Effort = %q, want high", got)
	}
}
