package translate

import (
	"context"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// The two no-op doubles below used to live in internal/testsupport as
// NopChatStore and NopMCPRecorder. Both had exactly ONE consumer package —
// this one — so a shared package was carrying them for a single caller, and
// NopMCPRecorder's assertion was what made testsupport import translate at all.
//
// Sizing them here is the point. NopChatStore implemented all 8 methods of the
// old vibekit.ChatStore; this package's contract is ChatRecords, 3 methods, so 5
// went. The recorder is unchanged at 5, because MCPRecorder is 5.

// nopChatRecords is a ChatRecords whose every method is a no-op, for embedding
// in a double that overrides only the one or two calls its test observes.
type nopChatRecords struct{}

func (nopChatRecords) Get(context.Context, vibekit.ChatID) (*vibekit.Chat, bool) { return nil, false }

func (nopChatRecords) Mutate(context.Context, vibekit.ChatID, func(*vibekit.Chat, bool) bool) error {
	return nil
}

func (nopChatRecords) AppendMessage(context.Context, vibekit.ChatID, *vibekit.Message) error {
	return nil
}

var _ ChatRecords = nopChatRecords{}

// nopMCPRecorder is a no-op MCPRecorder for the handler benchmarks, which drive
// frames whose MCP side effects they do not assert on.
type nopMCPRecorder struct{}

func (nopMCPRecorder) RecordConnected(context.Context, string, []string, []vibekit.MCPPromptInfo, []vibekit.MCPResourceInfo) {
}

func (nopMCPRecorder) RecordOAuth(context.Context, string, string) {}

func (nopMCPRecorder) RecordInitFailure(context.Context, string, string) {}

func (nopMCPRecorder) RecordDisabled(context.Context, string) {}

func (nopMCPRecorder) SignalReady() {}

var _ MCPRecorder = nopMCPRecorder{}
