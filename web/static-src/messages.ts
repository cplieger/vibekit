// ---------------------------------------------------------------------------
// Message view: signal-driven reactive renderer.
//
// One effect watches store.version + the active session's messages array
// and reconciles them into $.messages by message id. Per-message factories
// (buildUser / buildAssistant / buildEvent) own initial DOM construction;
// per-message updaters (updateAssistant, updateEvent) own incremental
// changes including streaming markdown chunks.
//
// The per-role subsystems (tool-card, crew-card, plan-actions, subagent,
// permission, code-blocks, markdown) are unchanged — this module is the
// shell that mounts and updates them by message identity.
//
// The "liquid" feel comes from CSS:
//   - @starting-style + transitions on `.msg-row` for entry animations
//   - .streaming class on the active assistant bubble (subtle pulse)
//   - interpolate-size: allow-keywords on :root so height: auto can
//     animate (set in css/01-tokens.css)
//   - content-visibility: auto on rows so off-screen messages don't pay
//     paint cost
// ---------------------------------------------------------------------------

import type { Message } from "./types.js";
import { createMarkdownStream, renderMarkdownInto, type MarkdownStream } from "./markdown.js";
import {
  getActive,
  getActiveId,
  messagesVersion,
  activeVersion,
} from "./store.js";
import {
  ensureStreamingSig,
  clearStreamingSig,
  ensureReasoningSig,
  clearReasoningSig,
} from "./store-signals.js";
import { effect } from "./lib/reactive/index.js";
import { reconcile, KEY_ATTR as RECONCILE_KEY, type ReconcileSpec } from "./reconcile.js";
import { $ } from "./dom.js";
import {
  getScrollEl,
  scrollToBottom,
  suppressScroll,
  setUserScrolledUp,
  resetScrollState,
  setLoadMore,
} from "./scroll.js";
import {
  resetSubAgents,
} from "./subagent.js";
import {
  breakToolGroup,
  summarize,
  CLS_COLLAPSED,
  CLS_AUTO_COLLAPSED,
} from "./tool-group.js";
import { linkifyPaths } from "./linkify.js";
import { explainError as explainErrorAction } from "./actions/messages.js";
import { initMessageActions, clearActionBindings } from "./messages-actions.js";
import {
  clearCrews,
} from "./crew-card.js";
import { send } from "./transport.js";
import {
  toolSpec,
  toolEls,
  disposeToolEffect,
  disposeAllToolEffects,
  initToolCallbacks,
} from "./messages-tools.js";
import { planElement, updatePlanElement } from "./messages-plan.js";
import {
  buildEvent,
  updateEvent,
  buildSystemFallback,
  disposeCrewEffect,
  disposeAllCrewEffects,
} from "./messages-events.js";
import { attachTurnActions, initTurnActionCallbacks } from "./messages-turn-actions.js";

// ---------------------------------------------------------------------------
// Public re-exports
// ---------------------------------------------------------------------------

export { getScrollEl, scrollToBottom, setLoadMore };
export type { BoundaryKind } from "./messages-events.js";

// ---------------------------------------------------------------------------
// Module state
// ---------------------------------------------------------------------------

const messagesEl = $.messages;

/** Per-message-id metadata kept for the duration the message is mounted. */
interface MessageState {
  el: HTMLElement;
  /** True while this is the live streaming bubble; transitions to false
   *  on turn end via finalizeStreaming(). */
  streaming: boolean;
}
const messageStates = new Map<string, MessageState>();

/** Markdown stream per assistant bubble. The stream owns its own
 *  parser, write buffer, and 200ms flush schedule; messages.ts just
 *  forwards delta strings. Created lazily on first writeDelta /
 *  on demand for replay. */
const streams = new WeakMap<HTMLDivElement, MarkdownStream>();

/** bindLoadingState unsubs accumulated within a chat. Cleared on
 *  message removal (via reconcile.onRemove) and on chat switch. */
const bindUnbinds = new Map<string, (() => void)[]>();
function pushBind(key: string, unbind: () => void): void {
  let arr = bindUnbinds.get(key);
  if (arr === undefined) {
    arr = [];
    bindUnbinds.set(key, arr);
  }
  arr.push(unbind);
}

/** Per-message streaming effect cleanups. Disposed both on turn end
 *  (when the message stays mounted but stops streaming) and on full
 *  unmount. A single message can register multiple cleanups (e.g.
 *  one for the content stream + one for the reasoning stream).
 *  Separate from bindUnbinds so tool-card loading-state bindings
 *  survive turn end. */
const streamingEffects = new Map<string, (() => void)[]>();
function pushStreamingEffect(id: string, fn: () => void): void {
  const arr = streamingEffects.get(id);
  if (arr === undefined) {
    streamingEffects.set(id, [fn]);
  } else {
    arr.push(fn);
  }
}
function disposeStreamingEffect(id: string): void {
  const arr = streamingEffects.get(id);
  if (arr !== undefined) {
    for (const fn of arr) {
      fn();
    }
    streamingEffects.delete(id);
  }
  clearStreamingSig(id);
  clearReasoningSig(id);
}

/** Per-paint table of message-id → checkpoint tag, populated by paint()
 *  before reconcile so buildUser can look up the tag without scanning
 *  the entire messages array. */
const checkpointTags = new Map<string, string>();

/** IDs of messages newly appended at the end since the last paint
 *  (i.e. streaming arrival). buildMessage uses this to mark new rows
 *  with `data-chat-entry` so the CSS entry animation plays for new
 *  content but NOT for chat-switch replay or pagination prepend. */
const appendNewIds = new Set<string>();
let lastNewestId: string | undefined;
let lastActiveId: string | undefined;

/** Per-paint stagger index for messages mounted in a single reconcile
 *  pass (chat-switch). Indexed from the bottom so the most-recent
 *  messages animate first, with a cap at 8 to prevent the cascade
 *  from looking laggy on long histories. */
const staggerIndex = new Map<string, number>();

// Avatars (parsed once, cloned per use).
const KIRO_AVATAR =
  '<svg class="avatar-icon" width="17" height="20" viewBox="-2 -2 44 52" fill="none"><path d="M7.58762 37.203C2.62272 48.1978 13.1975 50.9578 20.9974 44.5229C23.2923 51.7378 31.8872 46.3529 34.9771 40.758C41.772 28.4282 39.027 15.8585 38.322 13.2635C33.4921 -4.42116 9.34259 -4.45116 5.18767 13.3535C4.21269 16.4734 4.19769 20.0134 3.6577 23.6883C3.3877 25.5483 3.17771 26.7332 2.47272 28.6832C2.05273 29.8082 1.49774 30.7982 0.597756 32.4781C-0.782218 35.0881 -0.197229 40.113 6.94263 37.503L7.61762 37.203H7.58762Z" fill="white" stroke="#9046FF" stroke-width="1.5"/><path d="M21.9284 20.928C19.9484 20.928 19.6484 18.5581 19.6484 17.1481C19.6484 15.8731 19.8734 14.8681 20.3084 14.2231C20.6834 13.6532 21.2384 13.3682 21.9284 13.3682C22.6184 13.3682 23.2184 13.6532 23.6384 14.2381C24.1184 14.8981 24.3733 15.9031 24.3733 17.1481C24.3733 19.518 23.4584 20.928 21.9434 20.928H21.9284Z" fill="#1e1e2e"/><path d="M30.0729 20.928C28.093 20.928 27.793 18.5581 27.793 17.1481C27.793 15.8731 28.018 14.8681 28.453 14.2231C28.8279 13.6532 29.3829 13.3682 30.0729 13.3682C30.7629 13.3682 31.3629 13.6532 31.7829 14.2381C32.2629 14.8981 32.5179 15.9031 32.5179 17.1481C32.5179 19.518 31.6029 20.928 30.0879 20.928H30.0729Z" fill="#1e1e2e"/></svg>';
const USER_AVATAR =
  '<svg class="avatar-icon" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="8" r="4"/><path d="M20 21a8 8 0 00-16 0"/></svg>';

function svgTemplate(markup: string): () => Node {
  const tpl = document.createElement("template");
  tpl.innerHTML = markup;
  const content = tpl.content;
  return () => content.cloneNode(true);
}
const cloneKiroAvatar = svgTemplate(KIRO_AVATAR);
const cloneUserAvatar = svgTemplate(USER_AVATAR);

// ---------------------------------------------------------------------------
// Public entry point
// ---------------------------------------------------------------------------

let mounted = false;

// Initialize callbacks for extracted modules.
initToolCallbacks({
  pushBind,
  svgTemplate,
  refreshGroupHeader,
  explainError,
});
initTurnActionCallbacks({ svgTemplate });

/** Mount the chat view. Idempotent. Called once at app boot from app.ts.
 *  Subscribes to store.version and reconciles the message list on every
 *  bump. Streaming markdown chunks flow through the assistant-update
 *  path inside this same effect. */
export function mountChatView(): void {
  if (mounted) {
    return;
  }
  mounted = true;
  initMessageActions();
  effect(() => {
    void messagesVersion.value;
    void activeVersion.value;
    paint();
  });
}

function paint(): void {
  const session = getActive();
  if (session === undefined) {
    // No active session (rare; transient during chat-switch). Clear
    // mounted state but leave any standalone skeletons alone.
    teardownAll();
    return;
  }
  // Precompute checkpoint tags so buildUser is O(1) per row.
  checkpointTags.clear();
  const oldestTag = session.oldest_checkpoint_tag ?? "";
  let userTurnIdx = 0;
  for (const m of session.messages) {
    if (m.role === "user") {
      checkpointTags.set(m.id, checkpointTagForTurn(userTurnIdx, oldestTag));
      userTurnIdx++;
    }
  }
  // Mark genuinely-new appended messages (streaming arrival) so only
  // those get the entry animation. Chat-switches and paginated prepends
  // are silent (no animation).
  appendNewIds.clear();
  staggerIndex.clear();
  const isChatSwitch = lastActiveId !== session.id;
  if (!isChatSwitch && lastNewestId !== undefined) {
    // Reverse scan: lastNewestId is always near the tail (set at end of
    // previous paint), so scanning backward is O(1) amortized.
    let idx = -1;
    for (let i = session.messages.length - 1; i >= 0; i--) {
      if (session.messages[i]?.id === lastNewestId) {
        idx = i;
        break;
      }
    }
    if (idx >= 0) {
      for (let i = idx + 1; i < session.messages.length; i++) {
        const id = session.messages[i]?.id;
        if (id !== undefined) {
          appendNewIds.add(id);
        }
      }
    }
  } else if (isChatSwitch) {
    // Cascade the last 8 messages on chat-switch so they stagger
    // visually rather than flashing in together.
    const total = session.messages.length;
    for (let i = Math.max(0, total - 8); i < total; i++) {
      const id = session.messages[i]?.id;
      if (id !== undefined) {
        staggerIndex.set(id, total - 1 - i);
      }
    }
  }
  reconcile(messagesEl, session.messages, messageSpec);
  finalizeStreamingIfNeeded(session.messages);
  lastNewestId = session.messages[session.messages.length - 1]?.id;
  lastActiveId = session.id;
}

/** Pure: resolve the checkpoint tag the server stored for a turn, or
 *  "" when no backing snapshot exists. Tag form: "<turn>" or "<turn>.<...>". */
export function checkpointTagForTurn(turnIndex: number, oldestTag: string): string {
  if (oldestTag === "") {
    return "";
  }
  const candidate = String(turnIndex);
  const oldestTurn = parseInt(oldestTag.split(".")[0] ?? "0", 10);
  if (!Number.isFinite(oldestTurn) || turnIndex < oldestTurn) {
    return "";
  }
  return candidate;
}

/** Clear all per-message state, e.g. on chat switch when active session
 *  becomes undefined. The reconcile call after a real session arrives
 *  will rebuild from scratch. */
function teardownAll(): void {
  for (const arr of bindUnbinds.values()) {
    for (const fn of arr) {
      fn();
    }
  }
  bindUnbinds.clear();
  for (const id of [...streamingEffects.keys()]) {
    disposeStreamingEffect(id);
  }
  disposeAllToolEffects();
  disposeAllCrewEffects();
  for (const st of messageStates.values()) {
    finalizeStreamingPipeline(st.el.querySelector(".message.assistant.streaming"));
  }
  messageStates.clear();
  clearActionBindings();
  clearCrews();
  resetSubAgents();
  breakToolGroup();
  resetScrollState();
  reconcile(messagesEl, [] as Message[], messageSpec);
}

// ---------------------------------------------------------------------------
// Reconcile spec
// ---------------------------------------------------------------------------

const messageSpec: ReconcileSpec<Message> = {
  key: (m) => m.id,
  mount: (m) => {
    const el = buildMessage(m);
    // Only animate genuinely-new appended messages; chat-switch replay
    // and pagination prepends mount silently. See paint() for how
    // appendNewIds is populated.
    if (appendNewIds.has(m.id)) {
      el.setAttribute("data-chat-entry", "");
    }
    const stagger = staggerIndex.get(m.id);
    if (stagger !== undefined && stagger > 0) {
      el.style.setProperty("--stagger-index", String(stagger));
    }
    messageStates.set(m.id, {
      el,
      streaming: m.role === "assistant" && isLikelyLiveStreaming(m),
    });
    return el;
  },
  update: (el, m) => {
    updateMessage(el, m);
  },
  onRemove: (el, key) => {
    const arr = bindUnbinds.get(key);
    if (arr !== undefined) {
      for (const fn of arr) {
        fn();
      }
      bindUnbinds.delete(key);
    }
    disposeStreamingEffect(key);
    disposeCrewEffect(key);
    // Finalize any streaming bubble that lived in this message.
    finalizeStreamingPipeline(el.querySelector(".message.assistant.streaming"));
    messageStates.delete(key);
  },
};

// ---------------------------------------------------------------------------
// Per-role builders + updaters
// ---------------------------------------------------------------------------

function buildMessage(m: Message): HTMLElement {
  switch (m.role) {
    case "user":
      return buildUser(m);
    case "assistant":
      return buildAssistant(m);
    case "event":
      return buildEvent(m) ?? buildSystemFallback(m);
  }
}

function updateMessage(el: HTMLElement, m: Message): void {
  if (m.role === "assistant") {
    updateAssistant(el, m);
  } else if (m.role === "event") {
    updateEvent(el, m);
  }
  // user messages are immutable once mounted.
}

// --- User ---

function buildUser(m: Message): HTMLElement {
  const wrap = document.createElement("div");
  wrap.className = "msg-wrap msg-wrap-user";

  // Optional checkpoint divider above the bubble.
  const cp = checkpointTags.get(m.id) ?? "";
  if (cp !== "") {
    const line = document.createElement("div");
    line.className = "checkpoint-line";
    const label = document.createElement("span");
    label.className = "checkpoint-label";
    label.textContent = "Checkpoint";
    const btn = document.createElement("button");
    btn.className = "checkpoint-restore";
    btn.type = "button";
    btn.dataset["tag"] = cp;
    btn.title = "Restore files to this point";
    btn.setAttribute("aria-label", `Restore to checkpoint ${cp}`);
    btn.textContent = "Restore";
    // Rewind button: creates a new chat branched from this turn.
    const rewindBtn = document.createElement("button");
    rewindBtn.className = "checkpoint-rewind";
    rewindBtn.type = "button";
    rewindBtn.title = "Rewind conversation from this point";
    rewindBtn.setAttribute("aria-label", "Rewind from this turn");
    rewindBtn.textContent = "Rewind";
    rewindBtn.addEventListener("click", () => {
      // Find the turn index for this message.
      const session = getActive();
      if (session === undefined) {
        return;
      }
      const turnIdx = session.messages.findIndex((msg) => msg.id === m.id);
      if (turnIdx < 0) {
        return;
      }
      if (
        !confirm(
          "Rewind from this turn? Creates a new chat starting from this point. File contents on disk are not affected (use Restore for that).",
        )
      ) {
        return;
      }
      void send({
          type: "rewind_chat",
          chat_id: session.id,
          request_id: `rewind-${Date.now()}`,
          payload: { turn_index: turnIdx },
        });
    });
    line.append(label, btn, rewindBtn);
    wrap.appendChild(line);
  }

  const row = makeRow("user");
  const bubble = document.createElement("div");
  bubble.className = "message user";
  bubble.textContent = m.content ?? "";
  linkifyPaths(bubble);
  row.appendChild(bubble);
  wrap.appendChild(row);

  // User messages always pop the user back to the bottom.
  setUserScrolledUp(false);
  suppressScroll(400);
  return wrap;
}

// --- Assistant (with streaming) ---

function buildAssistant(m: Message): HTMLElement {
  const wrap = document.createElement("div");
  wrap.className = "msg-wrap msg-wrap-assistant";

  const content = m.content ?? "";
  const reasoning = m.reasoning ?? "";
  const live = isLikelyLiveStreaming(m);

  // Reasoning block above the content bubble. Mounted whenever there's
  // existing reasoning OR this is the live message (which might receive
  // reasoning chunks). The block stays in DOM after finalize so the
  // user can re-expand to read the model's thinking trace.
  if (reasoning !== "" || live) {
    mountReasoningBlock(wrap, reasoning, live, m.id);
  }

  // Content bubble. Mounted whenever there's existing content OR this
  // is the live message. The bubble may receive its first chunk after
  // mount; the per-message streaming signal effect handles that.
  if (content !== "" || live) {
    mountContentBubble(wrap, content, live, m.id);
  }

  // Plan, if present.
  if (m.plan !== undefined && m.plan.length > 0) {
    wrap.appendChild(planElement(m.plan));
  }

  // Tool calls — build a single .tool-group container with header +
  // keyed tool cards. Updates flow through reconcile inside
  // updateAssistant. Auto-collapse fires once 3+ calls finish via
  // maybeCollapseGroup() inside applyStatusUpdate.
  if (m.tool_calls !== undefined && m.tool_calls.length > 0) {
    const tools = mountToolGroup(wrap);
    reconcile(tools, m.tool_calls, toolSpec);
    refreshGroupHeader(tools);
  }

  return wrap;
}

/** Mount a reasoning block ("Thinking…" / "Thinking completed") into
 *  the wrap. Subscribes to the per-message reasoning signal so chunks
 *  fan in here without re-running the global reconcile. */
function mountReasoningBlock(
  wrap: HTMLElement,
  reasoning: string,
  live: boolean,
  messageID: string,
): HTMLDetailsElement {
  const details = document.createElement("details");
  details.className = "reasoning-block msg-reasoning";
  if (live) {
    details.open = true;
  }

  const summary = document.createElement("summary");
  summary.className = "reasoning-summary";
  summary.textContent = live ? "Thinking…" : "Reasoning";
  details.appendChild(summary);

  const body = document.createElement("blockquote");
  body.className = "reasoning-body";
  body.textContent = reasoning;
  details.appendChild(body);

  wrap.appendChild(details);

  if (live) {
    const sig = ensureReasoningSig(messageID, reasoning);
    let lastLen = reasoning.length;
    const cleanup = effect(() => {
      const full = sig.value;
      if (full.length <= lastLen) {
        return;
      }
      // Append the delta as a new text node — much cheaper than
      // re-rendering body.textContent on every chunk.
      body.appendChild(document.createTextNode(full.slice(lastLen)));
      lastLen = full.length;
    });
    // Wrap cleanup so it also flips the summary on disposal.
    const onFinalize = (): void => {
      cleanup();
      summary.textContent = "Thinking completed";
      details.open = false;
    };
    pushStreamingEffect(messageID, onFinalize);
  }
  return details;
}

/** Mount the content row + bubble. Subscribes to the per-message
 *  content signal for live streaming chunks; renders the full markdown
 *  in one shot for replay. */
function mountContentBubble(
  wrap: HTMLElement,
  content: string,
  live: boolean,
  messageID: string,
): HTMLDivElement {
  const row = makeRow("assistant");
  const bubble = document.createElement("div");
  bubble.className = "message assistant";
  if (live) {
    bubble.classList.add("streaming");
  }
  if (content !== "") {
    if (!live) {
      // Replay path: full markdown one-shot.
      renderMarkdownInto(bubble, content);
    } else {
      // Streaming seed: prime the incremental writer with what's
      // already arrived. Subsequent chunks flow through the signal
      // effect below.
      ensureStream(bubble).writeDelta(content);
    }
  }
  row.appendChild(bubble);
  wrap.appendChild(row);

  if (live) {
    const sig = ensureStreamingSig(messageID, content);
    let lastLen = content.length;
    const cleanup = effect(() => {
      const full = sig.value;
      if (full.length <= lastLen) {
        return;
      }
      ensureStream(bubble).writeDelta(full.slice(lastLen));
      lastLen = full.length;
    });
    pushStreamingEffect(messageID, cleanup);
  }
  return bubble;
}

/** Get-or-create the markdown stream for a bubble. Created on first
 *  use; cleaned up via finalizeStreamingPipeline. */
function ensureStream(bubble: HTMLDivElement): MarkdownStream {
  let s = streams.get(bubble);
  if (s === undefined) {
    s = createMarkdownStream(bubble);
    streams.set(bubble, s);
  }
  return s;
}

/** Build a `.tool-group` shell with a clickable header. The reconciled
 *  tool cards are appended as keyed children; the header is un-keyed
 *  so reconcile leaves it alone. */
function mountToolGroup(parent: HTMLElement): HTMLDivElement {
  const group = document.createElement("div");
  group.className = "tool-group";
  const header = document.createElement("div");
  header.className = "tool-group-header";
  header.setAttribute("role", "button");
  header.setAttribute("tabindex", "0");
  header.setAttribute("aria-expanded", "true");
  header.innerHTML = '<span class="tool-group-count"></span>';
  const onToggle = (): void => {
    group.classList.add(CLS_USER_TOGGLED);
    const wasAuto = group.classList.contains(CLS_AUTO_COLLAPSED);
    if (wasAuto) {
      group.classList.remove(CLS_AUTO_COLLAPSED);
    }
    group.classList.toggle(CLS_COLLAPSED);
    const collapsed = group.classList.contains(CLS_COLLAPSED);
    header.setAttribute("aria-expanded", collapsed ? "false" : "true");
    refreshGroupHeader(group);
    if (!collapsed || wasAuto) {
      setUserScrolledUp(true);
    }
  };
  header.addEventListener("click", onToggle);
  header.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      onToggle();
    }
  });
  group.appendChild(header);
  parent.appendChild(group);
  return group;
}

/** Recompute the group header text. Called after each reconcile pass
 *  AND on every tool status flip (via applyStatusUpdate). */
function refreshGroupHeader(group: HTMLElement): void {
  const headerText = group.querySelector(".tool-group-header .tool-group-count");
  if (headerText === null) {
    return;
  }
  const calls = [...group.querySelectorAll(":scope > .tool-call")] as HTMLElement[];
  const collapsed =
    group.classList.contains(CLS_COLLAPSED) ||
    group.classList.contains(CLS_AUTO_COLLAPSED);
  const summary = summarize(calls);
  headerText.textContent = collapsed ? `${summary} (collapsed)` : summary;
}

function updateAssistant(wrap: HTMLElement, m: Message): void {
  const state = messageStates.get(m.id);
  if (state === undefined) {
    return;
  }

  // Late-arriving reasoning: mount the block if it didn't exist at
  // initial mount time. Subsequent reasoning chunks flow through the
  // signal effect set up at mount.
  const reasoning = m.reasoning ?? "";
  let reasoningEl = wrap.querySelector<HTMLDetailsElement>(":scope > .msg-reasoning");
  if (reasoning !== "" && reasoningEl === null) {
    reasoningEl = mountReasoningBlock(wrap, reasoning, state.streaming, m.id);
    // Place at the top of the wrap if the row already exists.
    const firstChild = wrap.firstElementChild;
    if (firstChild !== reasoningEl) {
      wrap.prepend(reasoningEl);
    }
  }

  // Late-arriving content: mount the bubble if it didn't exist at
  // initial mount time (first-chunk-content scenario).
  const fullText = m.content ?? "";
  let bubble = wrap.querySelector<HTMLDivElement>(":scope > .msg-row > .message.assistant");
  if (fullText !== "" && bubble === null) {
    bubble = mountContentBubble(wrap, fullText, state.streaming, m.id);
    // mountContentBubble appends; if reasoning exists, ensure row is below it.
    const reasoningNow = wrap.querySelector(":scope > .msg-reasoning");
    if (reasoningNow !== null) {
      const row = bubble.parentElement; // .msg-row
      if (row !== null && reasoningNow.nextElementSibling !== row) {
        reasoningNow.after(row);
      }
    }
  }

  // Tool calls: reconcile inside the existing .tool-group (or create one).
  const calls = m.tool_calls ?? [];
  let tools = wrap.querySelector<HTMLDivElement>(":scope > .tool-group");
  if (calls.length > 0) {
    tools ??= mountToolGroup(wrap);
    reconcile(tools, calls, toolSpec);
    refreshGroupHeader(tools);
  } else if (tools !== null) {
    tools.remove();
  }

  // Plan: reconcile entries in place. Action buttons keep their
  // click handlers because the plan card itself isn't replaced.
  const plan = wrap.querySelector<HTMLDivElement>(":scope > .plan-message");
  if (m.plan !== undefined && m.plan.length > 0) {
    if (plan === null) {
      const fresh = planElement(m.plan);
      if (tools !== null) {
        tools.before(fresh);
      } else {
        wrap.appendChild(fresh);
      }
    } else {
      updatePlanElement(plan, m.plan);
    }
  } else if (plan !== null) {
    plan.remove();
  }
}

/** Finalize the streaming pipeline for one element: end the markdown
 *  stream (flushes pending buffer, decorates last block), drop the
 *  .streaming class, attach turn-actions row. Idempotent: callers may
 *  invoke without knowing whether the element was streaming. */
function finalizeStreamingPipeline(el: Element | null): void {
  if (el === null) {
    return;
  }
  const bubble = el as HTMLDivElement;
  const s = streams.get(bubble);
  if (s !== undefined) {
    s.end();
    streams.delete(bubble);
  }
  bubble.classList.remove("streaming");
  attachTurnActions(bubble);
}

/** Walk the messages array; finalize the live streaming bubble when
 *  either (a) another message has arrived after it, or (b) the agent
 *  has stopped thinking (turn ended). Driven from the same effect
 *  that paints, so this stays consistent with store state. */
function finalizeStreamingIfNeeded(messages: readonly Message[]): void {
  const lastAssistantIdx = lastAssistantIndex(messages);
  const session = getActive();
  const isThinking = session?.thinking ?? false;
  for (const [id, st] of messageStates) {
    if (!st.streaming) {
      continue;
    }
    const stillLast = id === messages[lastAssistantIdx]?.id;
    if (!stillLast || !isThinking) {
      st.streaming = false;
      finalizeStreamingPipeline(st.el.querySelector(".message.assistant.streaming"));
      disposeStreamingEffect(id);
    }
  }
}

function lastAssistantIndex(messages: readonly Message[]): number {
  for (let i = messages.length - 1; i >= 0; i--) {
    if (messages[i]?.role === "assistant") {
      return i;
    }
  }
  return -1;
}

/** Heuristic: an assistant message is "live streaming" when its parent
 *  session is currently thinking AND this is the last assistant in the
 *  array. Both content and reasoning chunks may flow into a live
 *  message; we hook both signals at mount time. Replay path skips this. */
function isLikelyLiveStreaming(m: Message): boolean {
  if (m.role !== "assistant") {
    return false;
  }
  const session = getActive();
  if (session === undefined) {
    return false;
  }
  if (!session.thinking) {
    return false;
  }
  const idx = lastAssistantIndex(session.messages);
  return idx >= 0 && session.messages[idx]?.id === m.id;
}


// --- Helpers ---

function makeRow(side: "user" | "assistant"): HTMLDivElement {
  const row = document.createElement("div");
  row.className = `msg-row msg-row-${side}`;
  const avatar = document.createElement("div");
  avatar.className = "msg-avatar";
  avatar.appendChild(side === "assistant" ? cloneKiroAvatar() : cloneUserAvatar());
  row.appendChild(avatar);
  return row;
}

async function explainError(errorText: string, toolTitle: string): Promise<string> {
  const d = await explainErrorAction.dispatch({ errorText, context: toolTitle });
  return d?.output ?? "";
}

// ---------------------------------------------------------------------------
// Permission DOM hook (called by permission.ts for tool approval UI)
// ---------------------------------------------------------------------------

/** Permission.ts calls this to find a tool card by id and attach the
 *  approve/deny prompt UI. Returns the tool card element or undefined. */
export function findToolCard(id: string): HTMLDivElement | undefined {
  return toolEls.get(id);
}
