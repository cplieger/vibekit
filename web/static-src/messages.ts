// ---------------------------------------------------------------------------
// Message rendering primitives: user/assistant bubbles, tool cards, plans,
// system events, boundary dividers, pagination. DOM state only — no server
// or store coupling beyond the plan-card handoff buttons.
//
// Heavy lifting is delegated to focused modules:
//   - bubble.ts         assistant markdown pipeline (shared by live + replay)
//   - tool-card.ts      tool-call renderer (shared by live + replay)
//   - tool-schema.ts    kind/title → structured tool info
//   - plan-actions.ts   plan handoff (Edit / Run)
//   - scroll.ts, subagent.ts, tool-group.ts, permission.ts, linkify.ts, code-blocks.ts
// ---------------------------------------------------------------------------

import type { ToolStatus, ToolDiff, PlanEntry, Message, ToolCall, EventKind } from "./types.js";
import { MarkdownRenderer, renderBubble } from "./markdown.js";
import { escText } from "./strings.js";
import { ansiToHtml } from "./ansi.js";
import { getActiveId } from "./store.js";
import { ICON_CHEVRON_UP, ICON_COPY, ICON_COPY_MD, ICON_LINK, ICON_EXPORT } from "./icons.js";
import { $ } from "./dom.js";
import {
  scroll, getScrollEl, scrollToBottom, suppressScroll, setUserScrolledUp,
  trimOldMessages, resetScrollState, setLoadMore,
} from "./scroll.js";
import {
  isSubAgent, isSubAgentActive,
  appendToSubAgent, createSubAgentCard, updateSubAgentCard, resetSubAgents,
} from "./subagent.js";
import {
  breakToolGroup, getOrCreateToolGroup, maybeCollapseGroup, formatDuration,
  untrackInProgress,
} from "./tool-group.js";
import { linkifyPaths } from "./linkify.js";
import { isToolDone } from "./tool-schema.js";
import { buildToolCard, insertDiffPreview } from "./tool-card.js";
import { planToMarkdown, writePlanDraft, runPlan } from "./plan-actions.js";
import { openPlanDraftPath } from "./editor-openers.js";
import { copyClipboard, explainError as explainErrorAction } from "./actions/messages.js";
import {
  addEditActions, initMessageActions,
} from "./messages-actions.js";
import {
  addCrew as addCrewInternal, updateCrew as updateCrewInternal,
  buildCrewCardForReplay, clearCrews, addToolToCrewRow, getCrewToolEl,
  onCrewToolCompleted, setSubagentActivity,
} from "./crew-card.js";
import { formatToolActivity } from "./format-tool-activity.js";

export { getScrollEl, scrollToBottom, setLoadMore };
export { showPermissionDialog, hidePermission } from "./permission.js";
export { appendToSubAgent } from "./subagent.js";
export { setShellRunCallback } from "./code-blocks.js";

const messagesEl = $.messages;
const toolEls = new Map<string, HTMLDivElement>();

/** Tracks active "copied" animation timers per button so rapid clicks
 *  don't stack timeouts. */
const copyTimers = new WeakMap<HTMLElement, ReturnType<typeof setTimeout>>();

// --- Avatars ---

const KIRO_AVATAR = '<svg class="avatar-icon" width="17" height="20" viewBox="-2 -2 44 52" fill="none"><path d="M7.58762 37.203C2.62272 48.1978 13.1975 50.9578 20.9974 44.5229C23.2923 51.7378 31.8872 46.3529 34.9771 40.758C41.772 28.4282 39.027 15.8585 38.322 13.2635C33.4921 -4.42116 9.34259 -4.45116 5.18767 13.3535C4.21269 16.4734 4.19769 20.0134 3.6577 23.6883C3.3877 25.5483 3.17771 26.7332 2.47272 28.6832C2.05273 29.8082 1.49774 30.7982 0.597756 32.4781C-0.782218 35.0881 -0.197229 40.113 6.94263 37.503L7.61762 37.203H7.58762Z" fill="white" stroke="#9046FF" stroke-width="1.5"/><path d="M21.9284 20.928C19.9484 20.928 19.6484 18.5581 19.6484 17.1481C19.6484 15.8731 19.8734 14.8681 20.3084 14.2231C20.6834 13.6532 21.2384 13.3682 21.9284 13.3682C22.6184 13.3682 23.2184 13.6532 23.6384 14.2381C24.1184 14.8981 24.3733 15.9031 24.3733 17.1481C24.3733 19.518 23.4584 20.928 21.9434 20.928H21.9284Z" fill="#1e1e2e"/><path d="M30.0729 20.928C28.093 20.928 27.793 18.5581 27.793 17.1481C27.793 15.8731 28.018 14.8681 28.453 14.2231C28.8279 13.6532 29.3829 13.3682 30.0729 13.3682C30.7629 13.3682 31.3629 13.6532 31.7829 14.2381C32.2629 14.8981 32.5179 15.9031 32.5179 17.1481C32.5179 19.518 31.6029 20.928 30.0879 20.928H30.0729Z" fill="#1e1e2e"/></svg>';

const USER_AVATAR = '<svg class="avatar-icon" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="8" r="4"/><path d="M20 21a8 8 0 00-16 0"/></svg>';

/** Parse an SVG string once into a template, then clone on each use.
 *  Avoids repeated HTML parsing and is CSP-safe (no runtime innerHTML
 *  on user-visible paths after initial template creation). */
function svgTemplate(markup: string): () => Node {
  const tpl = document.createElement("template");
  tpl.innerHTML = markup;
  const content = tpl.content;
  return () => content.cloneNode(true);
}

const cloneKiroAvatar = svgTemplate(KIRO_AVATAR);
const cloneUserAvatar = svgTemplate(USER_AVATAR);

function makeRow(side: "user" | "assistant"): HTMLDivElement {
  const row = document.createElement("div");
  row.className = `msg-row msg-row-${side}`;
  const avatar = document.createElement("div");
  avatar.className = "msg-avatar";
  avatar.appendChild(side === "assistant" ? cloneKiroAvatar() : cloneUserAvatar());
  row.appendChild(avatar);
  return row;
}

/** Append an element to the message list with an entry animation.
 *  Every realtime-appended node (user message, assistant bubble,
 *  checkpoint, plan, tool call, subagent card, boundary divider,
 *  reasoning block, system message, turn summary) flows through this
 *  helper so the entire chat chrome shares one motion language: a
 *  4px upward slide + opacity fade, 200ms with `ease-out` easing.
 *
 *  Historical paths (prependMessages, the initial chat-switch bulk
 *  render) deliberately bypass this helper and insert directly into
 *  `messagesEl`, so scrolling through old content doesn't re-animate
 *  messages the user has already read. See vibekit-ui.md "Streaming
 *  smoothness" for the rationale. */
function appendMessage(el: HTMLElement): void {
  el.setAttribute("data-chat-entry", "");
  messagesEl.appendChild(el);
}

// --- User messages ---

/** Render a user message, optionally preceded by a checkpoint line.
 *  When `checkpointTag` is non-empty, the dashed divider above the
 *  bubble gets a Restore button that rolls the workspace back to the
 *  moment just before this turn. Pass "" to skip the divider (e.g.
 *  when the server reports no backing shadow-git snapshot for it). */
export function addUserMessage(text: string, checkpointTag?: string): void {
  breakToolGroup();
  if (checkpointTag !== undefined && checkpointTag !== "") {
    const cp = document.createElement("div");
    cp.className = "checkpoint-line";
    const label = document.createElement("span");
    label.className = "checkpoint-label";
    label.textContent = "Checkpoint";
    cp.appendChild(label);
    const btn = document.createElement("button");
    btn.className = "checkpoint-restore";
    btn.type = "button";
    btn.dataset["tag"] = checkpointTag;
    btn.title = "Restore to this point";
    btn.setAttribute("aria-label", `Restore to checkpoint ${checkpointTag}`);
    btn.textContent = "Restore";
    cp.appendChild(btn);
    appendMessage(cp);
  }
  const row = makeRow("user");
  const el = document.createElement("div");
  el.className = "message user";
  el.textContent = text;
  linkifyPaths(el);
  row.appendChild(el);
  appendMessage(row);
  trimOldMessages();
  // User messages always scroll into view, even if the user was reading history.
  setUserScrolledUp(false);
  suppressScroll(400);
  requestAnimationFrame(() => {
    requestAnimationFrame(() => { const el = getScrollEl(); el.scrollTop = el.scrollHeight; });
  });
}

// --- Assistant messages (streaming) ---

/** Encapsulates the streaming render lifecycle: debounced markdown
 *  re-renders, pending element tracking, and turn-actions row state.
 *  The class makes the implicit state machine (start → append → flush
 *  → finalize) explicit and provides a clean dispose path for chat
 *  switches (no stale timers firing after the active chat changes). */
class StreamingRenderPipeline {
  private renderTimer: ReturnType<typeof setTimeout> | undefined;
  private pendingRenderEl: HTMLDivElement | null = null;
  private activeTurnActions: HTMLDivElement | null = null;
  private readonly renderers = new WeakMap<HTMLDivElement, MarkdownRenderer>();

  private getRenderer(el: HTMLDivElement): MarkdownRenderer {
    let r = this.renderers.get(el);
    if (r === undefined) {
      r = new MarkdownRenderer(el);
      this.renderers.set(el, r);
    }
    return r;
  }

  start(el: HTMLDivElement): void {
    this.getRenderer(el);
  }

  append(el: HTMLDivElement, text: string): void {
    const existing = el.dataset["raw"] ?? "";
    const chunk = existing === "" ? text.replace(/^\s+/, "") : text;
    if (chunk === "") return;
    el.dataset["raw"] = existing + chunk;
    this.pendingRenderEl = el;
    if (this.renderTimer === undefined) {
      this.renderTimer = setTimeout(() => this.flush(), 200);
    }
  }

  private flush(): void {
    this.renderTimer = undefined;
    if (this.pendingRenderEl === null) return;
    const el = this.pendingRenderEl;
    this.pendingRenderEl = null;
    this.getRenderer(el).write(el.dataset["raw"] ?? "");
    scroll();
  }

  finalize(el: HTMLDivElement | null): void {
    if (this.renderTimer !== undefined) { clearTimeout(this.renderTimer); this.flush(); }
    if (el !== null) {
      const renderer = this.renderers.get(el);
      if (renderer === undefined) {
        throw new Error("finalizeAssistantEl: no renderer for el");
      }
      renderer.finalize();
      this.renderers.delete(el);
      el.classList.remove("streaming");
      addTurnActions(el, this);
    }
    for (const s of document.querySelectorAll(".message.assistant.streaming")) {
      s.classList.remove("streaming");
    }
  }

  removeActiveTurnActions(): void {
    this.activeTurnActions?.remove();
    this.activeTurnActions = null;
  }

  setActiveTurnActions(row: HTMLDivElement): void {
    this.activeTurnActions = row;
  }

  dispose(): void {
    if (this.renderTimer !== undefined) { clearTimeout(this.renderTimer); this.renderTimer = undefined; }
    this.pendingRenderEl = null;
    this.activeTurnActions = null;
  }
}

const streamingPipeline = new StreamingRenderPipeline();

export function startStreamingMessage(): HTMLDivElement {
  breakToolGroup();
  const row = makeRow("assistant");
  const el = document.createElement("div");
  el.className = "message assistant streaming";
  row.appendChild(el);
  appendMessage(row);
  // Prime the renderer so the first chunk doesn't pay the WeakMap
  // miss cost inside a debounce-frame budget.
  streamingPipeline.start(el);
  scroll();
  return el;
}

export function appendToAssistant(el: HTMLDivElement, text: string): void {
  streamingPipeline.append(el, text);
}

export function finalizeAssistantEl(el: HTMLDivElement | null): void {
  streamingPipeline.finalize(el);
}

/** Turn-actions row (copy-as-text, copy-as-markdown, copy-chat-id,
 *  export) rendered under the finalized assistant message. Styled like
 *  the checkpoint line: thin, faded, not inside the bubble. The row is
 *  merged into the turn-summary row if handlers/turn.ts has already
 *  appended one (so credits + elapsed sit on the same line as the
 *  buttons); otherwise it renders standalone and the summary will
 *  later insert itself via the "turn-summary-marker" anchor. */
function addTurnActions(el: HTMLDivElement, pipeline: StreamingRenderPipeline): void {
  // Remove the previous turn-actions row (O(1) direct removal).
  pipeline.removeActiveTurnActions();

  const raw = el.dataset["raw"] ?? el.textContent ?? "";
  if (raw.trim() === "") return;

  const chatID = getActiveId();

  const row = document.createElement("div");
  row.className = "turn-actions";

  // Left slot: left empty here so the turn-summary handler can place
  // the duration/credits label in it. Keeps a single row of chrome
  // below the bubble instead of stacking two.
  const leftSlot = document.createElement("span");
  leftSlot.className = "turn-actions-summary";
  row.appendChild(leftSlot);

  const rightSlot = document.createElement("span");
  rightSlot.className = "turn-actions-buttons";
  row.appendChild(rightSlot);

  const makeBtn = (
    svgMarkup: string, ariaLabel: string, onClick: (btn: HTMLButtonElement) => void,
  ): HTMLButtonElement => {
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "turn-action-btn";
    btn.appendChild(svgTemplate(svgMarkup)());
    btn.setAttribute("aria-label", ariaLabel);
    btn.setAttribute("data-tooltip", ariaLabel);
    btn.addEventListener("click", () => { onClick(btn); });
    return btn;
  };

  const copyAndAnimate = (btn: HTMLButtonElement, text: string): void => {
    void copyClipboard.dispatch(text, { silent: true }).then((r) => {
      if (r !== null) {
        btn.classList.add("copied");
        const prev = copyTimers.get(btn);
        if (prev !== undefined) clearTimeout(prev);
        copyTimers.set(btn, setTimeout(() => btn.classList.remove("copied"), 1500));
      }
    });
  };

  rightSlot.appendChild(makeBtn(ICON_COPY, "Copy as text", (btn) => {
    copyAndAnimate(btn, el.textContent ?? "");
  }));
  rightSlot.appendChild(makeBtn(ICON_COPY_MD, "Copy as markdown", (btn) => {
    copyAndAnimate(btn, raw);
  }));
  if (chatID !== "") {
    rightSlot.appendChild(makeBtn(ICON_LINK, "Copy chat ID", (btn) => {
      copyAndAnimate(btn, chatID);
    }));
    rightSlot.appendChild(makeBtn(ICON_EXPORT, "Export chat as JSON", () => {
      const a = document.createElement("a");
      a.href = `/api/chats/${encodeURIComponent(chatID)}/export`;
      a.download = `${chatID}.json`;
      a.rel = "noopener";
      document.body.appendChild(a);
      a.click();
      a.remove();
    }));
  }

  el.insertAdjacentElement("afterend", row);
  row.setAttribute("data-chat-entry", "");
  pipeline.setActiveTurnActions(row);
}

// --- Tool calls (live) ---

export function addToolCall(tc: ToolCall): void {
  const { id, title, kind, status } = tc;
  const rawInput = tc.input as Record<string, unknown> | undefined;
  const storedOutput = tc.output;
  // Nested tool call inside an active sub-agent: fold into the preview
  // rather than rendering at the top level.
  if (!isSubAgent(title) && isSubAgentActive() && !toolEls.has(id)) {
    appendToSubAgent(`[${title}]\n`);
    return;
  }

  if (isSubAgent(title)) {
    const card = createSubAgentCard(id, status, rawInput, storedOutput);
    toolEls.set(id, card);
    appendMessage(card);
    trimOldMessages();
    scroll();
    return;
  }

  const opts: Parameters<typeof buildToolCard>[0] = {
    id, title, kind, status, live: true,
  };
  if (rawInput !== undefined) opts.input = rawInput;
  if (storedOutput !== undefined) opts.output = storedOutput;
  if (tc.diffs !== undefined && tc.diffs.length > 0) opts.diffs = tc.diffs;
  if (tc.locations !== undefined && tc.locations.length > 0) opts.locations = tc.locations;
  const el = buildToolCard(opts);
  toolEls.set(id, el);

  // Subagent tool calls: route into the crew-card row AND
  // render in the main transcript (dual-channel, matches kiro-cli).
  if (tc.sub_session_id !== undefined && tc.sub_session_id !== "") {
    addToolToCrewRow(tc.sub_session_id, tc);
  }

  const group = getOrCreateToolGroup(appendMessage);
  group.appendChild(el);
  scroll();
}

export function updateToolCall(tc: ToolCall): void {
  const el = toolEls.get(tc.id);
  if (el === undefined) return;
  if (el.classList.contains("subagent-call")) {
    updateSubAgentCard(tc.id, tc.status, tc.output);
    return;
  }
  if (tc.status !== undefined) applyStatusUpdate(el, tc.status, tc.duration_ms);
  if (tc.title !== undefined) applyTitleUpdate(el, tc.title);
  if (tc.output !== undefined && tc.output !== "") applyOutputUpdate(el, tc.output);
  if (tc.diffs !== undefined && tc.diffs.length > 0) applyDiffUpdate(el, tc.diffs);

  // Propagate the same updates to the crew-row clone (if any).
  const crewEl = getCrewToolEl(tc.id);
  if (crewEl !== undefined) {
    if (tc.status !== undefined) applyStatusUpdate(crewEl, tc.status, tc.duration_ms);
    if (tc.title !== undefined) applyTitleUpdate(crewEl, tc.title);
    if (tc.output !== undefined && tc.output !== "") applyOutputUpdate(crewEl, tc.output);
    if (tc.diffs !== undefined && tc.diffs.length > 0) applyDiffUpdate(crewEl, tc.diffs);
    // Update the collapsed-row activity line.
    if (tc.sub_session_id !== undefined && tc.sub_session_id !== "") {
      if (tc.status === "completed" || tc.status === "failed") {
        onCrewToolCompleted(tc.sub_session_id);
      } else if (tc.title !== undefined) {
        setSubagentActivity(tc.sub_session_id, formatToolActivity(tc.title));
      }
    }
  }
  scroll();
}

function applyStatusUpdate(el: HTMLDivElement, status: ToolStatus, serverDurationMs?: number): void {
  const s = el.querySelector(".tool-status");
  if (s !== null) { s.textContent = status; s.className = `tool-status ${status}`; }
  const done = isToolDone(status);
  if (done) {
    el.querySelector(".tool-spinner")?.remove();
    untrackInProgress(el);
    // Prefer server-computed duration (from kiro-cli timestamps) over
    // client-side timing (which can drift on slow networks).
    const ms = serverDurationMs ?? (() => {
      const start = el.dataset["startMs"];
      if (start === undefined) return 0;
      delete el.dataset["startMs"];
      return Date.now() - parseInt(start, 10);
    })();
    const dur = el.querySelector(".tool-duration") as HTMLElement | null;
    if (dur !== null && ms >= 1000) dur.textContent = formatDuration(ms);
    maybeCollapseGroup(el);

    // Edit-specific actions: "Undo" (restore file from checkpoint) and
    // "Diff" (open the file in the editor's diff mode). Only shown on
    // completed edit tool calls that have a file path.
    if (status === "completed" && el.dataset["kind"] === "edit") {
      addEditActions(el);
    }
  }
  if (status === "failed") {
    el.querySelector(".tool-details")?.classList.remove("collapsed");
    const b = el.querySelector(".tool-toggle");
    if (b !== null) { b.textContent = ""; b.appendChild(svgTemplate(ICON_CHEVRON_UP)()); }
    // Add "Explain this error" button if not already present.
    if (el.querySelector(".tool-explain-btn") === null) {
      const output = el.querySelector(".tool-output")?.textContent ?? "";
      if (output.trim() !== "") {
        const btn = document.createElement("button");
        btn.type = "button";
        btn.className = "tool-explain-btn";
        btn.textContent = "Explain this error";
        btn.addEventListener("click", () => {
          btn.disabled = true;
          btn.classList.add("btn-loading");
          void explainError(output, el.dataset["title"] ?? "").then((explanation) => {
            btn.classList.remove("btn-loading");
            if (explanation !== "") {
              btn.textContent = explanation;
              btn.className = "tool-explain-result";
            } else {
              btn.disabled = false;
            }
          });
        });
        el.appendChild(btn);
      }
    }
  }
}

function applyTitleUpdate(el: HTMLDivElement, title: string): void {
  const t = el.querySelector(".tool-title");
  if (t !== null) {
    const display = title.startsWith("Running: ") ? title.slice(9) : title;
    t.textContent = display;
    (t.parentElement as HTMLElement).title = title;
  }
}

function applyOutputUpdate(el: HTMLDivElement, output: string): void {
  // Complex-tier cards have a scrollable .tool-output-box; others have
  // a hidden .tool-output inside .tool-details.
  const box = el.querySelector(".tool-output-box") as HTMLDivElement | null;
  if (box !== null) {
    const pre = box.querySelector("pre");
    if (pre !== null) {
      pre.insertAdjacentHTML("beforeend", ansiToHtml(output));
    } else {
      const newPre = document.createElement("pre");
      newPre.innerHTML = ansiToHtml(output);
      box.appendChild(newPre);
    }
    box.scrollTop = box.scrollHeight;
    return;
  }
  const out = el.querySelector(".tool-output") as HTMLDivElement | null;
  if (out === null) return;
  const pre = document.createElement("pre");
  pre.innerHTML = ansiToHtml(output);
  out.appendChild(pre);
}

export { formatToolActivity } from "./format-tool-activity.js";


/** Insert an inline diff preview from ACP tool_call content.diff blocks.
 *  Delegates to tool-card.ts's insertDiffPreview. Skips if the card
 *  already has a diff preview. */
function applyDiffUpdate(el: HTMLDivElement, diffs: ToolDiff[]): void {
  if (el.querySelector(".tool-diff-preview") !== null) return;
  const d = diffs[0];
  if (d === undefined) return;
  insertDiffPreview(el, d.path, {
    oldText: d.old_text ?? "", newText: d.new_text,
  });
}

// --- Plans ---

export function addPlan(entries: PlanEntry[]): void {
  appendMessage(planElement(entries));
  trimOldMessages();
  scroll();
}

function planElement(entries: PlanEntry[]): HTMLDivElement {
  const el = document.createElement("div");
  el.className = "plan-message";
  let html = '<div class="plan-header">Plan</div>';
  for (const e of entries) {
    const icon = e.status === "completed" ? "✅" : e.status === "in_progress" ? "🔄" : "⬜";
    const pri = e.priority === "high" ? ' <span class="plan-hi">[high]</span>' : "";
    html += `<div class="plan-entry">${icon} ${escText(e.content)}${pri}</div>`;
  }
  html += '<div class="plan-actions">'
    + '<button class="plan-edit-btn btn-small" title="Open this plan in the editor for tweaks before handing it to the default agent">Edit</button>'
    + '<button class="plan-run-btn btn-small" title="Switch to the default agent and implement this plan">Run this plan</button>'
    + '</div>';
  el.innerHTML = html;

  const md = planToMarkdown(entries);
  el.querySelector(".plan-edit-btn")?.addEventListener("click", () => {
    void editPlanAction(getActiveId(), md);
  });
  el.querySelector(".plan-run-btn")?.addEventListener("click", () => {
    void runPlan(getActiveId(), md);
  });
  return el;
}

/** Edit flow: write the draft file on the server, then open the editor
 *  on the virtual plan-draft path. Two steps instead of one because
 *  editor.ts owns the open-in-editor step (it's the only module with
 *  access to the file-state map) while plan-actions owns the write
 *  step (so plan-actions doesn't need an editor dependency).
 *
 *  Bails out when the initial write fails (chat deleted out from under
 *  us, 413 oversize, disk full). Opening the editor tab anyway would
 *  land the user on an empty buffer that can't be saved — the next
 *  Save click would hit the same failure mode. */
async function editPlanAction(chatID: string, content: string): Promise<void> {
  if (chatID === "") return;
  const ok = await writePlanDraft(chatID, content);
  if (!ok) return;
  openPlanDraftPath(chatID);
}

// --- Crew monitor (subagent orchestration) ---
//
// Implementation in crew-card.ts. These wrappers pass our local
// `appendMessage` through so the crew card attaches to the message
// column at the right spot without exposing the column node globally.

export function addCrew(messageID: string, crew: import("./types.js").Crew): void {
  addCrewInternal(messageID, crew, appendMessage);
}

export function updateCrew(messageID: string, crew: import("./types.js").Crew): void {
  updateCrewInternal(messageID, crew, appendMessage);
}

// --- System events + boundary dividers ---

export function addSystemMessage(text: string): void {
  const el = document.createElement("div");
  el.className = "message system";
  el.textContent = text;
  appendMessage(el);
  trimOldMessages();
  scroll();
}

/** Render a collapsible reasoning/thinking block. */
export function addReasoningBlock(text: string): void {
  const details = document.createElement("details");
  details.className = "reasoning-block";
  const summary = document.createElement("summary");
  summary.className = "reasoning-summary";
  summary.textContent = "Reasoning";
  details.appendChild(summary);
  const body = document.createElement("blockquote");
  body.className = "reasoning-body";
  body.textContent = text;
  details.appendChild(body);
  appendMessage(details);
}

export type BoundaryKind = "switched" | "compacted" | "failed" | "agent";

/** Centralized boundary metadata: icon glyph, CSS class suffix (derived
 *  from the key), default aria-label, and optional label-builder for
 *  event kinds that carry dynamic content. Both the streaming path
 *  (renderer.ts) and the replay path (messages.ts) look up this table
 *  instead of maintaining parallel switch statements. Adding a new
 *  boundary-style event kind is a single row addition. */
export const EVENT_BOUNDARY_META: Readonly<Partial<Record<EventKind, {
  readonly boundary: BoundaryKind;
  readonly icon: string;
  readonly defaultLabel: string;
  readonly labelFn?: (content: string) => string;
}>>> = {
  model_switched: {
    boundary: "switched", icon: "\u21bb", defaultLabel: "Context reset",
    labelFn: (c) => c ? `Switched to ${c}` : "Context reset",
  },
  compacted: {
    boundary: "compacted", icon: "\u273b", defaultLabel: "Conversation compacted",
  },
  compaction_failed: {
    boundary: "failed", icon: "\u26a0", defaultLabel: "Compaction failed",
    labelFn: (c) => c ? `Compaction failed: ${c}` : "Compaction failed",
  },
  agent_switched: {
    boundary: "agent", icon: "\u2192", defaultLabel: "Agent switched",
    labelFn: (c) => c || "Agent switched",
  },
};

function buildBoundaryDivider(kind: BoundaryKind, label: string): HTMLDivElement {
  const el = document.createElement("div");
  el.className = `boundary boundary-${kind}`;
  // Look up icon from the meta table; fall back to empty string for
  // programmatic callers that pass a kind not in the event-driven table.
  let icon = "";
  for (const meta of Object.values(EVENT_BOUNDARY_META)) {
    if (meta !== undefined && meta.boundary === kind) { icon = meta.icon; break; }
  }
  const iconSpan = document.createElement("span");
  iconSpan.className = "boundary-icon";
  iconSpan.textContent = icon;
  el.appendChild(iconSpan);
  const labelSpan = document.createElement("span");
  labelSpan.className = "boundary-label";
  labelSpan.textContent = label;
  el.appendChild(labelSpan);
  return el;
}

export function addBoundaryDivider(kind: BoundaryKind, label: string): void {
  breakToolGroup();
  appendMessage(buildBoundaryDivider(kind, label));
  trimOldMessages();
  scroll();
}

// Re-export for external consumers (conflicts.ts).
export { refreshConflictBadges } from "./messages-actions.js";

// Wire bus subscriptions for pending-change actions.
initMessageActions();

// --- Clear on chat switch ---

export function clearMessages(): void {
  streamingPipeline.dispose();
  messagesEl.replaceChildren();
  toolEls.clear();
  clearCrews();
  resetSubAgents();
  breakToolGroup();
  resetScrollState();
}

// --- Scroll-back pagination ---

export function prependMessages(msgs: Message[]): void {
  document.getElementById("load-more-skeleton")?.remove();
  const indicator = document.getElementById("load-more-indicator");
  const insertBefore = indicator !== null ? indicator.nextSibling : messagesEl.firstChild;

  const frag = document.createDocumentFragment();
  for (const m of msgs) prependOne(frag, m);
  messagesEl.insertBefore(frag, insertBefore);
}

/** Build the DOM representation of a single message for the replay/pagination
 *  path. This is the single dispatch point for role→DOM in non-streaming
 *  contexts. The streaming path (renderer.ts renderOne) cannot share this
 *  because it manages streaming state, scroll, and tool-group side effects
 *  that are inherently imperative — but it shares the event-rendering logic
 *  via EVENT_BOUNDARY_META (see buildEventDOM). */
export function buildReplayMessage(frag: DocumentFragment, m: Message): void {
  switch (m.role) {
    case "user":
      frag.appendChild(buildUserReplay(m.content ?? ""));
      return;
    case "assistant":
      buildAssistantReplay(frag, m);
      return;
    case "event":
      frag.appendChild(buildEventReplay(m));
      return;
  }
}

function prependOne(frag: DocumentFragment, m: Message): void {
  buildReplayMessage(frag, m);
}

function buildUserReplay(content: string): HTMLDivElement {
  const row = makeRow("user");
  const el = document.createElement("div");
  el.className = "message user";
  el.textContent = content;
  linkifyPaths(el);
  row.appendChild(el);
  return row;
}

function buildAssistantReplay(frag: DocumentFragment, m: Message): void {
  if ((m.content ?? "") !== "") {
    const row = makeRow("assistant");
    const el = document.createElement("div");
    el.className = "message assistant";
    renderBubble(el, m.content ?? "");
    row.appendChild(el);
    frag.appendChild(row);
  }
  if (m.plan !== undefined && m.plan.length > 0) {
    frag.appendChild(planElement(m.plan));
  }
  if (m.tool_calls !== undefined) {
    for (const tc of m.tool_calls) {
      const rawInput = (tc.input !== undefined && tc.input !== null && typeof tc.input === "object")
        ? tc.input as Record<string, unknown> : undefined;
      const opts: Parameters<typeof buildToolCard>[0] = {
        id: tc.id, title: tc.title, kind: tc.kind, status: tc.status as ToolStatus,
        live: false,
      };
      if (rawInput !== undefined) opts.input = rawInput;
      if (tc.output !== undefined) opts.output = tc.output;
      frag.appendChild(buildToolCard(opts));
    }
  }
}

/** Shared event→DOM builder used by both the replay path (prependMessages)
 *  and the streaming path (renderer.ts renderEvent). Handles boundary-style
 *  events via EVENT_BOUNDARY_META, plus special cases (inbox, crew).
 *  Returns null for "crew" when crew data is missing (caller falls through
 *  to a generic system message). */
export function buildEventDOM(m: Message): HTMLElement | null {
  // Boundary-style events: look up the meta table.
  if (m.event_kind !== undefined) {
    const meta = EVENT_BOUNDARY_META[m.event_kind];
    if (meta !== undefined) {
      const content = m.content ?? "";
      const label = meta.labelFn ? meta.labelFn(content) : meta.defaultLabel;
      return buildBoundaryDivider(meta.boundary, label);
    }
  }

  switch (m.event_kind) {
    case "inbox": {
      const el = document.createElement("div");
      el.className = "message system inbox-message";
      el.textContent = m.content ?? "Subagent message";
      return el;
    }
    case "crew":
      if (m.crew !== undefined) {
        return buildCrewCardForReplay(m.id, m.crew);
      }
      return null;
    default:
      return null;
  }
}

function buildEventReplay(m: Message): HTMLElement {
  const el = buildEventDOM(m);
  if (el !== null) return el;
  const fallback = document.createElement("div");
  fallback.className = "message system";
  fallback.textContent = m.content ?? String(m.event_kind ?? "");
  return fallback;
}

/** Ask the utility bridge to explain a tool error in plain language. */
async function explainError(errorText: string, toolTitle: string): Promise<string> {
  const d = await explainErrorAction.dispatch({ errorText, context: toolTitle });
  return d?.output ?? "";
}
