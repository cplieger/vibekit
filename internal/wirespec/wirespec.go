// Package wirespec is the single source of truth for vibekit's wire contract:
// the registered wire types, the enums, the TS-name and path-name overrides, and
// the SSE event→decoder table that cmd/wire-codegen feeds into wiregen to emit
// static-src/wire/{types,decoders,registry}.gen.ts.
//
// wiregen is a BUILD-TIME-ONLY dependency: this package is imported by
// cmd/wire-codegen and by tests, never by the server runtime, because importing
// it there would pull go/packages and golang.org/x/tools into the binary.
package wirespec

import (
	"maps"
	"slices"

	"github.com/cplieger/vibekit/internal/auth"
	"github.com/cplieger/vibekit/internal/forges"
	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/wiregen/v3"
)

// wireTypes is every Go type the generator emits a TypeScript declaration for.
// Order is significant: the generator emits in slice order, so a type must be
// declared before any type that references it.
var wireTypes = []wiregen.WireType{
	wiregen.TypeRef[vibekit.ToolLocation](),
	wiregen.TypeRef[vibekit.ToolDiff](),
	wiregen.TypeRef[vibekit.ToolCheckpoint](),
	// Before ToolCall, which references both.
	wiregen.TypeRef[vibekit.ToolDisclosed](),
	wiregen.TypeRef[vibekit.ToolDenialRule](),
	wiregen.TypeRef[vibekit.ToolDenial](),
	wiregen.TypeRef[vibekit.TextSpan](),
	wiregen.TypeRef[vibekit.ToolTruncation](),
	wiregen.TypeRef[vibekit.ToolCall](),
	// A REST response, after ToolDiff and TextSpan, which it references. No
	// `Payload` suffix, so the SSE-binding test exempts it by construction.
	wiregen.TypeRef[vibekit.ToolCallBulk](),
	wiregen.TypeRef[vibekit.PlanEntry](),
	wiregen.TypeRef[vibekit.Block](),
	wiregen.TypeRef[vibekit.CodeReference](),
	wiregen.TypeRef[vibekit.RefusalInfo](),
	// Before Message, which references it.
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
	// Before the two types that reference it.
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
	// Before LiveRunsResponse, which references it.
	wiregen.TypeRef[vibekit.LiveRun](),
	wiregen.TypeRef[vibekit.LiveRunsResponse](),
	// REST replies; no `Payload` suffix, so the SSE-binding test exempts them.
	wiregen.TypeRef[vibekit.RunControlsResponse](),
	wiregen.TypeRef[vibekit.RunRetriedResponse](),
	wiregen.TypeRef[vibekit.RunLaunchRequest](),
	wiregen.TypeRef[vibekit.RunLaunchedResponse](),
	// A request body the client composes: generated, not hand-mirrored, so a
	// field rename cannot land on one side only.
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
	// Generated rather than mirrored: every field is required on both sides, which
	// is what lets the client hold no defaults of its own.
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

// wireEnums names the string enums to emit; values are auto-discovered from each
// type's const block. Transport stays explicit because it lives in internal/mcp,
// which discovery does not scan.
var wireEnums = map[string]wiregen.EnumDef{
	"Role": {}, "EventKind": {}, "ToolKind": {}, "ToolStatus": {},
	"PlanStatus": {},
	"StopReason": {}, "ErrorCode": {}, "Kind": {}, // forges.Kind → ForgeKind
	// Derived in BOTH languages (deriveTurnOutcome and turns.ts), so a
	// hand-written union would be a second spelling of one vocabulary.
	"TurnOutcome":  {},
	"SafetyStatus": {},
	// The client's label switch over it must be TOTAL.
	"SteerOrigin":      {},
	"RunProgressKind":  {},
	"DecisionKind":     {},
	"SettledBy":        {},
	"AlwaysAllowBlock": {},
	// One definition across both languages: this makes TabSubject.kind a TabKind,
	// so an unknown kind fails the generated decoder instead of reaching the
	// client's factory, which is TOTAL by contract.
	"TabKind": {},
	// The client's branch over it must be TOTAL: "vibekit could not ask" has to
	// render a retry rather than a sign-in prompt.
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
// decodes as. Every TypeName here must appear in wireTypes and every payload
// there must appear here; both directions are asserted by test.
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
	{EventType: "terminal_created", TypeName: "TerminalCreatedPayload"},
	{EventType: "terminal_output", TypeName: "TerminalOutputPayload"},
	{EventType: "terminal_exited", TypeName: "TerminalExitedPayload"},
	{EventType: "turn_ended", TypeName: "TurnEndedPayload"},
	{EventType: "turn_state", TypeName: "TurnStatePayload"},
	{EventType: "tabs_changed", TypeName: "TabsChangedPayload"},
}

// Registry returns the fully-populated wiregen registry: the generator options
// plus the declarative tables above. The tables are CLONED, not aliased — the
// generator may reorder or extend what it is given, and one call must not mutate
// what the next one reads.
func Registry() *wiregen.Registry {
	r := wiregen.NewRegistry(
		wiregen.WithValidatorsImport("../validators.js"),
		// Library-owned generated output: Generate rewrites it every run, so never
		// hand-edit it.
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
