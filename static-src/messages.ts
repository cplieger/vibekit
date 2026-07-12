// ---------------------------------------------------------------------------
// Message view: signal-driven reactive renderer.
//
// One effect watches store.version + the active session's messages array
// and reconciles them into $.messages by message id. Per-message factories
// (buildUser / buildAssistant / buildEvent) own initial DOM construction;
// per-message updaters (updateAssistant, updateEvent) own incremental
// changes.
//
// Assistant bodies are composed ENTIRELY from the fundamentals/ primitives by
// the single block dispatcher in messages-blocks.ts — this module is the shell
// that mounts and updates them by message identity, owns the streaming-effect
// registry + avatar rows, and drives turn finalization from store state.
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
import { getActive, getActiveId, get, messagesVersion, activeSession } from "./store.js";
import { clearStreamingSig, clearReasoningSig, clearAllBlockSigs } from "./store-signals.js";
import { effect, el } from "@cplieger/reactive";
import { reconcile, type ReconcileSpec } from "./reconcile.js";
import { $ } from "./dom.js";
import { getScrollEl, scrollToBottom, resetScrollState, setLoadMore } from "./scroll.js";
import { buildUserBubble } from "./fundamentals/text-bubble.js";
import {
  buildAssistantBody,
  updateAssistantBody,
  finalizeAssistantBody,
  disposeAssistantBody,
  resetBlockRenders,
  refreshGroupHeader,
  initBlockRenderer,
} from "./messages-blocks.js";
import { explainError as explainErrorAction } from "./actions/messages.js";
import { rewindChat } from "./actions/rewind.js";
import { initMessageActions, clearActionBindings } from "./messages-actions.js";
import { confirm as confirmDialog } from "./confirm.js";
import { disposeAllToolEffects, initToolCallbacks } from "./messages-tools.js";
import { buildEvent, updateEvent, buildSystemFallback } from "./messages-events.js";
import { attachTurnActions, initTurnActionCallbacks } from "./messages-turn-actions.js";
import { syncCodeReferences } from "./code-refs.js";

// ---------------------------------------------------------------------------
// Public re-exports
// ---------------------------------------------------------------------------

export { getScrollEl, scrollToBottom, setLoadMore };

// ---------------------------------------------------------------------------
// Module state
// ---------------------------------------------------------------------------

const messagesEl = $.messages;

/** Per-message-id metadata kept for the duration the message is mounted. */
interface MessageState {
  el: HTMLElement;
  /** True while this is the live streaming bubble; transitions to false
   *  on turn end via finalizeStreamingIfNeeded(). */
  streaming: boolean;
}
const messageStates = new Map<string, MessageState>();

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
 *  unmount. A single message can register multiple cleanups (one per
 *  live text/thinking block + subagent/todo status effects). Separate
 *  from bindUnbinds so tool-card loading-state bindings survive turn end. */
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
initBlockRenderer({ pushStreamingEffect, makeRow });

/** Mount the chat view. Idempotent. Called once at app boot from app.ts.
 *  Subscribes to store.version and reconciles the message list on every
 *  bump. Streaming markdown chunks flow through per-block signals bound
 *  at mount, not through this effect. */
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
    // No session for the active id. Only clear when there is genuinely NO
    // active chat (all closed). A transient undefined during a chat switch or
    // a not-yet-loaded session must NOT wipe the DOM — that empty reconcile
    // pass, immediately followed by a re-populate, was the flashing bug.
    if (getActiveId() === "") {
      teardownAll();
    }
    return;
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
  if (next?.role === "assistant") {
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

/**
 * handleRewindClick confirms the rewind, dispatches it, then opens AND
 * activates the returned branch chat. Mirrors chat.ts's restoreArchivedChat
 * pattern: refresh the header list so the branch session exists in the store,
 * then open its tab (openChatTab activates it via onShow → activateChatView,
 * which loads the branch's messages). chat.ts / store-load.ts are imported
 * dynamically because chat.ts statically imports this module — a static import
 * back would be a cycle. The confirm uses the app's confirmDialog (not the
 * native, unstyled, focus-trap-less window.confirm).
 */
async function handleRewindClick(m: Message): Promise<void> {
  const session = getActive();
  if (session === undefined) {
    return;
  }
  const turnIdx = session.messages.findIndex((msg) => msg.id === m.id);
  if (turnIdx < 0) {
    return;
  }
  const proceed = await confirmDialog(rewindConfirmText(m, session.messages[turnIdx + 1]));
  if (!proceed) {
    return;
  }
  const res = await rewindChat.dispatch({ chatID: session.id, turnIndex: turnIdx });
  const newID = res?.rewind_id ?? "";
  if (newID === "") {
    return;
  }
  const [storeLoad, chatMod] = await Promise.all([import("./store-load.js"), import("./chat.js")]);
  await storeLoad.loadList();
  chatMod.openChatTab(newID, get(newID)?.name ?? `Rewind: ${session.name}`);
}

/** Clear all per-message state, e.g. when the last chat is closed (active
 *  session genuinely gone). A real session arriving repaints from scratch. */
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
  resetBlockRenders();
  clearAllBlockSigs();
  messageStates.clear();
  clearActionBindings();
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
    // Licensed-code attribution footnote. One call site here + in update()
    // covers mount + update, keyed off m.code_references.
    if (m.role === "assistant") {
      syncCodeReferences(node, m);
    }
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
    // isLikelyLiveStreaming already returns false for non-assistant roles.
    const liveStreaming = isLikelyLiveStreaming(m);
    messageStates.set(m.id, { el: node, streaming: liveStreaming });
    // Historical / reloaded assistant turns finalize at mount — they never
    // pass through the live-stream finalize path — so attach the copy/export
    // turn-actions row here. Live turns get it later via finalizeTurn when the
    // stream ends. (This is why switching away and back to a chat used to drop
    // the buttons: re-mounted turns were finalized but never decorated.)
    if (m.role === "assistant" && !liveStreaming) {
      const bubble = node.querySelector<HTMLDivElement>(".message.assistant");
      if (bubble !== null) {
        attachTurnActions(bubble);
      }
    }
    return node;
  },
  update: (el, m) => {
    updateMessage(el, m);
    if (m.role === "assistant") {
      syncCodeReferences(el, m);
    }
  },
  onRemove: (_el, key) => {
    const arr = bindUnbinds.get(key);
    if (arr !== undefined) {
      for (const fn of arr) {
        fn();
      }
      bindUnbinds.delete(key);
    }
    disposeStreamingEffect(key);
    // Flush any live markdown stream, then drop the block render state
    // (cleanup only — the message row is being removed).
    finalizeAssistantBody(key);
    disposeAssistantBody(key);
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

  // Checkpoint / rewind affordances above the bubble. The server stamps the
  // REAL per-turn checkpoint tag on the user message (m.checkpoint_tag), set
  // only on turns whose agent wrote at least one file — so Restore renders
  // only when that tag is non-empty (no more off-by-one recompute from a
  // 0-based turn index). Rewind is decoupled from checkpoint existence:
  // branching a new chat is independent of file snapshots, so it is offered
  // on EVERY user turn (a read-only / Q&A turn can still be branched from).
  const cp = m.checkpoint_tag ?? "";
  const line = el("div", { className: "checkpoint-line" });
  if (cp !== "") {
    const label = el("span", { className: "checkpoint-label" }, "Checkpoint");
    const restoreBtn = el(
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
    line.append(label, restoreBtn);
  }
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
    void handleRewindClick(m).catch((e: unknown) => {
      console.warn("[messages] rewind failed", e);
    });
  });
  line.appendChild(rewindBtn);
  wrap.appendChild(line);

  const row = makeRow("user");
  const bubble = buildUserBubble(m.content ?? "");
  row.appendChild(bubble);
  wrap.appendChild(row);

  // User messages always pop the user back to the bottom. scrollToBottom()
  // does an explicit RAF-paced scroll that lands on the new user bubble
  // immediately (suppressScroll would have blocked the auto-scroll for the
  // very message that just arrived).
  scrollToBottom();
  return wrap;
}

// --- Assistant ---

/** Build an assistant turn. The whole body — text bubbles, reasoning,
 *  tool cards/groups, subagent blocks, todo checklists, plan, turn footer —
 *  is composed by the single block dispatcher (messages-blocks.ts) from the
 *  message's canonical `blocks` array. */
function buildAssistant(m: Message): HTMLElement {
  const wrap = el("div", { className: "msg-wrap msg-wrap-assistant" });
  buildAssistantBody(wrap, m, isLikelyLiveStreaming(m));
  return wrap;
}

/** Incremental update: mount newly-arrived blocks + refresh plan/footer.
 *  Per-block and per-tool signals feed streaming deltas straight into the
 *  already-mounted primitives, so this only handles structural growth. */
function updateAssistant(wrap: HTMLElement, m: Message): void {
  const state = messageStates.get(m.id);
  if (state === undefined) {
    return;
  }
  updateAssistantBody(wrap, m, state.streaming);
}

/** Finalize a streamed assistant turn: flush every markdown stream + seal
 *  every reasoning trace (via the block dispatcher), then attach the
 *  copy/export turn-actions row. */
function finalizeTurn(id: string, root: HTMLElement): void {
  finalizeAssistantBody(id);
  const bubble = root.querySelector<HTMLDivElement>(".message.assistant");
  if (bubble !== null) {
    attachTurnActions(bubble);
  }
}

/** Walk the message STATE (messageStates + the session's thinking flag), not
 *  the DOM, to decide which live turns to finalize: a streaming turn finalizes
 *  when either (a) another message arrived after it, or (b) the agent stopped
 *  thinking (turn ended). Driven from the same effect that paints, so it stays
 *  consistent with store state. */
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
      finalizeTurn(id, st.el);
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
 *  array. Replay path skips this. */
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
