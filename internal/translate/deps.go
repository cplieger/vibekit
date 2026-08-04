package translate

import (
	"context"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/ids"
)

// BufferAccess is the consumer-side interface for buffer store access.
// Narrows the coupling: translate only needs GetOrInit.
type BufferAccess interface {
	GetOrInit(chatID api.ChatID) *buffer.Buffer
}

// LineRecorder is the consumer-side interface for line tracking.
// Narrows the coupling: translate only needs RecordFromDiffs.
type LineRecorder interface {
	RecordFromDiffs(chatID api.ChatID, diffs []api.ToolDiff, turn int, kind string)
}

// Deps abstracts the Hub methods that stateful translate handlers need.
// Hub satisfies this interface, allowing the Translator to operate
// without importing the hub package.
type Deps interface {
	StreamingAccess
	PermissionAccess
	// MCPRecorder returns the MCP state recorder sub-interface.
	MCPRecorder() MCPRecorder
	// SetGovernance caches the latest account/workspace governance state so
	// GET /api/governance can serve it with no chat open (see hub/governance.go).
	SetGovernance(state api.GovernanceStatePayload)
}

// MCPRecorder groups MCP server state tracking methods.
// Extracted from Deps to narrow the interface (21→17 methods) and
// allow independent stubbing in tests.
type MCPRecorder interface {
	// RecordConnected marks a server connected and records what it advertises
	// (from _kiro/mcp/status): its tool names, prompts and resources. All three
	// may be nil, and all three arrive together — there is no separate
	// SetKnownTools, because a second call would be a second write of the same
	// notification and the record it lands in is replaced wholesale here.
	RecordConnected(ctx context.Context, serverName string, tools []string, prompts []api.MCPPromptInfo, resources []api.MCPResourceInfo)
	RecordOAuth(ctx context.Context, serverName, oauthURL string)
	RecordInitFailure(ctx context.Context, serverName, errMsg string)
	SignalReady()
}

// Translator holds stateful translate logic extracted from Hub.
// It delegates Hub access through Deps.
type Translator struct {
	deps     Deps
	newMsgID func() string
	// steps maps a workflow step's ACP session id to its run and node. Fed from
	// the wire (`node_start`) and from an `inspect` read; see workflow_steps.go.
	steps *stepRegistry
}

// New constructs a Translator with the given Hub dependency surface.
func New(deps Deps, opts ...Option) *Translator {
	t := &Translator{
		deps:  deps,
		steps: newStepRegistry(),
	}
	for _, o := range opts {
		o(t)
	}
	if t.newMsgID == nil {
		t.newMsgID = ids.NewMessageID
	}
	return t
}

// Option configures a Translator.
type Option func(*Translator)

// withIDGenerator overrides the default message ID generator. Unexported:
// the only caller is this package's own tests, which need deterministic
// message IDs; production always takes the ids.NewMessageID default.
func withIDGenerator(fn func() string) Option {
	return func(t *Translator) { t.newMsgID = fn }
}

// newEventMessage constructs a standard event message with a fresh ID,
// RoleEvent, and the current timestamp. Eliminates 5-site boilerplate.
func (t *Translator) newEventMessage(kind api.EventKind, content string) api.Message {
	return api.Message{
		ID:        t.newMsgID(),
		Role:      api.RoleEvent,
		Ts:        time.Now().UnixMilli(),
		EventKind: kind,
		Content:   content,
	}
}

// deriveSubSession returns the sessionID when it belongs to a SUBAGENT, and ""
// for the launching chat itself OR for a workflow step.
//
// A step returning "" is the point, not a loss: a step's frames arrive on the
// chat's connection with their own session id, and every caller of this function
// treats a non-empty result as "a subagent did this" — three by dropping the
// frame and three by stamping SubSessionID. Neither is true of a step, so the
// step case is answered where it is known (ClassifyFrame) and this function
// keeps its one narrow meaning. See workflow_steps.go for the whole problem.
func (t *Translator) deriveSubSession(chatID api.ChatID, sessionID string) string {
	if t.ClassifyFrame(chatID, sessionID, false) == OwnerSubagent {
		return sessionID
	}
	return ""
}

// MCP returns the MCP state recorder sub-interface.
func (t *Translator) MCP() MCPRecorder {
	return t.deps.MCPRecorder()
}
