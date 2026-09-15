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
  /** The tool RAN CORRECTLY AND REFUSED — the fifth card outcome, and a different
   *  fact from `status`, which stays `completed` because the call is over. The
   *  REASON is not here: it is the tool's own `output`, which a declined card
   *  opens without a click. */
  declined?: boolean;
  /** `input`, `output` and `diffs` above are a windowed PREVIEW and the whole of
   *  them is at `GET /api/chats/{id}/tools/{id}`. Set only by the transcript read
   *  path — a card built from the stream holds every byte already. */
  hasFull?: boolean;
  /** The full output's byte length, present only alongside `hasFull`: what says
   *  the bulk holds output the reader cannot see, which is the `reveals` half of
   *  the deferred output piece. */
  outputBytes?: number;
  /** Which chat to fetch the bulk from. Absent for a card with no chat behind it
   *  — a workflow step's, which streams and is never previewed. */
  chatID?: string;
  /** Build the details region ALREADY OPEN, for a card whose region the reader had
   *  open before this render: a window drop and re-mount in the transcript, a step
   *  card the run tab rebuilds on every frame. The reader's state is its ONLY input —
   *  a failed call is born closed like any other, because the expand-on-fail courtesy
   *  is an event rather than a resting state. Never set by `toolCardOptsFor` — it is a
   *  fact about the render rather than about the call — and never a substitute for
   *  `expandToolDetails`, which stays the path for a region opened by an EVENT (a call
   *  that fails or is refused mid-stream) and must keep animating. */
  detailsOpen?: boolean;
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
  if (tc.declined === true) {
    opts.declined = true;
  }
  return opts;
}
