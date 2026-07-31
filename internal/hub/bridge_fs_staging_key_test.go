package hub

import (
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/keyenc"
	"github.com/cplieger/vibekit/internal/api"
)

// The pending-store key composes (prefix, chatID, source tag, id) through
// keyenc.Join. Two of those can carry the old '-' separator — a chat id
// (api.ValidChatID permits [a-zA-Z0-9_-]) and an agent-supplied toolCallId
// (no validated alphabet at all) — so the pre-keyenc fmt.Sprintf("fs-%s-%s",
// …) form collapsed distinct (chat, request) pairs onto one key. Each pair
// below is one the old form spelled identically; a collapse cross-resolves one
// chat's accept/reject onto another chat's staged write, landing agent content
// on disk that its user never approved. The one collapse reachable with NO
// toolCallId at all — the live v3 shape — is the msg.ID-vs-fallback pair in
// the sibling test below.
func TestExtractToolCallID_DisjointAcrossChatsAndBranches(t *testing.T) {
	t.Parallel()

	id7 := int64(7)
	tcMsg := func(toolCallID string) *api.RPCResponse {
		return &api.RPCResponse{ID: &id7, Params: mustJSON(t, map[string]string{"toolCallId": toolCallID})}
	}
	idMsg := func(id int64) *api.RPCResponse {
		return &api.RPCResponse{ID: &id, Params: mustJSON(t, map[string]any{})}
	}

	cases := map[string]struct {
		aChat, bChat api.ChatID
		aMsg, bMsg   *api.RPCResponse
		oldForm      string
	}{
		// A '-' in the chat id moves the boundary: both spelled "fs-a-b-c".
		"chat-id hyphen vs tool-call id": {
			aChat: "a-b", aMsg: tcMsg("c"),
			bChat: "a", bMsg: tcMsg("b-c"),
			oldForm: "fs-a-b-c",
		},
		// The tool branch could spell the msg branch's decimal id.
		"tool-call id spells a message id": {
			aChat: "c1", aMsg: tcMsg("7"),
			bChat: "c1", bMsg: idMsg(7),
			oldForm: "fs-c1-7",
		},
		// A '-' in the chat id also moves the boundary between two tool-branch
		// requests whose ids differ only in where the hyphen falls.
		"chat-id hyphen absorbs a tool-call id prefix": {
			aChat: "chat-1", aMsg: tcMsg("call-9"),
			bChat: "chat", bMsg: tcMsg("1-call-9"),
			oldForm: "fs-chat-1-call-9",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a := extractToolCallID(tc.aChat, tc.aMsg)
			b := extractToolCallID(tc.bChat, tc.bMsg)
			if a == b {
				t.Fatalf("distinct requests share one key %q (the old form spelled them %s)", a, tc.oldForm)
			}
			// Distinctness alone would also be satisfied by an encoding that
			// merely happened to differ here. Assert the stronger property:
			// each key splits back into exactly the four components it was
			// built from, so the chat id survives as ONE component whatever
			// it contains.
			assertPendingKeyComponents(t, a, tc.aChat)
			assertPendingKeyComponents(t, b, tc.bChat)
		})
	}
}

// The fallback branch's sequence number is a process-global counter, so its
// collision with the msg.ID branch cannot be written as a literal. Derive the
// counter value from the key itself, then build the chat id that would have
// spelled the same string under the old '-' join. Chat "x-fallback" with
// msg.ID 3 and chat "x" on fallback 3 both produced "fs-x-fallback-3"; both
// names pass api.ValidChatID, and both branches fire on the live v3 wire.
func TestExtractToolCallID_FallbackBranchDisjointFromMessageIDBranch(t *testing.T) {
	t.Parallel()

	const base api.ChatID = "x"
	fallbackKey := extractToolCallID(base, &api.RPCResponse{ID: nil, Params: mustJSON(t, map[string]any{})})

	parts, err := keyenc.Split(fallbackKey)
	if err != nil {
		t.Fatalf("Split(%q): %v", fallbackKey, err)
	}
	if len(parts) != 4 || parts[2] != pendingSourceFallback {
		t.Fatalf("Split(%q) = %q, want 4 components tagged %q", fallbackKey, parts, pendingSourceFallback)
	}
	seq, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		t.Fatalf("fallback component %q is not a sequence number: %v", parts[3], err)
	}

	// The chat whose msg.ID branch collided with the above under "fs-%s-%d".
	collidingChat := base + "-" + api.ChatID(pendingSourceFallback)
	msgKey := extractToolCallID(collidingChat, &api.RPCResponse{ID: &seq, Params: mustJSON(t, map[string]any{})})

	oldFallback := "fs-" + string(base) + "-fallback-" + strconv.FormatInt(seq, 10)
	oldMsg := "fs-" + string(collidingChat) + "-" + strconv.FormatInt(seq, 10)
	if oldFallback != oldMsg {
		t.Fatalf("test setup no longer reproduces the old collision: %q vs %q", oldFallback, oldMsg)
	}
	if fallbackKey == msgKey {
		t.Fatalf("fallback and msg.ID branches share one key %q (old form: %q)", fallbackKey, oldFallback)
	}
	assertPendingKeyComponents(t, msgKey, collidingChat)
}

// A chat id and a tool-call id free of ':' and '\' are emitted verbatim, so an
// ordinary key is the plain colon join with no escaping overhead — the
// property that lets a key move to keyenc without a length or readability
// surprise. api.ValidChatID already excludes both reserved characters, so this
// is the shape every real key takes; only an agent-supplied toolCallId can
// push a component into the escaped form, and that is asserted too.
func TestExtractToolCallID_OrdinaryInputIsThePlainColonJoin(t *testing.T) {
	t.Parallel()

	id7 := int64(7)
	got := extractToolCallID("c1", &api.RPCResponse{
		ID:     &id7,
		Params: mustJSON(t, map[string]string{"toolCallId": "call_abc-123"}),
	})
	if want := "fs:c1:tool:call_abc-123"; got != want {
		t.Errorf("extractToolCallID = %q, want the plain colon join %q", got, want)
	}

	// A reserved character in the one unvalidated field is escaped rather
	// than emitted as a boundary.
	escaped := extractToolCallID("c1", &api.RPCResponse{
		ID:     &id7,
		Params: mustJSON(t, map[string]string{"toolCallId": "a:b"}),
	})
	if !strings.Contains(escaped, `a\:b`) {
		t.Errorf("extractToolCallID = %q, want the toolCallId's colon escaped", escaped)
	}
	assertPendingKeyComponents(t, escaped, "c1")
}

// assertPendingKeyComponents checks that key round-trips back to the four
// components extractToolCallID builds it from, with chatID recovered exactly.
func assertPendingKeyComponents(t *testing.T, key string, chatID api.ChatID) {
	t.Helper()
	parts, err := keyenc.Split(key)
	if err != nil {
		t.Fatalf("Split(%q): %v", key, err)
	}
	if len(parts) != 4 {
		t.Fatalf("Split(%q) = %q, want 4 components", key, parts)
	}
	if parts[0] != pendingKeyPrefix {
		t.Errorf("Split(%q)[0] = %q, want %q", key, parts[0], pendingKeyPrefix)
	}
	if parts[1] != string(chatID) {
		t.Errorf("Split(%q)[1] = %q, want the chat id %q", key, parts[1], chatID)
	}
	switch parts[2] {
	case pendingSourceToolCall, pendingSourceMessageID, pendingSourceFallback:
	default:
		t.Errorf("Split(%q)[2] = %q, want one of the three source tags", key, parts[2])
	}
	if parts[3] == "" {
		t.Errorf("Split(%q)[3] is empty, want a non-empty id", key)
	}
}
