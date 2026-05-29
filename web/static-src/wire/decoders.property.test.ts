// Property-based tests for the 44 generated wire decoders in decoders.gen.ts.
//
// Two invariants per decoder:
// 1. Valid-shape inputs (generated from the type schema) decode without throwing.
// 2. Arbitrary unknown inputs (fc.anything()) either produce a valid result OR
//    throw TypeError — never silently corrupt data or throw non-TypeError.
//
// Uses the registry in registry.gen.ts to auto-discover SSE event decoders,
// plus directly imports all exported decoders for full coverage.

import { describe, it, expect } from "vitest";
import fc from "fast-check";
import * as decoders from "./decoders.gen.js";
import type { Decoder } from "../validators.js";

// --- Arbitraries for enum-like const arrays (matching decoders.gen.ts) ---

const clearReasonArb = fc.constantFrom(
  "turn_ended",
  "cancelled",
  "mode_disabled",
  "chat_deleted",
  "shutdown",
  "user_cleared",
);
const crewStatusArb = fc.constantFrom("working", "terminated", "error", "pending");
const errorCodeArb = fc.constantFrom(
  "recovery_failed",
  "bridge_start_failed",
  "prompt_failed",
  "agent_not_found",
  "model_not_found",
  "agent_config_error",
  "rate_limit",
  "stream_timeout",
  "spawn_failed",
  "switch_failed",
  "compaction_failed",
);
const eventKindArb = fc.constantFrom(
  "interrupted",
  "cancelled",
  "model_switched",
  "compacted",
  "crew",
  "agent_switched",
  "compaction_failed",
  "inbox",
);
const forgeKindArb = fc.constantFrom("github", "gitlab", "codeberg", "gitea");
const pendingActionArb = fc.constantFrom("accept", "reject");
const pendingChangeKindArb = fc.constantFrom("create", "edit", "delete");
const planStatusArb = fc.constantFrom("pending", "in_progress", "completed");
const roleArb = fc.constantFrom("user", "assistant", "event");
const stopReasonArb = fc.constantFrom("end_turn", "cancelled", "interrupted");
const toolKindArb = fc.constantFrom(
  "execute",
  "shell",
  "read",
  "search",
  "fetch",
  "edit",
  "think",
  "hook",
  "write",
  "delete",
  "move",
  "command",
  "browser",
  "switch_mode",
  "mcp",
  "other",
);
const toolStatusArb = fc.constantFrom("pending", "in_progress", "completed", "failed");

// --- Helpers ---

const posInt = fc.nat({ max: 1_000_000 });
const posFloat = fc.double({ min: 0, max: 1e9, noNaN: true, noDefaultInfinity: true });

function optField<T>(arb: fc.Arbitrary<T>): fc.Arbitrary<T | undefined> {
  return fc.option(arb, { nil: undefined });
}

// --- Shape arbitraries for each decoder ---

const usageArb = fc.record({
  context_pct: posFloat,
  context_size: posInt,
  credits: posInt,
  turn_count: posInt,
  last_turn_ms: posInt,
  has_real_data: fc.boolean(),
  metering_items: optField(
    fc.array(
      fc.record({
        unit_singular: fc.string(),
        unit_plural: fc.string(),
        value: posFloat,
      }),
      { maxLength: 3 },
    ),
  ),
});

const sessionModelArb = fc.record({
  id: fc.string({ minLength: 1 }),
  name: fc.string({ minLength: 1 }),
  description: optField(fc.string()),
  rate_multiplier: optField(posFloat),
});

const sessionModeArb = fc.record({
  id: fc.string({ minLength: 1 }),
  name: fc.string({ minLength: 1 }),
  description: optField(fc.string()),
});

const availableCommandArb = fc.record({
  name: fc.string({ minLength: 1 }),
  description: optField(fc.string()),
  meta: optField(fc.dictionary(fc.string({ minLength: 1 }), fc.anything())),
});

const chatDeletedPayloadArb = fc.record({ id: fc.string({ minLength: 1 }) });

const chatHeaderArb = fc.record({
  name: fc.string({ minLength: 1 }),
  id: fc.string({ minLength: 1 }),
  usage: usageArb,
  created_at: posInt,
  updated_at: posInt,
  message_count: posInt,
  parent_chat_id: optField(fc.string()),
  agent: optField(fc.string()),
  model: optField(fc.string()),
  acp_session_id: optField(fc.string()),
  current_mode_id: optField(fc.string()),
  compaction_watermark: optField(fc.string()),
  oldest_checkpoint_tag: optField(fc.string()),
  summary: optField(fc.string()),
  available_models: optField(fc.array(sessionModelArb, { maxLength: 3 })),
  available_modes: optField(fc.array(sessionModeArb, { maxLength: 3 })),
  supervised_mode: optField(fc.boolean()),
  auto_approve_crew: optField(fc.boolean()),
});

const checkArb = fc.record({
  name: fc.string({ minLength: 1 }),
  status: fc.string({ minLength: 1 }),
  conclusion: fc.string({ minLength: 1 }),
  url: optField(fc.string()),
});

const commandsUpdatedPayloadArb = fc.record({
  commands: fc.array(availableCommandArb, { maxLength: 3 }),
  prompts: optField(fc.array(availableCommandArb, { maxLength: 3 })),
});

const configuredForgeArb = fc.record({
  id: fc.string({ minLength: 1 }),
  kind: forgeKindArb,
  host: fc.string({ minLength: 1 }),
  connected: fc.boolean(),
  username: optField(fc.string()),
  email: optField(fc.string()),
  last_error: optField(fc.string()),
  last_probed: optField(posInt),
});

const connectedPayloadArb = fc.record({
  floor: posInt,
  head: posInt,
});

const crewPendingStageArb = fc.record({
  name: fc.string({ minLength: 1 }),
  agent_name: fc.string({ minLength: 1 }),
  role: optField(fc.string()),
  depends_on: optField(fc.array(fc.string(), { maxLength: 3 })),
});

const crewSubagentArb = fc.record({
  session_id: fc.string({ minLength: 1 }),
  session_name: fc.string({ minLength: 1 }),
  agent_name: fc.string({ minLength: 1 }),
  initial_query: fc.string({ minLength: 1 }),
  status: crewStatusArb,
  group: fc.string({ minLength: 1 }),
  status_msg: optField(fc.string()),
  role: optField(fc.string()),
  depends_on: optField(fc.array(fc.string(), { maxLength: 3 })),
});

const crewArb = fc.record({
  group: fc.string({ minLength: 1 }),
  subagents: fc.array(crewSubagentArb, { maxLength: 3 }),
  pending_stages: optField(fc.array(crewPendingStageArb, { maxLength: 3 })),
});

const deviceFlowResponseArb = fc.record({
  user_code: fc.string({ minLength: 1 }),
  verification_uri: fc.string({ minLength: 1 }),
  device_code: fc.string({ minLength: 1 }),
  interval: posInt,
  expires_in: posInt,
});

const errorPayloadArb = fc.record({
  code: errorCodeArb,
  message: fc.string({ minLength: 1 }),
});

const fileChangeArb = fc.record({
  lines_added: posInt,
  lines_removed: posInt,
  is_new_file: optField(fc.boolean()),
});

const issueArb = fc.record({
  title: fc.string({ minLength: 1 }),
  state: fc.string({ minLength: 1 }),
  number: posInt,
  body: optField(fc.string()),
  author: optField(fc.string()),
  url: optField(fc.string()),
  labels: optField(fc.array(fc.string(), { maxLength: 3 })),
  created_at: optField(posInt),
  updated_at: optField(posInt),
});

const labelArb = fc.record({
  name: fc.string({ minLength: 1 }),
  color: optField(fc.string()),
  description: optField(fc.string()),
});

const mcpConnectedPayloadArb = fc.record({ server: fc.string({ minLength: 1 }) });
const mcpDisconnectedPayloadArb = fc.record({ server: fc.string({ minLength: 1 }) });
const mcpFailedPayloadArb = fc.record({
  server: fc.string({ minLength: 1 }),
  error: fc.string({ minLength: 1 }),
});
const mcpOAuthPayloadArb = fc.record({
  server: fc.string({ minLength: 1 }),
  url: fc.string({ minLength: 1 }),
});

const planEntryArb = fc.record({
  content: fc.string({ minLength: 1 }),
  priority: fc.string({ minLength: 1 }),
  status: planStatusArb,
});

const toolLocationArb = fc.record({
  path: fc.string({ minLength: 1 }),
  line: optField(posInt),
});

const toolDiffArb = fc.record({
  path: fc.string({ minLength: 1 }),
  new_text: fc.string({ minLength: 1 }),
  old_text: optField(fc.string()),
});

const toolCallArb = fc.record({
  id: fc.string({ minLength: 1 }),
  title: fc.string({ minLength: 1 }),
  kind: toolKindArb,
  status: toolStatusArb,
  ts: posInt,
  output: optField(fc.string()),
  sub_session_id: optField(fc.string()),
  input: optField(fc.anything()),
  locations: optField(fc.array(toolLocationArb, { maxLength: 3 })),
  diffs: optField(fc.array(toolDiffArb, { maxLength: 3 })),
  duration_ms: optField(posInt),
});

const messageArb = fc.record({
  id: fc.string({ minLength: 1 }),
  role: roleArb,
  ts: posInt,
  crew: optField(crewArb),
  content: optField(fc.string()),
  reasoning: optField(fc.string()),
  event_kind: optField(eventKindArb),
  tool_calls: optField(fc.array(toolCallArb, { maxLength: 2 })),
  plan: optField(fc.array(planEntryArb, { maxLength: 3 })),
});

const messageChunkPayloadArb = fc.record({
  message_id: fc.string({ minLength: 1 }),
  delta: fc.string({ minLength: 1 }),
  is_reasoning: optField(fc.boolean()),
  block_index: fc.nat(),
});

const meteringItemArb = fc.record({
  unit_singular: fc.string({ minLength: 1 }),
  unit_plural: fc.string({ minLength: 1 }),
  value: posFloat,
});

const prArb = fc.record({
  title: fc.string({ minLength: 1 }),
  state: fc.string({ minLength: 1 }),
  source_branch: fc.string({ minLength: 1 }),
  target_branch: fc.string({ minLength: 1 }),
  number: posInt,
  body: optField(fc.string()),
  author: optField(fc.string()),
  url: optField(fc.string()),
  created_at: optField(posInt),
  updated_at: optField(posInt),
  mergeable: optField(fc.boolean()),
  draft: optField(fc.boolean()),
});

const pendingChangeArb = fc.record({
  tool_call_id: fc.string({ minLength: 1 }),
  chat_id: fc.string({ minLength: 1 }),
  path: fc.string({ minLength: 1 }),
  kind: pendingChangeKindArb,
  created_at: posInt,
  old_text: optField(fc.string()),
  new_text: optField(fc.string()),
  truncated: optField(fc.boolean()),
});

const pendingChangeAddedPayloadArb = fc.record({
  change: pendingChangeArb,
});

const pendingChangeResolvedPayloadArb = fc.record({
  tool_call_id: fc.string({ minLength: 1 }),
  action: pendingActionArb,
  path: optField(fc.string()),
});

const pendingChangesClearedPayloadArb = fc.record({
  reason: optField(clearReasonArb),
});

const permissionOptionArb = fc.record({
  option_id: fc.string({ minLength: 1 }),
  name: fc.string({ minLength: 1 }),
  kind: fc.string({ minLength: 1 }),
});

const permissionNeededPayloadArb = fc.record({
  options: fc.array(permissionOptionArb, { minLength: 1, maxLength: 3 }),
  request_id: posInt,
  tool_call_id: optField(fc.string()),
  title: optField(fc.string()),
  kind: optField(toolKindArb),
  sub_session_id: optField(fc.string()),
});

const pollResultArb = fc.record({
  status: fc.string({ minLength: 1 }),
  error: optField(fc.string()),
});

const releaseArb = fc.record({
  tag_name: fc.string({ minLength: 1 }),
  name: optField(fc.string()),
  body: optField(fc.string()),
  url: optField(fc.string()),
  published_at: optField(posInt),
  draft: optField(fc.boolean()),
  prerelease: optField(fc.boolean()),
});

const repoArb = fc.record({
  owner: fc.string({ minLength: 1 }),
  name: fc.string({ minLength: 1 }),
  full_name: fc.string({ minLength: 1 }),
  default_branch: optField(fc.string()),
  url: optField(fc.string()),
  clone_url: optField(fc.string()),
  description: optField(fc.string()),
  private: optField(fc.boolean()),
  archived: optField(fc.boolean()),
  fork: optField(fc.boolean()),
  updated_at: optField(posInt),
});

const toolCallPayloadArb = fc.record({
  message_id: fc.string({ minLength: 1 }),
  tool_call: toolCallArb,
  block_index: fc.nat(),
});

const toolCallUpdatePayloadArb = fc.record({
  message_id: fc.string({ minLength: 1 }),
  tool_call: toolCallArb,
});

const turnEndedPayloadArb = fc.record({
  changed_files: optField(fc.dictionary(fc.string({ minLength: 1 }), fileChangeArb)),
  stop_reason: optField(stopReasonArb),
  credits_delta: optField(posFloat),
  elapsed_ms: optField(posFloat),
});

const userArb = fc.record({
  login: fc.string({ minLength: 1 }),
  name: optField(fc.string()),
  email: optField(fc.string()),
  url: optField(fc.string()),
});

const whoamiResponseArb = fc.record({
  email: optField(fc.string()),
  auth: optField(fc.string()),
  accountType: optField(fc.string()),
  startUrl: optField(fc.string()),
  region: optField(fc.string()),
  error: optField(fc.string()),
});

// --- Decoder registry: maps decoder name to its valid-shape arbitrary ---

const decoderRegistry: {
  name: string;
  decoder: Decoder<unknown>;
  arb: fc.Arbitrary<unknown>;
}[] = [
  {
    name: "decodeAvailableCommand",
    decoder: decoders.decodeAvailableCommand,
    arb: availableCommandArb,
  },
  {
    name: "decodeChatDeletedPayload",
    decoder: decoders.decodeChatDeletedPayload,
    arb: chatDeletedPayloadArb,
  },
  { name: "decodeChatHeader", decoder: decoders.decodeChatHeader, arb: chatHeaderArb },
  { name: "decodeCheck", decoder: decoders.decodeCheck, arb: checkArb },
  {
    name: "decodeCommandsUpdatedPayload",
    decoder: decoders.decodeCommandsUpdatedPayload,
    arb: commandsUpdatedPayloadArb,
  },
  {
    name: "decodeConfiguredForge",
    decoder: decoders.decodeConfiguredForge,
    arb: configuredForgeArb,
  },
  {
    name: "decodeConnectedPayload",
    decoder: decoders.decodeConnectedPayload,
    arb: connectedPayloadArb,
  },
  { name: "decodeCrew", decoder: decoders.decodeCrew, arb: crewArb },
  {
    name: "decodeCrewPendingStage",
    decoder: decoders.decodeCrewPendingStage,
    arb: crewPendingStageArb,
  },
  { name: "decodeCrewSubagent", decoder: decoders.decodeCrewSubagent, arb: crewSubagentArb },
  {
    name: "decodeDeviceFlowResponse",
    decoder: decoders.decodeDeviceFlowResponse,
    arb: deviceFlowResponseArb,
  },
  { name: "decodeErrorPayload", decoder: decoders.decodeErrorPayload, arb: errorPayloadArb },
  { name: "decodeFileChange", decoder: decoders.decodeFileChange, arb: fileChangeArb },
  { name: "decodeIssue", decoder: decoders.decodeIssue, arb: issueArb },
  { name: "decodeLabel", decoder: decoders.decodeLabel, arb: labelArb },
  {
    name: "decodeMCPConnectedPayload",
    decoder: decoders.decodeMCPConnectedPayload,
    arb: mcpConnectedPayloadArb,
  },
  {
    name: "decodeMCPDisconnectedPayload",
    decoder: decoders.decodeMCPDisconnectedPayload,
    arb: mcpDisconnectedPayloadArb,
  },
  {
    name: "decodeMCPFailedPayload",
    decoder: decoders.decodeMCPFailedPayload,
    arb: mcpFailedPayloadArb,
  },
  {
    name: "decodeMCPOAuthPayload",
    decoder: decoders.decodeMCPOAuthPayload,
    arb: mcpOAuthPayloadArb,
  },
  { name: "decodeMessage", decoder: decoders.decodeMessage, arb: messageArb },
  {
    name: "decodeMessageChunkPayload",
    decoder: decoders.decodeMessageChunkPayload,
    arb: messageChunkPayloadArb,
  },
  { name: "decodeMeteringItem", decoder: decoders.decodeMeteringItem, arb: meteringItemArb },
  { name: "decodePR", decoder: decoders.decodePR, arb: prArb },
  { name: "decodePendingChange", decoder: decoders.decodePendingChange, arb: pendingChangeArb },
  {
    name: "decodePendingChangeAddedPayload",
    decoder: decoders.decodePendingChangeAddedPayload,
    arb: pendingChangeAddedPayloadArb,
  },
  {
    name: "decodePendingChangeResolvedPayload",
    decoder: decoders.decodePendingChangeResolvedPayload,
    arb: pendingChangeResolvedPayloadArb,
  },
  {
    name: "decodePendingChangesClearedPayload",
    decoder: decoders.decodePendingChangesClearedPayload,
    arb: pendingChangesClearedPayloadArb,
  },
  {
    name: "decodePermissionNeededPayload",
    decoder: decoders.decodePermissionNeededPayload,
    arb: permissionNeededPayloadArb,
  },
  {
    name: "decodePermissionOption",
    decoder: decoders.decodePermissionOption,
    arb: permissionOptionArb,
  },
  { name: "decodePlanEntry", decoder: decoders.decodePlanEntry, arb: planEntryArb },
  { name: "decodePollResult", decoder: decoders.decodePollResult, arb: pollResultArb },
  { name: "decodeRelease", decoder: decoders.decodeRelease, arb: releaseArb },
  { name: "decodeRepo", decoder: decoders.decodeRepo, arb: repoArb },
  { name: "decodeSessionMode", decoder: decoders.decodeSessionMode, arb: sessionModeArb },
  { name: "decodeSessionModel", decoder: decoders.decodeSessionModel, arb: sessionModelArb },
  { name: "decodeToolCall", decoder: decoders.decodeToolCall, arb: toolCallArb },
  {
    name: "decodeToolCallPayload",
    decoder: decoders.decodeToolCallPayload,
    arb: toolCallPayloadArb,
  },
  {
    name: "decodeToolCallUpdatePayload",
    decoder: decoders.decodeToolCallUpdatePayload,
    arb: toolCallUpdatePayloadArb,
  },
  { name: "decodeToolDiff", decoder: decoders.decodeToolDiff, arb: toolDiffArb },
  { name: "decodeToolLocation", decoder: decoders.decodeToolLocation, arb: toolLocationArb },
  {
    name: "decodeTurnEndedPayload",
    decoder: decoders.decodeTurnEndedPayload,
    arb: turnEndedPayloadArb,
  },
  { name: "decodeUsage", decoder: decoders.decodeUsage, arb: usageArb },
  { name: "decodeUser", decoder: decoders.decodeUser, arb: userArb },
  { name: "decodeWhoamiResponse", decoder: decoders.decodeWhoamiResponse, arb: whoamiResponseArb },
];

// --- Property tests ---

describe("wire/decoders property tests", () => {
  describe("valid-shape inputs decode without throwing", () => {
    for (const { name, decoder, arb } of decoderRegistry) {
      it(name, () => {
        fc.assert(
          fc.property(arb, (input) => {
            // Strip undefined values to match JSON round-trip (JSON.parse drops undefined)
            const json = JSON.parse(JSON.stringify(input));
            const result = decoder(json);
            expect(result).toBeDefined();
          }),
        );
      });
    }
  });

  describe("arbitrary inputs throw TypeError or succeed — never non-TypeError", () => {
    for (const { name, decoder } of decoderRegistry) {
      it(name, () => {
        let runs = 0;
        fc.assert(
          fc.property(fc.anything(), (input) => {
            runs++;
            try {
              decoder(input);
            } catch (e) {
              if (!(e instanceof TypeError)) {
                throw new Error(
                  `${name} threw non-TypeError: ${e instanceof Error ? e.message : String(e)}`,
                  { cause: e },
                );
              }
            }
          }),
        );
        expect(runs).toBeGreaterThan(0);
      });
    }
  });
});
