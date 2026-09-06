package translate

import (
	"context"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// nopChatRecords is a ChatRecords whose every method is a no-op, for embedding in a double
// that overrides only the calls its test observes.
type nopChatRecords struct{}

func (nopChatRecords) Get(context.Context, vibekit.ChatID) (*vibekit.Chat, bool) { return nil, false }

func (nopChatRecords) Mutate(context.Context, vibekit.ChatID, func(*vibekit.Chat, bool) bool) error {
	return nil
}

func (nopChatRecords) AppendMessage(context.Context, vibekit.ChatID, *vibekit.Message) error {
	return nil
}

func (nopChatRecords) UpsertTurnPlan(context.Context, vibekit.ChatID, *vibekit.Message) error {
	return nil
}

var _ ChatRecords = nopChatRecords{}

// nopMCPRecorder is a no-op MCPRecorder for the handler benchmarks, which drive frames
// whose MCP side effects they do not assert on.
type nopMCPRecorder struct{}

func (nopMCPRecorder) RecordConnected(context.Context, string, []string, []vibekit.MCPPromptInfo, []vibekit.MCPResourceInfo) {
}

func (nopMCPRecorder) RecordOAuth(context.Context, string, string) {}

func (nopMCPRecorder) RecordInitFailure(context.Context, string, string) {}

func (nopMCPRecorder) RecordDisabled(context.Context, string) {}

func (nopMCPRecorder) SignalReady() {}

var _ MCPRecorder = nopMCPRecorder{}

// hostDouble names every role a Translator takes, so one value fills every slot of Roles.
// It exists ONLY for the doubles here and in the handler tests; production wires each role
// to its own owner.
type hostDouble interface {
	Broadcaster
	PendingPermAdder
	Pusher
	SessionResolver
	TerminalReader
	HookStatusReader
	ModelCatalog
	RunOriginAccess
	RunBoundsAccess
	TurnInterruptAccess
	TurnMetering
	ChatRecords
	Responder
	BufferAccess
	TurnBoundary
	RecordFromDiffs(chatID vibekit.ChatID, diffs []vibekit.ToolDiff, turn int, kind string)
	SteerOrigin(chatID vibekit.ChatID, steerID string) vibekit.SteerOrigin
	MCPRecorder() MCPRecorder
	SetGovernance(state vibekit.GovernanceStatePayload)
	// WorkDir is a Roles FIELD in production; the double answers it as a method so rolesOf can
	// fill that field per fixture, because relPath's table drives it.
	WorkDir() string
}

func rolesOf(d hostDouble) *Roles {
	return &Roles{
		Bus:           d,
		Chats:         d,
		Buffers:       d,
		Turns:         d,
		Lines:         d,
		Steers:        d,
		PendingPerms:  d,
		Respond:       d,
		Push:          d,
		Sessions:      d,
		Terminals:     d,
		HookStatus:    d,
		Catalog:       d,
		WorkDir:       d.WorkDir(),
		MCP:           d.MCPRecorder(),
		Governance:    d,
		RunOrigin:     d,
		RunBounds:     d,
		TurnInterrupt: d,
		Metering:      d,
	}
}
