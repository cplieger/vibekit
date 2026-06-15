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

import type { Message, Block } from "./types.js";
import { createMarkdownStream, renderMarkdownInto, type MarkdownStream } from "./markdown.js";
import { getActive, messagesVersion, activeSession } from "./store.js";
import {
  ensureStreamingSig,
  clearStreamingSig,
  ensureReasoningSig,
  clearReasoningSig,
  ensureBlockTextSig,
  ensureBlockThinkingSig,
} from "./store-signals.js";
import { effect, el } from "@cplieger/reactive";
import { reconcile, KEY_ATTR as RECONCILE_KEY, type ReconcileSpec } from "./reconcile.js";
import { $ } from "./dom.js";
import {
  getScrollEl,
  scrollToBottom,
  setUserScrolledUp,
  resetScrollState,
  setLoadMore,
} from "./scroll.js";
import { resetSubAgents } from "./subagent.js";
import {
  breakToolGroup,
  summarize,
  CLS_COLLAPSED,
  CLS_AUTO_COLLAPSED,
  CLS_USER_TOGGLED,
} from "./tool-group.js";
import { linkifyPaths } from "./linkify.js";
import { explainError as explainErrorAction } from "./actions/messages.js";
import { initMessageActions, clearActionBindings } from "./messages-actions.js";
import { clearCrews } from "./crew-card.js";
import { send } from "./transport.js";
import { toolSpec, toolEls, disposeAllToolEffects, initToolCallbacks } from "./messages-tools.js";
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
    void activeSession.value;
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

/**
 * rewindConfirmText builds the confirmation shown before branching a new
 * chat from a past turn. It surfaces what the user is rewinding from — the
 * prompt preview plus the following assistant turn's tool-call and
 * touched-file counts — mirroring kiro-cli 2.7's enriched /rewind preview,
 * built from data vibekit already persists (no extra round-trip). All
 * field reads are defensive so a sparse/legacy message never throws.
 */
function rewindConfirmText(m: Message, next: Message | undefined): string {
  const promptRaw = (m.content ?? "").trim().replace(/\s+/g, " ");
  const prompt = promptRaw.length > 100 ? promptRaw.slice(0, 100) + "…" : promptRaw;
  const lines = ["Rewind from this turn?", ""];
  if (prompt.length > 0) {
    lines.push(`Prompt: "${prompt}"`);
  }
  if (next !== undefined && next.role === "assistant") {
    const calls = next.tool_calls ?? [];
    if (calls.length > 0) {
      const files = [
        ...new Set(
          calls.flatMap((c) => (c.locations ?? []).map((l) => l.path.split("/").pop() ?? l.path)),
        ),
      ];
      const toolPart = `${String(calls.length)} tool call${calls.length === 1 ? "" : "s"}`;
      const filePart =
        files.length > 0
          ? `, ${String(files.length)} file${files.length === 1 ? "" : "s"} touched (${files.slice(0, 4).join(", ")}${files.length > 4 ? ", …" : ""})`
          : "";
      lines.push(`This turn's response: ${toolPart}${filePart}.`);
    }
  }
  lines.push("");
  lines.push(
    "Creates a new chat branched from this point. File contents on disk are not affected (use Restore for that).",
  );
  return lines.join("\n");
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
    const node = buildMessage(m);
    // Only animate genuinely-new appended messages; chat-switch replay
    // and pagination prepends mount silently. See paint() for how
    // appendNewIds is populated.
    if (appendNewIds.has(m.id)) {
      node.setAttribute("data-chat-entry", "");
    }
    const stagger = staggerIndex.get(m.id);
    if (stagger !== undefined && stagger > 0) {
      node.style.setProperty("--stagger-index", String(stagger));
    }
    messageStates.set(m.id, {
      el: node,
      streaming: m.role === "assistant" && isLikelyLiveStreaming(m),
    });
    return node;
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
  const wrap = el("div", { className: "msg-wrap msg-wrap-user" });

  // Optional checkpoint divider above the bubble.
  const cp = checkpointTags.get(m.id) ?? "";
  if (cp !== "") {
    const line = el("div", { className: "checkpoint-line" });
    const label = el("span", { className: "checkpoint-label" }, "Checkpoint");
    const btn = el(
      "button",
      {
        className: "checkpoint-restore",
        type: "button",
        "data-tag": cp,
        title: "Restore files to this point",
        "aria-label": `Restore to checkpoint ${cp}`,
      },
      "Restore",
    );
    // Rewind button: creates a new chat branched from this turn.
    const rewindBtn = el(
      "button",
      {
        className: "checkpoint-rewind",
        type: "button",
        title: "Rewind conversation from this point",
        "aria-label": "Rewind from this turn",
      },
      "Rewind",
    );
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
      if (!confirm(rewindConfirmText(m, session.messages[turnIdx + 1]))) {
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
  const bubble = el("div", { className: "message user" }, m.content ?? "");
  linkifyPaths(bubble);
  row.appendChild(bubble);
  wrap.appendChild(row);

  // User messages always pop the user back to the bottom. Previously
  // we did `setUserScrolledUp(false); suppressScroll(400)` here, but
  // suppressScroll(400) actively *blocks* the auto-scroll for the
  // message that just arrived — so when the user submits while
  // scrolled up, the new bubble appended off-screen and the page
  // didn't scroll until the model started streaming back ~hundreds of
  // milliseconds later. scrollToBottom() does an explicit RAF-paced
  // scroll that lands on the new user bubble immediately.
  scrollToBottom();
  return wrap;
}

// --- Assistant (with streaming) ---

function buildAssistant(m: Message): HTMLElement {
  const wrap = el("div", { className: "msg-wrap msg-wrap-assistant" });

  const blocks = m.blocks ?? [];
  // Block-aware path: render text, tool_use, and thinking blocks in
  // chronological order (Anthropic's content-blocks model). Server-side
  // accumulation guarantees `blocks` reflects the order the agent
  // emitted them. Legacy messages without `blocks` (older transcripts
  // persisted before the field existed) fall through to the historical
  // "all text + reasoning above + tool group below" layout.
  if (blocks.length > 0) {
    const live = isLikelyLiveStreaming(m);
    buildAssistantBlocks(wrap, m, blocks, live);
    if (m.plan !== undefined && m.plan.length > 0) {
      wrap.appendChild(planElement(m.plan));
    }
    return wrap;
  }

  const content = m.content ?? "";
  const reasoning = m.reasoning ?? "";
  const live = isLikelyLiveStreaming(m);

  // Reasoning block above the content bubble. Mounted whenever there's
  // existing reasoning. For a *live* message we don't pre-mount the
  // block — that produced a "Thinking…" affordance even on turns where
  // the model never emits reasoning, and (after onFinalize fired) left
  // an empty "Thinking completed" dropdown the user could expand to see
  // nothing. Instead we subscribe to the reasoning signal here and the
  // updateAssistant() late-mount path below renders the block on the
  // first non-empty delta.
  if (reasoning !== "") {
    const block = buildReasoningBlock(reasoning, live, m.id);
    wrap.appendChild(block);
  } else if (live) {
    // Subscribe to the signal so updateAssistant sees the reasoning
    // arrive (the signal is per-message and stored in the store; the
    // effect inside buildReasoningBlock is what consumes it once the
    // block actually mounts).
    void ensureReasoningSig(m.id, "");
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
 *  fan in here without re-running the global reconcile.
 *
 *  Lazy-mount semantics: this builds the details element but only the
 *  caller decides whether to attach it to the DOM. For a live message
 *  with empty initial reasoning we instead wait for the first signal
 *  delta and attach then — see the lazy-mount path in buildAssistant. */
function buildReasoningBlock(
  reasoning: string,
  live: boolean,
  messageID: string,
): HTMLDetailsElement {
  const details = el("details", {
    className: "reasoning-block msg-reasoning",
  }) as HTMLDetailsElement;
  if (live) {
    details.open = true;
    // .streaming gates the "active thinking" CSS affordances (pulsing
    // dot + accent border) so they don't re-fire when the user
    // re-expands a finalized trace.
    details.classList.add("streaming");
  }

  const summary = el(
    "summary",
    { className: "reasoning-summary" },
    live ? "Thinking…" : "Reasoning",
  );
  details.appendChild(summary);

  const body = el("blockquote", { className: "reasoning-body" }, reasoning);
  details.appendChild(body);

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
    // Wrap cleanup so it also flips the summary on disposal and clears
    // the live-streaming class.
    const onFinalize = (): void => {
      cleanup();
      summary.textContent = "Thinking completed";
      details.classList.remove("streaming");
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
  const bubble = el("div", { className: "message assistant" }) as HTMLDivElement;
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
  const group = el("div", { className: "tool-group" }) as HTMLDivElement;
  const header = el(
    "div",
    {
      className: "tool-group-header",
      role: "button",
      tabindex: "0",
      "aria-expanded": "true",
    },
    el("span", { className: "tool-group-count" }),
  );
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
    group.classList.contains(CLS_COLLAPSED) || group.classList.contains(CLS_AUTO_COLLAPSED);
  const summary = summarize(calls);
  headerText.textContent = collapsed ? `${summary} (collapsed)` : summary;
}

// ---------------------------------------------------------------------------
// Block-aware (chronological) assistant rendering
// ---------------------------------------------------------------------------

/** Render an assistant message's blocks in chronological order — text,
 *  tool_use, and thinking interleaved as the agent emitted them. Mirrors
 *  Anthropic's content_block model and claude-code's per-block dispatch.
 *  Each block element is tagged with `data-block-idx` so updateAssistant
 *  can detect new blocks arriving during a live turn. */
function buildAssistantBlocks(wrap: HTMLElement, m: Message, blocks: Block[], live: boolean): void {
  const lastIdx = blocks.length - 1;
  for (let i = 0; i < blocks.length; i++) {
    const block = blocks[i];
    if (block === undefined) {
      continue;
    }
    // Only the trailing block of a live message is itself "live" —
    // earlier blocks are sealed (a new block was started after them
    // because the run kind changed: text → tool, tool → text, etc.).
    const isLiveBlock = live && i === lastIdx;
    const node = mountBlockElement(block, i, m, isLiveBlock);
    if (node !== null) {
      node.setAttribute("data-block-idx", String(i));
      wrap.appendChild(node);
    }
  }
}

/** Build a single block's DOM. Returns null if the block references
 *  state that hasn't arrived yet (e.g. a tool_use block whose ToolCall
 *  isn't in m.tool_calls — we've seen this on out-of-order SSE arrivals). */
function mountBlockElement(
  block: Block,
  blockIndex: number,
  m: Message,
  live: boolean,
): HTMLElement | null {
  switch (block.type) {
    case "text":
      return mountTextBlockBubble(block, blockIndex, m.id, live);
    case "thinking": {
      const text = block.thinking ?? "";
      // Skip empty non-live thinking blocks — same rationale as the
      // legacy lazy-mount: an empty "Thinking completed" dropdown the
      // user can expand to see nothing is worse than no dropdown.
      if (text === "" && !live) {
        return null;
      }
      const detail = buildReasoningBlock(text, live, m.id);
      if (live) {
        // Wire a per-block thinking signal so further deltas for THIS
        // block index land here rather than firing the legacy
        // per-message reasoning signal which is only tied to the
        // legacy single-bubble layout.
        const sig = ensureBlockThinkingSig(m.id, blockIndex, text);
        const body = detail.querySelector(".reasoning-body");
        const summary = detail.querySelector(".reasoning-summary");
        let lastLen = text.length;
        const cleanup = effect(() => {
          const full = sig.value;
          if (full.length <= lastLen) {
            return;
          }
          if (body !== null) {
            body.appendChild(document.createTextNode(full.slice(lastLen)));
          }
          lastLen = full.length;
        });
        const onFinalize = (): void => {
          cleanup();
          if (summary !== null) {
            summary.textContent = "Thinking completed";
          }
          detail.classList.remove("streaming");
          detail.open = false;
        };
        pushStreamingEffect(m.id, onFinalize);
      }
      return detail;
    }
    case "tool_use": {
      const tc = m.tool_calls?.find((c) => c.id === block.tool_call_id);
      if (tc === undefined) {
        return null;
      }
      const node = toolSpec.mount(tc);
      // Reconcile-key marker so any future global reconcile pass over
      // tool_calls leaves these inline mounts alone (the data attribute
      // mirrors what reconcile.ts uses internally).
      if (node instanceof HTMLElement) {
        node.setAttribute(RECONCILE_KEY, tc.id);
        return node;
      }
      return null;
    }
    default:
      return null;
  }
}

/** Mount a single text block's bubble. For replay, renders the full
 *  markdown one-shot. For the live trailing block, primes a stream
 *  with the current text and subscribes to the per-block text signal
 *  so subsequent chunks for blockIndex land here without re-rendering
 *  earlier text blocks. */
function mountTextBlockBubble(
  block: Block,
  blockIndex: number,
  messageID: string,
  live: boolean,
): HTMLElement {
  const row = makeRow("assistant");
  const bubble = el("div", { className: "message assistant" }) as HTMLDivElement;
  if (live) {
    bubble.classList.add("streaming");
  }
  const text = block.text ?? "";
  if (text !== "") {
    if (!live) {
      renderMarkdownInto(bubble, text);
    } else {
      ensureStream(bubble).writeDelta(text);
    }
  }
  row.appendChild(bubble);

  if (live) {
    const sig = ensureBlockTextSig(messageID, blockIndex, text);
    let lastLen = text.length;
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
  return row;
}

function updateAssistant(wrap: HTMLElement, m: Message): void {
  const state = messageStates.get(m.id);
  if (state === undefined) {
    return;
  }

  // Block-aware path: if the message uses chronological blocks, mount
  // any newly-arrived blocks at the end (in order) and de-stream the
  // previously-trailing live block so only the newest one carries the
  // .streaming affordance. Per-block signals feed deltas into already-
  // mounted blocks without going through this function.
  const blocks = m.blocks ?? [];
  if (blocks.length > 0) {
    const rendered = wrap.querySelectorAll(":scope > [data-block-idx]").length;
    if (blocks.length > rendered) {
      // Strip the .streaming class from whatever was the previous
      // trailing block — a new block being added means that one is
      // sealed.
      const prevLast = wrap.querySelector<HTMLElement>(
        `:scope > [data-block-idx="${String(rendered - 1)}"] .message.assistant.streaming`,
      );
      if (prevLast !== null) {
        prevLast.classList.remove("streaming");
      }
      const lastIdx = blocks.length - 1;
      // Find the plan element if any so we keep blocks before it.
      const plan = wrap.querySelector<HTMLElement>(":scope > .plan-card");
      for (let i = rendered; i < blocks.length; i++) {
        const block = blocks[i];
        if (block === undefined) {
          continue;
        }
        const isLiveBlock = state.streaming && i === lastIdx;
        const node = mountBlockElement(block, i, m, isLiveBlock);
        if (node !== null) {
          node.setAttribute("data-block-idx", String(i));
          if (plan !== null) {
            wrap.insertBefore(node, plan);
          } else {
            wrap.appendChild(node);
          }
        }
      }
    }
    // Plan: mount/update lazily — late plans aren't part of blocks.
    if (m.plan !== undefined && m.plan.length > 0) {
      let planEl = wrap.querySelector<HTMLDivElement>(":scope > .plan-card");
      if (planEl === null) {
        planEl = planElement(m.plan);
        wrap.appendChild(planEl);
      } else {
        updatePlanElement(planEl, m.plan);
      }
    }
    // For tool-call status updates inside an already-mounted tool_use
    // block, the per-tool toolCallSig effect (set up by toolSpec.mount)
    // handles the reactive update — nothing to do here.
    return;
  }

  // Late-arriving reasoning: mount the block if it didn't exist at
  // initial mount time. Subsequent reasoning chunks flow through the
  // signal effect set up at mount.
  const reasoning = m.reasoning ?? "";
  let reasoningEl = wrap.querySelector<HTMLDetailsElement>(":scope > .msg-reasoning");
  if (reasoning !== "" && reasoningEl === null) {
    reasoningEl = buildReasoningBlock(reasoning, state.streaming, m.id);
    wrap.appendChild(reasoningEl);
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
  const row = el("div", { className: `msg-row msg-row-${side}` }) as HTMLDivElement;
  const avatar = el("div", { className: "msg-avatar" });
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
