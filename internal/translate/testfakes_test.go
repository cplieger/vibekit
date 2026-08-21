package translate

import (
	"context"

	"github.com/cplieger/vibekit/internal/buffer"
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

// hostDouble is what a single all-in-one test double answers: every role a
// Translator takes. It exists ONLY for the doubles below and in the handler
// tests — production wires each role to its own owner, and the shape pin in
// shape_test.go reads production files only, for exactly this reason. A double
// stands in for the whole host, so naming every role once is what makes one
// value fillable into every slot of Roles.
//
// It is a flat list because Roles is: the StreamingAccess and PermissionAccess
// composites it used to embed are gone, and with them the reason a host had to
// hold six collaborators to be usable here.
type hostDouble interface {
	Broadcaster
	PendingPermAdder
	Pusher
	SessionResolver
	TerminalReader
	HookStatusReader
	RunOriginAccess
	RunBoundsAccess
	ChatRecords
	GetOrInit(chatID vibekit.ChatID) *buffer.Buffer
	RecordFromDiffs(chatID vibekit.ChatID, diffs []vibekit.ToolDiff, turn int, kind string)
	MCPRecorder() MCPRecorder
	SetGovernance(state vibekit.GovernanceStatePayload)
	// WorkDir is a Roles FIELD in production (a process constant), but the double
	// still answers it as a method so rolesOf can fill that field per fixture —
	// relPath's table drives it, and a hardcoded root would make every case pass
	// or fail together.
	WorkDir() string
}

// rolesOf wires one double into every role slot.
func rolesOf(d hostDouble) *Roles {
	return &Roles{
		Bus:          d,
		Chats:        d,
		Buffers:      d,
		Lines:        d,
		PendingPerms: d,
		Push:         d,
		Sessions:     d,
		Terminals:    d,
		HookStatus:   d,
		WorkDir:      d.WorkDir(),
		MCP:          d.MCPRecorder(),
		Governance:   d,
		RunOrigin:    d,
		RunBounds:    d,
	}
}
