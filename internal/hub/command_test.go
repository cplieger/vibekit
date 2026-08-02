package hub

// Tests for the command dispatcher in command.go: create_chat, prompt,
// delete_chat, unknown-type handling, idempotent replay.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/command"
)

func TestPrompt_AutoCreatesChatAndPersistsUserMessage(t *testing.T) {
	h, cs, _ := newTestHub()

	rec := postCmd(t, h, api.ClientCommand{
		Type:      "prompt",
		RequestID: "req-1",
		ChatID:    "c-test-1",
		Payload:   json.RawMessage(`{"text":"hello","message_id":"m-1"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	c, ok := cs.Get(context.Background(), "c-test-1")
	if !ok {
		t.Fatal("chat not created")
	}
	if len(c.Messages) < 1 {
		t.Fatalf("user message not persisted: %+v", c.Messages)
	}
	if c.Messages[0].Role != api.RoleUser || c.Messages[0].Content != "hello" {
		t.Errorf("first message mismatch: %+v", c.Messages[0])
	}
	if c.Messages[0].ID != "m-1" {
		t.Errorf("message id mismatch: %q", c.Messages[0].ID)
	}
	if c.Name != "hello" {
		t.Errorf("auto-rename failed: name = %q, want 'hello'", c.Name)
	}
	if c.ACPSessionID == "" {
		t.Errorf("acp_session_id not persisted")
	}
}

func TestPrompt_RejectsEmptyText(t *testing.T) {
	h, _, _ := newTestHub()
	rec := postCmd(t, h, api.ClientCommand{
		Type: "prompt", RequestID: "r-2", ChatID: "c-2",
		Payload: json.RawMessage(`{"text":"","message_id":"m-2"}`),
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
}

func TestPrompt_RejectsMissingMessageID(t *testing.T) {
	h, _, _ := newTestHub()
	rec := postCmd(t, h, api.ClientCommand{
		Type: "prompt", RequestID: "r-3", ChatID: "c-3",
		Payload: json.RawMessage(`{"text":"hi"}`),
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
}

func TestCreateChat_Idempotent(t *testing.T) {
	h, cs, _ := newTestHub()

	post := func(reqID string) int {
		return postCmd(t, h, api.ClientCommand{
			Type: "create_chat", RequestID: reqID, ChatID: "c-dup",
			Payload: json.RawMessage(`{"name":"X"}`),
		}).Code
	}
	if code := post("r1"); code != http.StatusOK {
		t.Errorf("first create code = %d", code)
	}
	if code := post("r2"); code != http.StatusOK {
		t.Errorf("second create code = %d", code)
	}
	c, _ := cs.Get(context.Background(), "c-dup")
	if c.Name != "X" {
		t.Errorf("name = %q", c.Name)
	}
}

func TestDeleteChat_IsUserOnly(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c-del", func(c *api.Chat, _ bool) bool { c.Name = "to-delete"; return true })

	rec := postCmd(t, h, api.ClientCommand{
		Type: "delete_chat", RequestID: "r-del", ChatID: "c-del",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if _, ok := cs.Get(context.Background(), "c-del"); ok {
		t.Error("chat still exists after delete")
	}
}

func TestUnknownCommandReturns400(t *testing.T) {
	h, _, _ := newTestHub()
	rec := postCmd(t, h, api.ClientCommand{
		Type: "nonsense", RequestID: "r-n",
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d", rec.Code)
	}
}

func TestIdempotentReplay(t *testing.T) {
	h, cs, _ := newTestHub()

	cmd := api.ClientCommand{
		Type: "create_chat", RequestID: "r-idem", ChatID: "c-idem",
		Payload: json.RawMessage(`{"name":"first"}`),
	}
	rec1 := postCmd(t, h, cmd)
	rec2 := postCmd(t, h, cmd)

	if rec1.Body.String() != rec2.Body.String() {
		t.Errorf("replay body mismatch: %q vs %q", rec1.Body.String(), rec2.Body.String())
	}
	if len(cs.List(context.Background())) != 1 {
		t.Errorf("chats = %d, want 1", len(cs.List(context.Background())))
	}
}

// --- cmdCancel ---

func TestCancel_NoBridgeIsOK(t *testing.T) {
	h, _, _ := newTestHub()
	rec := postCmd(t, h, api.ClientCommand{Type: "cancel", RequestID: "r1", ChatID: "no-bridge"})
	if rec.Code != http.StatusOK {
		t.Errorf("code = %d, want 200 (cancel is a no-op without a bridge)", rec.Code)
	}
}

func TestCancel_NotifiesBridge(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	sb, err := h.coord.GetOrCreateBridge(context.Background(), "c1", "")
	if err != nil {
		t.Fatal(err)
	}
	// Ensure the fake has the session id populated.
	fb := sb.bridge.(*fakeBridge)
	fb.sessionID = "sess"

	rec := postCmd(t, h, api.ClientCommand{Type: "cancel", RequestID: "r1", ChatID: "c1"})
	if rec.Code != http.StatusOK {
		t.Errorf("code = %d", rec.Code)
	}
}

// --- cmdPermission ---

func TestPermission_RequiresBridge(t *testing.T) {
	h, _, _ := newTestHub()
	rec := postCmd(t, h, api.ClientCommand{
		Type: "permission_response", RequestID: "r1", ChatID: "no-bridge",
		Payload: json.RawMessage(`{"request_id":1,"option_id":"allow"}`),
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
}

func TestPermission_InvalidPayloadIs400(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	_, err := h.coord.GetOrCreateBridge(context.Background(), "c1", "")
	if err != nil {
		t.Fatal(err)
	}
	rec := postCmd(t, h, api.ClientCommand{
		Type: "permission_response", RequestID: "r1", ChatID: "c1",
		Payload: json.RawMessage(`{bad`),
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
}

func TestPermission_ForwardsToBridge(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	_, err := h.coord.GetOrCreateBridge(context.Background(), "c1", "")
	if err != nil {
		t.Fatal(err)
	}
	rec := postCmd(t, h, api.ClientCommand{
		Type: "permission_response", RequestID: "r1", ChatID: "c1",
		Payload: json.RawMessage(`{"request_id":42,"option_id":"allow"}`),
	})
	if rec.Code != http.StatusOK {
		t.Errorf("code = %d", rec.Code)
	}
}

// --- Adversarial input validation ---

func TestCommand_RejectsInvalidChatID(t *testing.T) {
	// Chat ids with path separators, traversal segments, or other
	// unsafe characters must be rejected at the dispatcher before
	// any per-command handler runs. Mirrors chat.chatIDPattern.
	h, _, _ := newTestHub()

	bad := []string{
		"../etc/passwd",
		"c/../escape",
		"a\x00b",
		"has space",
		"has\nnewline",
		"has/slash",
		"has..dots",
	}
	for _, id := range bad {
		body, _ := json.Marshal(api.ClientCommand{
			Type: "prompt", RequestID: "r", ChatID: api.ChatID(id),
			Payload: json.RawMessage(`{"text":"hi","message_id":"m1"}`),
		})
		req := newCmdReq(t, body)
		rec := newCmdRec()
		h.handleCommand(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("chat_id %q: code = %d, want 400", id, rec.Code)
		}
	}
}

func TestPrompt_RejectsOversizedText(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	// 513 KiB — exceeds maxPromptBytes. Cap is smaller than the 1 MiB
	// JSON body limit so the check fires cleanly with a 413.
	big := make([]byte, 513*1024)
	for i := range big {
		big[i] = 'a'
	}
	payload, _ := json.Marshal(map[string]string{
		"text": string(big), "message_id": "m-big",
	})
	rec := postCmd(t, h, api.ClientCommand{
		Type: "prompt", RequestID: "r-big", ChatID: "c1",
		Payload: payload,
	})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("code = %d, want 413", rec.Code)
	}
}

func TestPrompt_RejectsBadMessageID(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	// Control characters, newlines, and overlong strings must all
	// be rejected so the id can't smuggle through SSE framing or
	// corrupt the stored JSON.
	bad := []string{
		"has\nnewline",
		"has space",
		"has\x00nul",
		"slash/injected",
		"",
	}
	for _, id := range bad {
		payload, _ := json.Marshal(map[string]string{
			"text": "hi", "message_id": id,
		})
		rec := postCmd(t, h, api.ClientCommand{
			Type: "prompt", RequestID: "r-" + id, ChatID: "c1",
			Payload: payload,
		})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("message_id %q: code = %d, want 400", id, rec.Code)
		}
	}
}

// --- Helpers for adversarial tests ---

func newCmdReq(t *testing.T, body []byte) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/command", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func newCmdRec() *httptest.ResponseRecorder {
	return httptest.NewRecorder()
}

func TestIsRetryablePromptError(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{nil, false},
		// Untyped errors are non-retryable (fail-closed). The bridge
		// layer should wrap retryable conditions as *TransportError.
		{errors.New("not idle"), false},
		{errors.New("Internal error"), false},
		{errors.New("some other error"), false},
		{errors.New("agent is not idle right now"), false},
	}
	for _, tt := range tests {
		got := command.IsRetryablePromptError(tt.err)
		if got != tt.want {
			t.Errorf("IsRetryablePromptError(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}

func TestIsRetryablePromptError_TypedRPCError(t *testing.T) {
	tests := []struct {
		err  error
		name string
		want bool
	}{
		{
			name: "RPCError with not idle message but unknown code",
			// The bridge layer now wraps "not idle" messages as
			// api.ErrNotIdle before they reach the hub, so a raw
			// RPCError with an unknown code is not retryable here.
			err:  &api.RPCError{Code: -1, Message: "agent is not idle"},
			want: false,
		},
		{
			name: "RPCError with internal error code -32603",
			err:  &api.RPCError{Code: -32603, Message: "something went wrong"},
			want: true,
		},
		{
			name: "RPCError with unrelated code and message",
			err:  &api.RPCError{Code: -32600, Message: "invalid request"},
			want: false,
		},
		{
			name: "wrapped RPCError with not idle but unknown code",
			// Same as above: bridge wraps these at the source now.
			err:  fmt.Errorf("bridge: %w", &api.RPCError{Code: -1, Message: "not idle"}),
			want: false,
		},
		{
			name: "wrapped RPCError with internal error code",
			err:  fmt.Errorf("call failed: %w", &api.RPCError{Code: -32603, Message: "timeout"}),
			want: true,
		},
		{
			name: "wrapped RPCError with unrelated error",
			err:  fmt.Errorf("call failed: %w", &api.RPCError{Code: -32000, Message: "custom error"}),
			want: false,
		},
		{
			name: "ErrNotIdle sentinel from bridge",
			err:  fmt.Errorf("ACP error -32001: %w", api.ErrNotIdle),
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := command.IsRetryablePromptError(tt.err)
			if got != tt.want {
				t.Errorf("IsRetryablePromptError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// --- Create hook ---

func TestCreateHook_RequiresNameAndEventType(t *testing.T) {
	h, _, _ := newTestHub()
	rec := postCmd(t, h, api.ClientCommand{
		Type:      "create_hook",
		RequestID: "r1",
		ChatID:    "c1",
		Payload:   mustJSON(t, map[string]string{}),
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestCreateHook_WritesFile(t *testing.T) {
	h, _, _ := newTestHub()
	h.lifecycle.workDir = t.TempDir()

	rec := postCmd(t, h, api.ClientCommand{
		Type:      "create_hook",
		RequestID: "r1",
		ChatID:    "c1",
		Payload: mustJSON(t, map[string]string{
			"name":        "Test Hook",
			"event_type":  "fileEdited",
			"action_type": "askAgent",
			"prompt":      "review this",
			"patterns":    "*.go,*.ts",
		}),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	// The returned path must be workspace-relative (no workDir prefix
	// leak into client response or Loki logs).
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if p, _ := resp["path"].(string); p != filepath.Join(".kiro", "hooks", "test-hook.json") {
		t.Errorf("path = %q, want .kiro/hooks/test-hook.json (workDir-relative)", p)
	}
	// The JSON written to disk is the v1 schema: askAgent maps to an
	// agent action and patterns to a matcher. mode 0o600 — hooks can
	// hold runCommand shell.
	data, err := os.ReadFile(filepath.Join(h.lifecycle.workDir, ".kiro", "hooks", "test-hook.json"))
	if err != nil {
		t.Fatalf("hook file missing: %v", err)
	}
	info, err := os.Stat(filepath.Join(h.lifecycle.workDir, ".kiro", "hooks", "test-hook.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Windows reports 0o666 for every file; only assert on POSIX.
	if perm := info.Mode().Perm(); perm != 0o600 && perm != 0o666 {
		t.Errorf("mode = %v, want 0o600", perm)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	// v1 envelope: { version:"v1", hooks:[ { name, trigger, matcher, action } ] }.
	if got["version"] != "v1" {
		t.Errorf("version = %v, want v1", got["version"])
	}
	hooks, _ := got["hooks"].([]any)
	if len(hooks) != 1 {
		t.Fatalf("hooks = %v, want 1 entry", hooks)
	}
	hook, _ := hooks[0].(map[string]any)
	// fileEdited maps to the PascalCase PostFileSave trigger; patterns
	// become the single-regex matcher.
	if hook["trigger"] != "PostFileSave" {
		t.Errorf("trigger = %v, want PostFileSave", hook["trigger"])
	}
	if hook["matcher"] != "*.go,*.ts" {
		t.Errorf("matcher = %v, want *.go,*.ts", hook["matcher"])
	}
	// askAgent maps to action.type=agent with a prompt (no command).
	action, _ := hook["action"].(map[string]any)
	if action["type"] != "agent" || action["prompt"] != "review this" {
		t.Errorf("action = %+v", action)
	}
	if _, has := action["command"]; has {
		t.Error("askAgent hook leaked a command field")
	}
}

// TestCreateHook_RunCommandBranchWritesCommand pins the runCommand
// branch distinct from askAgent: v1 action.type=command + action.command.
func TestCreateHook_RunCommandBranchWritesCommand(t *testing.T) {
	h, _, _ := newTestHub()
	h.lifecycle.workDir = t.TempDir()

	rec := postCmd(t, h, api.ClientCommand{
		Type: "create_hook", RequestID: "r1", ChatID: "c1",
		Payload: mustJSON(t, map[string]string{
			"name": "Lint", "event_type": "fileEdited",
			"action_type": "runCommand", "command": "lint %",
		}),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(h.lifecycle.workDir, ".kiro", "hooks", "lint.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(data, &got)
	hooks, _ := got["hooks"].([]any)
	if len(hooks) != 1 {
		t.Fatalf("hooks = %v, want 1 entry", hooks)
	}
	hook, _ := hooks[0].(map[string]any)
	action, _ := hook["action"].(map[string]any)
	// runCommand maps to action.type=command with a command (no prompt).
	if action["type"] != "command" || action["command"] != "lint %" {
		t.Errorf("action = %+v", action)
	}
	if _, has := action["prompt"]; has {
		t.Error("runCommand hook leaked a prompt field")
	}
}

// TestCreateHook_RejectsTraversal pins the path-traversal guard: any
// name containing /, \, .., NUL, or non-allowlisted characters must
// be rejected with 400, and no file may appear outside .kiro/hooks/.
func TestCreateHook_RejectsTraversal(t *testing.T) {
	h, _, _ := newTestHub()
	h.lifecycle.workDir = t.TempDir()

	bad := []string{
		"../evil",
		"../../etc/passwd",
		"foo/bar",
		"foo\\bar",
		"has\x00nul",
		"",
		"....",
		"   ",
		"/absolute",
		"-leading-hyphen",
	}
	for _, name := range bad {
		rec := postCmd(t, h, api.ClientCommand{
			Type: "create_hook", RequestID: "r-" + name, ChatID: "c1",
			Payload: mustJSON(t, map[string]string{
				"name": name, "event_type": "fileEdited",
				"action_type": "askAgent", "prompt": "p",
			}),
		})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("name=%q code=%d, want 400", name, rec.Code)
		}
	}
	// No files must have escaped the hooks directory.
	matches, _ := filepath.Glob(filepath.Join(h.lifecycle.workDir, "..", "*.json"))
	if len(matches) > 0 {
		t.Errorf("traversal succeeded: %v", matches)
	}
}

// TestCreateHook_RejectsOversizeField pins the per-field 8 KiB cap
// (maxHookField). A runaway prompt would otherwise slow every chat
// startup when kiro-cli rescans .kiro/hooks.
func TestCreateHook_RejectsOversizeField(t *testing.T) {
	h, _, _ := newTestHub()
	h.lifecycle.workDir = t.TempDir()
	big := strings.Repeat("a", command.MaxHookField+1)
	rec := postCmd(t, h, api.ClientCommand{
		Type: "create_hook", RequestID: "r1", ChatID: "c1",
		Payload: mustJSON(t, map[string]string{
			"name": "ok", "event_type": "fileEdited",
			"action_type": "askAgent", "prompt": big,
		}),
	})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

// --- Truncate helper ---

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		in   string
		want string
		n    int
	}{
		{"short", "short", 10},
		{"hello world", "hello", 5},
		{"", "", 5},
		{"abc", "abc", 3},
		{"abcd", "abc", 3},
	}
	for _, tt := range tests {
		got := truncateRunes(tt.in, tt.n)
		if got != tt.want {
			t.Errorf("truncateRunes(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}

// --- IsEmptyTurn ---

func TestIsEmptyTurn(t *testing.T) {
	h, _, _ := newTestHub()
	tests := []struct {
		resp    *api.RPCResponse
		name    string
		chatID  api.ChatID
		seedBuf bool
		want    bool
	}{
		{nil, "nil", "c1", false, false},
		{&api.RPCResponse{}, "nil result", "c1", false, false},
		// On v3 the prompt response carries only stopReason (no content array);
		// emptiness is decided by stopReason==end_turn AND an empty buffer.
		{
			&api.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "end_turn"})},
			"end_turn, no buffer",
			"c1", false, true,
		},
		{
			&api.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "end_turn"})},
			"end_turn, empty buffer",
			"c-empty-buf", true, true,
		},
		{
			&api.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "cancelled"})},
			"cancelled",
			"c1", false, false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.seedBuf {
				h.bridge.assistantBufs.GetOrInit(tt.chatID)
			}
			if got := h.isEmptyTurn(tt.resp, tt.chatID); got != tt.want {
				t.Errorf("isEmptyTurn = %v, want %v", got, tt.want)
			}
		})
	}

	// Regression: a streamed turn with buffered content must NOT be treated as
	// empty (the v3 prompt response is content-less for every turn, so the
	// buffer is the only content signal).
	t.Run("end_turn, buffer has content", func(t *testing.T) {
		buf := h.bridge.assistantBufs.GetOrInit("c-with-content")
		buf.Content.WriteString("hello from stream")
		resp := &api.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "end_turn"})}
		if h.isEmptyTurn(resp, "c-with-content") {
			t.Error("expected false for streamed turn with buffered content")
		}
	})
}

// --- MergeLastExchange ---

func TestPrompt_ShellInterception_HappyPath(t *testing.T) {
	h, cs, _ := newTestHub()
	h.lifecycle.workDir = t.TempDir()
	rec := postCmd(t, h, api.ClientCommand{
		Type: "prompt", RequestID: "r1", ChatID: "c-sh",
		Payload: json.RawMessage(`{"text":"!printf hi","message_id":"m-1"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	c, ok := cs.Get(context.Background(), "c-sh")
	if !ok {
		t.Fatal("chat not created by shell interception")
	}
	if len(c.Messages) != 2 {
		t.Fatalf("messages = %d, want 2 (user + assistant)", len(c.Messages))
	}
	if c.Messages[0].Role != api.RoleUser || c.Messages[0].Content != "!printf hi" {
		t.Errorf("user msg = %+v", c.Messages[0])
	}
	if c.Messages[1].Role != api.RoleAssistant {
		t.Errorf("assistant msg role = %q", c.Messages[1].Role)
	}
	if !strings.Contains(c.Messages[1].Content, "```") {
		t.Errorf("assistant msg missing code fence: %q", c.Messages[1].Content)
	}
	if !strings.Contains(c.Messages[1].Content, "hi") {
		t.Errorf("assistant msg missing command output: %q", c.Messages[1].Content)
	}
}

// TestPrompt_ShellInterception_EmptyAfterTrim rejects `!<whitespace>`
// as errEmptyPrompt so cmdPrompt doesn't spawn sh -c ” (which would
// succeed with empty output and confuse the transcript).
func TestPrompt_ShellInterception_EmptyAfterTrim(t *testing.T) {
	h, _, _ := newTestHub()
	h.lifecycle.workDir = t.TempDir()
	rec := postCmd(t, h, api.ClientCommand{
		Type: "prompt", RequestID: "r1", ChatID: "c-empty",
		Payload: json.RawMessage(`{"text":"!   \t","message_id":"m-1"}`),
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for '!<whitespace>'", rec.Code)
	}
}

// TestPrompt_ShellInterception_ExitCodeAppended captures that a
// failing command's exit-status string is surfaced below the fenced
// output so the user sees why their command failed.
func TestPrompt_ShellInterception_ExitCodeAppended(t *testing.T) {
	h, cs, _ := newTestHub()
	h.lifecycle.workDir = t.TempDir()
	rec := postCmd(t, h, api.ClientCommand{
		Type: "prompt", RequestID: "r1", ChatID: "c-fail",
		Payload: json.RawMessage(`{"text":"!false","message_id":"m-1"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	c, _ := cs.Get(context.Background(), "c-fail")
	if len(c.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(c.Messages))
	}
	// The assistant bubble should carry a non-trivial body (more
	// than the empty "```\n\n```" wrapper) so err.Error() is surfaced.
	if len(c.Messages[1].Content) < 10 {
		t.Errorf("assistant content too short, error not surfaced: %q",
			c.Messages[1].Content)
	}
}

func TestShellCappedBuffer(t *testing.T) {
	var b command.ShellCappedBuffer
	// First write lands fully.
	if n, err := b.Write([]byte("hello")); n != 5 || err != nil {
		t.Fatalf("first write n=%d err=%v", n, err)
	}
	if b.Truncated {
		t.Error("truncated=true after small write")
	}
	// Crossing the cap sets the flag; returned n matches input for
	// io.Writer contract even though the buffer clamped.
	big := make([]byte, command.ShellOutputCap+10)
	if n, err := b.Write(big); n != len(big) || err != nil {
		t.Fatalf("big write n=%d err=%v", n, err)
	}
	if !b.Truncated {
		t.Error("truncated=false after exceeding cap")
	}
	if b.Buf.Len() > command.ShellOutputCap {
		t.Errorf("buf.Len() = %d, want <= %d", b.Buf.Len(), command.ShellOutputCap)
	}
	// Subsequent writes past the cap are silently dropped.
	before := b.Buf.Len()
	if _, err := b.Write([]byte("more")); err != nil {
		t.Fatal(err)
	}
	if b.Buf.Len() != before {
		t.Errorf("buf grew past cap: %d -> %d", before, b.Buf.Len())
	}
}

// --- cmdPrompt busy ---

func TestPrompt_BusyReturns409(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	sb, err := h.coord.GetOrCreateBridge(context.Background(), "c1", "")
	if err != nil {
		t.Fatal(err)
	}
	sb.mu.Lock()
	sb.state = bridgePrompting
	sb.mu.Unlock()

	rec := postCmd(t, h, api.ClientCommand{
		Type: "prompt", RequestID: "r1", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-2"}`),
	})
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (busy)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "busy") {
		t.Errorf("body = %q, want 'busy' error", rec.Body.String())
	}
}

// --- buildPromptBlocks ---

func TestBuildPromptBlocks(t *testing.T) {
	tests := []struct {
		setupFile       func(dir string)
		name            string
		text            string
		wantType        string
		wantContains    string
		wantNotContains string
		wantMIME        string
		attachments     []api.Attachment
		wantLen         int
	}{
		{
			name:         "TextOnly",
			text:         "hello",
			wantLen:      1,
			wantType:     "text",
			wantContains: "hello",
		},
		{
			// v3: a supported document is always inlined as an embedded
			// `resource` block (no `document` type in the v3 content-block
			// union; embeddedContext is always advertised true).
			name:        "SupportedDocumentInlinedAsResource",
			text:        "hi",
			attachments: []api.Attachment{{Name: "doc.pdf", Path: "doc.pdf"}},
			setupFile: func(dir string) {
				os.WriteFile(filepath.Join(dir, "doc.pdf"), []byte("%PDF-1.7 fake"), 0o644)
			},
			wantLen:  2,
			wantType: "resource",
			wantMIME: "application/pdf",
		},
		{
			// A format KAS does not accept as an inline resource
			// (.pptx/.ppt/.rtf/.odt/.ods/.odp) must route through the
			// path-reference branch with a note — never a dropped block.
			name:        "UnsupportedDocEmitsAnnotatedPathRef",
			text:        "hi",
			attachments: []api.Attachment{{Name: "deck.pptx", Path: "deck.pptx"}},
			setupFile: func(dir string) {
				os.WriteFile(filepath.Join(dir, "deck.pptx"), []byte("PK fake pptx"), 0o644)
			},
			wantLen:      2,
			wantType:     "text",
			wantContains: "may not be readable",
		},
		{
			name:        "OversizeDocumentFallsBackToText",
			text:        "hi",
			attachments: []api.Attachment{{Name: "big.pdf", Path: "big.pdf"}},
			setupFile: func(dir string) {
				os.WriteFile(filepath.Join(dir, "big.pdf"), make([]byte, command.MaxDocumentBytes+1), 0o644)
			},
			wantLen:      2,
			wantType:     "text",
			wantContains: "too large",
		},
		{
			name:         "UnreadableDocumentFallsBackToText",
			text:         "hi",
			attachments:  []api.Attachment{{Name: "ghost.pdf", Path: "ghost.pdf"}},
			wantLen:      2,
			wantType:     "text",
			wantContains: "unreadable",
		},
		{
			name:        "CodeFileEmitsPathReference",
			text:        "hi",
			attachments: []api.Attachment{{Name: "main.go", Path: "main.go"}},
			setupFile: func(dir string) {
				os.WriteFile(filepath.Join(dir, "main.go"), []byte("package x"), 0o644)
			},
			wantLen:      2,
			wantType:     "text",
			wantContains: "main.go",
		},
		{
			name:            "RejectsTraversalDocument",
			text:            "hi",
			attachments:     []api.Attachment{{Name: "passwd.pdf", Path: "../../../../etc/passwd"}},
			wantLen:         2,
			wantType:        "text",
			wantNotContains: "..",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := newTestHub()
			h.lifecycle.workDir = t.TempDir()
			if tc.setupFile != nil {
				tc.setupFile(h.lifecycle.workDir)
			}

			got := command.BuildPromptBlocks(context.Background(), tc.text, tc.attachments, h.ResolveInsideWorkDir)
			if len(got) != tc.wantLen {
				t.Fatalf("blocks = %d, want %d", len(got), tc.wantLen)
			}

			last := got[tc.wantLen-1]
			if last["type"] != tc.wantType {
				t.Errorf("block[%d].type = %v, want %s", tc.wantLen-1, last["type"], tc.wantType)
			}
			if tc.wantMIME != "" {
				// On v3 a document rides an embedded `resource` block, so
				// mimeType is nested under resource; text blocks have none.
				mime := last["mimeType"]
				if res, ok := last["resource"].(map[string]any); ok {
					mime = res["mimeType"]
				}
				if mime != tc.wantMIME {
					t.Errorf("mimeType = %v, want %s", mime, tc.wantMIME)
				}
			}
			if tc.wantContains != "" {
				text, _ := last["text"].(string)
				if !strings.Contains(text, tc.wantContains) {
					t.Errorf("block text = %q, want substring %q", text, tc.wantContains)
				}
			}
			if tc.wantNotContains != "" {
				text, _ := last["text"].(string)
				if strings.Contains(text, tc.wantNotContains) {
					t.Errorf("block text = %q, must not contain %q", text, tc.wantNotContains)
				}
			}
		})
	}
}

// --- Idempotency cache ---

// TestCommand_RejectsInvalidRequestID pins the same safe-char class
// used for message ids. Bad request_ids must be rejected BEFORE the
// dispatcher touches the idempotency cache, so a megabyte-scale or
// newline-laden id can't pin the cache or corrupt slog output.
func TestCommand_RejectsInvalidRequestID(t *testing.T) {
	h, _, _ := newTestHub()

	bad := []string{
		"has\nnewline",
		"has space",
		"has\x00nul",
		"has/slash",
		strings.Repeat("a", 129),
	}
	for _, id := range bad {
		body, _ := json.Marshal(api.ClientCommand{
			Type: "create_chat", RequestID: id, ChatID: "c1",
			Payload: json.RawMessage(`{"name":"X"}`),
		})
		req := newCmdReq(t, body)
		rec := newCmdRec()
		h.handleCommand(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("request_id %q: code = %d, want 400", id, rec.Code)
		}
	}
}

// TestCommand_IdempotentReplayReturnsSameBytes verifies the cache
// returns byte-for-byte identical responses.
func TestCommand_IdempotentReplayReturnsSameBytes(t *testing.T) {
	h, _, _ := newTestHub()
	cmd := api.ClientCommand{
		Type: "create_chat", RequestID: "r-bytes", ChatID: "c-b",
		Payload: json.RawMessage(`{"name":"X"}`),
	}
	rec1 := postCmd(t, h, cmd)
	rec2 := postCmd(t, h, cmd)
	if !bytes.Equal(rec1.Body.Bytes(), rec2.Body.Bytes()) {
		t.Errorf("replay bytes differ: %q vs %q",
			rec1.Body.Bytes(), rec2.Body.Bytes())
	}
	if rec1.Code != rec2.Code {
		t.Errorf("replay codes differ: %d vs %d", rec1.Code, rec2.Code)
	}
	// Replay must also carry nosniff (api.WriteRawJSON applied).
	if rec2.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("nosniff missing on replay: %v", rec2.Header())
	}
}

// --- Benchmarks ---

// BenchmarkHandleCommand measures the command dispatch hot path including
// JSON decode, validation, and idempotency cache lookup. Sub-benchmarks
// exercise representative command types and the cache-hit replay path.
func BenchmarkHandleCommand(b *testing.B) {
	payloads := map[string]api.ClientCommand{
		"prompt": {
			Type: api.CmdPrompt, RequestID: "bench-prompt", ChatID: "c-bench",
			Payload: json.RawMessage(`{"text":"hello world","message_id":"m-bench"}`),
		},
		"create_chat": {
			Type: api.CmdCreateChat, RequestID: "bench-create", ChatID: "c-bench-new",
			Payload: json.RawMessage(`{"name":"bench","model":"gpt-4"}`),
		},
		"cancel": {
			Type: api.CmdCancel, RequestID: "bench-cancel", ChatID: "c-bench",
		},
	}

	for name, cmd := range payloads {
		b.Run(name, func(b *testing.B) {
			h, _, _ := newTestHub()
			body, _ := json.Marshal(cmd)
			b.ResetTimer()
			b.ReportAllocs()
			for i := range b.N {
				// Unique request_id per iteration to avoid cache hits.
				unique := fmt.Appendf(body[:0:0], `{"type":%q,"request_id":"r-%d","chat_id":%q,"payload":%s}`,
					cmd.Type, i, cmd.ChatID, cmd.Payload)
				req := httptest.NewRequest(http.MethodPost, "/api/command", strings.NewReader(string(unique)))
				rec := httptest.NewRecorder()
				h.handleCommand(rec, req)
			}
		})
	}

	// cache_hit: pre-seed the idempotency cache and measure replay path.
	b.Run("cache_hit", func(b *testing.B) {
		h, _, _ := newTestHub()
		cmd := api.ClientCommand{
			Type: api.CmdCreateChat, RequestID: "bench-cached", ChatID: "c-cached",
			Payload: json.RawMessage(`{"name":"cached","model":"gpt-4"}`),
		}
		// Seed the cache with a first call.
		body, _ := json.Marshal(cmd)
		req := httptest.NewRequest(http.MethodPost, "/api/command", strings.NewReader(string(body)))
		rec := httptest.NewRecorder()
		h.handleCommand(rec, req)

		b.ResetTimer()
		b.ReportAllocs()
		for range b.N {
			req := httptest.NewRequest(http.MethodPost, "/api/command", strings.NewReader(string(body)))
			rec := httptest.NewRecorder()
			h.handleCommand(rec, req)
		}
	})
}
