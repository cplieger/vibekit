package agent

// The acceptance test for a run's tab: nothing is faked below the runtime, and the tab
// assertions read the PERSISTED tabs.json rather than the in-memory set, because the
// document is the set every device projects.

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

// persistedTabs is the collection as it sits on disk, the set every device projects.
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

// subjectFor finds the persisted tab for one (kind, ref), which is what names a subject.
func subjectFor(doc persistedTabs, kind vibekit.TabKind, ref string) (vibekit.TabSubject, bool) {
	for _, tab := range doc.Tabs {
		if tab.Kind == kind && tab.Ref == ref {
			return tab, true
		}
	}
	return vibekit.TabSubject{}, false
}

// newTabbedRuntime returns the temp config dir too, so a test can read the document.
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

// openChatTab creates a chat through the coordinator, as a New chat gesture does. The
// returned subject's Ref is the chat id and its ID is what a run tab nests under.
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

// TestAcceptance_ADeepLinkOpensTheRunAsAChildOfItsChat drives an `open_tab` carrying a
// workflow id and NOTHING else — no store, no frames, no chat id on that client. The
// subject is the command boundary, so payload validation and the parent fill are both
// in the path.
func TestAcceptance_ADeepLinkOpensTheRunAsAChildOfItsChat(t *testing.T) {
	h, dir := newTabbedRuntime(t)
	chatTab := openChatTab(t, h, "op-chat")
	// The lease names the launching chat, the fact the deep link cannot carry.
	h.translateACPEvent(vibekit.ChatID(chatTab.Ref), runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_deeplink", "workflowName": "publish-pr",
	}))

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

// TestAcceptance_AParentlessRunKeepsItsLeaseWithNoChat is the lease half of the deep
// link's precondition, for the population that has no chat to nest under: a manual or
// scheduled run's frames are workspace-global and it hosts its own bridge under the
// synthetic `run:` id, so its lease must exist and must name no chat.
func TestAcceptance_AParentlessRunKeepsItsLeaseWithNoChat(t *testing.T) {
	h, _ := newTabbedRuntime(t)
	openChatTab(t, h, "op-chat")

	// The workspace-global lifecycle frame a launch verb's run produces.
	h.translateACPEvent("", runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_scheduled", "workflowName": "nightly",
	}))
	// And a frame arriving on the run's OWN bridge, whose chat id is synthetic.
	h.translateACPEvent(runChatID("wf_scheduled"), runNotif(methodWFNodeStart, map[string]any{
		"workflowId": "wf_scheduled", "nodeId": "coder",
	}))

	// The orphan sweep and the deadline read it, and fillRunParent answers off its chat id.
	if l, held := h.runs.lease("wf_scheduled"); !held {
		t.Error("the parentless run lost its lease, so nothing bounds or sweeps it")
	} else if l.ChatID != "" {
		t.Errorf("lease chat id = %q, want empty for a parentless run", l.ChatID)
	}
}
