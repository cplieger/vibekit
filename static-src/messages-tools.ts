// ---------------------------------------------------------------------------
// Tool-call rendering: build, update, and lifecycle management for tool cards.
//
// Extracted from messages.ts — the "Tool calls" section (lines 824-1133).
// This module owns the ReconcileSpec for tool calls, the per-tool signal
// effects, and the lifecycle finalization on turn end.
//
// Defensive optional-field checks on tool-call payloads: the @typescript-eslint/no-unnecessary-condition
// rule sees ToolCall.* fields as non-nullable from the wire types, but
// the wire decoder only marks REQUIRED fields non-optional and many
// status updates arrive with subsets — the runtime checks ARE necessary.
/* eslint-disable @typescript-eslint/no-unnecessary-condition */
// subscription lifecycle, and the DOM update helpers (status, title, output,
// diffs). The parent (messages.ts) passes module-internal state via init().
// ---------------------------------------------------------------------------

import type { ToolCall, ToolStatus, ToolDiff, TextSpan } from "./types.js";
import { ensureToolCallSig, clearToolCallSig, toolCallSigKey } from "./store-signals.js";
import { effect, el } from "@cplieger/reactive";
import type { ReconcileSpec } from "./reconcile.js";

import {
  maybeCollapseGroup,
  formatDuration,
  trackInProgress,
  untrackInProgress,
} from "./tool-group.js";
import { isToolDone, type ToolKind } from "./tool-schema.js";
import { buildToolCard, insertDiffPreview, expandToolDetails, applyOutcome } from "./tool-card.js";
import { toolCardOptsFor } from "./tool-card-opts.js";
import { windowOutput, windowSpans } from "./strings.js";
import { renderOutput, appendOutput as appendOutputChunk } from "./output-render.js";
import { linkifyPaths } from "./linkify.js";
import { bindLoadingState } from "./actions/index.js";

// ---------------------------------------------------------------------------
// Module state (tool-specific)
// ---------------------------------------------------------------------------

/** Tool-call DOM elements, keyed on `toolCallSigKey(chatID, toolID)`.
 *
 *  COMPOSITE, not the bare `tool_call.id`: a tool call id is backend-authored
 *  and carries no uniqueness guarantee across chats (the identity contract
 *  store-signals.ts documents). Bare-id keying was survivable only while a chat
 *  switch disposed the old view; with parked views resident, two chats'
 *  identical tool ids would corrupt each other's cards. */
const toolEls = new Map<string, HTMLDivElement>();

/** What a terminal id resolves to: the tool call that claimed it AND the chat
 *  that owns the card, because the `toolEls` lookup needs both halves of the
 *  composite key. The KEY stays the bare terminal id — vibekit mints terminal
 *  ids, so they are globally unique where tool ids are not. */
interface TermLink {
  chatID: string;
  toolID: string;
}

/** terminal_id → its claiming tool call, for routing an agent terminal's live
 *  output to the card that spawned it. Populated from ToolCall.terminal_id,
 *  which arrives on the ACP `terminal` content block. */
const termToTool = new Map<string, TermLink>();

/** Live chunks that arrived before a card claimed their terminal id, per id.
 *
 *  Needed because the two facts arrive in the wrong order: KAS creates the
 *  terminal and its first output lands BEFORE the tool_call_update that names
 *  the terminal on the tool call. Without a short hold, the opening lines of
 *  every command would be missing from the live view and only appear at
 *  completion, when the server's authoritative output replaces it. */
interface PendingHold {
  chunks: { text: string; spans: TextSpan[]; base: number }[];
  /** Characters held, tracked rather than recomputed so appending N chunks is
   *  linear instead of quadratic. */
  chars: number;
  /** Set once a chunk did not fit under the cap. Nothing further is accepted
   *  after that, so the hold is always a contiguous PREFIX of the stream —
   *  see holdChunk for why a hole would be invisible. */
  full: boolean;
}
const pendingChunks = new Map<string, PendingHold>();

/** Cap on held characters per terminal. A command can produce megabytes before
 *  its link arrives; the completion snapshot is authoritative anyway, so the
 *  hold only has to cover the opening moments. */
const PENDING_CHARS_CAP = 64 * 1024;

/** Cap on how many terminals may be held at once.
 *
 *  A per-id cap alone is not a bound: a terminal that NEVER links keeps its
 *  entry until the whole chat is disposed, and nothing else evicts it, so a run
 *  of interrupted or unlinked commands retains PENDING_CHARS_CAP each without
 *  limit. When the cap is reached the OLDEST hold is dropped, since the newest
 *  is the one most likely still to find its card. */
const PENDING_TERMINALS_CAP = 16;

/** Cap on the live text a card's output region may hold before the authoritative
 *  snapshot replaces it at completion.
 *
 *  The surface this replaced capped at the same figure; without it a chatty
 *  long-running command grows the DOM for the life of the page. Trimming keeps
 *  the TAIL, matching the server ring and the old terminal pane: a build's last
 *  lines say how it ended. */
const LIVE_OUTPUT_CHARS_CAP = 256 * 1024;

/** Per-tool-call effect cleanups, keyed on `toolCallSigKey(chatID, toolID)`.
 *  Disposed on unmount or chat-view disposal; SUSPENDED (cleanup runs, entry
 *  stays with `cleanup: null`) while the owning view is parked, so resume can
 *  find the card and re-arm without a second registry.
 *
 *  The chat and tool id the card mounted for are recorded here rather than
 *  looked up at dispose time: the tool-call signal is keyed on (chat, call),
 *  and by the time a chat switch runs the teardown the active chat is already
 *  the NEW one, so a disposal that asked for it would clear the wrong key and
 *  leak the old chat's signals. */
interface ToolEffect {
  chatID: string;
  toolID: string;
  /** The live effect's disposer, or null while suspended (view parked). */
  cleanup: (() => void) | null;
}
const toolEffects = new Map<string, ToolEffect>();

/** Whether a chat's transcript view is parked. Injected by messages.ts (the
 *  multiplexer) so terminal output for a parked view buffers instead of
 *  writing DOM — this module sits below messages.ts and cannot import it. */
let _isChatParked: (chatID: string) => boolean = () => false;

/** Live terminal output that arrived while its card's view was PARKED, per
 *  terminal id. Drained into the card exactly once, at resume. Bounded like a
 *  shell scrollback: drop-OLDEST on overflow, so the resume shows the newest
 *  output — the opposite end from `pendingChunks`' prefix hold, because a
 *  pre-claim hold protects the OPENING lines until the authoritative snapshot
 *  lands, while a parked buffer stands in for a live tail. */
interface ParkedTermBuffer {
  chunks: { text: string; spans: TextSpan[]; base: number }[];
  /** Characters held; tracked so overflow trimming is linear. */
  chars: number;
}
const parkedTermBuffers = new Map<string, ParkedTermBuffer>();

/** Cap on characters buffered per parked terminal. The completion snapshot is
 *  authoritative anyway; the buffer only bridges the parked window. */
const PARKED_TERM_CHARS_CAP = 64 * 1024;

function bufferParkedChunk(termID: string, text: string, spans: TextSpan[], base: number): void {
  let buf = parkedTermBuffers.get(termID);
  if (buf === undefined) {
    buf = { chunks: [], chars: 0 };
    parkedTermBuffers.set(termID, buf);
  }
  buf.chunks.push({ text, spans, base });
  buf.chars += text.length;
  // Whole-chunk granularity: a chunk's spans are rebased against its own text,
  // so splitting one would mean re-deriving span offsets. A single chunk larger
  // than the cap therefore stays whole; SSE terminal chunks are far smaller.
  while (buf.chars > PARKED_TERM_CHARS_CAP && buf.chunks.length > 1) {
    const dropped = buf.chunks.shift();
    buf.chars -= dropped?.text.length ?? 0;
  }
}

/** Drain every parked terminal buffer owned by `chatID` into its card, once.
 *  Called by the multiplexer at unpark, after the view is active again — the
 *  replay goes through `appendTerminalChunk`, which now finds the view live
 *  and writes the card. */
export function drainParkedTerminals(chatID: string): void {
  for (const [termID, buf] of [...parkedTermBuffers]) {
    if (termToTool.get(termID)?.chatID !== chatID) {
      continue;
    }
    parkedTermBuffers.delete(termID);
    for (const chunk of buf.chunks) {
      appendTerminalChunk(termID, chunk.text, chunk.spans, chunk.base);
    }
  }
}

export function initToolViewCallbacks(cbs: { isChatParked: (chatID: string) => boolean }): void {
  _isChatParked = cbs.isChatParked;
}

function disposeToolEffect(chatID: string, toolID: string): void {
  const key = toolCallSigKey(chatID, toolID);
  const entry = toolEffects.get(key);
  if (entry !== undefined) {
    entry.cleanup?.();
    toolEffects.delete(key);
    clearToolCallSig(entry.chatID, entry.toolID);
  }
  toolEls.delete(key);
}

export function disposeAllToolEffects(): void {
  for (const entry of toolEffects.values()) {
    entry.cleanup?.();
    clearToolCallSig(entry.chatID, entry.toolID);
  }
  toolEffects.clear();
  toolEls.clear();
  termToTool.clear();
  pendingChunks.clear();
  parkedTermBuffers.clear();
}

/** The real per-view dispose for one chat's tool state: effects (live or
 *  suspended), cards, signals, terminal links and parked buffers. `withinEl`
 *  is the ownership guard — a slot whose card lives OUTSIDE the disposed view
 *  (the subagent page renders the same (chat, tool) pairs) is skipped, so
 *  disposing a chat's transcript view cannot strip the page's live cards. */
export function disposeToolEffectsForChat(chatID: string, withinEl?: HTMLElement): void {
  for (const [key, entry] of [...toolEffects]) {
    if (entry.chatID !== chatID) {
      continue;
    }
    const card = toolEls.get(key);
    if (withinEl !== undefined && card !== undefined && !withinEl.contains(card)) {
      continue;
    }
    entry.cleanup?.();
    toolEffects.delete(key);
    toolEls.delete(key);
    clearToolCallSig(entry.chatID, entry.toolID);
    for (const [termID, link] of [...termToTool]) {
      if (link.chatID === chatID && link.toolID === entry.toolID) {
        termToTool.delete(termID);
        parkedTermBuffers.delete(termID);
      }
    }
  }
}

/** Suspend the live tool-card effects for the named calls (view parked): the
 *  effect is disposed, the card, the entry and the SIGNAL stay, so background
 *  `tool_call_update`s keep landing in the store with no DOM write behind
 *  them, and resume can re-read the latest snapshot. Also stops the shared
 *  duration ticker for the message's in-progress cards — that ticker writes
 *  text into the card every second, which a parked view must never receive. */
export function suspendToolEffectsFor(
  chatID: string,
  toolIDs: readonly string[],
  withinEl?: HTMLElement,
): void {
  for (const toolID of toolIDs) {
    const key = toolCallSigKey(chatID, toolID);
    const entry = toolEffects.get(key);
    if (entry?.cleanup === null || entry?.cleanup === undefined) {
      continue; // unknown, or already suspended
    }
    const card = toolEls.get(key);
    if (withinEl !== undefined && card !== undefined && !withinEl.contains(card)) {
      continue; // another surface (the subagent page) owns this slot
    }
    entry.cleanup();
    entry.cleanup = null;
    if (card !== undefined) {
      untrackInProgress(card);
    }
  }
}

/** Re-arm suspended tool-card effects (view unparked): apply the CURRENT
 *  signal snapshot to the card — everything that arrived while parked lands in
 *  one write — then subscribe again, and rejoin the duration ticker when the
 *  card is still running. Entries that are already live (a card mounted by the
 *  catch-up paint) are left alone, so a resume pass is idempotent. */
export function resumeToolEffectsFor(
  chatID: string,
  toolCalls: readonly ToolCall[],
  withinEl?: HTMLElement,
): void {
  for (const tc of toolCalls) {
    const key = toolCallSigKey(chatID, tc.id);
    const entry = toolEffects.get(key);
    if (entry?.cleanup !== null) {
      continue; // unknown, or already live (a card the catch-up paint mounted)
    }
    const card = toolEls.get(key);
    if (card === undefined) {
      continue;
    }
    if (withinEl !== undefined && !withinEl.contains(card)) {
      continue;
    }
    const sig = ensureToolCallSig(chatID, tc.id, tc);
    let lastApplied = sig.peek();
    applyToolCallUpdate(card, lastApplied, chatID);
    entry.cleanup = effect(() => {
      const next = sig.value;
      if (next === lastApplied) {
        return;
      }
      applyToolCallUpdate(card, next, chatID);
      lastApplied = next;
    });
    if (card.dataset["outcome"] === "running" && card.dataset["startMs"] !== undefined) {
      trackInProgress(card);
    }
  }
}

/** Record a tool call's terminal link and settle anything already held for it.
 *  Idempotent: the same link arrives on every later update. `chatID` is the
 *  card's owning chat — the value is chat-bearing so `appendTerminalChunk` can
 *  compose the composite `toolEls` key from the terminal id alone.
 *
 *  A hold is FLUSHED only when the tool call carries no output of its own. When
 *  it does, that output is the server's whole-stream snapshot and the card has
 *  already rendered it, so appending the held chunks would print the opening
 *  lines twice — reachable whenever a fast command's first frame arrives already
 *  completed while its early chunks were still waiting for a card to claim them.
 *  The snapshot supersedes the hold; the hold is dropped either way, because a
 *  second call must not flush what the first discarded. */
function linkTerminal(chatID: string, tc: ToolCall): void {
  const termID = tc.terminal_id;
  if (termID === undefined || termID === "") {
    return;
  }
  const existing = termToTool.get(termID);
  if (existing?.chatID === chatID && existing.toolID === tc.id) {
    return;
  }
  termToTool.set(termID, { chatID, toolID: tc.id });
  const held = pendingChunks.get(termID);
  if (held === undefined) {
    return;
  }
  pendingChunks.delete(termID);
  if (tc.output !== undefined && tc.output !== "") {
    return;
  }
  for (const chunk of held.chunks) {
    appendTerminalChunk(termID, chunk.text, chunk.spans, chunk.base);
  }
}

/** Forget a terminal that will never send more output. Called on terminal_exited
 *  so an unclaimed hold is released at the one moment the server tells us the
 *  terminal is finished, rather than waiting for the chat to be disposed. A
 *  parked buffer is deliberately KEPT: the card exists and its view will drain
 *  the buffer at resume — exiting only ends the stream, not the record. */
export function forgetTerminal(termID: string): void {
  pendingChunks.delete(termID);
}

/**
 * Append one live chunk of an agent terminal's output to the card that spawned
 * it. Called by terminal-stream.ts on every terminal_output event.
 *
 * This is the whole of what "agent terminals render inline" means: the terminal
 * is the tool call's, so the tool call's card is where its output goes. There is
 * no separate terminal surface to keep in sync with the transcript.
 *
 * The live text is provisional. At completion the server stamps the terminal's
 * full output onto the tool call and applyOutputUpdate replaces this content
 * with it, which is also what a page reload renders. So a chunk lost here costs
 * smoothness, never the record.
 */
export function appendTerminalChunk(
  termID: string,
  text: string,
  spans: TextSpan[],
  base: number,
): void {
  const link = termToTool.get(termID);
  if (link === undefined) {
    holdChunk(termID, text, spans, base);
    return;
  }
  if (_isChatParked(link.chatID)) {
    // The card's view is parked: no DOM may move under it. The chunk waits in
    // the bounded per-terminal buffer and lands at resume, newest-first-kept.
    bufferParkedChunk(termID, text, spans, base);
    return;
  }
  const card = toolEls.get(toolCallSigKey(link.chatID, link.toolID));
  if (card === undefined) {
    return;
  }
  const out = card.querySelector(".tool-output");
  if (out === null) {
    return;
  }
  // The card's own snapshot rendering owns the <pre>; create one only if no
  // update has built it yet, so the two paths never fight over the element.
  const existing = out.querySelector("pre");
  const pre = existing ?? (el("pre") as HTMLPreElement);
  if (existing === null) {
    out.appendChild(pre);
  }
  appendOutputChunk(pre, text, spans, base);
  trimToLiveCap(pre);
  linkifyPaths(pre, { insidePre: true });
}

function holdChunk(termID: string, text: string, spans: TextSpan[], base: number): void {
  let held = pendingChunks.get(termID);
  if (held === undefined) {
    // Evict the oldest hold first: Map iterates in insertion order, so the
    // first key is the least recently started terminal.
    while (pendingChunks.size >= PENDING_TERMINALS_CAP) {
      const oldest = pendingChunks.keys().next();
      if (oldest.done === true) {
        break;
      }
      pendingChunks.delete(oldest.value);
    }
    held = { chunks: [], chars: 0, full: false };
    pendingChunks.set(termID, held);
  }
  if (held.full || held.chars + text.length > PENDING_CHARS_CAP) {
    // Refusing everything after the first chunk that did not fit is what keeps
    // the hold a contiguous PREFIX. Skipping only the oversized chunk and taking
    // later smaller ones leaves a HOLE that nothing marks: each chunk is rebased
    // by its own `base`, so the two sides of the gap render as though they were
    // adjacent and the missing middle is invisible. A prefix is enough because
    // the completion snapshot replaces all of it.
    held.full = true;
    return;
  }
  held.chunks.push({ text, spans, base });
  held.chars += text.length;
}

/** Drop leading nodes until the element is under the live cap, keeping the tail.
 *
 *  Node-granular rather than character-exact: a node is a text run or one styled
 *  span, so dropping whole nodes keeps every remaining span's styling intact. An
 *  exact trim would have to split a node and re-derive its style. */
function trimToLiveCap(pre: HTMLElement): void {
  let total = pre.textContent?.length ?? 0;
  while (total > LIVE_OUTPUT_CHARS_CAP) {
    const first = pre.firstChild;
    if (first === null) {
      return;
    }
    total -= first.textContent?.length ?? 0;
    first.remove();
  }
}

// ---------------------------------------------------------------------------
// Callbacks injected by messages.ts at init time
// ---------------------------------------------------------------------------

let _pushBind: (key: string, unbind: () => void) => void = () => {
  /* default until init */
};
let _refreshGroupHeader: (group: HTMLElement) => void = () => {
  /* default until init */
};
let _explainError: (errorText: string, toolTitle: string) => Promise<string> = () =>
  Promise.resolve("");

// (svgTemplate was dropped from this contract: the failed-status chevron flip
// it fed now rides the tool-card disclosure controller's onToggle.)
export function initToolCallbacks(cbs: {
  pushBind: (key: string, unbind: () => void) => void;
  refreshGroupHeader: (group: HTMLElement) => void;
  explainError: (errorText: string, toolTitle: string) => Promise<string>;
}): void {
  _pushBind = cbs.pushBind;
  _refreshGroupHeader = cbs.refreshGroupHeader;
  _explainError = cbs.explainError;
}

// ---------------------------------------------------------------------------
// Tool-call ReconcileSpec
// ---------------------------------------------------------------------------

/** The tool-card spec for one CHAT.
 *
 *  Per chat rather than one module-level spec, because a card's signal is keyed
 *  on (chat, call): a tool call id is backend-authored and carries no uniqueness
 *  guarantee, so the mount and its writer have to agree on the chat or the card
 *  mounts, never updates, and shows a permanently pending spinner. The
 *  RECONCILE key stays the bare id — it only has to be unique within one
 *  message's body — while the module registries key on the composite. */
export function toolSpecFor(chatID: string): ReconcileSpec<ToolCall> {
  return {
    key: (tc) => tc.id,
    mount: (tc) => mountToolCall(chatID, tc),
    update: (el, tc) => {
      applyToolCallUpdate(el as HTMLDivElement, tc, chatID);
    },
    onRemove: (_, key) => {
      disposeToolEffect(chatID, key);
    },
  };
}

function mountToolCall(chatID: string, tc: ToolCall): HTMLDivElement {
  // Every tool call — including a subagent's nested tools — renders as a
  // real tool card. Subagent GROUPING (the invocation → a SubagentBlock
  // header, the nested tools → cards inside its body) is handled one level
  // up in messages-blocks.ts, keyed by agent_subtask_id, so this spec has no
  // subagent-specific branches.
  const opts = toolCardOptsFor(tc, true);
  const card = buildToolCard(opts);
  const key = toolCallSigKey(chatID, tc.id);
  toolEls.set(key, card);
  // After buildToolCard, deliberately: the card is what a held chunk would be
  // appended to, and whether the hold is flushed at all depends on the output
  // this build just rendered.
  linkTerminal(chatID, tc);

  const sig = ensureToolCallSig(chatID, tc.id, tc);
  let lastApplied = tc;
  const cleanup = effect(() => {
    const next = sig.value;
    if (next === lastApplied) {
      return;
    }
    applyToolCallUpdate(card, next, chatID);
    lastApplied = next;
  });
  toolEffects.set(key, { chatID, toolID: tc.id, cleanup });
  return card;
}

// ---------------------------------------------------------------------------
// Public helpers
// ---------------------------------------------------------------------------

/** Apply a ToolCall snapshot to an already-mounted card. Reads no signal; the
 *  chat id exists to stamp the terminal link with the card's owner. */
export function updateToolCall(el: HTMLElement, tc: ToolCall, chatID: string): void {
  applyToolCallUpdate(el as HTMLDivElement, tc, chatID);
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

/** Apply a ToolCall snapshot's updatable fields to its DOM card. Idempotent. */
function applyToolCallUpdate(el: HTMLDivElement, tc: ToolCall, chatID: string): void {
  linkTerminal(chatID, tc);
  if (tc.status !== undefined) {
    applyStatusUpdate(el, tc.status, tc.duration_ms, tc.id);
  }
  if (tc.title !== undefined) {
    applyTitleUpdate(el, tc.title);
  }
  if (tc.output !== undefined && tc.output !== "") {
    applyOutputUpdate(el, tc.output, tc.output_spans ?? []);
  }
  if (tc.diffs !== undefined && tc.diffs.length > 0) {
    applyDiffUpdate(el, tc.diffs);
  }
}

function applyStatusUpdate(
  card: HTMLDivElement,
  status: ToolStatus,
  serverDurationMs: number | undefined,
  toolId: string,
): void {
  // The outcome is the glyph's, through the one writer that owns that
  // vocabulary. This used to set `.tool-status` text to the wire enum, so a
  // finished card printed the word `completed`.
  applyOutcome(card, status, card.dataset["title"] ?? "", {
    kind: (card.dataset["kind"] ?? "other") as ToolKind,
    writesFile: false,
    filePath: card.dataset["filePath"] ?? "",
    fileBasename: card.dataset["filename"] ?? "",
    diffSources: null,
    mcp: null,
    // Both come from the DOM on this path: applyOutcome reads
    // dataset.denied for the refusal state, and a disclosed card's claim was
    // already written at build time.
    disclosed: null,
    denial: null,
  });
  const done = isToolDone(status);
  if (done) {
    card.querySelector(".tool-spinner")?.remove();
    untrackInProgress(card);
    const ms =
      serverDurationMs ??
      (() => {
        const start = card.dataset["startMs"];
        if (start === undefined) {
          return 0;
        }
        delete card.dataset["startMs"];
        return Date.now() - parseInt(start, 10);
      })();
    const dur = card.querySelector(".tool-duration");
    if (dur !== null && ms >= 1000) {
      dur.textContent = formatDuration(ms);
    }
    maybeCollapseGroup(card);
    const group = card.closest(".tool-group");
    if (group !== null) {
      _refreshGroupHeader(group as HTMLElement);
    }
  }
  if (status === "failed") {
    // Failed tools open their details so the error output is visible without
    // a click; the disclosure controller flips the chevron + ARIA itself.
    expandToolDetails(card);
    if (card.querySelector(".tool-explain-btn") === null) {
      const output = card.querySelector(".tool-output")?.textContent ?? "";
      if (output.trim() !== "") {
        const btn = el(
          "button",
          { type: "button", className: "tool-explain-btn" },
          "Explain this error",
        ) as HTMLButtonElement;
        _pushBind(
          toolId,
          bindLoadingState("messages.explain_error", btn, { pendingClass: "btn-loading" }),
        );
        btn.addEventListener("click", () => {
          void _explainError(output, card.dataset["title"] ?? "").then((explanation) => {
            if (explanation !== "") {
              btn.textContent = explanation;
              btn.className = "tool-explain-result";
            }
          });
        });
        card.appendChild(btn);
      }
    }
  }
}

function applyTitleUpdate(el: HTMLDivElement, title: string): void {
  const t = el.querySelector(".tool-title");
  if (t !== null) {
    const display = title.startsWith("Running: ") ? title.slice(9) : title;
    t.textContent = display;
    t.parentElement!.title = title; // eslint-disable-line @typescript-eslint/no-non-null-assertion
  }
}

/** Replace the tool card's rendered output with the latest cumulative output.
 *  kiro-cli sends the FULL output-so-far on every tool_call_update (server-side
 *  `tc.Output += output` then broadcasts the whole thing), NOT deltas — so the
 *  card's <pre> must be REPLACED, not appended. Appending each cumulative
 *  snapshot compounds it (two updates "A" then "AB" → "AAB"). Exported for
 *  unit testing. */
export function applyOutputUpdate(
  card: HTMLDivElement,
  output: string,
  spans: readonly TextSpan[] = [],
): void {
  const out = card.querySelector(".tool-output");
  if (out === null) {
    return;
  }
  // There is no `.tool-output-box` branch any more: the always-visible unwindowed
  // box is gone, so every kind's output lands in the one `.tool-output` region
  // inside the disclosure.
  //
  // A command's output is WINDOWED here too, not just at build time. Streaming
  // the middle of a 5,000-line build into the card would undo the window on the
  // first update, and the reveal control is what re-offers the full text.
  const windowed = card.dataset["depth1"] === "output";
  const shown = windowed
    ? windowOutput(output)
    : { text: output, elided: 0, kept: [{ from: 0, to: output.length, at: 0 }] };

  const existingPre = out.querySelector("pre");
  const pre = existingPre ?? el("pre");
  const paint = (text: string, s: readonly TextSpan[]): void => {
    renderOutput(pre, text, s);
    linkifyPaths(pre, { insidePre: true });
  };
  paint(shown.text, windowSpans(spans, shown.kept));
  if (existingPre === null) {
    out.appendChild(pre);
  }

  // Rebuild the reveal so its count tracks the growing output.
  out.querySelector(".tool-output-reveal")?.remove();
  if (shown.elided > 0) {
    const reveal = el(
      "button",
      { type: "button", className: "tool-output-reveal" },
      `Show ${String(shown.elided)} more line${shown.elided === 1 ? "" : "s"}`,
    );
    reveal.addEventListener("click", (e: Event) => {
      e.stopPropagation();
      paint(output, spans);
      reveal.remove();
    });
    out.appendChild(reveal);
  }
}

function applyDiffUpdate(el: HTMLDivElement, diffs: ToolDiff[]): void {
  if (el.querySelector(".tool-diff-preview") !== null) {
    return;
  }
  const d = diffs[0];
  if (d === undefined) {
    return;
  }
  insertDiffPreview(el, d.path, { oldText: d.old_text ?? "", newText: d.new_text });
}
