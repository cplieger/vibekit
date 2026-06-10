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
import {
  isSubAgent,
  isSubAgentActive,
  appendToSubAgent,
  createSubAgentCard,
  updateSubAgentCard,
} from "./subagent.js";
import { maybeCollapseGroup, formatDuration, untrackInProgress } from "./tool-group.js";
import { isToolDone } from "./tool-schema.js";
import { buildToolCard, insertDiffPreview } from "./tool-card.js";
import { ansiToHtml } from "./ansi.js";
import {
  addToolToCrewRow,
  getCrewToolEl,
  onCrewToolCompleted,
  setSubagentActivity,
} from "./crew-card.js";
import { formatToolActivity } from "./format-tool-activity.js";
import { bindLoadingState } from "./actions/index.js";
import { addEditActions } from "./messages-actions.js";

// ---------------------------------------------------------------------------
// Module state (tool-specific)
// ---------------------------------------------------------------------------

/** Tool-call DOM elements, keyed on tool_call.id. */
export const toolEls = new Map<string, HTMLDivElement>();

/** Per-tool-call effect cleanups. Disposed on unmount or chat-switch. */
const toolEffects = new Map<string, () => void>();

export function disposeToolEffect(id: string): void {
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
    if (isSubAgent(tc.title)) {
      const card = createSubAgentCard(
        tc.id,
        tc.status,
        tc.input as Record<string, unknown> | undefined,
        tc.output,
      );
      toolEls.set(tc.id, card);
      const sig = ensureToolCallSig(tc.id, tc);
      let lastApplied = tc;
      const cleanup = effect(() => {
        const next = sig.value;
        if (next === lastApplied) {
          return;
        }
        updateSubAgentCard(next.id, next.status, next.output);
        lastApplied = next;
      });
      toolEffects.set(tc.id, cleanup);
      return card;
    }

    if (isSubAgentActive()) {
      const preview = formatNestedToolPreview(tc);
      appendToSubAgent(preview);
      const placeholder = el("div", {
        className: "subagent-folded-tool",
        "data-tool-id": tc.id,
      }) as HTMLDivElement;
      toolEls.set(tc.id, placeholder);
      const sig = ensureToolCallSig(tc.id, tc);
      let last = tc;
      const cleanup = effect(() => {
        const next = sig.value;
        if (next === last) {
          return;
        }
        if (next.status !== last.status || (next.output ?? "") !== (last.output ?? "")) {
          appendToSubAgent(formatNestedToolUpdate(next, last));
        }
        last = next;
      });
      toolEffects.set(tc.id, cleanup);
      return placeholder;
    }

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

    if (tc.sub_session_id !== undefined && tc.sub_session_id !== "") {
      addToolToCrewRow(tc.sub_session_id, tc);
    }

    const sig = ensureToolCallSig(tc.id, tc);
    let lastApplied = tc;
    const cleanup = effect(() => {
      const next = sig.value;
      if (next === lastApplied) {
        return;
      }
      applyToolCallUpdate(card, next);
      mirrorToolUpdateToCrew(next);
      lastApplied = next;
    });
    toolEffects.set(tc.id, cleanup);
    return card;
  },
  update: (el, tc) => {
    if (el.classList.contains("subagent-call")) {
      updateSubAgentCard(tc.id, tc.status, tc.output);
      return;
    }
    applyToolCallUpdate(el as HTMLDivElement, tc);
    mirrorToolUpdateToCrew(tc);
  },
  onRemove: (_, key) => {
    disposeToolEffect(key);
    toolEls.delete(key);
  },
};

// ---------------------------------------------------------------------------
// Public helpers (used by messages.ts reconcile)
// ---------------------------------------------------------------------------

/** Build a tool call element (delegates to toolSpec.mount). */
export function buildToolCall(tc: ToolCall): HTMLElement {
  return toolSpec.mount(tc);
}

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

function formatNestedToolPreview(tc: ToolCall): string {
  const tag = tc.kind !== undefined ? `[${tc.kind}]` : "[tool]";
  return `\n${tag} ${tc.title}\n`;
}

function formatNestedToolUpdate(next: ToolCall, prev: ToolCall): string {
  if (next.status === prev.status) {
    return "";
  }
  if (next.status !== "completed" && next.status !== "failed") {
    return "";
  }
  const verb = next.status === "completed" ? "✓" : "✗";
  const out = (next.output ?? "").trim();
  const tail = out !== "" ? `: ${out.slice(0, 80)}${out.length > 80 ? "…" : ""}` : "";
  return `  ${verb} ${next.title}${tail}\n`;
}

function mirrorToolUpdateToCrew(tc: ToolCall): void {
  const crewEl = getCrewToolEl(tc.id);
  if (crewEl === undefined) {
    return;
  }
  applyToolCallUpdate(crewEl, tc);
  if (tc.sub_session_id !== undefined && tc.sub_session_id !== "") {
    if (tc.status === "completed" || tc.status === "failed") {
      onCrewToolCompleted(tc.sub_session_id);
    } else if (tc.title !== undefined) {
      setSubagentActivity(tc.sub_session_id, formatToolActivity(tc.title));
    }
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

function applyOutputUpdate(card: HTMLDivElement, output: string): void {
  const box = card.querySelector(".tool-output-box");
  if (box !== null) {
    const pre = box.querySelector("pre");
    if (pre !== null) {
      pre.insertAdjacentHTML("beforeend", ansiToHtml(output));
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
    existingPre.insertAdjacentHTML("beforeend", ansiToHtml(output));
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
