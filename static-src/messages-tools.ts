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

import type { ToolCall, ToolStatus, ToolDiff } from "./types.js";
import { ensureToolCallSig, clearToolCallSig } from "./store-signals.js";
import { effect, el } from "@cplieger/reactive";
import type { ReconcileSpec } from "./reconcile.js";
import { ICON_CHEVRON_UP } from "./icons.js";
import { maybeCollapseGroup, formatDuration, untrackInProgress } from "./tool-group.js";
import { isToolDone } from "./tool-schema.js";
import { buildToolCard, insertDiffPreview } from "./tool-card.js";
import { ansiToHtml } from "./ansi.js";
import { bindLoadingState } from "./actions/index.js";
import { addEditActions } from "./messages-actions.js";

// ---------------------------------------------------------------------------
// Module state (tool-specific)
// ---------------------------------------------------------------------------

/** Tool-call DOM elements, keyed on tool_call.id. */
const toolEls = new Map<string, HTMLDivElement>();

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
}

// ---------------------------------------------------------------------------
// Callbacks injected by messages.ts at init time
// ---------------------------------------------------------------------------

let _pushBind: (key: string, unbind: () => void) => void = () => {
  /* default until init */
};
let _svgTemplate: (markup: string) => () => Node = () => () => document.createDocumentFragment();
let _refreshGroupHeader: (group: HTMLElement) => void = () => {
  /* default until init */
};
let _explainError: (errorText: string, toolTitle: string) => Promise<string> = () =>
  Promise.resolve("");

export function initToolCallbacks(cbs: {
  pushBind: (key: string, unbind: () => void) => void;
  svgTemplate: (markup: string) => () => Node;
  refreshGroupHeader: (group: HTMLElement) => void;
  explainError: (errorText: string, toolTitle: string) => Promise<string>;
}): void {
  _pushBind = cbs.pushBind;
  _svgTemplate = cbs.svgTemplate;
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
    // up in messages-blocks.ts by contiguous agent_subtask_id runs, so this
    // spec has no subagent-specific branches.
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
    if (tc.diffs !== undefined && tc.diffs.length > 0) {
      opts.diffs = tc.diffs;
    }
    if (tc.locations !== undefined && tc.locations.length > 0) {
      opts.locations = tc.locations;
    }
    const card = buildToolCard(opts);
    toolEls.set(tc.id, card);

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
  if (tc.status !== undefined) {
    applyStatusUpdate(el, tc.status, tc.duration_ms, tc.id);
  }
  if (tc.title !== undefined) {
    applyTitleUpdate(el, tc.title);
  }
  if (tc.output !== undefined && tc.output !== "") {
    applyOutputUpdate(el, tc.output);
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
  const s = card.querySelector(".tool-status");
  if (s !== null) {
    s.textContent = status;
    s.className = `tool-status ${status}`;
  }
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
    if (status === "completed" && card.dataset["kind"] === "edit") {
      addEditActions(card);
    }
  }
  if (status === "failed") {
    card.querySelector(".tool-details")?.classList.remove("collapsed");
    const b = card.querySelector(".tool-toggle");
    if (b !== null) {
      b.textContent = "";
      b.appendChild(_svgTemplate(ICON_CHEVRON_UP)());
      b.setAttribute("aria-expanded", "true");
    }
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
export function applyOutputUpdate(card: HTMLDivElement, output: string): void {
  const box = card.querySelector(".tool-output-box");
  if (box !== null) {
    const pre = box.querySelector("pre");
    if (pre !== null) {
      pre.innerHTML = ansiToHtml(output);
    } else {
      const newPre = el("pre");
      newPre.innerHTML = ansiToHtml(output);
      box.appendChild(newPre);
    }
    box.scrollTop = box.scrollHeight;
    return;
  }
  const out = card.querySelector(".tool-output");
  if (out === null) {
    return;
  }
  const existingPre = out.querySelector("pre");
  if (existingPre !== null) {
    existingPre.innerHTML = ansiToHtml(output);
  } else {
    const pre = el("pre");
    pre.innerHTML = ansiToHtml(output);
    out.appendChild(pre);
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
