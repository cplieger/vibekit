package agent

// The acceptance test for a run's tab, end to end over the REAL stores.
//
// Nothing is faked below the runtime: a real tabs.Store on a temp dir, the real
// membership coordinator, the real run surface, the real lease store, and the
// assertion reads the PERSISTED tabs.json rather than the in-memory set. That is
// what makes it the acceptance test rather than a third unit test — the two
// symptoms were both "the document ends up wrong", and only the document can say
// whether it does.
//
// The frame is what a run launched from a chat by the agent's run_workflow tool
// produces: KAS creates and invokes the run itself, so `run_start` on the
// launching chat's bridge is the first thing vibekit sees of it.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/vibekit/internal/command"
	"github.com/cplieger/vibekit/internal/tabs"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// persistedTabs is the collection as it sits on disk, which is the server-owned
// set every device projects.
type persistedTabs struct {
	Tabs    []vibekit.TabSubject `json:"tabs"`
	Version uint64               `json:"version"`
}

func readTabsFile(t *testing.T, dir string) persistedTabs {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "tabs.json"))
	if err != nil {
		t.Fatalf("read tabs.json: %v", err)
	}
	var doc persistedTabs
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse tabs.json: %v", err)
	}
	return doc
}

// subjectFor finds the persisted tab for one (kind, ref), which is what names a
// subject.
func subjectFor(doc persistedTabs, kind vibekit.TabKind, ref string) (vibekit.TabSubject, bool) {
	for _, tab := range doc.Tabs {
		if tab.Kind == kind && tab.Ref == ref {
			return tab, true
		}
	}
	return vibekit.TabSubject{}, false
}

// newTabbedRuntime builds a runtime over a real tab store in a temp config dir,
// and returns that dir so a test can read the document back.
func newTabbedRuntime(t *testing.T) (*Runtime, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := tabs.NewStore(dir)
	if err != nil {
		t.Fatalf("tabs.NewStore: %v", err)
	}
	cs := newFakeChatStore()
	br := newFakeBridge()
	h := New(context.Background(), t.TempDir(), func() ACPBridge { return br }, cs,
		WithTabs(st), WithConfigDir(dir))
	cs.Bus = h
	h.mcpRegistry.SignalReady()
	t.Cleanup(func() { shutdownHub(t, h) })
	return h, dir
}

// openChatTab creates a chat through the coordinator, which is what a New chat
// gesture does: the record and its tab, as one operation. The returned subject's
// Ref is the chat id and its ID is what a run tab must nest under.
func openChatTab(t *testing.T, h *Runtime, opID string) vibekit.TabSubject {
	t.Helper()
	opened, err := h.Membership().CreateChatAndOpen(t.Context(), command.ChatCreate{
		OpID: opID,
		Init: func(c *vibekit.Chat) { c.Name = vibekit.DefaultChatName },
	})
	if err != nil {
		t.Fatalf("CreateChatAndOpen: %v", err)
	}
	return opened.Subject
}

// TestAcceptance_ARunLaunchedFromAChatGetsASubTabInThePersistedSet is symptom A
// and symptom B in one document read: the run's tab EXISTS, and its parent is
// exactly the launching chat's tab id.
//
// Before the fix, both facts depended on a browser: the open was an SSE reaction
// with two per-page-load guards, and the parent came from a client-side map that a
// reload emptied. So the set could hold no run tab at all, or hold one at top
// level for good, and which one differed per device.
func TestAcceptance_ARunLaunchedFromAChatGetsASubTabInThePersistedSet(t *testing.T) {
	h, dir := newTabbedRuntime(t)
	chatTab := openChatTab(t, h, "op-chat")
	before := readTabsFile(t, dir)

	// What the agent's run_workflow tool produces: KAS creates and invokes the run
	// itself, so this frame on the launching chat's bridge is vibekit's first sight
	// of it.
	h.translateACPEvent(vibekit.ChatID(chatTab.Ref), runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_acceptance", "workflowName": "publish-pr",
	}))

	doc := readTabsFile(t, dir)
	runTab, ok := subjectFor(doc, vibekit.TabKindRun, "wf_acceptance")
	if !ok {
		t.Fatalf("the persisted set holds no run tab for wf_acceptance: %+v", doc.Tabs)
	}
	if runTab.Parent != chatTab.ID {
		t.Errorf("run tab parent = %q, want the launching chat's tab %q", runTab.Parent, chatTab.ID)
	}
	if runTab.Owns {
		t.Error("owns = true: a run tab is a view, so its × must stop nothing")
	}
	if doc.Version <= before.Version {
		t.Errorf("collection version stayed at %d; a committed open must advance it", doc.Version)
	}
}

// TestAcceptance_TheRunTabSurvivesAReloadOfTheCollection is the case that was
// broken and the reason the open moved server-side at all: the tab is in the
// SERVER's set, so a fresh boot's `listTabs()` restores it with no client state
// and no new frame.
func TestAcceptance_TheRunTabSurvivesAReloadOfTheCollection(t *testing.T) {
	h, dir := newTabbedRuntime(t)
	chatTab := openChatTab(t, h, "op-chat")
	h.translateACPEvent(vibekit.ChatID(chatTab.Ref), runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_reload", "workflowName": "publish-pr",
	}))

	// A second process reading the same volume, which is what a reload projects.
	reopened, err := tabs.NewStore(dir)
	if err != nil {
		t.Fatalf("reopen the tab store: %v", err)
	}
	open, _ := reopened.List()
	var found bool
	for _, tab := range open {
		if tab.Kind == vibekit.TabKindRun && tab.Ref == "wf_reload" {
			found = true
			if tab.Parent != chatTab.ID {
				t.Errorf("the reloaded run tab's parent = %q, want %q", tab.Parent, chatTab.ID)
			}
		}
	}
	if !found {
		t.Errorf("the reloaded set holds no run tab: %+v", open)
	}
}

// TestAcceptance_AReadersCloseOfTheRunTabIsFinal is what the durable offer flag
// buys, and it is asserted across a RESTART because that is the half an in-memory
// set fails: `run_start` re-fires on every resume.
func TestAcceptance_AReadersCloseOfTheRunTabIsFinal(t *testing.T) {
	h, dir := newTabbedRuntime(t)
	chatTab := openChatTab(t, h, "op-chat")
	h.translateACPEvent(vibekit.ChatID(chatTab.Ref), runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_closed", "workflowName": "publish-pr",
	}))
	runTab, ok := subjectFor(readTabsFile(t, dir), vibekit.TabKindRun, "wf_closed")
	if !ok {
		t.Fatal("the run was never offered a tab, so there is nothing to close")
	}

	// The reader closes it.
	if _, _, err := h.Membership().CloseTab(t.Context(), runTab.ID, "op-close"); err != nil {
		t.Fatalf("CloseTab: %v", err)
	}

	// The run resumes, and the frame that re-fires must not bring the tab back.
	h.translateACPEvent(vibekit.ChatID(chatTab.Ref), runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_closed", "workflowName": "publish-pr",
	}))
	// Its first step reports, which is the offer's retry.
	h.translateACPEvent(vibekit.ChatID(chatTab.Ref), runNotif(methodWFNodeStart, map[string]any{
		"workflowId": "wf_closed", "nodeId": "coder",
	}))

	if _, back := subjectFor(readTabsFile(t, dir), vibekit.TabKindRun, "wf_closed"); back {
		t.Error("the closed run tab came back; a reader's close must stay final")
	}
}

// TestAcceptance_ADeepLinkOpensTheRunAsAChildOfItsChat is symptom B as the user
// reported it: opening the run in a separate browser tab, which is an `open_tab`
// carrying a workflow id and NOTHING else — no store, no frames, no chat id
// anywhere on that client.
//
// The command boundary is the subject rather than the coordinator's method, so the
// payload validation and the parent fill are both in the path.
func TestAcceptance_ADeepLinkOpensTheRunAsAChildOfItsChat(t *testing.T) {
	h, dir := newTabbedRuntime(t)
	chatTab := openChatTab(t, h, "op-chat")
	// The run exists and its lease names the launching chat, which is the fact the
	// deep link cannot carry.
	h.translateACPEvent(vibekit.ChatID(chatTab.Ref), runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_deeplink", "workflowName": "publish-pr",
	}))
	// The reader closes the offered tab, so the deep link is a fresh open rather
	// than an activation of the one already there.
	offered, ok := subjectFor(readTabsFile(t, dir), vibekit.TabKindRun, "wf_deeplink")
	if !ok {
		t.Fatal("the run was never offered a tab")
	}
	if _, _, err := h.Membership().CloseTab(t.Context(), offered.ID, "op-close"); err != nil {
		t.Fatalf("CloseTab: %v", err)
	}

	rec := postCmd(t, h, vibekit.ClientCommand{
		Type:    vibekit.CmdOpenTab,
		Payload: json.RawMessage(`{"kind":"run","ref":"wf_deeplink","op_id":"opdeeplink"}`),
	})
	if rec.Code != 200 {
		t.Fatalf("open_tab = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	reopened, ok := subjectFor(readTabsFile(t, dir), vibekit.TabKindRun, "wf_deeplink")
	if !ok {
		t.Fatal("the deep link opened no run tab")
	}
	if reopened.Parent != chatTab.ID {
		t.Errorf("parent = %q, want the launching chat's tab %q — a deep link carries no chat id, "+
			"so the coordinator is the only thing that can answer this",
			reopened.Parent, chatTab.ID)
	}
}

// TestAcceptance_AParentlessRunIsOfferedNoTab is the regression guard on the
// deliberately parentless case: a manual or scheduled run's lifecycle frames are
// workspace-global and it hosts its own bridge under the synthetic `run:` id, so
// nothing here may put it in the set or in a chat's subtree.
func TestAcceptance_AParentlessRunIsOfferedNoTab(t *testing.T) {
	h, dir := newTabbedRuntime(t)
	openChatTab(t, h, "op-chat")

	// The workspace-global lifecycle frame a launch verb's run produces.
	h.translateACPEvent("", runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_scheduled", "workflowName": "nightly",
	}))
	// And a frame arriving on the run's OWN bridge, whose chat id is synthetic.
	h.translateACPEvent(runChatID("wf_scheduled"), runNotif(methodWFNodeStart, map[string]any{
		"workflowId": "wf_scheduled", "nodeId": "coder",
	}))

	doc := readTabsFile(t, dir)
	if _, ok := subjectFor(doc, vibekit.TabKindRun, "wf_scheduled"); ok {
		t.Errorf("a parentless run was offered a tab: %+v", doc.Tabs)
	}
	// Its lease still exists, which is what the orphan sweep and the deadline read.
	if l, held := h.runs.lease("wf_scheduled"); !held {
		t.Error("the parentless run lost its lease, so nothing bounds or sweeps it")
	} else if l.ChatID != "" {
		t.Errorf("lease chat id = %q, want empty for a parentless run", l.ChatID)
	}
}
