package command

// The certification rule on the three command-path emitters: a frame carries the
// stamp of the projection it completes, the broadcasts run AFTER the save, and one
// Mutate that emits two chat-projection frames stamps only the last.

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/testsupport"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// storeAndBus is an in-memory store whose own broadcasts (the header frame) land
// on the same bus the command's broadcasts do, so a test sees the whole sequence
// one Mutate produces in wire order.
func storeAndBus(t *testing.T) (*testsupport.InMemoryChatStore, *capturingBus, *storeDeps) {
	t.Helper()
	store := testsupport.NewInMemoryChatStore()
	bus := &capturingBus{}
	store.Bus = bus
	return store, bus, &storeDeps{benchDeps: newBenchDeps(), store: store}
}

func typesOf(evts []vibekit.ServerEvent) []vibekit.EventType {
	out := make([]vibekit.EventType, len(evts))
	for i, e := range evts {
		out[i] = e.Type
	}
	return out
}

func chatStamps(evts []vibekit.ServerEvent) []vibekit.ServerEvent {
	var out []vibekit.ServerEvent
	for _, e := range evts {
		if e.Subject != nil && e.Subject.Kind == "chat" {
			out = append(out, e)
		}
	}
	return out
}

func TestAppendUserMessage_HeaderFrameFirstThenTheStampedTranscriptFrame(t *testing.T) {
	store, bus, deps := storeAndBus(t)
	seedEmptyChat(t, store, "c1")
	bus.events = nil

	err := appendUserMessage(t.Context(), deps, bus, Workspace{Dir: t.TempDir(), ConfigDir: t.TempDir()}, "c1", &vibekit.PromptCommand{
		Text: "typed and sent without pausing", MessageID: "m-1",
	})
	if err != nil {
		t.Fatalf("appendUserMessage: %v", err)
	}

	got := typesOf(bus.events)
	if len(got) != 2 || got[0] != vibekit.EventChatUpdated || got[1] != vibekit.EventMessageAppended {
		t.Fatalf("broadcast order = %v, want [chat_updated message_appended]: the header frame comes from inside the save, the transcript frame after it", got)
	}
	stamped := chatStamps(bus.events)
	if len(stamped) != 1 || stamped[0].Type != vibekit.EventMessageAppended {
		t.Fatalf("chat-stamped frames = %v, want exactly message_appended", typesOf(stamped))
	}
	if stamped[0].Subject.Ref != "c1" || stamped[0].Subject.Version == "" {
		t.Errorf("message_appended Subject = %+v, want {chat c1 <version>}", *stamped[0].Subject)
	}
}

// The last-frame rule: with a draft to clear, draft_changed follows message_appended
// and is the ONLY chat-stamped frame. A client that received message_appended
// stamped and lost the stream before draft_changed would otherwise hold the sent
// text in its composer at a version the digest calls unchanged.
func TestAppendUserMessage_WithADraftOnlyDraftChangedCarriesTheChatStamp(t *testing.T) {
	store, bus, deps := storeAndBus(t)
	seedEmptyChat(t, store, "c1")
	if _, err := store.SetDraft(t.Context(), "c1", "the message about to be sent"); err != nil {
		t.Fatalf("SetDraft: %v", err)
	}
	bus.events = nil

	err := appendUserMessage(t.Context(), deps, bus, Workspace{Dir: t.TempDir(), ConfigDir: t.TempDir()}, "c1", &vibekit.PromptCommand{
		Text: "the message about to be sent", MessageID: "m-1",
	})
	if err != nil {
		t.Fatalf("appendUserMessage: %v", err)
	}

	got := typesOf(bus.events)
	want := []vibekit.EventType{vibekit.EventChatUpdated, vibekit.EventMessageAppended, vibekit.EventDraftChanged}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("broadcast order = %v, want %v", got, want)
	}
	if bus.events[1].Subject != nil {
		t.Errorf("message_appended carries %+v; with a draft_changed following it must carry no Subject", *bus.events[1].Subject)
	}
	stamped := chatStamps(bus.events)
	if len(stamped) != 1 || stamped[0].Type != vibekit.EventDraftChanged {
		t.Fatalf("chat-stamped frames = %v, want exactly draft_changed", typesOf(stamped))
	}
	if stamped[0].Subject.Ref != "c1" || stamped[0].Subject.Version == "" {
		t.Errorf("draft_changed Subject = %+v, want {chat c1 <version>}", *stamped[0].Subject)
	}
	if hdr := bus.events[0].Subject; hdr != nil && hdr.Kind == "chat" {
		t.Errorf("the header frame carries a chat stamp %+v; it completes the chats projection, not the transcript", *hdr)
	}
}

func TestAppendUserMessage_ARetriedPromptBroadcastsNothing(t *testing.T) {
	store, bus, deps := storeAndBus(t)
	seedEmptyChat(t, store, "c1")
	ws := Workspace{Dir: t.TempDir(), ConfigDir: t.TempDir()}
	p := &vibekit.PromptCommand{Text: "once", MessageID: "m-1"}
	if err := appendUserMessage(t.Context(), deps, bus, ws, "c1", p); err != nil {
		t.Fatalf("first appendUserMessage: %v", err)
	}
	bus.events = nil

	if err := appendUserMessage(t.Context(), deps, bus, ws, "c1", p); err != nil {
		t.Fatalf("retried appendUserMessage: %v", err)
	}
	if len(bus.events) != 0 {
		t.Errorf("a retried prompt broadcast %v, want nothing: the mutator declined and no version was minted", typesOf(bus.events))
	}
}

func TestAppendShellUserMessage_StampsTheTranscriptFrameAfterTheSave(t *testing.T) {
	store, bus, deps := storeAndBus(t)
	seedEmptyChat(t, store, "c1")
	bus.events = nil
	msg := &vibekit.Message{ID: "m-1", Role: vibekit.RoleUser, Content: "!ls"}

	persisted, err := appendShellUserMessage(t.Context(), deps, bus, "c1", msg, "!ls")
	if err != nil || !persisted {
		t.Fatalf("appendShellUserMessage = (%v, %v), want (true, nil)", persisted, err)
	}
	got := typesOf(bus.events)
	if len(got) != 2 || got[0] != vibekit.EventChatUpdated || got[1] != vibekit.EventMessageAppended {
		t.Fatalf("broadcast order = %v, want [chat_updated message_appended]", got)
	}
	stamped := chatStamps(bus.events)
	if len(stamped) != 1 || stamped[0].Type != vibekit.EventMessageAppended || stamped[0].Subject.Ref != "c1" || stamped[0].Subject.Version == "" {
		t.Errorf("chat-stamped frames = %v, want exactly message_appended stamped {chat c1 <version>}", typesOf(stamped))
	}
}

// recordingRoles is the shell interception's role set over a real in-memory store
// with a capturing bus, so the assistant message's frames are the store's own.
type recordingRoles struct {
	*storeDeps
	bus *capturingBus
}

func (r *recordingRoles) Broadcast(ctx context.Context, evt vibekit.ServerEvent) {
	r.bus.Broadcast(ctx, evt)
}

// TestHandleShellInterception_OneStampedMessageAppendedPerInterception pins the
// deletion of the duplicate: AppendMessage's own broadcast is the one
// message_appended for the assistant message, stamped from the save, and nothing
// broadcasts the same message a second time with no version source.
func TestHandleShellInterception_OneStampedMessageAppendedPerInterception(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("sh not available: %v", err)
	}
	store, bus, deps := storeAndBus(t)
	seedEmptyChat(t, store, "c1")
	roles := &recordingRoles{storeDeps: deps, bus: bus}
	pr := promptRolesOf(deps)
	pr.chats = roles
	pr.bus = roles
	bus.events = nil

	if _, err := HandleShellInterception(t.Context(), pr, &vibekit.ClientCommand{Type: "prompt", ChatID: "c1"}, &vibekit.PromptCommand{
		Text: "!echo shell-output", MessageID: "m-1",
	}); err != nil {
		t.Fatalf("HandleShellInterception: %v", err)
	}

	var assistantFrames []vibekit.ServerEvent
	for _, e := range bus.events {
		if e.Type != vibekit.EventMessageAppended {
			continue
		}
		msg, ok := e.Payload.(*vibekit.Message)
		if ok && msg.Role == vibekit.RoleAssistant && strings.Contains(msg.Content, "shell-output") {
			assistantFrames = append(assistantFrames, e)
		}
	}
	if len(assistantFrames) != 1 {
		t.Fatalf("the assistant message was broadcast %d times, want exactly once (the store's own frame)", len(assistantFrames))
	}
	if s := assistantFrames[0].Subject; s == nil || s.Kind != "chat" || s.Ref != "c1" || s.Version == "" {
		t.Errorf("the assistant message_appended Subject = %+v, want {chat c1 <version>}", s)
	}
}

func TestBroadcastComposer_StampsFromTheStateVersion(t *testing.T) {
	bus := &capturingBus{}
	broadcastComposer(t.Context(), bus, "c1", &vibekit.ComposerState{Text: "draft", Version: "42"})
	if len(bus.events) != 1 {
		t.Fatalf("broadcast %d frames, want 1", len(bus.events))
	}
	want := vibekit.SubjectStamp{Kind: "chat", Ref: "c1", Version: "42"}
	if bus.events[0].Type != vibekit.EventDraftChanged || bus.events[0].Subject == nil || *bus.events[0].Subject != want {
		t.Errorf("draft_changed = %s with Subject %+v, want Subject %+v", bus.events[0].Type, bus.events[0].Subject, want)
	}
}

// TestCmdSetDraft_DraftChangedCarriesTheStoresVersion drives the command itself so
// the stamp is the one the store minted under its lock, not one the command read.
func TestCmdSetDraft_DraftChangedCarriesTheStoresVersion(t *testing.T) {
	store, bus, deps := storeAndBus(t)
	seedEmptyChat(t, store, "c1")
	bus.events = nil

	if _, err := CmdSetDraft(t.Context(), deps, bus, draftReq(t, "c1", "half a thought")); err != nil {
		t.Fatalf("CmdSetDraft: %v", err)
	}
	if len(bus.events) != 1 || bus.events[0].Type != vibekit.EventDraftChanged {
		t.Fatalf("broadcasts = %v, want exactly draft_changed", typesOf(bus.events))
	}
	state, err := store.SetDraft(t.Context(), "c1", "the next thought")
	if err != nil || state == nil {
		t.Fatalf("SetDraft: %v", err)
	}
	got := bus.events[0].Subject
	if got == nil || got.Kind != "chat" || got.Ref != "c1" || got.Version == "" || got.Version == state.Version {
		t.Errorf("draft_changed Subject = %+v, want {chat c1 <the version of the command's own write>}, distinct from the later write's %q", got, state.Version)
	}
}
