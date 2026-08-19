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

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/auth"
	"github.com/cplieger/vibekit/internal/forges"
	"github.com/cplieger/wiregen/v2"
)

// wireTypes is every Go type the generator emits a TypeScript declaration
// for. Order is significant: the generator emits in slice order, so a type
// must be declared before any type that references it.
var wireTypes = []wiregen.WireType{
	wiregen.TypeRef[api.ToolLocation](),
	wiregen.TypeRef[api.ToolDiff](),
	wiregen.TypeRef[api.ToolCheckpoint](),
	// Declared BEFORE ToolCall: the generator emits in order and ToolCall
	// references both.
	wiregen.TypeRef[api.ToolDisclosed](),
	wiregen.TypeRef[api.ToolDenialRule](),
	wiregen.TypeRef[api.ToolDenial](),
	wiregen.TypeRef[api.TextSpan](),
	wiregen.TypeRef[api.ToolCall](),
	wiregen.TypeRef[api.PlanEntry](),
	wiregen.TypeRef[api.Block](),
	wiregen.TypeRef[api.CodeReference](),
	wiregen.TypeRef[api.RefusalInfo](),
	// Declared BEFORE Message, which references it: the generator emits in
	// order.
	wiregen.TypeRef[api.Attachment](),
	wiregen.TypeRef[api.Message](),
	wiregen.TypeRef[api.MeteringItem](),
	wiregen.TypeRef[api.Usage](),
	wiregen.TypeRef[api.SessionMode](),
	wiregen.TypeRef[api.SessionModel](),
	wiregen.TypeRef[api.SessionEffortLevel](),
	wiregen.TypeRef[api.ChatHeader](),
	wiregen.TypeRef[api.PermissionOption](),
	wiregen.TypeRef[api.ApprovalFile](),
	wiregen.TypeRef[api.FileChange](),
	wiregen.TypeRef[api.ConnectedPayload](),
	wiregen.TypeRef[api.MessageChunkPayload](),
	wiregen.TypeRef[api.TurnEndedPayload](),
	wiregen.TypeRef[api.SteerQueuedPayload](),
	wiregen.TypeRef[api.SteerInjectedPayload](),
	wiregen.TypeRef[api.SteerClearedPayload](),
	wiregen.TypeRef[api.AgentNoticePayload](),
	wiregen.TypeRef[api.TurnStatePayload](),
	wiregen.TypeRef[api.PermissionNeededPayload](),
	wiregen.TypeRef[api.ErrorPayload](),
	wiregen.TypeRef[api.MCPConnectedPayload](),
	wiregen.TypeRef[api.MCPOAuthPayload](),
	wiregen.TypeRef[api.MCPFailedPayload](),
	wiregen.TypeRef[api.MCPDisconnectedPayload](),
	wiregen.TypeRef[api.ChatDeletedPayload](),
	wiregen.TypeRef[api.ToolCallPayload](),
	wiregen.TypeRef[api.ToolCallUpdatePayload](),
	wiregen.TypeRef[api.ElicitationPropertySchema](),
	wiregen.TypeRef[api.ElicitationRequestSchema](),
	wiregen.TypeRef[api.ElicitationNeededPayload](),
	wiregen.TypeRef[api.UserInputSubOption](),
	wiregen.TypeRef[api.UserInputOption](),
	wiregen.TypeRef[api.UserInputNeededPayload](),
	wiregen.TypeRef[api.DecisionSettledPayload](),
	wiregen.TypeRef[api.OpenExternalURLPayload](),
	wiregen.TypeRef[api.CodeReferencesPayload](),
	wiregen.TypeRef[api.AccountUsageBreakdown](),
	wiregen.TypeRef[api.AccountUsage](),
	wiregen.TypeRef[api.PolicyRuleCore](),
	wiregen.TypeRef[api.PolicyRule](),
	wiregen.TypeRef[api.PolicyView](),
	wiregen.TypeRef[api.PolicyExplainResult](),
	wiregen.TypeRef[api.PolicyErrorItem](),
	wiregen.TypeRef[api.PermissionsChangedPayload](),
	wiregen.TypeRef[api.PolicyErrorPayload](),
	wiregen.TypeRef[api.SafetyProperty](),
	wiregen.TypeRef[api.SafetyStatusPayload](),
	wiregen.TypeRef[api.SafetyPropertiesPayload](),
	wiregen.TypeRef[api.GovernanceFeatures](),
	wiregen.TypeRef[api.GovernanceStatePayload](),
	wiregen.TypeRef[api.ToolJob](),
	wiregen.TypeRef[api.ToolInfo](),
	wiregen.TypeRef[api.SystemTool](),
	wiregen.TypeRef[api.ToolsList](),
	wiregen.TypeRef[api.ToolCatalogHit](),
	wiregen.TypeRef[api.ToolsSearchResponse](),
	wiregen.TypeRef[api.ToolJobAccepted](),
	wiregen.TypeRef[api.ToolRemoveResponse](),
	wiregen.TypeRef[api.ToolsJobsResponse](),
	wiregen.TypeRef[api.ToolCatalogInfo](),
	wiregen.TypeRef[api.Recipe](),
	wiregen.TypeRef[api.RecipesResponse](),
	wiregen.TypeRef[api.RunLaunchRequest](),
	wiregen.TypeRef[api.RunLaunchedResponse](),
	wiregen.TypeRef[api.RunStartedPayload](),
	wiregen.TypeRef[api.RunProgressPayload](),
	wiregen.TypeRef[api.RunFinishedPayload](),
	wiregen.TypeRef[api.ToolJobChangedPayload](),
	wiregen.TypeRef[api.ToolJobOutputPayload](),
	wiregen.TypeRef[api.TerminalCreatedPayload](),
	wiregen.TypeRef[api.TerminalOutputPayload](),
	wiregen.TypeRef[api.TerminalExitedPayload](),
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
	"SafetyStatus":    {},
	"RunProgressKind": {},
	"DecisionKind":    {},
	"SettledBy":       {},
	"Transport":       {Values: []string{"stdio", "http", "sse"}},
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

// typeMessage is named because 3 SSE events decode to api.Message.
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
