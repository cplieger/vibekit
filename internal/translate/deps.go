package translate

import (
	"context"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/ids"
	"golang.org/x/sync/singleflight"
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
	BridgeComm
	// MCPRecorder returns the MCP state recorder sub-interface.
	MCPRecorder() MCPRecorder
}

// MCPRecorder groups MCP server state tracking methods.
// Extracted from Deps to narrow the interface (21→17 methods) and
// allow independent stubbing in tests.
type MCPRecorder interface {
	RecordConnected(ctx context.Context, serverName string)
	RecordOAuth(ctx context.Context, serverName, oauthURL string)
	RecordInitFailure(ctx context.Context, serverName, errMsg string)
	SignalReady()
	SetKnownTools(ctx context.Context, name string, tools []string)
}

// Translator holds stateful translate logic extracted from Hub.
// It owns the crew cache and delegates Hub access through Deps.
type Translator struct {
	deps      Deps
	crewSF    singleflight.Group
	newMsgID  func() string
	crewCache *crewCache
	configDir string
}

// New constructs a Translator with the given Hub dependency surface.
func New(deps Deps, configDir string, opts ...Option) *Translator {
	t := &Translator{
		deps:      deps,
		configDir: configDir,
		crewCache: newCrewCache(),
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

// WithIDGenerator overrides the default message ID generator (for tests).
func WithIDGenerator(fn func() string) Option {
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

// deriveSubSession determines whether a notification belongs to a
// subagent session. Returns the sessionID if it differs from the
// parent ACP session (indicating a subagent), or "" for the parent.
func (t *Translator) deriveSubSession(chatID api.ChatID, sessionID string) string {
	parent := t.deps.ParentACPSession(chatID)
	if sessionID != "" && parent != "" && sessionID != parent {
		return sessionID
	}
	return ""
}

// MCP returns the MCP state recorder sub-interface.
func (t *Translator) MCP() MCPRecorder {
	return t.deps.MCPRecorder()
}
