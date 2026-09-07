// ---------------------------------------------------------------------------
// What a tool card is built FROM, and the mapping from a domain tool call to it.
//
// Its own module, and that placement is the whole point rather than tidiness. Two
// surfaces build tool cards now — the transcript's reconcile spec and the run
// tab's step blocks — because a workflow step runs exactly the same tools a chat
// turn does, and the mapping between them is a pure field copy with no DOM in it.
// Keeping it beside `buildToolCard` looked natural and had one consequence: every
// test that mocks `tool-card.js` to avoid its graph (the editor openers, the diff
// renderer, the highlighter) mocks this away too, and a STUB of a field copy
// silently drops whichever field the case under test depended on. Measured: two
// terminal cases went green against a stub that returned `{}` and then failed on
// `output`, which is the field they exist to assert. A pure mapper nobody needs to
// mock cannot be stubbed by accident.
//
// Every field is a guarded assignment rather than a spread because
// `exactOptionalPropertyTypes` refuses `undefined` as a value for an optional
// property — which is also why this is worth having once instead of transcribed at
// each call site.
// ---------------------------------------------------------------------------

import type { ToolStatus, ToolLocation, ToolDiff, TextSpan, ToolCall } from "./types.js";
import type { ToolDenial, ToolDisclosed } from "./types.js";

export interface BuildToolCardOpts {
  id: string;
  title: string;
  kind: string;
  status: ToolStatus;
  input?: Record<string, unknown>;
  output?: string;
  /** Style spans for `output`, parsed server-side. Empty for output with no
   *  escape sequences, which is nearly all of it. */
  outputSpans?: TextSpan[];
  locations?: ToolLocation[];
  diffs?: ToolDiff[];
  /** Live mode: show spinner + start timestamp + show-raw-input block +
   *  expand-on-fail. Replay mode: omit those since the call has settled. */
  live: boolean;
  /** KAS's `_meta.kiro.disclosedContext`: the skill or steering document a
   *  `disclose_context` call loaded. */
  disclosed?: ToolDisclosed | undefined;
  /** KAS's `_meta.kiro.policyDenial`: the rule that refused this call. */
  denial?: ToolDenial | undefined;
  /** `input`, `output` and `diffs` above are a windowed PREVIEW and the whole of
   *  them is at `GET /api/chats/{id}/tools/{id}`. Set only by the transcript read
   *  path — a card built from the stream holds every byte already. */
  hasFull?: boolean;
  /** The full output's byte length and the full diff count, present only
   *  alongside `hasFull`: what the reader cannot see, so a control can offer it
   *  without first fetching it. */
  outputBytes?: number;
  diffCount?: number;
  /** Which chat to fetch the bulk from. Absent for a card with no chat behind it
   *  — a workflow step's, which streams and is never previewed. */
  chatID?: string;
}

/** The `BuildToolCardOpts` a domain tool call describes.
 *
 *  Every field is optional on the wire and `exactOptionalPropertyTypes` refuses
 *  `undefined` as a value for an optional property, so each one is a guarded
 *  assignment rather than a spread — which is why this is worth having once
 *  instead of at each caller. TWO callers: the transcript's reconcile spec
 *  (messages-tools.ts) and the run tab's step blocks (run-step-blocks.ts), and a
 *  step runs exactly the same tools as a chat turn does. The copy that existed
 *  before this had already been transcribed once; a third would be where a field
 *  like `denial` or `diffs` quietly stops reaching one of the two surfaces. */
export function toolCardOptsFor(tc: ToolCall, live: boolean, chatID = ""): BuildToolCardOpts {
  const opts: BuildToolCardOpts = {
    id: tc.id,
    title: tc.title,
    kind: tc.kind,
    status: tc.status,
    live,
  };
  if (chatID !== "") {
    opts.chatID = chatID;
  }
  if (tc.has_full === true) {
    opts.hasFull = true;
    if (tc.output_bytes !== undefined) {
      opts.outputBytes = tc.output_bytes;
    }
    if (tc.diff_count !== undefined) {
      opts.diffCount = tc.diff_count;
    }
  }
  const rawInput = tc.input as Record<string, unknown> | undefined;
  if (rawInput !== undefined) {
    opts.input = rawInput;
  }
  if (tc.output !== undefined) {
    opts.output = tc.output;
  }
  if (tc.output_spans !== undefined && tc.output_spans.length > 0) {
    opts.outputSpans = tc.output_spans;
  }
  if (tc.diffs !== undefined && tc.diffs.length > 0) {
    opts.diffs = tc.diffs;
  }
  if (tc.locations !== undefined && tc.locations.length > 0) {
    opts.locations = tc.locations;
  }
  if (tc.disclosed !== undefined) {
    opts.disclosed = tc.disclosed;
  }
  if (tc.denial !== undefined) {
    opts.denial = tc.denial;
  }
  return opts;
}
