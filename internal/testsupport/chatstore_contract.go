package testsupport

import (
	"context"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// ChatStoreContract is the subject of ChatStoreContractTest: the 8 methods of a
// chat store this suite exercises. There is no shared ChatStore interface any
// more — each consumer declares 1 to 6 methods of the 11 — and a contract suite
// has no business naming a method it does not assert on.
//
// SetDraft is absent because the suite does not exercise it, and that is a GAP
// rather than a decision: SetDraft has the subtlest contract of the eleven (no
// UpdatedAt stamp, no broadcast, a silent no-op on a chat that does not exist)
// and no shared case pins any of it. RegisterRoutes is absent because a store's
// HTTP mounting is not a storage behaviour.
//
// UpsertTurnPlan is here because its turn-boundary rule is derived rather than
// remembered — the row is found by walking back to the first user message — so a
// fake that appends instead would let every plan test pass while the real store's
// one-row-per-turn behaviour went unpinned.
type ChatStoreContract interface {
	Get(ctx context.Context, id vibekit.ChatID) (*vibekit.Chat, bool)
	List(ctx context.Context) []vibekit.ChatHeader
	BuildHistory(ctx context.Context, id vibekit.ChatID) string
	Mutate(ctx context.Context, id vibekit.ChatID, mutate func(c *vibekit.Chat, exists bool) bool) error
	Delete(ctx context.Context, id vibekit.ChatID) error
	AppendMessage(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.Message) error
	UpdateMessage(ctx context.Context, chatID vibekit.ChatID, msgID string, mutate func(*vibekit.Message)) error
	UpsertTurnPlan(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.Message) error
}

// contractChatName is the name every case gives the chat it opens; the suite
// asserts on messages, never on the name.
const contractChatName = "test"

// ChatStoreContractTest exercises the behavioral expectations of any chat store
// implementation. Run against both fakes and real implementations to catch
// drift. Each behavior lives in its own helper so the
// suite stays flat; this is the dispatcher.
func ChatStoreContractTest(t *testing.T, newStore func(t *testing.T) ChatStoreContract) {
	t.Helper()

	t.Run("Get_missing_returns_false", func(t *testing.T) { testGetMissingReturnsFalse(t, newStore(t)) })
	t.Run("Get_returns_an_independent_copy", func(t *testing.T) { testGetReturnsIndependentCopy(t, newStore(t)) })
	t.Run("Mutate_creates_new_chat", func(t *testing.T) { testMutateCreatesNewChat(t, newStore(t)) })
	t.Run("Mutate_updates_existing_chat", func(t *testing.T) { testMutateUpdatesExistingChat(t, newStore(t)) })
	t.Run("Mutate_noop_when_false_returned", func(t *testing.T) { testMutateNoopWhenFalseReturned(t, newStore(t)) })
	t.Run("Delete_removes_chat", func(t *testing.T) { testDeleteRemovesChat(t, newStore(t)) })
	t.Run("List_returns_created_chats", func(t *testing.T) { testListReturnsCreatedChats(t, newStore(t)) })
	t.Run("BuildHistory_empty_for_missing", func(t *testing.T) { testBuildHistoryEmptyForMissing(t, newStore(t)) })
	t.Run("AppendMessage_adds_to_chat", func(t *testing.T) { testAppendMessageAddsToChat(t, newStore(t)) })
	t.Run("UpdateMessage_mutates_in_place", func(t *testing.T) { testUpdateMessageMutatesInPlace(t, newStore(t)) })
	t.Run("UpsertTurnPlan_appends_when_turn_has_none", func(t *testing.T) {
		testUpsertTurnPlanAppendsWhenTurnHasNone(t, newStore(t))
	})
	t.Run("UpsertTurnPlan_overwrites_within_one_turn", func(t *testing.T) {
		testUpsertTurnPlanOverwritesWithinOneTurn(t, newStore(t))
	})
	t.Run("UpsertTurnPlan_starts_a_row_per_turn", func(t *testing.T) {
		testUpsertTurnPlanStartsARowPerTurn(t, newStore(t))
	})
}

func testGetMissingReturnsFalse(t *testing.T, s ChatStoreContract) {
	t.Helper()
	_, ok := s.Get(context.Background(), "nonexistent")
	if ok {
		t.Error("Get returned true for missing chat")
	}
}

// testGetReturnsIndependentCopy pins the property that makes "Mutate is the only
// write path" true: a caller holding a chat Get handed it cannot reach the stored
// one through it.
//
// The real store gets this for free — Get decodes the file, so the value is new
// bytes — and the two fakes did NOT, because `clone := *c` copies a struct and
// shares every slice inside it. Chat has seven slice fields and Message has more,
// so a caller could edit a stored message's Content, or its Blocks, with no
// Mutate anywhere. Nothing here asserted it, so both fakes were more permissive
// than the thing they stand in for, which is the direction that makes a test pass
// while production breaks.
//
// Three depths, because they fail independently: the slice header, an element,
// and an element's own slice.
func testGetReturnsIndependentCopy(t *testing.T, s ChatStoreContract) {
	t.Helper()
	const tampered = "tampered"
	ctx := context.Background()
	_ = s.Mutate(ctx, "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "keep"
		c.Messages = []vibekit.Message{{
			ID: "m1", Role: "assistant", Content: "original",
			Blocks: []vibekit.Block{{Type: vibekit.BlockText, Text: "block-original"}},
		}}
		return true
	})

	got, ok := s.Get(ctx, "c1")
	if !ok {
		t.Fatal("Get returned false for a chat that was just created")
	}
	if len(got.Messages) != 1 || len(got.Messages[0].Blocks) != 1 {
		t.Fatalf("fixture did not store what this case mutates: %+v", got.Messages)
	}
	got.Name = tampered
	got.Messages[0].Content = tampered
	got.Messages[0].Blocks[0].Text = tampered
	got.Messages = append(got.Messages, vibekit.Message{ID: "m2"})

	after, _ := s.Get(ctx, "c1")
	if after.Name != "keep" {
		t.Errorf("Chat.Name = %q after a caller edited a Get result, want %q", after.Name, "keep")
	}
	if len(after.Messages) != 1 {
		t.Fatalf("Messages len = %d after a caller appended to a Get result, want 1", len(after.Messages))
	}
	if after.Messages[0].Content != "original" {
		t.Errorf("Messages[0].Content = %q after a caller edited a Get result, want %q",
			after.Messages[0].Content, "original")
	}
	if len(after.Messages[0].Blocks) != 1 || after.Messages[0].Blocks[0].Text != "block-original" {
		t.Errorf("Messages[0].Blocks = %+v after a caller edited a Get result, want the stored block",
			after.Messages[0].Blocks)
	}
}

func testMutateCreatesNewChat(t *testing.T, s ChatStoreContract) {
	t.Helper()
	err := s.Mutate(context.Background(), "c1", func(c *vibekit.Chat, exists bool) bool {
		if exists {
			t.Error("exists should be false for new chat")
		}
		c.Name = "hello"
		return true
	})
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	c, ok := s.Get(context.Background(), "c1")
	if !ok {
		t.Fatal("Get returned false after Mutate create")
	}
	if c.Name != "hello" {
		t.Errorf("Name = %q, want hello", c.Name)
	}
}

func testMutateUpdatesExistingChat(t *testing.T, s ChatStoreContract) {
	t.Helper()
	_ = s.Mutate(context.Background(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "first"
		return true
	})
	_ = s.Mutate(context.Background(), "c1", func(c *vibekit.Chat, exists bool) bool {
		if !exists {
			t.Error("exists should be true for existing chat")
		}
		c.Name = "second"
		return true
	})
	c, _ := s.Get(context.Background(), "c1")
	if c.Name != "second" {
		t.Errorf("Name = %q, want second", c.Name)
	}
}

func testMutateNoopWhenFalseReturned(t *testing.T, s ChatStoreContract) {
	t.Helper()
	_ = s.Mutate(context.Background(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "created"
		return true
	})
	_ = s.Mutate(context.Background(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "should-not-persist"
		return false
	})
	c, _ := s.Get(context.Background(), "c1")
	if c.Name != "created" {
		t.Errorf("Name = %q, want created (mutate returned false)", c.Name)
	}
}

func testDeleteRemovesChat(t *testing.T, s ChatStoreContract) {
	t.Helper()
	_ = s.Mutate(context.Background(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "doomed"
		return true
	})
	if err := s.Delete(context.Background(), "c1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, ok := s.Get(context.Background(), "c1")
	if ok {
		t.Error("Get returned true after Delete")
	}
}

func testListReturnsCreatedChats(t *testing.T, s ChatStoreContract) {
	t.Helper()
	_ = s.Mutate(context.Background(), "a", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "alpha"
		return true
	})
	_ = s.Mutate(context.Background(), "b", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "beta"
		return true
	})
	headers := s.List(context.Background())
	if len(headers) != 2 {
		t.Fatalf("List len = %d, want 2", len(headers))
	}
}

func testBuildHistoryEmptyForMissing(t *testing.T, s ChatStoreContract) {
	t.Helper()
	if h := s.BuildHistory(context.Background(), "nope"); h != "" {
		t.Errorf("BuildHistory = %q, want empty", h)
	}
}

func testAppendMessageAddsToChat(t *testing.T, s ChatStoreContract) {
	t.Helper()
	_ = s.Mutate(context.Background(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = contractChatName
		return true
	})
	msg := &vibekit.Message{ID: "m1", Role: "user", Content: "hi"}
	if err := s.AppendMessage(context.Background(), "c1", msg); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	c, _ := s.Get(context.Background(), "c1")
	if len(c.Messages) != 1 {
		t.Fatalf("Messages len = %d, want 1", len(c.Messages))
	}
	if c.Messages[0].Content != "hi" {
		t.Errorf("Content = %q, want hi", c.Messages[0].Content)
	}
}

func testUpdateMessageMutatesInPlace(t *testing.T, s ChatStoreContract) {
	t.Helper()
	_ = s.Mutate(context.Background(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = contractChatName
		return true
	})
	_ = s.AppendMessage(context.Background(), "c1", &vibekit.Message{ID: "m1", Role: "assistant", Content: "draft"})
	err := s.UpdateMessage(context.Background(), "c1", "m1", func(m *vibekit.Message) {
		m.Content = "final"
	})
	if err != nil {
		t.Fatalf("UpdateMessage: %v", err)
	}
	c, _ := s.Get(context.Background(), "c1")
	if c.Messages[0].Content != "final" {
		t.Errorf("Content = %q, want final", c.Messages[0].Content)
	}
}

// planMsg is a plan-bearing assistant message carrying one entry at status.
func planMsg(id, content string, status vibekit.PlanStatus) *vibekit.Message {
	return &vibekit.Message{
		ID:   id,
		Role: vibekit.RoleAssistant,
		Plan: []vibekit.PlanEntry{{Content: content, Status: status}},
	}
}

// seedTurn opens a chat and appends one user message, which is what makes a turn.
func seedTurn(s ChatStoreContract, chatID vibekit.ChatID, msgID string) {
	_ = s.Mutate(context.Background(), chatID, func(c *vibekit.Chat, _ bool) bool {
		c.Name = contractChatName
		return true
	})
	_ = s.AppendMessage(context.Background(), chatID, &vibekit.Message{ID: msgID, Role: vibekit.RoleUser, Content: "go"})
}

func testUpsertTurnPlanAppendsWhenTurnHasNone(t *testing.T, s ChatStoreContract) {
	t.Helper()
	seedTurn(s, "c1", "u1")
	if err := s.UpsertTurnPlan(context.Background(), "c1", planMsg("p1", "step", vibekit.PlanPending)); err != nil {
		t.Fatalf("UpsertTurnPlan: %v", err)
	}
	c, _ := s.Get(context.Background(), "c1")
	if len(c.Messages) != 2 {
		t.Fatalf("UpsertTurnPlan on a turn with no plan row: Messages len = %d, want 2", len(c.Messages))
	}
	if len(c.Messages[1].Plan) != 1 || c.Messages[1].Plan[0].Content != "step" {
		t.Errorf("appended row Plan = %+v, want one entry \"step\"", c.Messages[1].Plan)
	}
}

func testUpsertTurnPlanOverwritesWithinOneTurn(t *testing.T, s ChatStoreContract) {
	t.Helper()
	seedTurn(s, "c1", "u1")
	_ = s.UpsertTurnPlan(context.Background(), "c1", planMsg("p1", "step", vibekit.PlanPending))
	if err := s.UpsertTurnPlan(context.Background(), "c1", planMsg("p2", "step", vibekit.PlanCompleted)); err != nil {
		t.Fatalf("UpsertTurnPlan (second frame): %v", err)
	}
	c, _ := s.Get(context.Background(), "c1")
	if len(c.Messages) != 2 {
		t.Fatalf("two plan frames in one turn: Messages len = %d, want 2 (one user, one plan)", len(c.Messages))
	}
	if got := c.Messages[1].Plan[0].Status; got != vibekit.PlanCompleted {
		t.Errorf("plan row Status = %q, want %q (the newest frame's)", got, vibekit.PlanCompleted)
	}
	if c.Messages[1].ID != "p1" {
		t.Errorf("plan row ID = %q, want p1 — the overwrite keeps the row's identity", c.Messages[1].ID)
	}
}

func testUpsertTurnPlanStartsARowPerTurn(t *testing.T, s ChatStoreContract) {
	t.Helper()
	seedTurn(s, "c1", "u1")
	_ = s.UpsertTurnPlan(context.Background(), "c1", planMsg("p1", "first turn", vibekit.PlanPending))
	// A user message opens the next turn, so the plan above is out of reach.
	_ = s.AppendMessage(context.Background(), "c1", &vibekit.Message{ID: "u2", Role: vibekit.RoleUser, Content: "again"})
	if err := s.UpsertTurnPlan(context.Background(), "c1", planMsg("p2", "second turn", vibekit.PlanPending)); err != nil {
		t.Fatalf("UpsertTurnPlan (second turn): %v", err)
	}
	c, _ := s.Get(context.Background(), "c1")
	if len(c.Messages) != 4 {
		t.Fatalf("a plan in each of two turns: Messages len = %d, want 4", len(c.Messages))
	}
	if c.Messages[1].Plan[0].Content != "first turn" {
		t.Errorf("turn 1 plan = %q, want \"first turn\" — a later turn must not overwrite it", c.Messages[1].Plan[0].Content)
	}
	if c.Messages[3].Plan[0].Content != "second turn" {
		t.Errorf("turn 2 plan = %q, want \"second turn\"", c.Messages[3].Plan[0].Content)
	}
}
