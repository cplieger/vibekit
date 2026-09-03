// Property-based tests for the 44 generated wire decoders in decoders.gen.ts.
//
// Two invariants per decoder:
// 1. Round-trip identity: a valid-shape input (generated from the type schema)
//    decodes to a value deep-equal to the input — no field dropped, renamed,
//    or coerced.
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

const errorCodeArb = fc.constantFrom(
  "recovery_failed",
  "bridge_start_failed",
  "prompt_failed",
  "agent_not_found",
  "agent_config_error",
  "rate_limit",
  "switch_failed",
  "compaction_failed",
);
const eventKindArb = fc.constantFrom(
  "interrupted",
  "cancelled",
  "model_switched",
  "compacted",
  "compaction_failed",
);
const forgeKindArb = fc.constantFrom("github", "gitlab", "codeberg", "gitea");
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

// Dictionary-key guard for the round-trip identity test. Excludes "__proto__":
// JSON.parse materializes a "__proto__" key as an own property, but the
// generated decoders' decodeRecord does `out[k] = v`, and `out["__proto__"] =
// obj` sets the prototype rather than an own key — so the field silently
// vanishes from the decoded object and breaks deep-equality. Real wire payloads
// never key a map on "__proto__" (see QUESTIONS for the validators.ts note).
const notProto = (k: string): boolean => k !== "__proto__";

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
  source: optField(fc.string()),
});

const chatDeletedPayloadArb = fc.record({ id: fc.string({ minLength: 1 }) });

const chatHeaderArb = fc.record({
  name: fc.string({ minLength: 1 }),
  id: fc.string({ minLength: 1 }),
  usage: usageArb,
  created_at: posInt,
  updated_at: posInt,
  message_count: posInt,
  model: optField(fc.string()),
  acp_session_id: optField(fc.string()),
  current_mode_id: optField(fc.string()),
  compaction_watermark: optField(fc.string()),
  supervised_mode: optField(fc.boolean()),
});

const checkArb = fc.record({
  name: fc.string({ minLength: 1 }),
  status: fc.string({ minLength: 1 }),
  conclusion: fc.string({ minLength: 1 }),
  url: optField(fc.string()),
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
  agent_subtask_id: optField(fc.string()),
  input: optField(fc.anything()),
  locations: optField(fc.array(toolLocationArb, { maxLength: 3 })),
  diffs: optField(fc.array(toolDiffArb, { maxLength: 3 })),
  duration_ms: optField(posInt),
});

const messageArb = fc.record({
  id: fc.string({ minLength: 1 }),
  role: roleArb,
  ts: posInt,
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
  agent_subtask_id: optField(fc.string()),
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
  // check_status and merge_blocked are REQUIRED on the wire (no
  // omitempty in Go): their empty value is meaningful, so the server
  // always sends them and the decoder always demands them.
  check_status: fc.string(),
  merge_blocked: fc.string(),
  body: optField(fc.string()),
  author: optField(fc.string()),
  url: optField(fc.string()),
  head_sha: optField(fc.string()),
  checks_total: optField(posInt),
  checks_failing: optField(posInt),
  created_at: optField(posInt),
  updated_at: optField(posInt),
  mergeable: optField(fc.boolean()),
  draft: optField(fc.boolean()),
  auto_merge_armed: optField(fc.boolean()),
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

const elicitationPropertySchemaArb = fc.record({
  type: fc.string({ minLength: 1 }),
  title: optField(fc.string()),
  description: optField(fc.string()),
  format: optField(fc.string()),
  pattern: optField(fc.string()),
  enum: optField(fc.array(fc.string())),
  default: optField(fc.string()),
  minLength: optField(posInt),
  maxLength: optField(posInt),
});

const elicitationRequestSchemaArb = fc.record({
  type: optField(fc.string()),
  title: optField(fc.string()),
  description: optField(fc.string()),
  properties: optField(fc.dictionary(fc.string().filter(notProto), elicitationPropertySchemaArb)),
  required: optField(fc.array(fc.string())),
});

const elicitationNeededPayloadArb = fc.record({
  request_id: posInt,
  requested_schema: optField(elicitationRequestSchemaArb),
  mode: optField(fc.string()),
  message: optField(fc.string()),
  url: optField(fc.string()),
  tool_call_id: optField(fc.string()),
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
  changed_files: optField(
    fc.dictionary(fc.string({ minLength: 1 }).filter(notProto), fileChangeArb),
  ),
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
  state: fc.constantFrom("signed_in", "signed_out", "unavailable"),
  email: optField(fc.string()),
  auth: optField(fc.string()),
  accountType: optField(fc.string()),
  startUrl: optField(fc.string()),
  region: optField(fc.string()),
  reason: optField(fc.string()),
});

// --- Decoder registry: maps decoder name to its valid-shape arbitrary ---

const decoderRegistry: {
  name: string;
  decoder: Decoder<unknown>;
  arb: fc.Arbitrary<unknown>;
}[] = [
  {
    name: "decodeChatDeletedPayload",
    decoder: decoders.decodeChatDeletedPayload,
    arb: chatDeletedPayloadArb,
  },
  { name: "decodeChatHeader", decoder: decoders.decodeChatHeader, arb: chatHeaderArb },
  { name: "decodeCheck", decoder: decoders.decodeCheck, arb: checkArb },
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
  {
    name: "decodePermissionNeededPayload",
    decoder: decoders.decodePermissionNeededPayload,
    arb: permissionNeededPayloadArb,
  },
  {
    name: "decodeElicitationNeededPayload",
    decoder: decoders.decodeElicitationNeededPayload,
    arb: elicitationNeededPayloadArb,
  },
  {
    name: "decodeElicitationPropertySchema",
    decoder: decoders.decodeElicitationPropertySchema,
    arb: elicitationPropertySchemaArb,
  },
  {
    name: "decodeElicitationRequestSchema",
    decoder: decoders.decodeElicitationRequestSchema,
    arb: elicitationRequestSchemaArb,
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
  // Round-trip identity: decoding a valid JSON-shaped value preserves every
  // field verbatim. Strictly stronger than the old "result is defined" check —
  // a decoder that silently dropped a field, renamed one, or coerced a value
  // would pass "toBeDefined" but fail here. This is the highest-leverage
  // property for a generated codec (Wlaschin's round-trip). `toEqual` treats an
  // absent key and an `undefined`-valued key as equal, which matches the
  // JSON.parse(JSON.stringify(...)) normalization applied to the input first
  // (JSON drops `undefined`-valued optional fields).
  describe("valid-shape inputs round-trip without loss", () => {
    for (const { name, decoder, arb } of decoderRegistry) {
      it(name, () => {
        fc.assert(
          fc.property(arb, (input) => {
            const json: unknown = JSON.parse(JSON.stringify(input));
            expect(decoder(json)).toEqual(json);
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
