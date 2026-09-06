// ---------------------------------------------------------------------------
// The KAS-route half of a run's step transcript: read on demand, per step.
//
// A run's step transcript arrives by three different routes and this is the third,
// which is the one that works whatever happened before:
//
//   run_step frames   a PARENTLESS run's live content. Live-only — nothing stores
//                     it — so a reload loses it and a review of a finished run
//                     never had it.
//   the chat slice    a CHAT-PARENTED run's blocks, which land in the launching
//                     chat's file (`run-step-slice.ts`). Durable, but only while
//                     this client holds that chat's message WINDOW: a chat never
//                     opened here, or one whose window has paged the run's turn
//                     out, projects nothing.
//   THIS MODULE       `GET /api/runs/{id}/steps/{path...}`, which loads the step's
//                     own KAS session and projects its replay server-side. It does
//                     not care which route launched the run or what this client
//                     happens to hold.
//
// So it is the PREFERRED source when the slice is empty, and it is what makes the
// two launch routes read alike in the run tab rather than merely similar.
//
// DOM-FREE and fetch-owning, like `run-store.ts`: the page reads it inside its own
// effect. It is deliberately NOT part of `run-store.ts`, because that module is the
// one owner of `GET /api/runs/{id}` and its per-run signals exist for many cards on
// screen at once; this is a different endpoint with a different key and a different
// repaint shape (see stepTranscriptVersion).
//
// IT ACCUMULATES NOTHING FROM EVENTS. There is no `run_step` path in here and no
// merge with one: the endpoint is the whole source, and a settled answer is never
// refetched.
// ---------------------------------------------------------------------------

import { signal } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import { apiGetTypedOrError } from "./api-client.js";
import { decodeRunStepTranscript } from "./wire/decoders.gen.js";
import { parseStepSubtask } from "./step-subtask.js";
import type { RunStepSlice } from "./run-step-slice.js";
import type { Block, Message, RunStepTranscriptState, ToolCall } from "./types.js";

/** One step's read, as this module holds it.
 *
 *  TWO members are this side's own and neither is on the wire (`RunStepTranscriptState`
 *  is unchanged, so nothing here needs regenerating): `loading` means a request is
 *  outstanding, and `unaddressable` means the SERVER refused the address with a 4xx,
 *  which only this side can distinguish from a transport failure. Both are members of
 *  the same union rather than flags beside it, so the consumer's branch is total. */
export type StepReadState = "loading" | "unaddressable" | RunStepTranscriptState;

/** One cache entry: the verdict, plus the content when there is any. */
export interface StepRead {
  readonly state: StepReadState;
  readonly blocks: Block[];
  readonly toolCalls: ToolCall[];
}

/** ONE signal for every step read, bumped when a fetch RESOLVES.
 *
 *  One rather than one per step, which is the inverse of `run-store.ts`'s per-run
 *  signals — and the reason those exist does not apply here. That module is read by
 *  a transcript full of run cards, so a bump for run A must not repaint run B's
 *  card. This module is read by the run PAGE, which shows one node at a time, so a
 *  coarse bump costs exactly one repaint of the page the reader is looking at.
 *
 *  Bumped on resolution rather than on request: the request is what the caller just
 *  did, so it needs no telling. */
export const stepTranscriptVersion = signal(0);

/** The cache, keyed by (workflow, node path). */
const reads = new Map<string, StepRead>();

/** In-flight keys, so a repaint during a fetch cannot start a second one.
 *
 *  Separate from the cache rather than a `loading` entry in it, because the two
 *  answer different questions: the cache answers "what do we know", this answers
 *  "is a request outstanding". Folding them would make the no-refetch rule depend
 *  on an entry a failed fetch has to remember to clean up. */
const inFlight = new Set<string>();

/** The cache key.
 *
 *  keyenc `join`, the fleet rule for a composite key over text this app does not
 *  author — a node path is arbitrary text from a workflow file, joined with "/".
 *  Honest scope: with the arbitrary component LAST, a template literal would not
 *  actually collide for the ids KAS mints, so this removes the question rather than
 *  answering a live defect, and it keeps holding if a third component is ever
 *  added. */
function readKey(workflowID: string, nodePath: string): string {
  return join(workflowID, nodePath);
}

/** What this client knows about one step's transcript, or undefined for a step
 *  nobody has asked about. */
export function stepRead(workflowID: string, nodePath: string): StepRead | undefined {
  return reads.get(readKey(workflowID, nodePath));
}

/** Drop every read. Called from the page's own mount, which is this cache's whole
 *  bound: a run tab retargeting or closing is when the entries stop being wanted,
 *  and there is no other moment a step's answer becomes wrong. */
export function clearStepTranscripts(): void {
  reads.clear();
  inFlight.clear();
}

/** The URL for one step read.
 *
 *  Segments are `encodeURIComponent`-ed INDIVIDUALLY and rejoined with a raw "/",
 *  which is the one encoding rule this route has: the separators must stay raw
 *  because a node path contains them and `internal/server`'s canonical-path gate
 *  refuses an encoded spelling (it compares the DECODED path against what ServeMux
 *  would route), while a segment's own `#`, space or `%` has to be encoded or the
 *  path is truncated at the fragment or mis-split.
 *
 *  Measured against Go's ServeMux: `{path...}` hands the handler the DECODED
 *  remainder, so the server compares it to the run tree's own join as it stands. */
function stepURL(workflowID: string, nodePath: string): string {
  const path = nodePath.split("/").map(encodeURIComponent).join("/");
  return `/api/runs/${encodeURIComponent(workflowID)}/steps/${path}`;
}

/** Whether an answer is settled — never worth asking again.
 *
 *  `unavailable` is the one verdict that is NOT settled: it means the read could not
 *  be completed, which is transient by definition. That retry is bounded by the
 *  CALLER arming a read once per shown (node, state) rather than once per repaint,
 *  which is the run page's own gate; there is no backoff here, because a repaint is
 *  not a retry loop.
 *
 *  `unaddressable` IS settled, and that is what the state exists for: the server
 *  refused the address itself, so asking again fails identically and a retry the
 *  reader can spend is a lie about what it costs.
 *
 *  `loading` is deliberately absent, and the reason is that it would decide nothing:
 *  a `loading` entry exists exactly while its key is in `inFlight` (both are written
 *  together and every answer path overwrites both), so the in-flight gate always
 *  returns first and a `loading` arm here is a condition no behaviour can depend on.
 *  Naming it would read as a second guard while being unfalsifiable. */
function settled(state: StepReadState): boolean {
  return state === "ready" || state === "gone" || state === "unaddressable";
}

/** Ask for one step's transcript, unless the answer is already settled or a request
 *  is outstanding.
 *
 *  Fire-and-forget: the answer arrives through the cache plus one version bump, so
 *  a caller inside a reactive effect never awaits it. */
export function requestStepTranscript(workflowID: string, nodePath: string): void {
  if (workflowID === "" || nodePath === "") {
    return;
  }
  const key = readKey(workflowID, nodePath);
  if (inFlight.has(key)) {
    return;
  }
  const known = reads.get(key);
  if (known !== undefined && settled(known.state)) {
    return;
  }
  inFlight.add(key);
  reads.set(key, { state: "loading", blocks: [], toolCalls: [] });
  void fetchStep(key, workflowID, nodePath);
}

/** Perform one read and record its answer.
 *
 *  `apiGetTypedOrError` rather than `apiGetTyped`, because the STATUS is what
 *  separates a settled refusal from a transient one and the collapsing helper hands
 *  back one null for both. Its own doc names this defect class. */
async function fetchStep(key: string, workflowID: string, nodePath: string): Promise<void> {
  try {
    const r = await apiGetTypedOrError(stepURL(workflowID, nodePath), decodeRunStepTranscript);
    const read =
      r.ok && r.data !== null ? projectRead(r.data.state, r.data.messages) : failedRead(r.status);
    reads.set(key, read);
  } catch {
    // A THROW never reached the server, so it takes the transient verdict via
    // status 0. Caught rather than left to propagate for two reasons: the caller
    // `void`s this promise (it is fire-and-forget by design), so an escaping
    // rejection is an unhandled one; and the entry would otherwise keep claiming
    // `loading` with no request behind it.
    reads.set(key, failedRead(0));
  } finally {
    // In the `finally`, so neither answer path can leave a key in flight and stop
    // that step ever being asked about again.
    inFlight.delete(key);
    stepTranscriptVersion.value = stepTranscriptVersion.peek() + 1;
  }
}

/** The verdict for a read that produced no content, keyed on the HTTP status.
 *
 *  A 4xx on this route is SETTLED: `handleStepTranscript`'s only 4xx answers are 400
 *  (missing id, missing path, a path whose first segment is not this run's id) and
 *  404 (`errStepUnknown`), plus a 405 for a method this client never sends, and
 *  asking again cannot change any of them. No route under `/api/runs` emits a 429,
 *  so an unknown future 4xx landing here is the safe direction and a rate limiter
 *  arriving would need its own handling.
 *
 *  Everything else keeps `unavailable`: a 5xx, a status-0 transport failure, and an
 *  undecodable body, which arrives on the failure side carrying its real 2xx status
 *  and is therefore graded transient rather than as the caller's mistake. */
function failedRead(status: number): StepRead {
  const settledRefusal = status >= 400 && status < 500;
  return { state: settledRefusal ? "unaddressable" : "unavailable", blocks: [], toolCalls: [] };
}

/** Flatten a reply's ASSISTANT messages into one step's blocks and tool calls.
 *
 *  The role test is the PROJECTION rule rather than a guard: this function turns a
 *  message LIST into one flat block list, and `role` is the only field that says
 *  whether a message's blocks are the step's output. Without it a step's own prompt
 *  would render as its answer.
 *
 *  The server filters too (`assistantRows`), and that is not a duplicated fact: the
 *  endpoint owns what a step transcript CONTAINS — which is what keeps the prompt off
 *  the wire, since the pane already shows it as its Instruction row — while this owns
 *  what the block stream RENDERS. Note the bundle is embedded in the binary that
 *  serves the endpoint, so the two cannot version apart; the reason this reads `role`
 *  is the projection, not drift.
 *
 *  Blocks are COPIES: the `agent_subtask_id` rewrite below must not mutate what the
 *  decoder handed back, and a copy costs one object per block on a path that runs
 *  once per read. */
function projectRead(state: RunStepTranscriptState, messages: Message[]): StepRead {
  const blocks: Block[] = [];
  const toolCalls: ToolCall[] = [];
  for (const m of messages) {
    if (m.role !== "assistant") {
      continue;
    }
    for (const b of m.blocks ?? []) {
      blocks.push(stripStepSubtask(b));
    }
    for (const tc of m.tool_calls ?? []) {
      toolCalls.push(tc);
    }
  }
  return { state, blocks, toolCalls };
}

/** Clear a block's `agent_subtask_id` ONLY when it names a workflow step.
 *
 *  A REAL difference from `run-step-slice.ts`, which clears every id, and the
 *  asymmetry is not an oversight. On the chat route every selected block carries the
 *  step's own `wf:` id by construction — that field is what selected it — so
 *  clearing unconditionally loses nothing. Here the blocks come from the step's OWN
 *  session, which can also hold a DELEGATE's uuid id, and the dispatcher reads that
 *  field: clearing a delegate's id would collapse its box into the step's prose,
 *  while leaving a `wf:` id would have the block DROPPED outright, because
 *  `placeBlock` renders no step content anywhere.
 *
 *  `parseStepSubtask` rather than the prefix test, deliberately: a malformed `wf:`
 *  id parses to null and is KEPT, which is the same fall-through the transcript
 *  takes — the renderer draws it as a delegate box rather than losing the block. */
function stripStepSubtask(b: Block): Block {
  if (b.agent_subtask_id === undefined || parseStepSubtask(b.agent_subtask_id) === null) {
    return b;
  }
  const { agent_subtask_id: _dropped, ...rest } = b;
  return rest;
}

/** One step's read as the render lifecycle takes it, or undefined when there is
 *  nothing to render.
 *
 *  `sourceKeys` is EMPTY and that is a fact rather than a gap: these blocks are not
 *  in the message store, so they have no per-(message, block) streaming signals to
 *  subscribe to. `live` is false for the same reason — the answer is a settled read,
 *  not a stream, so a caret over it would claim content is still arriving. */
export function stepSliceFor(workflowID: string, nodePath: string): RunStepSlice | undefined {
  const read = reads.get(readKey(workflowID, nodePath));
  if (read?.state !== "ready" || read.blocks.length === 0) {
    return undefined;
  }
  return { blocks: read.blocks, toolCalls: read.toolCalls, sourceKeys: [], live: false };
}
