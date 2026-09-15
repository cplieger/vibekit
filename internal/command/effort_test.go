package command

// Reasoning effort is PER-CHAT: a field on the chat record beside model, mode
// and supervised. It used to be one global `model_effort` setting keyed by the
// LAST model, so two chats could not disagree and switching models discarded the
// previous model's level. What is pinned here is that the level lands on the
// chat, that a bridgeless chat is no longer a 409, and that a refused live
// switch is not persisted as a level the session never took.

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/vibekit/internal/settings"
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
		Type:    vibekit.CmdSetEffort,
		ChatID:  chatID,
		Payload: payload,
	}
}

// setEffort drives the command with one double answering the bridge, the store
// and the bus. An empty configDir means the seed is not this case's subject: the
// per-model memory is skipped, and nothing touches a settings file.
func setEffort(t *testing.T, host hostDouble, configDir string, chatID vibekit.ChatID, level string) (any, error) {
	t.Helper()
	return CmdSetEffort(t.Context(), host, host, host, Workspace{ConfigDir: configDir},
		effortReq(t, chatID, level))
}

// recordingBus counts the events a command published, so a test can tell a seed
// write that landed from one that was refused.
type recordingBus struct {
	events []vibekit.ServerEvent
}

func (b *recordingBus) Broadcast(_ context.Context, evt vibekit.ServerEvent) {
	b.events = append(b.events, evt)
}

func (b *recordingBus) countOf(kind vibekit.EventType) int {
	n := 0
	for _, e := range b.events {
		if e.Type == kind {
			n++
		}
	}
	return n
}

func seedChatOnModel(t *testing.T, store ChatStore, id vibekit.ChatID, model string) {
	t.Helper()
	if _, err := store.Mutate(t.Context(), id, func(c *vibekit.Chat, _ bool) bool {
		c.Name = "a chat"
		c.Model = model
		return true
	}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, settings.Filename), []byte(body), 0o600); err != nil {
		t.Fatalf("seed config.json: %v", err)
	}
}

// effortSeeds reads the per-model memory back off disk, which is where the client
// used to write it and where the pill now reads it from.
func effortSeeds(t *testing.T, dir string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, settings.Filename))
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	var doc struct {
		ByModel map[string]string `json:"last_effort_by_model"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse config.json (%s): %v", data, err)
	}
	return doc.ByModel
}

// TestCmdSetEffort_ARefusedSwitchWritesNoSeed is the hazard the seed's move
// server-side closes: the client wrote it before dispatching, so a level the
// session REFUSED was still recorded as the user's preference for that model —
// they were told it did not apply and the app had already remembered it.
func TestCmdSetEffort_ARefusedSwitchWritesNoSeed(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"last_effort_by_model":{"opus-5":"low"}}`)
	store := testsupport.NewInMemoryChatStore()
	seedChatOnModel(t, store, "c1", "opus-5")
	b := &recordingBridge{callErr: errors.New("no such config option"), sessionID: "s"}
	host := newBridgeHost(store, b)
	bus := &recordingBus{}

	_, err := CmdSetEffort(t.Context(), host, host, bus, Workspace{ConfigDir: dir},
		effortReq(t, "c1", "max"))

	if statusOf(err) != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", statusOf(err))
	}
	if got := effortSeeds(t, dir)["opus-5"]; got != "low" {
		t.Errorf("seed for opus-5 = %q, want %q; a refused level was remembered as a preference", got, "low")
	}
	if n := bus.countOf(vibekit.EventSettingsUpdated); n != 0 {
		t.Errorf("settings_updated broadcasts = %d, want 0 after a refusal", n)
	}
}

// TestCmdSetEffort_SeedsOnlyThePickedModel pins the per-model scope of the write.
// One slot for the whole app made a pick on any chat retract every other model's
// remembered level, which is the user report the map shape exists for.
func TestCmdSetEffort_SeedsOnlyThePickedModel(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"theme":"dark","last_effort_by_model":{"opus-5":"low","gpt-luna":"high"}}`)
	store := testsupport.NewInMemoryChatStore()
	seedChatOnModel(t, store, "c1", "opus-5")
	host := newBridgeHost(store, &recordingBridge{result: map[string]any{}, sessionID: "s"})
	bus := &recordingBus{}

	_, err := CmdSetEffort(t.Context(), host, host, bus, Workspace{ConfigDir: dir},
		effortReq(t, "c1", "max"))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
	got := effortSeeds(t, dir)
	want := map[string]string{"opus-5": "max", "gpt-luna": "high"}
	if !maps.Equal(got, want) {
		t.Errorf("last_effort_by_model = %v, want %v", got, want)
	}
	// The sibling key proves the write merged rather than replacing the document.
	if theme := storedKey(t, dir, settings.KeyTheme); theme != `"dark"` {
		t.Errorf("theme = %s, want \"dark\"; the seed write replaced the document", theme)
	}
	if n := bus.countOf(vibekit.EventSettingsUpdated); n != 1 {
		t.Errorf("settings_updated broadcasts = %d, want 1 so the other devices converge", n)
	}
}

func storedKey(t *testing.T, dir, key string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, settings.Filename))
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse config.json: %v", err)
	}
	return string(doc[key])
}

// TestCmdSetEffort_AChatWithNoModelIsNotSeeded holds the one skip: an auto-created
// record has no model, and a seed under an empty key is one no reader resolves.
func TestCmdSetEffort_AChatWithNoModelIsNotSeeded(t *testing.T) {
	dir := t.TempDir()
	store := testsupport.NewInMemoryChatStore()
	host := &noBridgeDeps{storeDeps: &storeDeps{benchDeps: newBenchDeps(), store: store}}

	_, err := CmdSetEffort(t.Context(), host, host, host, Workspace{ConfigDir: dir},
		effortReq(t, "c-brand-new", "high"))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
	if _, err := os.Stat(filepath.Join(dir, settings.Filename)); !os.IsNotExist(err) {
		t.Errorf("config.json exists after a modelless pick (stat err %v), want no seed file at all", err)
	}
}

func TestCmdSetEffort_PersistsOnTheChatRecord(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedEmptyChat(t, store, "c1")
	b := &recordingBridge{result: map[string]any{}, sessionID: "sess-1"}
	host := newBridgeHost(store, b)

	_, err := setEffort(t, host, "", "c1", "high")

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
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
	host := newBridgeHost(store, &recordingBridge{result: map[string]any{}, sessionID: "s"})

	_, _ = setEffort(t, host, "", "c1", "low")
	_, _ = setEffort(t, host, "", "c2", "max")

	c1, _ := store.Get(t.Context(), "c1")
	c2, _ := store.Get(t.Context(), "c2")
	if c1.Effort != "low" || c2.Effort != "max" {
		t.Errorf("efforts = %q / %q, want low / max", c1.Effort, c2.Effort)
	}
}

// noBridgeDeps reports no live bridge, the state of every chat before its first
// prompt.
type noBridgeDeps struct{ *storeDeps }

func (d *noBridgeDeps) Bridge(vibekit.ChatID) Bridge { return nil }

// A bridgeless chat used to answer 409, which is why the client had a second
// path that wrote a GLOBAL setting instead — a different store and a different
// scope reached by the same click. The persisted level is enough now: spawnBridge
// applies it at session/new.
func TestCmdSetEffort_NoBridgeIsNotAConflict(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedEmptyChat(t, store, "c1")
	host := &noBridgeDeps{storeDeps: &storeDeps{benchDeps: newBenchDeps(), store: store}}

	_, err := setEffort(t, host, "", "c1", "medium")

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
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
	host := &noBridgeDeps{storeDeps: &storeDeps{benchDeps: newBenchDeps(), store: store}}

	_, err := setEffort(t, host, "", "c-brand-new", "xhigh")

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
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
	host := newBridgeHost(store, b)

	_, err := setEffort(t, host, "", "c1", "max")

	if statusOf(err) != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", statusOf(err))
	}
	c, _ := store.Get(t.Context(), "c1")
	if c.Effort != "" {
		t.Errorf("Effort = %q, want it unset after a refused switch", c.Effort)
	}
}

func TestCmdSetEffort_RejectsAMalformedLevel(t *testing.T) {
	// Shape only: uppercase, spaces and a leading digit are not tier ids. The
	// vocabulary itself is per model and KAS's to judge — see the "none" test.
	for _, level := range []string{"", "LOW", "x high", "9high"} {
		t.Run(level, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			seedEmptyChat(t, store, "c1")
			b := &recordingBridge{result: map[string]any{}, sessionID: "s"}
			host := newBridgeHost(store, b)

			_, err := setEffort(t, host, "", "c1", level)

			if statusOf(err) != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", statusOf(err))
			}
			if b.callCount != 0 {
				t.Error("a malformed level reached the bridge")
			}
		})
	}
}

func TestCmdSetEffort_AcceptsATierOutsideTheConstants(t *testing.T) {
	// gpt-luna ships a "none" tier the old closed five-member set rejected at
	// this boundary — the user-visible "thinking: none throws an error". The
	// catalog is upstream-owned, so an unknown-but-well-formed tier flows and
	// KAS (fail-fast on the live session) stays the authority.
	store := testsupport.NewInMemoryChatStore()
	seedEmptyChat(t, store, "c1")
	b := &recordingBridge{result: map[string]any{}, sessionID: "s"}
	host := newBridgeHost(store, b)

	_, err := setEffort(t, host, "", "c1", "none")

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (err %v)", statusOf(err), err)
	}
	if b.callCount == 0 {
		t.Error("the level never reached the live session")
	}
	c, _ := store.Get(t.Context(), "c1")
	if c.Effort != "none" {
		t.Errorf("Effort = %q, want %q persisted on the chat", c.Effort, "none")
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
