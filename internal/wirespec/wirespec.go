// Package wirespec is the single source of truth for vibekit's wire
// contract: the registered wire types, the enums, the TS-name and path-name
// overrides, and the SSE event→decoder table that cmd/wire-codegen feeds into
// wiregen to emit static-src/wire/{types,decoders,registry}.gen.ts.
//
// It exists so the registration table is a reviewable declaration rather than
// the body of a main function. The table is the contract every typed client
// decode depends on, and a contract nothing can import cannot be read by a
// test, so cmd/wire-codegen is reduced to the flags and the generate call.
//
// wiregen is a BUILD-TIME-ONLY dependency: this package is imported by
// cmd/wire-codegen and by tests, never by the server runtime. Importing it
// from a runtime package would pull go/packages and golang.org/x/tools into
// the server binary.
//
// vibekit has NO endpoint table, deliberately. subflux's counterpart carries
// one (endpoint name, method, path, auth group) because it generates a typed
// client plus Go path constants and cross-checks each endpoint's auth group
// against its route registration. vibekit generates neither, so an endpoint
// table here would be a second, unverified copy of internal/server's routing
// with nothing reading it. Adding one is a feature with its own consistency
// test to write, not part of moving this table off main.
package wirespec

import (
	"maps"
	"slices"

	"github.com/cplieger/vibekit/internal/auth"
	"github.com/cplieger/vibekit/internal/forges"
	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/wiregen/v3"
)

// wireTypes is every Go type the generator emits a TypeScript declaration
// for. Order is significant: the generator emits in slice order, so a type
// must be declared before any type that references it.
var wireTypes = []wiregen.WireType{
	wiregen.TypeRef[vibekit.ToolLocation](),
	wiregen.TypeRef[vibekit.ToolDiff](),
	wiregen.TypeRef[vibekit.ToolCheckpoint](),
	// Declared BEFORE ToolCall: the generator emits in order and ToolCall
	// references both.
	wiregen.TypeRef[vibekit.ToolDisclosed](),
	wiregen.TypeRef[vibekit.ToolDenialRule](),
	wiregen.TypeRef[vibekit.ToolDenial](),
	wiregen.TypeRef[vibekit.TextSpan](),
	wiregen.TypeRef[vibekit.ToolTruncation](),
	wiregen.TypeRef[vibekit.ToolCall](),
	// GET /api/chats/{id}/tools/{toolCallID}'s response, after ToolDiff and
	// TextSpan, which it references. No `Payload` suffix, so
	// TestRegistry_EveryRegisteredPayloadHasAnSSEBinding exempts it by
	// construction — the slot EffectiveSettings occupies as a REST response.
	wiregen.TypeRef[vibekit.ToolCallBulk](),
	wiregen.TypeRef[vibekit.PlanEntry](),
	wiregen.TypeRef[vibekit.Block](),
	wiregen.TypeRef[vibekit.CodeReference](),
	wiregen.TypeRef[vibekit.RefusalInfo](),
	// Declared BEFORE Message, which references it: the generator emits in
	// order.
	wiregen.TypeRef[vibekit.Attachment](),
	wiregen.TypeRef[vibekit.Message](),
	wiregen.TypeRef[vibekit.MeteringItem](),
	wiregen.TypeRef[vibekit.Usage](),
	wiregen.TypeRef[vibekit.SessionMode](),
	wiregen.TypeRef[vibekit.SessionModel](),
	wiregen.TypeRef[vibekit.SessionEffortLevel](),
	wiregen.TypeRef[vibekit.ChatHeader](),
	wiregen.TypeRef[vibekit.PermissionOption](),
	wiregen.TypeRef[vibekit.ApprovalFile](),
	wiregen.TypeRef[vibekit.FileChange](),
	wiregen.TypeRef[vibekit.ConnectedPayload](),
	wiregen.TypeRef[vibekit.MessageChunkPayload](),
	wiregen.TypeRef[vibekit.TurnEndedPayload](),
	wiregen.TypeRef[vibekit.SteerQueuedPayload](),
	wiregen.TypeRef[vibekit.SteerInjectedPayload](),
	wiregen.TypeRef[vibekit.SteerClearedPayload](),
	wiregen.TypeRef[vibekit.AgentNoticePayload](),
	wiregen.TypeRef[vibekit.TurnStatePayload](),
	// Declared BEFORE the two types that reference it: the generator emits in
	// slice order.
	wiregen.TypeRef[vibekit.TabSubject](),
	wiregen.TypeRef[vibekit.TabsChangedPayload](),
	wiregen.TypeRef[vibekit.TabList](),
	wiregen.TypeRef[vibekit.PermissionNeededPayload](),
	wiregen.TypeRef[vibekit.ErrorPayload](),
	wiregen.TypeRef[vibekit.MCPConnectedPayload](),
	wiregen.TypeRef[vibekit.MCPOAuthPayload](),
	wiregen.TypeRef[vibekit.MCPFailedPayload](),
	wiregen.TypeRef[vibekit.MCPDisconnectedPayload](),
	wiregen.TypeRef[vibekit.ChatDeletedPayload](),
	wiregen.TypeRef[vibekit.DraftChangedPayload](),
	wiregen.TypeRef[vibekit.ToolCallPayload](),
	wiregen.TypeRef[vibekit.ToolCallUpdatePayload](),
	wiregen.TypeRef[vibekit.ElicitationPropertySchema](),
	wiregen.TypeRef[vibekit.ElicitationRequestSchema](),
	wiregen.TypeRef[vibekit.ElicitationNeededPayload](),
	wiregen.TypeRef[vibekit.UserInputSubOption](),
	wiregen.TypeRef[vibekit.UserInputOption](),
	wiregen.TypeRef[vibekit.UserInputNeededPayload](),
	wiregen.TypeRef[vibekit.DecisionSettledPayload](),
	wiregen.TypeRef[vibekit.OpenExternalURLPayload](),
	wiregen.TypeRef[vibekit.CodeReferencesPayload](),
	wiregen.TypeRef[vibekit.AccountUsageBreakdown](),
	wiregen.TypeRef[vibekit.AccountUsage](),
	wiregen.TypeRef[vibekit.PolicyRuleCore](),
	wiregen.TypeRef[vibekit.PolicyRule](),
	wiregen.TypeRef[vibekit.SecurityProfile](),
	wiregen.TypeRef[vibekit.PolicyView](),
	wiregen.TypeRef[vibekit.PolicyExplainResult](),
	wiregen.TypeRef[vibekit.PolicyErrorItem](),
	wiregen.TypeRef[vibekit.PermissionsChangedPayload](),
	wiregen.TypeRef[vibekit.PolicyErrorPayload](),
	wiregen.TypeRef[vibekit.SafetyProperty](),
	wiregen.TypeRef[vibekit.SafetyStatusPayload](),
	wiregen.TypeRef[vibekit.SafetyPropertiesPayload](),
	wiregen.TypeRef[vibekit.GovernanceFeatures](),
	wiregen.TypeRef[vibekit.GovernanceStatePayload](),
	wiregen.TypeRef[vibekit.ToolJob](),
	wiregen.TypeRef[vibekit.ToolInfo](),
	wiregen.TypeRef[vibekit.SystemTool](),
	wiregen.TypeRef[vibekit.AptPackage](),
	wiregen.TypeRef[vibekit.ToolsList](),
	wiregen.TypeRef[vibekit.ToolCatalogHit](),
	wiregen.TypeRef[vibekit.ToolsSearchResponse](),
	wiregen.TypeRef[vibekit.ToolJobAccepted](),
	wiregen.TypeRef[vibekit.ToolRemoveResponse](),
	wiregen.TypeRef[vibekit.ToolsJobsResponse](),
	wiregen.TypeRef[vibekit.ToolCatalogInfo](),
	wiregen.TypeRef[vibekit.Recipe](),
	wiregen.TypeRef[vibekit.RecipesResponse](),
	// Declared BEFORE LiveRunsResponse, which references it: the generator
	// emits in slice order.
	wiregen.TypeRef[vibekit.LiveRun](),
	wiregen.TypeRef[vibekit.LiveRunsResponse](),
	// GET /api/runs/{id}/controls and POST /api/runs/{id}/retry's replies. No
	// `Payload` suffix, so TestRegistry_EveryRegisteredPayloadHasAnSSEBinding
	// exempts them by construction — the slot LiveRunsResponse occupies as a
	// REST response.
	wiregen.TypeRef[vibekit.RunControlsResponse](),
	wiregen.TypeRef[vibekit.RunRetriedResponse](),
	wiregen.TypeRef[vibekit.RunLaunchRequest](),
	wiregen.TypeRef[vibekit.RunLaunchedResponse](),
	// POST /api/runs/{id}/answer's body, registered for RunLaunchRequest's
	// reason: a request shape the client composes is generated rather than
	// hand-mirrored, so a field rename cannot land on one side only.
	wiregen.TypeRef[vibekit.RunAnswerRequest](),
	wiregen.TypeRef[vibekit.RunStartedPayload](),
	wiregen.TypeRef[vibekit.RunProgressPayload](),
	wiregen.TypeRef[vibekit.RunFinishedPayload](),
	wiregen.TypeRef[vibekit.RunStepPayload](),
	wiregen.TypeRef[vibekit.RunInputNeededPayload](),
	wiregen.TypeRef[vibekit.RunInputSettledPayload](),
	wiregen.TypeRef[vibekit.ToolJobChangedPayload](),
	wiregen.TypeRef[vibekit.ToolJobOutputPayload](),
	wiregen.TypeRef[vibekit.TerminalCreatedPayload](),
	wiregen.TypeRef[vibekit.TerminalOutputPayload](),
	wiregen.TypeRef[vibekit.TerminalExitedPayload](),
	// The GET /api/settings response. Registered so the client's payload type is
	// GENERATED rather than a hand-written mirror: every field is required on both
	// sides (no omitempty), which is what lets the client hold no defaults of its
	// own. See vibekit.EffectiveSettings.
	wiregen.TypeRef[vibekit.EffectiveSettings](),
	wiregen.TypeRef[forges.ConfiguredForge](),
	wiregen.TypeRef[forges.Repo](),
	wiregen.TypeRef[forges.PR](),
	wiregen.TypeRef[forges.Issue](),
	wiregen.TypeRef[forges.Check](),
	wiregen.TypeRef[forges.Release](),
	wiregen.TypeRef[forges.Label](),
	wiregen.TypeRef[forges.User](),
	wiregen.TypeRef[forges.DeviceFlowResponse](),
	wiregen.TypeRef[forges.PollResult](),
	wiregen.TypeRef[auth.WhoamiResponse](),
}

// wireEnums names the string enums to emit. Values are auto-discovered from
// each type's const block in source. Transport stays explicit: it lives in
// internal/mcp, which isn't a registered-type (root) package, so discovery
// doesn't scan it.
var wireEnums = map[string]wiregen.EnumDef{
	"Role": {}, "EventKind": {}, "ToolKind": {}, "ToolStatus": {},
	"PlanStatus": {},
	"StopReason": {}, "ErrorCode": {}, "Kind": {}, // forges.Kind → ForgeKind
	// TurnOutcome is registered for the same reason TabKind is, and it is the
	// stronger case: the rule producing it is implemented in BOTH languages
	// (internal/chat's deriveTurnOutcome and turns.ts's), so a hand-written union
	// on the client is a second enumeration of one vocabulary that the shared
	// fixture pins the BEHAVIOUR of and nothing pins the SPELLING of.
	"TurnOutcome":  {},
	"SafetyStatus": {},
	// SteerOrigin is registered for TabKind's reason: the client's label switch
	// over it must be TOTAL, and the two origins want different words.
	"SteerOrigin":      {},
	"RunProgressKind":  {},
	"DecisionKind":     {},
	"SettledBy":        {},
	"AlwaysAllowBlock": {},
	// TabKind is registered so the nine kinds have exactly ONE definition
	// across both languages: the const block in internal/vibekit/domain_tabs.go,
	// discovered here and emitted as the TypeScript union. It was a hand-written
	// union in tabs.ts derived from the TAB_VIEWS keys, which meant a new kind
	// added server-side reached a client switch with no case for it and no build
	// error anywhere — and the client's factory over a subject is TOTAL by
	// contract, so that is precisely the failure the type has to prevent. With
	// this row TabSubject.kind is TabKind rather than string, so a subject
	// carrying an unknown kind fails the generated decoder at the boundary
	// instead of reaching the factory.
	"TabKind": {},
	// WhoamiState is registered for TabKind's reason and for one more: the
	// client's branch over it must be TOTAL, because the state it used to be
	// missing — "vibekit could not ask" — is the one that must render a retry
	// rather than a sign-in prompt. A hand-written union could gain a fourth arm
	// server-side with no build error anywhere.
	"WhoamiState": {},
	"Transport":   {Values: []string{"stdio", "http", "sse"}},
}

// enumTSNames renames an enum on the TypeScript side.
var enumTSNames = map[string]string{
	"Kind": "ForgeKind", // forges.Kind → ForgeKind in TS
}

// pathNameOverrides pins the snake_case path for a name whose acronym cluster
// cannot be split unambiguously.
var pathNameOverrides = map[string]string{
	"MCPOAuthPayload": "mcp_oauth_payload",
	// URL acronym cluster can't be split unambiguously; pin the path.
	"OpenExternalURLPayload": "open_external_url_payload",
}

// typeMessage is named because 3 SSE events decode to vibekit.Message.
const typeMessage = "Message"

// sseEvents binds each SSE event type to the registered struct its payload
// decodes as. Every TypeName here must appear in wireTypes, and every payload
// in wireTypes must appear here — both directions are asserted by
// TestRegistry_EveryRegisteredPayloadHasAnSSEBinding.
var sseEvents = []wiregen.SSERegEntry{
	{EventType: "chat_created", TypeName: "ChatHeader"},
	{EventType: "chat_deleted", TypeName: "ChatDeletedPayload"},
	{EventType: "chat_updated", TypeName: "ChatHeader"},
	{EventType: "code_references", TypeName: "CodeReferencesPayload"},
	{EventType: "connected", TypeName: "ConnectedPayload"},
	{EventType: "decision_settled", TypeName: "DecisionSettledPayload"},
	{EventType: "draft_changed", TypeName: "DraftChangedPayload"},
	{EventType: "elicitation_needed", TypeName: "ElicitationNeededPayload"},
	{EventType: "user_input_needed", TypeName: "UserInputNeededPayload"},
	{EventType: "error", TypeName: "ErrorPayload"},
	{EventType: "governance_state", TypeName: "GovernanceStatePayload"},
	{EventType: "mcp_connected", TypeName: "MCPConnectedPayload"},
	{EventType: "mcp_disconnected", TypeName: "MCPDisconnectedPayload"},
	{EventType: "mcp_failed", TypeName: "MCPFailedPayload"},
	{EventType: "mcp_oauth_needed", TypeName: "MCPOAuthPayload"},
	{EventType: "message_appended", TypeName: typeMessage},
	{EventType: "message_chunk", TypeName: "MessageChunkPayload"},
	{EventType: "message_created", TypeName: typeMessage},
	{EventType: "message_updated", TypeName: typeMessage},
	{EventType: "open_external_url", TypeName: "OpenExternalURLPayload"},
	{EventType: "permission_needed", TypeName: "PermissionNeededPayload"},
	{EventType: "permissions_changed", TypeName: "PermissionsChangedPayload"},
	{EventType: "policy_error", TypeName: "PolicyErrorPayload"},
	{EventType: "run_started", TypeName: "RunStartedPayload"},
	{EventType: "run_progress", TypeName: "RunProgressPayload"},
	{EventType: "run_finished", TypeName: "RunFinishedPayload"},
	{EventType: "run_step", TypeName: "RunStepPayload"},
	{EventType: "run_input_needed", TypeName: "RunInputNeededPayload"},
	{EventType: "run_input_settled", TypeName: "RunInputSettledPayload"},
	{EventType: "safety_properties", TypeName: "SafetyPropertiesPayload"},
	{EventType: "safety_status", TypeName: "SafetyStatusPayload"},
	{EventType: "tool_call", TypeName: "ToolCallPayload"},
	{EventType: "tool_call_update", TypeName: "ToolCallUpdatePayload"},
	{EventType: "tool_job_changed", TypeName: "ToolJobChangedPayload"},
	{EventType: "tool_job_output", TypeName: "ToolJobOutputPayload"},
	{EventType: "steer_queued", TypeName: "SteerQueuedPayload"},
	{EventType: "steer_injected", TypeName: "SteerInjectedPayload"},
	{EventType: "steer_cleared", TypeName: "SteerClearedPayload"},
	{EventType: "agent_notice", TypeName: "AgentNoticePayload"},
	// The agent-terminal trio. These were the only SSE events with no
	// generated decoder, so their payloads were hand-declared in bus.ts and
	// carried no runtime validation — which is exactly the shape a path
	// nothing exercises ends up in.
	{EventType: "terminal_created", TypeName: "TerminalCreatedPayload"},
	{EventType: "terminal_output", TypeName: "TerminalOutputPayload"},
	{EventType: "terminal_exited", TypeName: "TerminalExitedPayload"},
	{EventType: "turn_ended", TypeName: "TurnEndedPayload"},
	{EventType: "turn_state", TypeName: "TurnStatePayload"},
	{EventType: "tabs_changed", TypeName: "TabsChangedPayload"},
}

// Registry returns the fully-populated wiregen registry: the generator
// options plus the declarative tables above.
//
// The tables are cloned into the returned registry rather than aliased. They
// are package-level state and the registry is handed to a generator that is
// free to reorder or extend what it is given, so sharing the backing array
// would let one call mutate what the next one reads — including this package's
// own tests, which call Registry() more than once per run.
func Registry() *wiregen.Registry {
	r := wiregen.NewRegistry(
		wiregen.WithValidatorsImport("../validators.js"),
		// The validators module is library-owned generated output (wiregen
		// v2): Generate rewrites it next to the hand-written source on every
		// run. Never hand-edit it.
		wiregen.WithValidatorsFile("../validators.ts"),
		wiregen.WithBusImport("../bus.js"),
		wiregen.WithHeaderComment("// CODE-GENERATED by cmd/wire-codegen, DO NOT EDIT.\n\n"),
	)
	r.Types = slices.Clone(wireTypes)
	r.Enums = maps.Clone(wireEnums)
	r.EnumTSName = maps.Clone(enumTSNames)
	r.PathNameOverride = maps.Clone(pathNameOverrides)
	r.SSEEvents = slices.Clone(sseEvents)
	return r
}
