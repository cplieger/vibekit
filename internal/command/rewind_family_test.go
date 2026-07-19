package command

// Tests for the rewind-family transitions (P7): CmdDeleteChat's
// truthful cascade response and CmdPromoteRewindChat's store-backed
// promote with sentinel-mapped errors.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/testsupport"
)

// familyDeps is benchDeps with a real (in-memory or overridden) chat
// store and a record of CleanupChatState calls.
type familyDeps struct {
	*benchDeps
	store   api.ChatStore
	cleaned []api.ChatID
}

func (d *familyDeps) ChatStore() api.ChatStore { return d.store }
func (d *familyDeps) CleanupChatState(_ context.Context, id api.ChatID) {
	d.cleaned = append(d.cleaned, id)
}

// failingFamilyStore injects DeleteFamily partial failure.
type failingFamilyStore struct {
	testsupport.NopChatStore
	failed []api.ChatID
	err    error
}

func (s *failingFamilyStore) DeleteFamily(_ context.Context, _ api.ChatID, prepare func(api.ChatID)) ([]api.ChatID, error) {
	if prepare != nil {
		prepare("whatever")
	}
	return s.failed, s.err
}

func TestCmdDeleteChat_ReportsSurvivingChildren(t *testing.T) {
	deps := &familyDeps{
		benchDeps: newBenchDeps(),
		store:     &failingFamilyStore{failed: []api.ChatID{"c-child"}},
	}
	d := New(deps)
	w := httptest.NewRecorder()
	CmdDeleteChat(d, context.Background(), w, &api.ClientCommand{
		Type: api.CmdDeleteChat, RequestID: "r1", ChatID: "c-parent",
	})

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200 (parent deleted; failure is child-scoped)", w.Code)
	}
	var resp struct {
		OK             bool     `json:"ok"`
		FailedChildren []string `json:"failed_children"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v; body=%s", err, w.Body.String())
	}
	if len(resp.FailedChildren) != 1 || resp.FailedChildren[0] != "c-child" {
		t.Errorf("failed_children = %v, want [c-child] (the old code reported unconditional OK)", resp.FailedChildren)
	}
}

func TestCmdDeleteChat_CleanupRunsViaPrepareHook(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedChat := func(id, parent api.ChatID) {
		_ = store.Mutate(context.Background(), id, func(c *api.Chat, _ bool) bool {
			c.Name = "x"
			c.ParentChatID = parent
			return true
		})
	}
	seedChat("p1", "")
	seedChat("p1-r1", "p1")

	deps := &familyDeps{benchDeps: newBenchDeps(), store: store}
	d := New(deps)
	w := httptest.NewRecorder()
	CmdDeleteChat(d, context.Background(), w, &api.ClientCommand{
		Type: api.CmdDeleteChat, RequestID: "r1", ChatID: "p1",
	})

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	// Side-effect teardown ran per record, child first, parent last —
	// the same order the records were deleted in.
	if len(deps.cleaned) != 2 || deps.cleaned[0] != "p1-r1" || deps.cleaned[1] != "p1" {
		t.Errorf("cleanup order = %v, want [p1-r1 p1]", deps.cleaned)
	}
	if _, ok := store.Get(context.Background(), "p1"); ok {
		t.Error("parent survived")
	}
	if _, ok := store.Get(context.Background(), "p1-r1"); ok {
		t.Error("child survived")
	}
}

func TestCmdPromoteRewindChat_PromotesAndDeletesParent(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	_ = store.Mutate(context.Background(), "par", func(c *api.Chat, _ bool) bool { c.Name = "p"; return true })
	_ = store.Mutate(context.Background(), "rew", func(c *api.Chat, _ bool) bool {
		c.Name = "r"
		c.ParentChatID = "par"
		c.RewindFromTurn = 3
		return true
	})

	deps := &familyDeps{benchDeps: newBenchDeps(), store: store}
	d := New(deps)
	w := httptest.NewRecorder()
	CmdPromoteRewindChat(d, context.Background(), w, &api.ClientCommand{
		Type: api.CmdPromoteRewindChat, RequestID: "r1", ChatID: "rew",
	})

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	child, ok := store.Get(context.Background(), "rew")
	if !ok || child.ParentChatID != "" || child.RewindFromTurn != 0 {
		t.Errorf("promoted chat = %+v (ok=%v), want cleared linkage", child, ok)
	}
	if _, ok := store.Get(context.Background(), "par"); ok {
		t.Error("parent survived promote")
	}
	// Parent side effects were torn down before its record deletion.
	if len(deps.cleaned) != 1 || deps.cleaned[0] != "par" {
		t.Errorf("cleaned = %v, want [par]", deps.cleaned)
	}
}

func TestCmdPromoteRewindChat_ErrorMapping(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	_ = store.Mutate(context.Background(), "plain", func(c *api.Chat, _ bool) bool { c.Name = "n"; return true })

	deps := &familyDeps{benchDeps: newBenchDeps(), store: store}
	d := New(deps)

	cases := []struct {
		chatID api.ChatID
		want   int
	}{
		{"ghost", 404}, // ErrChatNotFound
		{"plain", 400}, // ErrNotRewind
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		CmdPromoteRewindChat(d, context.Background(), w, &api.ClientCommand{
			Type: api.CmdPromoteRewindChat, RequestID: "r1", ChatID: tc.chatID,
		})
		if w.Code != tc.want {
			t.Errorf("promote %s: status = %d, want %d", tc.chatID, w.Code, tc.want)
		}
	}
	// No parent was deleted on either error path.
	if len(deps.cleaned) != 0 {
		t.Errorf("cleanup ran on error paths: %v", deps.cleaned)
	}
}
