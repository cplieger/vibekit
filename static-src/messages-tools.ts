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
import { ensureToolCallSig, clearToolCallSig } from "./store-signals.js";
import { effect, el } from "@cplieger/reactive";
import type { ReconcileSpec } from "./reconcile.js";

import { maybeCollapseGroup, formatDuration, untrackInProgress } from "./tool-group.js";
import { isToolDone, type ToolKind } from "./tool-schema.js";
import { buildToolCard, insertDiffPreview, expandToolDetails, applyOutcome } from "./tool-card.js";
import { windowOutput, windowSpans } from "./strings.js";
import { renderOutput, appendOutput as appendOutputChunk } from "./output-render.js";
import { linkifyPaths } from "./linkify.js";
import { bindLoadingState } from "./actions/index.js";

// ---------------------------------------------------------------------------
// Module state (tool-specific)
// ---------------------------------------------------------------------------

/** Tool-call DOM elements, keyed on tool_call.id. */
const toolEls = new Map<string, HTMLDivElement>();

/** terminal_id → tool_call.id, for routing an agent terminal's live output to
 *  the card that spawned it. Populated from ToolCall.terminal_id, which arrives
 *  on the ACP `terminal` content block. */
const termToTool = new Map<string, string>();

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

/** Per-tool-call effect cleanups. Disposed on unmount or chat-switch. */
const toolEffects = new Map<string, () => void>();

function disposeToolEffect(id: string): void {
  const fn = toolEffects.get(id);
  if (fn !== undefined) {
    fn();
    toolEffects.delete(id);
  }
  clearToolCallSig(id);
}

export function disposeAllToolEffects(): void {
  for (const [id, fn] of toolEffects) {
    fn();
    clearToolCallSig(id);
  }
  toolEffects.clear();
  toolEls.clear();
  termToTool.clear();
  pendingChunks.clear();
}

/** Record a tool call's terminal link and settle anything already held for it.
 *  Idempotent: the same link arrives on every later update.
 *
 *  A hold is FLUSHED only when the tool call carries no output of its own. When
 *  it does, that output is the server's whole-stream snapshot and the card has
 *  already rendered it, so appending the held chunks would print the opening
 *  lines twice — reachable whenever a fast command's first frame arrives already
 *  completed while its early chunks were still waiting for a card to claim them.
 *  The snapshot supersedes the hold; the hold is dropped either way, because a
 *  second call must not flush what the first discarded. */
function linkTerminal(tc: ToolCall): void {
  const termID = tc.terminal_id;
  if (termID === undefined || termID === "" || termToTool.get(termID) === tc.id) {
    return;
  }
  termToTool.set(termID, tc.id);
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
 *  terminal is finished, rather than waiting for the chat to be disposed. */
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
  const toolID = termToTool.get(termID);
  if (toolID === undefined) {
    holdChunk(termID, text, spans, base);
    return;
  }
  const card = toolEls.get(toolID);
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

export const toolSpec: ReconcileSpec<ToolCall> = {
  key: (tc) => tc.id,
  mount: (tc) => {
    // Every tool call — including a subagent's nested tools — renders as a
    // real tool card. Subagent GROUPING (the invocation → a SubagentBlock
    // header, the nested tools → cards inside its body) is handled one level
    // up in messages-blocks.ts, keyed by agent_subtask_id, so this spec has no
    // subagent-specific branches.
    const opts: Parameters<typeof buildToolCard>[0] = {
      id: tc.id,
      title: tc.title,
      kind: tc.kind,
      status: tc.status,
      live: true,
    };
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
    const card = buildToolCard(opts);
    toolEls.set(tc.id, card);
    // After buildToolCard, deliberately: the card is what a held chunk would be
    // appended to, and whether the hold is flushed at all depends on the output
    // this build just rendered.
    linkTerminal(tc);

    const sig = ensureToolCallSig(tc.id, tc);
    let lastApplied = tc;
    const cleanup = effect(() => {
      const next = sig.value;
      if (next === lastApplied) {
        return;
      }
      applyToolCallUpdate(card, next);
      lastApplied = next;
    });
    toolEffects.set(tc.id, cleanup);
    return card;
  },
  update: (el, tc) => {
    applyToolCallUpdate(el as HTMLDivElement, tc);
  },
  onRemove: (_, key) => {
    disposeToolEffect(key);
    toolEls.delete(key);
  },
};

// ---------------------------------------------------------------------------
// Public helpers (used by messages.ts reconcile)
// ---------------------------------------------------------------------------

/** Update a tool call element (delegates to toolSpec.update). */
export function updateToolCall(el: HTMLElement, tc: ToolCall): void {
  if (toolSpec.update !== undefined) {
    toolSpec.update(el, tc);
  }
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

/** Apply a ToolCall snapshot's updatable fields to its DOM card. Idempotent. */
function applyToolCallUpdate(el: HTMLDivElement, tc: ToolCall): void {
  linkTerminal(tc);
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
