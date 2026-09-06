// ---------------------------------------------------------------------------
// Message view: signal-driven reactive renderer, and the transcript
// MULTIPLEXER — `#messages` holds one `.transcript-view` per resident chat,
// the active one live, the parked ones frozen (see the multiplexer section).
//
// One effect watches the active chat id + that chat's messages version and
// reconciles its messages into the ACTIVE view by message id. Per-message
// factories (buildUser / buildAssistant / buildEvent) own initial DOM
// construction; per-message updaters (updateAssistant, updateEvent) own
// incremental changes.
//
// Assistant bodies are composed ENTIRELY from the fundamentals/ primitives by
// the single block dispatcher in messages-blocks.ts — this module is the shell
// that mounts and updates them by message identity, owns the streaming-effect
// registry + avatar rows, and drives turn finalization from store state.
//
// The "liquid" feel comes from CSS:
//   - @starting-style + transitions on `.msg-row` for entry animations
//   - .streaming class on the active assistant TEXT bubble: an accent wash
//     plus a blinking block caret (css/13-messages.css). A reasoning trace
//     carries no such marker — see fundamentals/reasoning.ts
//   - interpolate-size: allow-keywords on :root so height: auto can
//     animate (set in css/01-tokens.css)
//   - content-visibility: auto on rows so off-screen messages don't pay
//     paint cost
// ---------------------------------------------------------------------------

import type { Message, Session } from "./types.js";
import {
  get,
  getActive,
  getActiveId,
  watchActiveId,
  messagesVersionOf,
  renderCauseOf,
  steerMarks,
  bumpMessages,
  registerEvictionExemption,
} from "./store.js";
import { clearStreamingSig, clearReasoningSig, clearBlockSigsFor } from "./store-signals.js";
import { effect, el, touch } from "@cplieger/reactive";
import { reconcile, KEY_ATTR, type ReconcileSpec } from "./reconcile.js";
import { CHAT_SKELETON_ID } from "./skeleton.js";
import { $, forceReflow } from "./dom.js";
import { setComposerValue } from "./composer-value.js";
import {
  getScrollEl,
  scrollToBottom,
  resetScrollState,
  setLoadMore,
  deferWhileReading,
  preserveReadingPosition,
  fillViewport,
  onReadingStateChange,
  onViewportChange,
  setAnchorProvider,
  setResumeLabel,
  readingState,
  attach as attachScroll,
  detach as detachScroll,
  type ReadingState,
} from "./scroll.js";
import {
  buildTurnHeader,
  updateTurnHeader,
  initTurnHeaderCallbacks,
  type TurnHeaderData,
} from "./fundamentals/turn-header.js";
import {
  buildTurnFooter,
  updateTurnFooter,
  hasTurnSummary,
  type TurnSummaryData,
} from "./fundamentals/turn-footer.js";
import {
  projectTurns,
  turnLedger,
  turnAnchorID,
  turnFaceProse,
  turnFaceError,
  turnRunIDs,
  turnFoldHides,
  type Turn,
} from "./turns.js";
import { buildAssistantBubble } from "./fundamentals/text-bubble.js";
import { isTurnOpen, setTurnOpen } from "./fold-state.js";
import {
  planResidency,
  sliceTurn,
  turnCost,
  turnOrdinalOf,
  OVERSCAN_BLOCKS,
  RESIDENT_BLOCKS,
  type BlockRange,
  type ResidencyAnchor,
  type TurnRange,
} from "./block-window.js";
import { recordRowHeight, spacerHeight } from "./block-heights.js";
import { wireRowToggle } from "./disclosure-row.js";
import { initSearchRevealBuilder, searchHitCount } from "./chat-search.js";
import {
  mountTurnRail,
  observeTurns,
  resetTurnRail,
  loadTurnRail,
  pointTurnRail,
  initTurnRailCallbacks,
} from "./turn-rail.js";
import {
  buildAssistantBody,
  updateAssistantBody,
  finalizeAssistantBody,
  disposeAssistantBody,
  pauseAssistantBody,
  resumeAssistantBody,
  resetBlockRenders,
  refreshGroupHeader,
  refreshMessageCard,
  liveRenderIDs,
  openContainerKeys,
  initBlockRenderer,
  getLiveAnchor,
  mountFaceRunCard,
  mountedWindow,
  mountHeadRange,
  dropHead,
  dropTail,
} from "./messages-blocks.js";
import { explainError as explainErrorAction } from "./actions/messages.js";
import { rewindChat } from "./actions/rewind.js";
import { confirm as confirmDialog } from "./confirm.js";
import { registerCleanup } from "./actions/index.js";
import {
  disposeAllToolEffects,
  disposeToolEffectsForChat,
  suspendToolEffectsFor,
  resumeToolEffectsFor,
  drainParkedTerminals,
  initToolCallbacks,
  initToolViewCallbacks,
} from "./messages-tools.js";
import { buildEvent, updateEvent, buildSystemFallback } from "./messages-events.js";
import {
  mountTurnFooterActions,
  resetTurnSourceView,
  turnMarkdown,
  initTurnActionCallbacks,
  initTurnActionsBodyProbe,
  syncSourceView,
  copyWithFeedback,
} from "./messages-turn-actions.js";
import { syncCodeReferences } from "./code-refs.js";
import { syncRefusal, setRefusalRewindHandler } from "./refusal.js";

// ---------------------------------------------------------------------------
// Public re-exports
// ---------------------------------------------------------------------------

export { getScrollEl, setLoadMore };
// Re-exported for the same reason the scroll helpers are: this module owns the
// rail (it mounts it and feeds it the painted cards), so chat.ts reaching the
// rail THROUGH here keeps ownership in one place instead of two modules driving
// the same surface.
export { loadTurnRail, pointTurnRail };

// ---------------------------------------------------------------------------
// Module state
// ---------------------------------------------------------------------------

const messagesEl = $.messages;

// ---------------------------------------------------------------------------
// The transcript multiplexer.
//
// `#messages` holds one `.transcript-view` per RESIDENT chat view; exactly one
// carries `.is-active` and the scroller's observers. A parked view keeps its
// DOM, its `MsgRender`s and its `messageStates` rows, with every writer that
// could reach that DOM paused: streaming effects disposed, tool + run effects
// suspended, clock holds released, tickers stopped, terminal output buffered.
// The store keeps ingesting for parked chats — only rendering is frozen.
// ---------------------------------------------------------------------------

/** How many PARKED views stay resident (the active view is not counted).
 *  Past this, the least-recently-used parked view runs the real dispose. */
export const PARKED_VIEWS = 3;

/** One resident chat view. The saved-at-park fields are meaningful only while
 *  `parked` is true; the view→message index is deliberately NOT here — it is
 *  the store read (`chatID` + the session's message ids ∩ `messageStates`). */
interface ChatView {
  chatID: string;
  el: HTMLElement;
  parked: boolean;
  scrollTop: number;
  readingState: ReadingState;
  followBaseline: number;
  reachableBlocks: number;
  lastNewestId: string | undefined;
  resumeLabel: string;
  /** Message ids that were live-streaming when the view parked. Resume
   *  rebuilds these bodies fresh whether or not they are still streaming: the
   *  ones that settled while parked missed their finalizing updates (their
   *  binding effects were disposed), so only a fresh render from the current
   *  store is equivalent to a cold rebuild. */
  pausedStreaming: Set<string>;
}

/** Resident views by chat id. Iteration order is the LRU order: activation
 *  re-inserts, so the first parked entry is the eviction candidate. */
const views = new Map<string, ChatView>();
let activeView: ChatView | null = null;

/** The active view's element, for the container consumers that mount transcript
 *  furniture (chat.ts's skeleton and load-error retry). Null when no chat view
 *  is active (boot, the last-tab window). */
export function activeTranscriptView(): HTMLElement | null {
  return activeView?.el ?? null;
}

/** A resident view's element for `chatID` — active or parked — or null. */
export function transcriptViewFor(chatID: string): HTMLElement | null {
  return views.get(chatID)?.el ?? null;
}

/** Where paint reconciles and the card walk runs: the active view, or the bare
 *  multiplexer before any view exists (kept for the boot instant; nothing
 *  renders turns there). */
function paintRoot(): HTMLElement {
  return activeView?.el ?? messagesEl;
}

/** The message ids this view has mounted: the session's messages ∩
 *  `messageStates` (the design's view→message index — a store read, no second
 *  index maintained). */
function viewMessages(session: Session): Message[] {
  return session.messages.filter((m) => messageStates.has(m.id));
}

/** Park the active view: pause every writer, save the handle, hide. */
function parkView(view: ChatView): void {
  // Focus relocation first: an inert subtree drops focus to <body> on its own,
  // and the composer is the app's focus home.
  const focused = document.activeElement;
  if (focused !== null && view.el.contains(focused)) {
    $.promptInput.focus();
  }
  const scroll = detachScroll();
  view.scrollTop = scroll.scrollTop;
  view.readingState = scroll.readingState;
  view.followBaseline = followBaseline;
  view.reachableBlocks = reachableBlocks;
  view.lastNewestId = lastNewestId;
  view.resumeLabel = $.scrollBottom.querySelector("span")?.textContent ?? "";
  view.pausedStreaming = new Set();
  const session = get(view.chatID);
  if (session !== undefined) {
    for (const m of viewMessages(session)) {
      if (messageStates.get(m.id)?.streaming === true) {
        view.pausedStreaming.add(m.id);
      }
      pauseMessage(view, m);
    }
  }
  view.el.classList.remove("is-active");
  view.el.inert = true;
  view.parked = true;
  if (activeView === view) {
    activeView = null;
  }
}

/** Make `chatID`'s view the active one, creating it when it is not resident.
 *  Returns true when the view was UNPARKED (a catch-up paint must follow and
 *  the pass's end must resume the view's messages). */
function activateView(chatID: string): boolean {
  let view = views.get(chatID);
  const unparking = view?.parked === true;
  if (view === undefined) {
    view = {
      chatID,
      el: el("div", { className: "transcript-view" }),
      parked: false,
      scrollTop: 0,
      readingState: "following",
      followBaseline: 0,
      reachableBlocks: 0,
      lastNewestId: undefined,
      resumeLabel: "",
      pausedStreaming: new Set(),
    };
    messagesEl.appendChild(view.el);
    resetScrollState();
  } else {
    // LRU refresh: re-insertion moves this chat to the back of the order.
    views.delete(chatID);
  }
  views.set(chatID, view);
  view.parked = false;
  view.el.inert = false;
  view.el.classList.add("is-active");
  activeView = view;
  attachScroll({ el: view.el, scrollTop: view.scrollTop, readingState: view.readingState });
  // After attach: entering Reading recomputes the baseline from the ACTIVE
  // session, and these are the parked chat's own numbers.
  followBaseline = view.followBaseline;
  reachableBlocks = view.reachableBlocks;
  lastNewestId = view.lastNewestId;
  if (unparking) {
    setResumeLabel(view.resumeLabel);
  }
  evictParkedViews();
  return unparking;
}

/** Dispose least-recently-used parked views past the budget. */
function evictParkedViews(): void {
  const parked = [...views.values()].filter((v) => v.parked);
  for (let i = 0; i <= parked.length - 1 - PARKED_VIEWS; i++) {
    const victim = parked[i];
    if (victim !== undefined) {
      disposeChatView(victim.chatID);
    }
  }
}

/** Dispose views whose chat left the store (close, delete, mass removal). */
function pruneDeadViews(): void {
  for (const chatID of [...views.keys()]) {
    if (get(chatID) === undefined) {
      disposeChatView(chatID);
    }
  }
}

/** The REAL per-view dispose: every message row's disposal (bind unbinds,
 *  streaming effects, block renders, per-message signals, `messageStates`
 *  pruning), the chat's tool effects through their composite keys, and the
 *  container's removal. LRU eviction, chat close/delete and `teardownAll` all
 *  run this — never a bare empty reconcile, which would strip render state
 *  while leaving parked DOM behind. */
export function disposeChatView(chatID: string): void {
  const view = views.get(chatID);
  if (view === undefined) {
    return;
  }
  for (const body of view.el.querySelectorAll<HTMLElement>(".turn-body")) {
    disposeBodyRows(body);
  }
  disposeToolEffectsForChat(chatID, view.el);
  view.el.remove();
  views.delete(chatID);
  if (activeView === view) {
    activeView = null;
    lastActiveId = undefined;
  }
}

/** Pause one message: dispose its live-binding effects (per-block signal
 *  effects, streaming effects — the same registry turn end drains), finish its
 *  reveals and suspend its run cards (messages-blocks), and suspend its
 *  generic tool-card effects through the owning view's composite keys, which
 *  also stops the duration ticker for its in-progress cards. `renders` maps,
 *  DOM, and message-lifetime bookkeeping stay. */
function pauseMessage(view: ChatView, m: Message): void {
  disposeStreamingEffect(m.id);
  pauseAssistantBody(m.id);
  suspendToolEffectsFor(
    view.chatID,
    (m.tool_calls ?? []).map((tc) => tc.id),
    view.el,
  );
}

/** Resume one message after the catch-up paint. Settled messages need nothing
 *  beyond re-arming what pause suspended — the catch-up paint's
 *  `syncMountedText` already trued grown text. A message that was streaming at
 *  park rebuilds its body fresh instead: its binding effects were disposed, so
 *  every update that landed while parked (text, tool finalizations, subagent
 *  headers) is only guaranteed to appear through a fresh render of the current
 *  store — the B5 watermark guard makes the first live delta a clean resync. */
function resumeMessage(view: ChatView, session: Session, m: Message): void {
  const state = messageStates.get(m.id);
  if (state === undefined) {
    return;
  }
  if (view.pausedStreaming.has(m.id)) {
    rebuildMessageBody(session, m, state.el);
    return;
  }
  if (m.role !== "assistant") {
    return;
  }
  resumeAssistantBody(m.id);
  resumeToolEffectsFor(session.id, m.tool_calls ?? [], view.el);
}

/** Rebuild a message's body in place, keeping the row node (its reconcile key
 *  and DOM position). The old render state goes through the same disposal an
 *  unmount runs, minus the row itself. */
function rebuildMessageBody(session: Session, m: Message, row: HTMLElement): void {
  const arr = bindUnbinds.get(m.id);
  if (arr !== undefined) {
    for (const fn of arr) {
      fn();
    }
    bindUnbinds.delete(m.id);
  }
  disposeStreamingEffect(m.id);
  finalizeAssistantBody(m.id);
  disposeAssistantBody(m.id);
  clearBlockSigsFor(m.id);
  // Drop the message's suspended tool entries so the fresh mount below cannot
  // clobber-leak them; the rebuild re-creates cards, effects and signals.
  disposeToolEffectsForChat(session.id, row);
  row.replaceChildren();
  const live = isLikelyLiveStreaming(m);
  messageStates.set(m.id, { el: row, streaming: live });
  if (live) {
    streamingIds.add(m.id);
  } else {
    streamingIds.delete(m.id);
  }
  if (m.role === "assistant") {
    buildAssistantBody(row, m, session.id, live, steerMarks(session.id), rowRange(m, row));
    syncCodeReferences(row, m);
    syncRefusal(row, m);
  }
}

/** The block range the plan currently wants for `m`'s row, read off the card the
 *  row sits in. Undefined for a row the plan has no entry for, which rebuilds the
 *  whole message — what an unranged rebuild already did. */
function rowRange(m: Message, row: HTMLElement): BlockRange | undefined {
  const turnID = row.closest<HTMLElement>(".turn")?.getAttribute(KEY_ATTR) ?? undefined;
  if (turnID === undefined) {
    return undefined;
  }
  const t = turnByID.get(turnID);
  const want = wantedWindow.get(turnID);
  return t === undefined || want === undefined ? undefined : sliceTurn(t, want).get(m.id);
}

/** Resume every paused message of the freshly unparked view, then drain the
 *  terminal output that buffered while it was parked — once. */
function resumeView(view: ChatView, session: Session): void {
  for (const m of viewMessages(session)) {
    resumeMessage(view, session, m);
  }
  view.pausedStreaming.clear();
  drainParkedTerminals(session.id, view.el);
}

/** Per-message-id metadata kept for the duration the message is mounted. */
interface MessageState {
  el: HTMLElement;
  /** True while this is the live streaming bubble; transitions to false
   *  on turn end via finalizeStreamingIfNeeded(). */
  streaming: boolean;
}
const messageStates = new Map<string, MessageState>();

/** Ids whose MessageState is still `streaming: true` — the finalize loop's
 *  population, so a full pass touches only what is live instead of walking
 *  every mounted message. Maintained beside the flag: set at mount, cleared at
 *  finalize and dispose. */
const streamingIds = new Set<string>();

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
  const per = blockEffects.get(id);
  if (per !== undefined) {
    disposeBlockEffects(id, [...per.keys()]);
  }
  clearStreamingSig(id);
  clearReasoningSig(id);
}

/** Per-BLOCK cleanups: message id → block index → cleanups. Beside
 *  `streamingEffects` because the lifetime differs by the axis the window needs — a
 *  block that leaves takes its own subscriptions and its siblings keep theirs. Turn
 *  end and row removal still release everything, through the sibling below. */
const blockEffects = new Map<string, Map<number, (() => void)[]>>();

function pushBlockEffect(id: string, blockIndex: number, fn: () => void): void {
  let per = blockEffects.get(id);
  if (per === undefined) {
    per = new Map();
    blockEffects.set(id, per);
  }
  const arr = per.get(blockIndex);
  if (arr === undefined) {
    per.set(blockIndex, [fn]);
  } else {
    arr.push(fn);
  }
}

/** Run and clear the cleanups for `indices`: the window drop's half of the
 *  contract. */
function disposeBlockEffects(id: string, indices: Iterable<number>): void {
  const per = blockEffects.get(id);
  if (per === undefined) {
    return;
  }
  for (const i of indices) {
    const arr = per.get(i);
    per.delete(i);
    for (const fn of arr ?? []) {
      fn();
    }
  }
  if (per.size === 0) {
    blockEffects.delete(id);
  }
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

function svgTemplate(markup: string): () => Node {
  const tpl = document.createElement("template");
  tpl.innerHTML = markup;
  const content = tpl.content;
  return () => content.cloneNode(true);
}

// ---------------------------------------------------------------------------
// Public entry point
// ---------------------------------------------------------------------------

let mounted = false;

// Initialize callbacks for extracted modules.
initToolCallbacks({
  pushBind,
  refreshGroupHeader,
  explainError,
});
initTurnActionCallbacks({ svgTemplate });
// The header's Copy reuses the assistant side's copy behaviour verbatim.
// Injected because turn-header.ts is a pure fundamental: it renders the band,
// it does not know about the actions framework.
initTurnHeaderCallbacks({ copy: copyWithFeedback });
initBlockRenderer({
  pushStreamingEffect,
  pushBlockEffect,
  disposeBlockEffects,
  makeRow,
  // The restore control on an UNDELIVERED steer note. The same pair
  // `pending-steers.ts` uses for Edit — fill the box, then focus it — because it
  // is the same gesture: put the message back where it was typed so the next
  // Send is the retry.
  restoreSteer: (text: string) => {
    setComposerValue(text);
    $.promptInput.focus();
  },
});
// The refusal callout's Rewind CTA reuses the standard rewind flow (confirm →
// branch → open the new tab). Injected — refusal.ts can't import messages.ts.
setRefusalRewindHandler((m) => {
  void handleRewindClick(m).catch((e: unknown) => {
    console.warn("refusal rewind failed", e);
  });
});

/** Mount the chat view. Idempotent. Called once at app boot from app.ts.
 *  Subscribes to store.version and reconciles the message list on every
 *  bump. Streaming markdown chunks flow through per-block signals bound
 *  at mount, not through this effect. */
export function mountChatView(): void {
  if (mounted) {
    return;
  }
  mounted = true;
  initFollowModel();
  // The rail lives in the transcript's positioned outer wrapper rather than in
  // the scroller, so it stays put instead of scrolling away with the content.
  mountTurnRail($.messagesWrapOuter);
  // The two navigation surfaces that can land on a stub call the same
  // on-demand build this module's own fold toggle uses. Injected — both
  // modules are imported BY this one, so a static import back would cycle.
  initTurnRailCallbacks({ mountTurnBody, activeView: activeTranscriptView });
  // The hit's ordinal is resolved HERE, in the projection the plan is grown over:
  // a second projection could price the same block at a different ordinal, and the
  // grant is clamped against this one.
  initSearchRevealBuilder(
    (chatID, turnID, messageID, blockIndex) => {
      const t = turnByID.get(turnID);
      return mountTurnBody(
        chatID,
        turnID,
        t === undefined ? undefined : turnOrdinalOf(t, messageID, blockIndex),
      );
    },
    mountTurnBodyForWalk,
    endWalkReveal,
  );
  // Scrolling does not repaint; it re-windows. Through the controller's own listener,
  // which owns the self-write marker and the reading-state derivation.
  onViewportChange(windowPass);
  // The tool layer sits below this module, so the two facts only the
  // multiplexer knows arrive injected: whether a chat's view is parked (its
  // terminal output buffers then), and — registered with the store — that a
  // RESIDENT view's chat must not have its messages evicted out from under the
  // DOM. Registered here rather than in app.ts because, unlike the live-run
  // and subagent-tab predicates (which the composition root wires to keep
  // store.ts a leaf), the view registry lives in this module and this module
  // already imports store.js — routing the predicate through app.ts would add
  // an indirection with no cycle to break.
  initToolViewCallbacks({
    isCardParked: (card) => {
      for (const v of views.values()) {
        if (v.parked && v.el.contains(card)) {
          return true;
        }
      }
      return false;
    },
  });
  registerEvictionExemption((chatID) => views.has(chatID));
  // Page unload is the one production moment every view goes away at once;
  // the close/delete/LRU paths dispose per view.
  registerCleanup(teardownAll);
  // The transcript's two inputs: WHICH chat is active, and THAT chat's own
  // transcript version. Header-only updates (usage ticks, titles, modes) write
  // the session signal but bump no version, so they never reach paint();
  // removing the active chat repaints via the activeId write in removeChat.
  effect(() => {
    const id = watchActiveId();
    touch(messagesVersionOf(id));
    paint();
  });
}

/** Fade the whole transcript in once, for the swap out of a loading skeleton.
 *
 *  It reuses `data-chat-entry`, the transcript's own entry animation, rather than
 *  introducing a second motion vocabulary for one transition; the attribute stays
 *  this module's to write, which is why chat.ts calls in here instead of setting
 *  it itself. Remove, reflow, re-add is what RESTARTS the animation — a second
 *  call while the first is still running otherwise does nothing at all — and the
 *  attribute is left in place afterwards because the animation's `both` fill
 *  holds the element's ordinary state, so a stale one changes nothing. Targets
 *  the ACTIVE view: the fade belongs to the transcript being revealed, and a
 *  parked sibling must not replay it on unpark. */
export function fadeInTranscript(): void {
  const root = paintRoot();
  root.removeAttribute("data-chat-entry");
  forceReflow(root);
  root.setAttribute("data-chat-entry", "");
}

// ---------------------------------------------------------------------------
// The follow model's two client-side obligations (§3.4).
// ---------------------------------------------------------------------------

/** Blocks the reader can REACH, which is what the resume chip counts.
 *
 *  Blocks, not messages: a single streaming turn can produce dozens of blocks,
 *  and a chip reading "1 new message" for four minutes of work is a static badge
 *  rather than a progress read-out.
 *
 *  REACHABLE, not merely present, and that is the same argument one level down. A
 *  delegate's blocks are members of the parent assistant message's `blocks` array,
 *  but they render into a card that collapses to `block-size: 0` with
 *  `overflow: hidden` — so while that card is shut they contribute ZERO document
 *  height. Counting them makes the control promise a distance that does not exist:
 *  the reader resumes expecting nine blocks of new content and lands on the same
 *  view they parked at. The resume control is the only element on screen that
 *  knows the reader is behind, so it is the only one that says how far, and a
 *  number nothing on the page can account for is worse than no number.
 *
 *  A block with no `agent_subtask_id` is the parent stream: always inline, always
 *  counted. One with a subtask id counts only while its container chain is open —
 *  `openContainerKeys` (messages-blocks.ts) owns what "open" means, including a
 *  workflow step needing BOTH the run card and its row — so opening a card
 *  legitimately raises the count: those blocks became reachable at that moment. */
function blockCount(msgs: readonly Message[]): number {
  const open = openContainerKeys();
  let n = 0;
  for (const m of msgs) {
    for (const b of m.blocks ?? []) {
      const subtask = b.agent_subtask_id ?? "";
      if (subtask === "" || open.has(subtask)) {
        n++;
      }
    }
  }
  return n;
}

/** Blocks present when the reader last entered Reading. */
let followBaseline = 0;

/** The last FULL pass's reachable-block count. `refreshResumeLabel` runs on
 *  chunk- and tool-cause paints too, and those causes cannot add blocks (a new
 *  block is `shape`), so the walk happens once per full pass instead of once
 *  per streamed delta. */
let reachableBlocks = 0;

function initFollowModel(): void {
  // Following pins to the ACTIVE TEXT BLOCK rather than the document bottom.
  // Without this, the agent streams a sentence, a 400-line diff card renders
  // below it, and pinning to scrollHeight scrolls the sentence being read off
  // the top — an edge case before evidence went full width, and the common case
  // after. Tall evidence stays below the fold until the reader goes to it.
  //
  // WHICH bubble is the anchor registry's call, in messages-blocks.ts: that
  // module owns the `.streaming` class and the delegate boxes, and both of its
  // rules are about never handing back a bubble that sits above the live edge.
  // A registry read, not a selector walk — the follow path runs per frame.
  setAnchorProvider(getLiveAnchor);
  onReadingStateChange((next) => {
    if (next === "reading") {
      // A fresh count at the park, so the baseline and the cached count agree
      // on what is reachable right now.
      reachableBlocks = blockCount(getActive()?.messages ?? []);
      followBaseline = reachableBlocks;
    }
    refreshResumeLabel();
  });
}

/** The resume control is the only element on screen that knows the reader is
 *  behind, so it is the only one that can say how far. */
function refreshResumeLabel(): void {
  if (readingState() === "following") {
    return;
  }
  const session = getActive();
  const behind = reachableBlocks - followBaseline;
  if (behind > 0) {
    setResumeLabel(`${String(behind)} new block${behind === 1 ? "" : "s"}`);
    return;
  }
  // Nothing new since they parked: say what the turn is doing instead of
  // claiming a count of zero.
  setResumeLabel(session?.thinking === true ? session.working_label || "Working" : "Latest");
}

/** Per-turn workflow-run ids, memoized at projection. `turnRunIDs` (turns.ts)
 *  stays the one derivation; this is its result cached per turn per FULL pass,
 *  so the fold pass reads a map entry instead of re-walking every turn's blocks.
 *  The `tool` branch refreshes the owning turn's entry only — additive, because
 *  an attached workflow id never detaches, and every structural change declares
 *  fact/shape, which rebuilds the whole cache. */
const turnRunIDsCache = new Map<string, string[]>();

/** What this pass wants per turn, on the two independent axes: the fold policy's
 *  open/closed (`fold-state.ts`) and residency's mountedness
 *  (`block-window.ts`). Computed per full pass BEFORE the reconcile so a new card
 *  can be born in its final state, and applied to existing cards by
 *  `applyFoldPass` — transitions run through the fold pass, never inside the
 *  reconcile. */
interface FoldPlan {
  open: boolean;
  mounted: boolean;
  /** Whether the header offers the fold at all. False for the newest turn
   *  (nothing after it to get back to), a running turn, and a turn whose fold
   *  would hide nothing — the card carries `data-no-fold` and the toggle
   *  disappears. True for every stub, whatever those rules say: there the
   *  toggle is the only way to reach a body that does not exist yet. */
  canFold: boolean;
}
const foldPlan = new Map<string, FoldPlan>();

/** turn id → the ordinals that turn's body may hold NOW: the union of the plan's
 *  WINDOW and the reader's own DEMAND. TWO WRITERS, and the second writes only
 *  what the first will re-derive. `has()` IS `mounted`, so this mirrors exactly
 *  which turns get a body — hence the per-pass CLEAR beside `foldPlan`, since a
 *  turn that left the projection is never visited again. */
const wantedWindow = new Map<string, TurnRange>();

/** A range covering nothing, at a turn's first ordinal: what a policy-open turn
 *  outside the window gets, so its body exists and its spacers hold its height. */
const EMPTY_RANGE: TurnRange = { from: 0, to: 0 };

/** Every ordinal a turn could have, for a caller with no range to name. */
const WHOLE_TURN: TurnRange = { from: 0, to: Number.MAX_SAFE_INTEGER };

/** Whether `outer` holds every ordinal of `inner`. */
function covers(outer: TurnRange, inner: TurnRange): boolean {
  return outer.from <= inner.from && outer.to >= inner.to;
}

function computeFoldPlan(
  chatID: string,
  turns: readonly Turn[],
  openable: readonly Turn[],
  anchor: ResidencyAnchor | undefined,
): void {
  foldPlan.clear();
  turnByID.clear();
  wantedWindow.clear();
  const window = planResidency(openable, anchor);
  for (const [i, t] of turns.entries()) {
    turnByID.set(t.id, t);
    const hides = turnFoldHides(t);
    const policyOpen = isTurnOpen(chatID, t, i, turns.length);
    // The reader's own REQUEST outranks the plan's silence until it expires: a
    // jump does not open the turn it lands on. Whichever range COVERS the request
    // wins; the hull is refused, since a hit and the reader at opposite ends of one
    // 700-block turn hull into all of it.
    const asked = demandRange(chatID, t);
    const grown = window.get(t.id);
    // A REPLACE retracts nothing here: `applyFoldPass`'s drop plus `bodyRowSpec`'s
    // spacer update own that on the FOLLOWING pass, so until then the body is
    // OVER-height — never under, which is what keeps `scrollHeight` past the viewport.
    const merged =
      grown === undefined ? asked : asked === undefined || covers(grown, asked) ? grown : asked;
    // A turn holding NO ordinal is bodied whatever the budget says: `.is-bodyless`
    // needs a body element to mark, and that body holds no row to price.
    const range = merged ?? (policyOpen || turnCost(t).blocks === 0 ? EMPTY_RANGE : undefined);
    const mounted = range !== undefined;
    if (range !== undefined) {
      wantedWindow.set(t.id, range);
    }
    // A hides-nothing turn stays OPEN while it is resident: its face would be
    // identical to its body, so an auto-fold buys nothing and its animation
    // reads as "something happened, nothing changed".
    const wantOpen = policyOpen || (!hides && mounted);
    foldPlan.set(t.id, {
      // A stub has no body, so it cannot render open however the disclosure
      // rules read. The override survives — revealing it gives it presence on the
      // next pass, and then this reads true.
      open: wantOpen && mounted,
      mounted,
      canFold: !mounted || (hides && i < turns.length - 1 && t.outcome !== "running"),
    });
  }
}

/** How long a `demandPin`'s grant lives when nothing clears it earlier. A smooth
 *  `jumpTo` flight is ~50 events over a few hundred milliseconds and `scroll.ts`
 *  gives its own pin pass 700ms to settle, so 2000 clears both with room. */
const PIN_GOAL_MS = 2000;

/** Where the reader last asked to be, and how long the grant holding it stands.
 *  `mountTurnBody` is the only writer, so no caller can forget to record it. ONE
 *  SLOT, chat included: a jump moves the reader to one turn, and the turn they
 *  jumped away from is no longer where they are. */
let demandPin: { chatID: string; turnID: string; at: number; until: number } | undefined;

/** The turns a search-wide reveal built for the DOM walker, held until the reveal
 *  ends. ONE SCOPE, NOT N DEADLINES: a reader's arrival cannot be awaited so a pin
 *  needs a clock, while the reveal's end is an event `chat-search.ts` fires. */
let demandWalk: { chatID: string; turnIDs: Set<string> } | undefined;

/** The ordinals somebody explicitly ASKED for inside `t`: the pin while its chat matches and
 *  `Date.now() <= until`, else the walk while its set holds `t.id`. One overscan each side of the
 *  position asked for — the floor the window guarantees around its own anchor, so arrival is a
 *  handover. The PIN outranks the WALK, which asked for no ordinal. */
function demandRange(chatID: string, t: Turn): TurnRange | undefined {
  const span = turnCost(t).blocks;
  const pin = demandPin;
  if (pin?.chatID === chatID && pin.turnID === t.id && Date.now() <= pin.until) {
    // Clamped into the span, for `turnOrdinalOf`'s reason: a recorded ordinal
    // outlives the block it named, and an `at` past the span grants `from > to`.
    const at = Math.min(Math.max(pin.at, 0), Math.max(0, span - 1));
    return {
      from: Math.max(0, at - OVERSCAN_BLOCKS),
      to: Math.min(span, at + OVERSCAN_BLOCKS),
    };
  }
  if (demandWalk?.chatID === chatID && demandWalk.turnIDs.has(t.id)) {
    return { from: 0, to: Math.min(span, 2 * OVERSCAN_BLOCKS) };
  }
  return undefined;
}

/** The latest projection per turn id, refreshed each fold-plan pass. The fold
 *  toggle reads it, because its bound closure holds the BUILD-time turn and a
 *  face built from that would show a stale body. */
const turnByID = new Map<string, Turn>();

/** The last FULL pass's projection, in order: what `windowPass` re-filters rather
 *  than re-projecting per scroll frame. A store change schedules a paint, so a turn
 *  that vanished is caught by the builder's own "card gone" guards. */
let lastTurns: readonly Turn[] = [];

// The anchor ladder. COORDINATES: every level compares `el.offsetTop` against `scrollTop`,
// valid only because `#messages-wrap` is `position: absolute` (css/13-messages.css) and nothing
// below it is positioned — adding `position: relative` to a card type breaks this silently.
// PICK: the last entry at or above the viewport top, or the first; descend only while the
// entry's own box still holds that top. That containment test is also what answers a collapsed
// disclosure, laid out normally and clipped.

/** Where the reader is, or `undefined` for the live edge. Membership is the STORE predicate,
 *  never the card's `data-folded`: that is the last APPLIED plan, so through a deferral a turn
 *  that left `openable` is still bodied on screen, and seeding there hands back an ordinal
 *  `planResidency` cannot place — the absent-turn fallback then unmounts it under the reader. */
function residencyAnchor(openable: readonly Turn[]): ResidencyAnchor | undefined {
  // PRECEDENCE 1, outranking every DOM read: Following MEANS pinned to the live
  // edge, which is what `undefined` says. Measuring instead reads a body that is
  // still filling, so the answer lands behind the edge and the window follows it.
  if (readingState() === "following") {
    return undefined;
  }
  const scrollEl = getScrollEl();
  if (scrollEl.scrollHeight <= scrollEl.clientHeight) {
    return undefined;
  }
  const top = scrollEl.scrollTop;
  const cards = turnCards(paintRoot());
  const at = pickIndex(cards, top);
  const grown = new Set(openable.map((t) => t.id));
  for (let i = at; i < cards.length; i++) {
    const card = cards[i];
    const id = card?.getAttribute(KEY_ATTR);
    if (card === undefined || id === null || id === undefined || !grown.has(id)) {
      continue;
    }
    const t = openable.find((x) => x.id === id);
    if (t === undefined) {
      continue;
    }
    // A card the walk STEPPED FORWARD to starts at the reader, so its first
    // ordinal is the seed; only the card they are actually in gets descended.
    return i === at ? { turnID: id, at: cardOrdinal(t, card, top) } : { turnID: id, at: 0 };
  }
  return undefined;
}

/** The index of the last entry at or above `top`, or 0. */
function pickIndex(entries: readonly HTMLElement[], top: number): number {
  let at = 0;
  for (const [i, e] of entries.entries()) {
    if (e.offsetTop <= top) {
      at = i;
    }
  }
  return at;
}

/** The ordinal of `t` the viewport top sits at, inside its own card. A card rendered
 *  FOLDED answers its first ordinal, and that test is a DOM read on purpose: a
 *  folded body's rows are not laid out whatever the plan says. */
function cardOrdinal(t: Turn, card: HTMLElement, top: number): number {
  const body = card.querySelector<HTMLElement>(":scope > .turn-body");
  if (body === null || card.hasAttribute("data-folded")) {
    return 0;
  }
  const rows = [...body.querySelectorAll<HTMLElement>(`:scope > [${KEY_ATTR}]`)];
  const row = rows[pickIndex(rows, top)];
  const key = row?.getAttribute(KEY_ATTR);
  if (row === undefined || key === null || key === undefined) {
    return 0;
  }
  if (isSpacerKey(key)) {
    // The ordinal the spacer's side starts at: 0 for the head, the range's end for
    // the tail.
    return key === SPACER_HEAD_KEY ? 0 : (wantedWindow.get(t.id)?.to ?? 0);
  }
  const m = t.body.find((x) => x.id === key);
  const base = m === undefined ? undefined : turnOrdinalOf(t, m.id);
  if (m === undefined || base === undefined) {
    return 0;
  }
  const blocksEl = row.querySelector<HTMLElement>(":scope > .assistant-blocks");
  if (blocksEl === null) {
    return base;
  }
  return blockOrdinal(m, [...blocksEl.children] as HTMLElement[], top) ?? base;
}

/** The block index the viewport top sits at, walking down `entries`. */
function blockOrdinal(
  m: Message,
  entries: readonly HTMLElement[],
  top: number,
): number | undefined {
  const entry = entries[pickIndex(entries, top)];
  if (entry === undefined) {
    return undefined;
  }
  const own = elementIndex(m, entry);
  const bottom = entry.offsetTop + entry.offsetHeight;
  if (bottom <= top || !ownsIndexed(m, entry)) {
    return own;
  }
  // `content-visibility: auto` skips an element's CONTENTS and never its own box,
  // so the entry the walk descends into has laid-out children — which is why the
  // ladder is built per level and never from one `querySelectorAll`, whose nested
  // members read `offsetTop === 0` inside a skipped card.
  const kids = ladderChildren(entry).filter(
    (k) => k.offsetTop >= entry.offsetTop && k.offsetTop < bottom,
  );
  return kids.length === 0 ? own : (blockOrdinal(m, kids, top) ?? own);
}

/** The layout-bearing children at this level: the entry's own, or — for a container
 *  whose members live in a body region — that region's, since no container type puts
 *  its blocks in its OWN direct children. */
function ladderChildren(entry: HTMLElement): HTMLElement[] {
  const region = entry.classList.contains("tool-group")
    ? ":scope > .tool-group-body > *"
    : entry.classList.contains("subagent-block")
      ? ":scope > .subagent-body > *"
      : entry.classList.contains("run-card")
        ? ":scope > .run-body > .run-steps > .run-step"
        : ":scope > *";
  return [...entry.querySelectorAll<HTMLElement>(region)];
}

/** `el`'s own block index, or the first one inside it that `m` OWNS — a `.run-card`
 *  in this row can hold ANOTHER message's step blocks at the same indices. The id is
 *  a wire value, so it goes through `CSS.escape` like every other in this tree. */
function elementIndex(m: Message, el: HTMLElement): number | undefined {
  const own = el.dataset["blockIndex"];
  if (own !== undefined) {
    return Number(own);
  }
  const inner = ownedIndexed(m, el)?.dataset["blockIndex"];
  return inner === undefined ? undefined : Number(inner);
}

function ownsIndexed(m: Message, el: HTMLElement): boolean {
  return ownedIndexed(m, el) !== null;
}

function ownedIndexed(m: Message, el: HTMLElement): HTMLElement | null {
  return el.querySelector<HTMLElement>(`[data-block-msg="${CSS.escape(m.id)}"][data-block-index]`);
}

/** Drop the pin once the reader has ARRIVED: the ladder's own answer within one
 *  overscan of the ordinal asked for. A pin on a turn outside `openable` has no
 *  ordinal the ladder can name, so that one expires at `until` instead. */
function clearArrivedPin(chatID: string, anchor: ResidencyAnchor | undefined): void {
  const pin = demandPin;
  if (pin === undefined || anchor === undefined) {
    return;
  }
  if (pin.chatID === chatID && pin.turnID === anchor.turnID) {
    if (Math.abs(anchor.at - pin.at) < OVERSCAN_BLOCKS) {
      demandPin = undefined;
    }
  }
}

/** Whether the current full pass mounted at least one new card. The fold pass
 *  reads it: a pass whose cards were born already folded queues no changes, so
 *  the `fillViewport` that used to ride the change batch needs this door too —
 *  without it a page of born-folded stubs could leave the viewport unfilled
 *  with no scroll event left to trigger the next fetch. */
let paintMountedCards = false;

function paint(): void {
  const session = getActive();
  if (session === undefined) {
    // No session for the active id. Only touch the views when there is
    // genuinely NO active chat (all closed, or the last-tab window). A
    // transient undefined during a chat switch or a not-yet-loaded session
    // must NOT wipe the DOM — that empty reconcile pass, immediately followed
    // by a re-populate, was the flashing bug.
    if (getActiveId() === "") {
      // HIDE without disposing: the last-tab window can reopen this chat, and
      // its view unparks then. Disposal belongs to the close/delete paths
      // (disposeChatView) — a view whose CHAT left the store runs it here.
      pruneDeadViews();
      if (activeView !== null) {
        parkView(activeView);
      }
      lastActiveId = undefined;
    }
    return;
  }
  const isChatSwitch = lastActiveId !== session.id;
  let unparked = false;
  if (isChatSwitch) {
    pruneDeadViews();
    if (activeView !== null && activeView.chatID !== session.id) {
      parkView(activeView);
    }
    unparked = activateView(session.id);
  }
  // What the flushed version was FOR — what this pass may skip. Read after the
  // effect's version read; untracked by design. A chat switch is always the
  // full pass: the flushed cause describes the previous chat's delta, not the
  // transcript this pass must now show whole.
  const flushed = isChatSwitch ? undefined : renderCauseOf(session.id);
  if (flushed?.cause === "chunk") {
    // Pure text growth of MOUNTED blocks: their signal effects painted the
    // text, so nothing mounts and nothing folds. Tail bookkeeping only.
    refreshResumeLabel();
    lastNewestId = session.messages[session.messages.length - 1]?.id;
    return;
  }
  if (flushed?.cause === "tool" && refreshToolMessage(session, flushed.msgID)) {
    // An existing call's update, and its card was mounted: the keyed update
    // refreshed that one message. An absent render falls through to the full
    // pass instead — only the full pass mounts.
    refreshResumeLabel();
    lastNewestId = session.messages[session.messages.length - 1]?.id;
    return;
  }
  // Mark genuinely-new appended messages (streaming arrival) so only
  // those get the entry animation. Chat-switches and paginated prepends
  // are silent (no animation).
  appendNewIds.clear();
  staggerIndex.clear();
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
  const turns = projectTurns(session.messages, session.thinking);
  turnRunIDsCache.clear();
  for (const t of turns) {
    turnRunIDsCache.set(t.id, turnRunIDs(t));
  }
  // The turns the window is grown OVER: a folded body is `block-size: 0` +
  // `content-visibility: hidden`, so its ordinals hold zero height and would spend
  // the budget on content nobody can see. A STORE predicate, never a card's
  // `data-folded`, which is the last applied plan and can be a deferral behind.
  const openable = turns.filter(
    (t, i) => isTurnOpen(session.id, t, i, turns.length) || !turnFoldHides(t),
  );
  lastTurns = turns;
  const anchor = residencyAnchor(openable);
  clearArrivedPin(session.id, anchor);
  computeFoldPlan(session.id, turns, openable, anchor);
  paintMountedCards = false;
  paintSyncBlocks = PAINT_SYNC_BLOCKS;
  const root = paintRoot();
  // The placeholder and the conversation may never share this container, and the
  // rule is enforced HERE because this is the line where content lands. Two
  // reasons it cannot be left to the activation's continuation, which removes the
  // skeleton one microtask later: reconcile inserts the newest turn AFTER any
  // unkeyed sibling, so a skeleton still mounted at this point ends up sitting
  // above the whole conversation rather than below it; and "no frame is painted
  // between two microtasks" is a timing property of one call order, not an
  // invariant of the renderer. Only when there is something to replace it with —
  // an empty turn list is a chat still loading, which is what the placeholder is
  // for. Scoped to THIS view: a parked view's skeleton is that view's own to
  // drop at its unpark paint. Unkeyed pagination furniture
  // (`load-more-indicator`, the load-more skeleton) is deliberately untouched:
  // that one is mounted BESIDE real turns on purpose.
  if (turns.length > 0) {
    const skel = document.getElementById(CHAT_SKELETON_ID);
    if (skel !== null && root.contains(skel)) {
      skel.remove();
    }
  }
  reconcile(root, turns, turnSpec);
  // ONE walk over the container's children builds the card list every full-pass
  // consumer shares — the rail's observer and the fold pass.
  const cards = turnCards(root);
  // Tell the rail which cards exist so it can track the turn in view. Re-run per
  // full pass because the set changes as pages load and turns arrive.
  observeTurns(cards);
  applyFoldPass(turns, cards, false);
  // After the fold pass: a card that unmounted here is not owed a build, and a
  // card the pass folded is one the remaining slices land under invisibly.
  drainColdBuilds();
  finalizeStreamingIfNeeded(session.messages);
  reachableBlocks = blockCount(session.messages);
  refreshResumeLabel();
  lastNewestId = session.messages[session.messages.length - 1]?.id;
  lastActiveId = session.id;
  if (unparked && activeView !== null) {
    // The catch-up pass above brought the DOM to the store's current state;
    // now the paused effects come back: settled messages re-arm, the paused
    // tail rebuilds, parked terminal output drains once.
    resumeView(activeView, session);
  }
}

/** The turn cards of `root`, in document order. Unkeyed furniture lives beside
 *  them, hence the filter. */
function turnCards(root: HTMLElement): HTMLElement[] {
  const out: HTMLElement[] = [];
  for (const child of root.children) {
    if (child.classList.contains("turn")) {
      out.push(child as HTMLElement);
    }
  }
  return out;
}

/** Not re-entrant: the head-side compensation writes `scrollTop`, which emits a
 *  scroll. Across FRAMES the plan-equality exit is what terminates — the next pass
 *  measures after the compensation, and an unchanged plan emits nothing. */
let inWindowPass = false;

/** The plan the DOM was last brought to. What the window pass compares against, and
 *  it cannot be the CURRENT plan: `startDemandBuild` writes its grant into
 *  `wantedWindow` before building, so a pass comparing the plan with itself refuses
 *  the one pass that would apply the grant. */
let appliedPlan = "";

/** Re-window on a settled scroll frame. Not a paint: the projection is the last full
 *  pass's, and only residency can change. The `openable` filter is RECOMPUTED rather
 *  than carried, because a fold toggle between two frames changes it. */
function windowPass(): void {
  if (inWindowPass) {
    return;
  }
  const session = getActive();
  const turns = lastTurns;
  if (session === undefined || session.id !== lastActiveId || turns.length === 0) {
    return;
  }
  // A body still FILLING reports coordinates for a partial range — the same premise
  // the rows reconcile is guarded on. The builder's settle runs the pass this skips.
  if (coldBuilds.size > 0 || turnBodyBuilds.size > 0) {
    return;
  }
  const openable = turns.filter(
    (t, i) => isTurnOpen(session.id, t, i, turns.length) || !turnFoldHides(t),
  );
  const anchor = residencyAnchor(openable);
  clearArrivedPin(session.id, anchor);
  computeFoldPlan(session.id, turns, openable, anchor);
  if (planSignature() === appliedPlan) {
    return; // the common case for a scroll that stays inside the overscan
  }
  inWindowPass = true;
  try {
    applyFoldPass(turns, turnCards(paintRoot()), true);
  } finally {
    inWindowPass = false;
  }
  drainColdBuilds();
  refreshResumeLabel();
}

/** What the current plan asks of every projected turn, as one comparable string:
 *  the window pass's no-op exit. */
function planSignature(): string {
  const parts: string[] = [];
  for (const [id, plan] of foldPlan) {
    const range = wantedWindow.get(id);
    const window = range === undefined ? "-" : `${String(range.from)}:${String(range.to)}`;
    parts.push(
      `${id}|${String(plan.open)}${String(plan.mounted)}${String(plan.canFold)}|${window}`,
    );
  }
  return parts.join(",");
}

/** The `tool`-cause fast path: refresh the owning message's card state through
 *  the renderer's existing keyed update, touching no sibling. False when the
 *  message is gone or nothing is mounted for it — the caller runs the full pass
 *  then. */
function refreshToolMessage(session: Session, msgID: string | undefined): boolean {
  if (msgID === undefined) {
    return false;
  }
  // Reverse scan: tool updates target recent turns.
  let msg: Message | undefined;
  let msgIdx = -1;
  for (let i = session.messages.length - 1; i >= 0; i--) {
    if (session.messages[i]?.id === msgID) {
      msg = session.messages[i];
      msgIdx = i;
      break;
    }
  }
  if (msg === undefined) {
    return false;
  }
  // `live` exactly as the full path's keyed update passes it: the mount-time
  // judgment, re-promoted upward when the store now says the turn is live
  // (see liveStateOf).
  const live = liveStateOf(msg);
  if (!refreshMessageCard(msgID, msg, session.id, live, steerMarks(session.id))) {
    return false;
  }
  refreshTurnRunIDs(session.messages, msgIdx, msg);
  return true;
}

/** Merge an updated message's run ids into its turn's cached list — a tool
 *  update can attach one. Additive only; see `turnRunIDsCache`. */
function refreshTurnRunIDs(messages: readonly Message[], msgIdx: number, m: Message): void {
  // The owning turn's id, exactly as projectTurns assigns it: the nearest user
  // message at or before this one, else the loaded window's first message.
  let turnID = messages[0]?.id;
  for (let i = msgIdx; i >= 0; i--) {
    const t = messages[i];
    if (t?.role === "user") {
      turnID = t.id;
      break;
    }
  }
  if (turnID === undefined) {
    return;
  }
  const cached = turnRunIDsCache.get(turnID);
  if (cached === undefined) {
    return; // no full pass has projected this turn yet
  }
  for (const tc of m.tool_calls ?? []) {
    const id = tc.workflow_id ?? "";
    if (id !== "" && !cached.includes(id)) {
      cached.push(id);
    }
  }
}

/**
 * rewindConfirmText builds the confirmation shown before a rewind.
 *
 * THE CONFIRM IS THE ONLY GUARD, so it has to state the losses rather than
 * describe an operation. A rewind is destructive in two directions now: the
 * addressed turn and every turn after it are dropped from the transcript, and
 * the files roll back to KAS's snapshots from before them. There is no branch to
 * fall back to, no "keep both histories", and no undo — the previous version
 * said "File contents on disk are not affected (use Restore for that)", which is
 * now the exact opposite of the truth.
 *
 * It still surfaces what is being rewound FROM — the prompt preview plus the
 * following turn's tool-call and touched-file counts — because that is the only
 * thing that makes the cost legible before you accept it. All field reads stay
 * defensive so a sparse message never throws.
 */
function rewindConfirmText(m: Message, following: readonly Message[]): string {
  const promptRaw = (m.content ?? "").trim().replace(/\s+/g, " ");
  const prompt = promptRaw.length > 100 ? promptRaw.slice(0, 100) + "\u2026" : promptRaw;
  const lines = ["Rewind to this turn?", ""];
  if (prompt.length > 0) {
    lines.push(`Prompt: "${prompt}"`);
  }

  // Count across EVERY turn being dropped, not just the next one. The old text
  // described one turn's work because a fork left the rest alive somewhere else;
  // a revert discards all of it, so summarising only the first would understate
  // the cost by however many turns follow.
  const calls = following.flatMap((f) => f.tool_calls ?? []);
  const files = [
    ...new Set(
      calls.flatMap((c) => (c.locations ?? []).map((l) => l.path.split("/").pop() ?? l.path)),
    ),
  ];
  const turnWord = following.length === 1 ? "turn" : "turns";
  lines.push(`Discards this prompt and ${String(following.length)} later ${turnWord}.`);
  if (calls.length > 0) {
    const toolPart = `${String(calls.length)} tool call${calls.length === 1 ? "" : "s"}`;
    const filePart =
      files.length > 0
        ? `, ${String(files.length)} file${files.length === 1 ? "" : "s"} touched (${files.slice(0, 4).join(", ")}${files.length > 4 ? ", \u2026" : ""})`
        : "";
    lines.push(`Work being undone: ${toolPart}${filePart}.`);
  }

  lines.push("");
  lines.push(
    "Files are rolled back on disk to their state before this turn. " +
      "The prompt itself is discarded too, so you will need to retype it. " +
      "This cannot be undone.",
  );
  return lines.join("\n");
}

/**
 * handleRewindClick confirms the rewind and dispatches it. That is the whole
 * flow now.
 *
 * It used to dispatch, read back a server-assigned branch id, refresh the header
 * list so the branch existed in the store, then open and activate its tab —
 * which is why this module needed dynamic imports of chat.ts and store-load.ts
 * to dodge a cycle. A revert changes the chat you are already looking at, so the
 * new transcript arrives over SSE and there is no tab to open and no list to
 * refresh.
 *
 * REFUSED MID-TURN, not queued. KAS throws on a session with a live
 * abortController ("Cannot revert while the agent is still running"), so
 * offering the button during a turn would only produce an error the user cannot
 * act on. The button is disabled instead (see mountRewind), and this is the
 * second gate for the race between the two.
 */
async function handleRewindClick(m: Message): Promise<void> {
  const session = getActive();
  if (session === undefined) {
    return;
  }
  if (session.thinking) {
    return;
  }
  const idx = session.messages.findIndex((msg) => msg.id === m.id);
  if (idx < 0) {
    return;
  }
  const proceed = await confirmDialog(
    rewindConfirmText(m, session.messages.slice(idx + 1)),
    "Rewind",
    "destructive",
  );
  if (!proceed) {
    return;
  }
  await rewindChat.dispatch({ chatID: session.id, messageID: m.id });
}

/** The multiplexer-wide teardown: the REAL per-view dispose applied to every
 *  resident view — bind unbinds, streaming and tool effect disposal (composite
 *  keys), block-render resets, per-message signal clears, `messageStates`
 *  pruning, container removal — then the shared surfaces (scroll, rail) and
 *  the module-global belts for state no view owns (detached renders, unclaimed
 *  terminal holds). Runs on page unload (registered in mountChatView); the
 *  close/delete/eviction paths dispose per view instead. Exported for the
 *  op-set tests. */
export function teardownAll(): void {
  for (const chatID of [...views.keys()]) {
    disposeChatView(chatID);
  }
  // Belts for what no view reaches: a detached render's effects (the subagent
  // page shares these registries) and per-message state for rows a view walk
  // could not see. Each is idempotent over what the view disposes already ran.
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
  messageStates.clear();
  streamingIds.clear();
  resetScrollState();
  resetTurnRail();
  lastActiveId = undefined;
  lastNewestId = undefined;
}

// ---------------------------------------------------------------------------
// Reconcile specs
//
// Two levels, both keyed. The outer list is TURNS keyed by the turn's opening
// message id; each card's `.turn-body` is an inner keyed list of that turn's
// messages. Nesting is safe because reconcile only considers children carrying
// its key attribute, so a card's unkeyed header and footer are invisible to
// the inner pass and the inner pass is invisible to the outer one.
// ---------------------------------------------------------------------------

const turnSpec: ReconcileSpec<Turn> = {
  key: (t) => t.id,
  mount: (t) => {
    const card = buildTurn(t);
    // Only animate a genuinely-new turn; chat-switch replay and pagination
    // prepends mount silently. A new turn's id is its trigger's message id,
    // which is what paint() records in appendNewIds.
    if (appendNewIds.has(t.id)) {
      card.setAttribute("data-chat-entry", "");
    }
    const stagger = staggerIndex.get(t.id);
    if (stagger !== undefined && stagger > 0) {
      card.style.setProperty("--stagger-index", String(stagger));
    }
    return card;
  },
  update: updateTurn,
  onRemove: (card) => {
    // The face's run-card effects die with the card, or a removed turn keeps
    // subscribing to its run's cell.
    disposeTurnFace(card);
    // Dispose the body's messages: the inner reconcile never runs again for a
    // removed card, so its onRemove would not fire on its own.
    const body = card.querySelector<HTMLElement>(":scope > .turn-body");
    if (body !== null) {
      disposeBodyRows(body);
    }
  },
};

/** One child of a `.turn-body`: a message row over the block range the window
 *  gives it, or a keyed SPACER standing in for the ordinals it does not. */
type BodyRow =
  | { readonly kind: "msg"; readonly m: Message; readonly range: BlockRange }
  | { readonly kind: "space"; readonly side: "head" | "tail"; readonly px: number };

/** Keyed rather than padding on `.turn-body`, which transitions `padding-block`
 *  and would animate every window move past `preserveReadingPosition`'s
 *  measurement — and rather than an unkeyed sibling, which `reconcile` seats
 *  ABOVE every keyed row. */
const SPACER_HEAD_KEY = "__space_head__";
const SPACER_TAIL_KEY = "__space_tail__";

/** The rows a body may hold over `range`: the ONE function that turns a range into a row list,
 *  and where the spacers are born. A spacer standing in for at least one ordinal is floored at
 *  1px, so ordinals behind it are never priced out of the document — reachable on an
 *  all-`padBlocks` turn, whose estimates are all zero. One standing in for NO ordinal is not
 *  emitted. */
function bodyRows(t: Turn, range: TurnRange): BodyRow[] {
  const rows: BodyRow[] = [];
  const span = turnCost(t).blocks;
  if (range.from > 0) {
    rows.push({ kind: "space", side: "head", px: spacerPx(t, range, "head") });
  }
  const slices = sliceTurn(t, range);
  for (const m of t.body) {
    const slice = slices.get(m.id);
    if (slice !== undefined) {
      rows.push({ kind: "msg", m, range: slice });
    }
  }
  if (range.to < span) {
    rows.push({ kind: "space", side: "tail", px: spacerPx(t, range, "tail") });
  }
  return rows;
}

/** What one spacer is worth, floored at 1px. Read when a change is COLLECTED, deliberately:
 *  pricing the same batch's own drops from the measurements that batch takes moved the reader
 *  19x further on the 700-block fixture (−47px of drift became −909px), because the
 *  compensation's error over the tail extension no longer cancels. The estimate reads HIGH, the
 *  safe direction, and the next window move re-prices it. */
function spacerPx(t: Turn, range: TurnRange, side: "head" | "tail"): number {
  return Math.max(1, spacerHeight(t, range, side));
}

const bodyRowSpec: ReconcileSpec<BodyRow> = {
  key: (row) =>
    row.kind === "msg" ? row.m.id : row.side === "head" ? SPACER_HEAD_KEY : SPACER_TAIL_KEY,
  mount: (row) => {
    if (row.kind === "space") {
      const space = el("div", { className: "turn-space" });
      space.style.blockSize = `${String(row.px)}px`;
      return space;
    }
    const m = row.m;
    const node = buildMessage(m, row.range);
    // Licensed-code attribution footnote + model-refusal callout. One call
    // site here + in update() covers mount + update, keyed off
    // m.code_references / m.refusal.
    if (m.role === "assistant") {
      syncCodeReferences(node, m);
      syncRefusal(node, m);
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
    if (liveStreaming) {
      streamingIds.add(m.id);
    }
    return node;
  },
  update: (node, row) => {
    if (row.kind === "space") {
      node.style.blockSize = `${String(row.px)}px`;
      return;
    }
    updateMessage(node, row.m, row.range);
    if (row.m.role === "assistant") {
      syncCodeReferences(node, row.m);
      syncRefusal(node, row.m);
    }
  },
  onRemove: (_el, key) => {
    if (!isSpacerKey(key)) {
      disposeMessage(key);
    }
  },
};

function isSpacerKey(key: string): boolean {
  return key === SPACER_HEAD_KEY || key === SPACER_TAIL_KEY;
}

/** Dispose every MESSAGE row of `body`. A spacer key names no message and owns
 *  nothing, so the three walkers that hand keys to `disposeMessage` route through
 *  here rather than each carrying the test. */
function disposeBodyRows(body: ParentNode): void {
  for (const row of body.querySelectorAll<HTMLElement>(`:scope > [${KEY_ATTR}]`)) {
    const key = row.getAttribute(KEY_ATTR);
    if (key !== null && !isSpacerKey(key)) {
      disposeMessage(key);
    }
  }
}

/** Drop every per-message resource for `key`. Called from the body reconcile's
 *  onRemove, and from the turn reconcile's onRemove for each of a discarded
 *  card's rows — a removed card's inner list never reconciles again, so its
 *  own onRemove would never fire. */
function disposeMessage(key: string): void {
  // MEASURED first, so the spacer replacing this row can hold its height: `reconcile`
  // runs `onRemove` in place, and a row measuring 0 is detached and answers nothing.
  const row = messageStates.get(key)?.el;
  const held = mountedWindow(key);
  if (row !== undefined && held !== undefined && row.offsetHeight > 0) {
    recordRowHeight(key, held, row.offsetHeight);
  }
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
  // The row's per-block streaming signals go with it: nothing else clears a
  // mounted row's signals before page teardown, so signals left here would
  // outlive the message for the rest of the page.
  clearBlockSigsFor(key);
  messageStates.delete(key);
  streamingIds.delete(key);
}

// ---------------------------------------------------------------------------
// Per-role builders + updaters
// ---------------------------------------------------------------------------

/** Build one message of a turn's BODY over `range`. A user message never reaches
 *  here: projectTurns promotes it to its turn's header. An unexpected role still
 *  renders as a plain system row rather than vanishing from the transcript. */
function buildMessage(m: Message, range: BlockRange): HTMLElement {
  switch (m.role) {
    case "assistant":
      return buildAssistant(m, range);
    case "event":
      return buildEvent(m) ?? buildSystemFallback(m);
    case "user":
      return buildSystemFallback(m);
  }
}

function updateMessage(el: HTMLElement, m: Message, range: BlockRange): void {
  if (m.role === "assistant") {
    updateAssistant(el, m, range);
  } else if (m.role === "event") {
    updateEvent(el, m);
  }
  // user messages are immutable once mounted.
}

/** One collected transition and which EDGE of the reader it lands at. The side is
 *  measured in the COLLECT loop: at application time "later" is whenever
 *  `deferWhileReading` releases, and reading between mutations forces one layout
 *  per change on a batch that can be one per mounted card. */
interface FoldChange {
  readonly side: "head" | "tail";
  readonly fn: () => void;
}

/** Apply the plan to every card: fold, unfold, and the ordinals each body holds. DEFERRED
 *  WHILE READING and COMPENSATED, both mandatory — content vanishing from above the reader is
 *  the failure this guards. `immediate` is the WINDOW pass, which skips the deferral only: a
 *  scrolling reader is Reading by definition. Every HEAD change runs before every TAIL one, so
 *  ONE compensation wraps them; the tail runs BARE, or it drags the reader's view. */
function applyFoldPass(
  turns: readonly Turn[],
  cards: readonly HTMLElement[],
  immediate: boolean,
): void {
  const hits = new Map<string, number>();
  const byID = new Map<string, Turn>();
  for (const t of turns) {
    hits.set(t.id, searchHitCount(t.n));
    byID.set(t.id, t);
  }
  const changes: FoldChange[] = [];
  const top = getScrollEl().scrollTop;
  const sideOf = (el: HTMLElement): "head" | "tail" => (el.offsetTop < top ? "head" : "tail");
  // A pass that refused a turn brought the DOM to LESS than the plan, so it records
  // nothing: `appliedPlan` is what the window pass exits on, and recording a plan
  // this batch did not apply drops the delta until the reader's next gesture.
  let refused = false;
  const record = (): void => {
    if (!refused) {
      appliedPlan = planSignature();
    }
  };
  for (const card of cards) {
    const id = card.getAttribute(KEY_ATTR);
    if (id === null) {
      continue;
    }
    setHitCount(card, hits.get(id) ?? 0);
    const plan = foldPlan.get(id);
    const open = plan?.open ?? true;
    const wantMounted = plan?.mounted ?? true;
    // The affordance tracks the plan: the previously-newest turn gains its
    // toggle when the next turn arrives, and a turn that stops running gains
    // or loses it by what its fold would hide.
    card.toggleAttribute("data-no-fold", !(plan?.canFold ?? true));
    const t = byID.get(id);
    const folded = card.hasAttribute("data-folded");
    const body = card.querySelector<HTMLElement>(":scope > .turn-body");
    const side = sideOf(card);
    if (wantMounted && body === null && t !== undefined) {
      // No whole-turn fallback: `wantMounted` IS `wantedWindow.has(id)`, so an
      // absent range is the presence rule breaking, not a body to guess at.
      const range = wantedWindow.get(id);
      if (range !== undefined) {
        if (folded) {
          // Hidden build: the card is folded, so the body lands at zero height.
          startTurnBody(card, t, range);
        } else {
          // A card mid-deferral (its fold is still queued) is visible, so its
          // build moves content; it joins the compensated batch instead.
          changes.push({
            side,
            fn: () => {
              if (card.isConnected) {
                startTurnBody(card, t, range);
              }
            },
          });
        }
      }
    } else if (!wantMounted && body !== null) {
      changes.push({
        side,
        fn: () => {
          // A deferred transition is a REQUEST, re-checked when it runs: the window
          // pass applies its own transitions from a newer plan while this closure
          // is still queued, and unmounting a body that plan wants would take its
          // `coldBuilds` entry with it.
          if (!card.isConnected || wantedWindow.has(id)) {
            return;
          }
          unmountTurnBody(card);
        },
      });
    } else if (body !== null && t !== undefined) {
      if (!collectWindowMove(changes, card, body, t, side, sideOf)) {
        refused = true;
      }
    }
    if (open === !folded) {
      if (!open && t !== undefined) {
        // Already folded: keep the face current (a run card or the persisted
        // outcome can arrive after the fold). Cheap — keyed no-op when nothing
        // changed.
        syncTurnFace(card, t);
      }
      continue;
    }
    changes.push({
      side,
      fn: () => {
        setCardFolded(card, !open);
        if (t !== undefined) {
          syncTurnFace(card, t);
        }
      },
    });
  }
  if (changes.length === 0) {
    record();
    // Born-folded cards queue nothing, so the pagination chain below still
    // needs its trigger restored when this pass mounted cards.
    if (paintMountedCards) {
      fillViewport();
    }
    return;
  }
  const apply = (): void => {
    const head = changes.filter((c) => c.side === "head");
    preserveReadingPosition(() => {
      for (const c of head) {
        c.fn();
      }
    }, "content-growth");
    for (const c of changes) {
      if (c.side === "tail") {
        c.fn();
      }
    }
    // Read AFTER the batch: a change the plan moved under has skipped itself.
    record();
    // A build in that batch got its first slice only, and this batch can run long
    // after the paint that queued it — so it drains its own.
    drainColdBuilds();
    // Folding can starve its own pagination trigger — once the resident turns
    // fold, the page can be shorter than the viewport, and then there is no
    // overflow, no scroll event and no fetch. Restore the trigger.
    fillViewport();
  };
  if (immediate) {
    apply();
    return;
  }
  deferWhileReading(apply);
}

/** What moving `t`'s window costs a body that already exists: the rows the range
 *  gains and loses, then each boundary row's own edges. False when the turn was
 *  REFUSED rather than collected, which is what stops the pass recording a plan it
 *  did not bring the DOM to. */
function collectWindowMove(
  changes: FoldChange[],
  card: HTMLElement,
  body: HTMLElement,
  t: Turn,
  side: "head" | "tail",
  sideOf: (el: HTMLElement) => "head" | "tail",
): boolean {
  if (hasPendingBuild(t.id)) {
    // A cold build still owed would be mounted whole on this frame, which is the cost
    // the yielded builder exists to refuse. It converges on the moved range itself.
    return false;
  }
  const range = wantedWindow.get(t.id);
  if (range === undefined) {
    return true; // no plan entry, so nothing of this turn is in the signature either
  }
  const rows = bodyRows(t, range);
  // A deferred transition is a REQUEST, re-checked when it runs: a newer plan's own
  // pass has already applied itself, and this batch's rows are that plan's rows.
  const stale = (): boolean => {
    const now = wantedWindow.get(t.id);
    return now?.from !== range.from || now.to !== range.to;
  };
  const chatID = getActiveId();
  const marks = steerMarks(chatID);
  for (const [msgID, want] of sliceTurn(t, range)) {
    const m = t.body.find((x) => x.id === msgID);
    const have = mountedWindow(msgID);
    const row = m === undefined ? null : messageStates.get(msgID)?.el;
    if (m === undefined || have === undefined || row === null || row === undefined) {
      continue; // absent, or about to be mounted whole by the rows reconcile
    }
    const rowSide = sideOf(row);
    if (want.from < have.from) {
      changes.push({
        side: rowSide,
        fn: () => {
          if (!stale()) {
            mountHeadRange(m, want, liveStateOf(m), marks);
          }
        },
      });
    } else if (want.from > have.from) {
      changes.push({
        side: rowSide,
        fn: () => {
          if (!stale()) {
            dropHead(m, want, marks);
          }
        },
      });
    }
    // Two calls, never one: a relocation retracts both edges, and one compensated
    // call would correct by a delta that includes the below-the-reader removal.
    if (want.to < have.to) {
      changes.push({
        side: "tail",
        fn: () => {
          if (!stale()) {
            dropTail(m, want, marks);
          }
        },
      });
    }
  }
  if (bodyHolds(body, rows)) {
    return true; // the card-level no-op exit: nothing to reconcile and nothing to price
  }
  changes.push({
    side,
    fn: () => {
      if (card.isConnected && !hasPendingBuild(t.id) && !stale()) {
        reconcile(body, rows, bodyRowSpec);
        syncSourceView(body);
      }
    },
  });
  return true;
}

/** Whether `body` already holds exactly `rows`, each over the range it wants: the
 *  card-level equivalent of the window pass's plan-equality exit, and what keeps an
 *  ordinary streaming paint from queueing a reconcile per new block. */
function bodyHolds(body: HTMLElement, rows: readonly BodyRow[]): boolean {
  const held: string[] = [];
  for (const child of body.children) {
    const key = child.getAttribute(KEY_ATTR);
    if (key !== null) {
      held.push(key);
    }
  }
  if (held.length !== rows.length) {
    return false;
  }
  for (const [i, row] of rows.entries()) {
    if (bodyRowSpec.key(row) !== held[i]) {
      return false;
    }
    if (row.kind !== "msg") {
      continue;
    }
    // An `event` row registers no window (only `buildBody` does) and holds no block
    // to window, so the keyed node the check above just found IS its whole answer.
    const have = mountedWindow(row.m.id);
    if (have !== undefined && (have.from !== row.range.from || have.to !== row.range.to)) {
      return false;
    }
  }
  return true;
}

/** Whether `card`'s body holds every ordinal `t`'s body has, MOUNTED: what makes
 *  "copy as text" a complete answer rather than a hole. Asked of the DOM, never of
 *  `wantedWindow`, which leads the body by a build that has not landed AND by a window
 *  move `deferWhileReading` holds until the reader returns. */
function bodyHoldsWholeTurn(card: HTMLElement, t: Turn): boolean {
  const body = card.querySelector<HTMLElement>(":scope > .turn-body");
  return body !== null && bodyHolds(body, bodyRows(t, WHOLE_TURN));
}
initTurnActionsBodyProbe(bodyHoldsWholeTurn);

/** Wire the header's fold toggle. One control, both directions, and the click
 *  RECORDS the reader's choice — an explicit fold or unfold outranks the
 *  two-newest rule and persists per chat, so the transcript does not undo a
 *  deliberate decision on the next paint. */
function mountFoldToggle(header: HTMLElement, card: HTMLElement, t: Turn): void {
  const btn = header.querySelector<HTMLButtonElement>(
    ":scope > .turn-head-row > .turn-fold-toggle",
  );
  if (btn === null || btn.dataset["bound"] === "") {
    return;
  }
  btn.dataset["bound"] = "";
  btn.addEventListener("click", () => {
    // A no-fold turn's header is not a control: the newest turn is the one
    // being read, a running one is the one being watched, and a hides-nothing
    // turn has nothing to hide — isTurnOpen ignores overrides for the first
    // two anyway, so recording one here would only spring a surprise fold
    // later.
    if (card.hasAttribute("data-no-fold")) {
      return;
    }
    const open = card.hasAttribute("data-folded");
    const chatID = getActiveId();
    if (chatID !== "") {
      setTurnOpen(chatID, t.id, open);
    }
    const fresh = turnByID.get(t.id) ?? t;
    if (open && chatID !== "" && card.querySelector(":scope > .turn-body") === null) {
      // Opening a STUB: its body does not exist yet, so the disclosure creates
      // the region content — build it hidden (the card is still folded), then
      // unfold through the same compensated write a resident toggle uses, and
      // declare the shape change so the pass that follows reconverges the rail
      // and the fold state. All in this interaction; no wait for an unrelated
      // paint. The keyboard path lands here too: the toggle is a native
      // button, so Enter/Space activation IS this click.
      mountTurnBody(chatID, t.id)
        .then(() => {
          preserveReadingPosition(() => {
            setCardFolded(card, false);
            syncTurnFace(card, fresh);
          }, "content-growth");
          bumpMessages(chatID, "shape");
        })
        .catch((e: unknown) => {
          console.warn("[messages] stub body build failed", e);
        });
      return;
    }
    // Applied immediately and compensated: this is the reader's own action, so
    // it is not deferred, but it still must not move what they are looking at.
    preserveReadingPosition(() => {
      setCardFolded(card, !open);
      syncTurnFace(card, fresh);
    }, "content-growth");
  });
  // The band activates that button, so folding a turn is not a 16x16 target —
  // the WHOLE header, both states, matching the tool and delegate cards. Copy,
  // the show-more, an attachment pill and a linkified path inside the request
  // all keep their own click, and a drag that selects the prompt keeps its
  // selection — `wireRowToggle` skips a control by kind and a click that ends
  // a selection. The cursor and hover wash in 29-turns.css mark the surface.
  wireRowToggle(header, btn);
}

/** Show how many search hits a turn holds, so scanning the folded list tells the
 *  reader which turns are worth opening before they open any. */
function setHitCount(card: HTMLElement, n: number): void {
  const badge = card.querySelector<HTMLElement>(
    ":scope > .turn-header > .turn-head-row > .turn-hit-count",
  );
  if (badge === null) {
    return;
  }
  badge.textContent = n > 0 ? String(n) : "";
}

function setCardFolded(card: HTMLElement, folded: boolean): void {
  if (card.hasAttribute("data-folded") !== folded) {
    // The raw-source view belongs to the surface it was opened on (body or
    // face); crossing the fold renders the other surface fresh, so the toggle
    // resets rather than latching against a view that no longer shows raw.
    resetTurnSourceView(card);
  }
  if (folded) {
    card.setAttribute("data-folded", "");
  } else {
    card.removeAttribute("data-folded");
  }
  const header = card.querySelector<HTMLElement>(":scope > .turn-header");
  // On the TOGGLE, never on the header: the band is a plain div, and a div with
  // no role takes no `aria-expanded` (axe `aria-allowed-attr`, critical). The
  // button is the disclosure control the keyboard reaches anyway — the band
  // only forwards its click.
  header
    ?.querySelector<HTMLButtonElement>(":scope > .turn-head-row > .turn-fold-toggle")
    ?.setAttribute("aria-expanded", folded ? "false" : "true");
}

// --- The collapsed turn's FACE ---
//
// A collapsed turn is input + output, in the OPEN layout: the header carries
// the request exactly as when open, and a face slots in where the body was —
// the run cards the turn launched (duplicates of the in-body cards, above the
// prose), the final answer prose in full (real markdown, default type), and a
// failed turn's error text. The ledger footer stays below it, unchanged, so
// credits/model/duration survive the fold. Open, the body shows all of it in
// place and the face does not exist, so the duplication is never visible twice.

/** Face bookkeeping per CARD element: the key detects a content change (a run
 *  arriving for a folded turn), the dispose stops the face cards' effects. */
const turnFaces = new WeakMap<HTMLElement, { key: string; dispose: () => void }>();

function faceKey(t: Turn): string {
  const runs = turnRunIDsCache.get(t.id) ?? turnRunIDs(t);
  return `${t.outcome}|${runs.join(",")}|${String(turnFaceProse(t).length)}`;
}

function disposeTurnFace(card: HTMLElement): void {
  const face = turnFaces.get(card);
  if (face === undefined) {
    return;
  }
  turnFaces.delete(card);
  face.dispose();
  card.querySelector(":scope > .turn-face")?.remove();
}

/** Build or refresh the face to match the card's fold state. Idempotent per
 *  content key, so the fold pass can call it every pass for cheap. */
function syncTurnFace(card: HTMLElement, t: Turn): void {
  if (!card.hasAttribute("data-folded")) {
    disposeTurnFace(card);
    return;
  }
  const key = faceKey(t);
  if (turnFaces.get(card)?.key === key) {
    return;
  }
  disposeTurnFace(card);
  const face = el("div", { className: "turn-face" });
  const disposers: (() => void)[] = [];
  for (const id of turnRunIDsCache.get(t.id) ?? turnRunIDs(t)) {
    const mounted = mountFaceRunCard(id);
    disposers.push(mounted.dispose);
    face.appendChild(mounted.root);
  }
  const prose = turnFaceProse(t);
  if (prose !== "") {
    const bubble = buildAssistantBubble(prose, false);
    bubble.root.classList.add("turn-face-prose");
    face.appendChild(bubble.root);
  }
  const error = turnFaceError(t);
  if (error !== "") {
    face.appendChild(el("div", { className: "turn-face-error" }, error));
  }
  const dispose = (): void => {
    for (const d of disposers) {
      d();
    }
  };
  if (face.childElementCount === 0) {
    // Nothing to show: the ledger row alone carries the fold, as before.
    turnFaces.set(card, { key, dispose });
    return;
  }
  // A card-level child in the body's slot, NOT inside the footer: the footer
  // keeps its open-state grid (ledger, Rewind, file rows) untouched, and a
  // turn with no footer at all still gets its face.
  const footer = card.querySelector<HTMLElement>(":scope > .turn-footer");
  if (footer !== null) {
    footer.before(face);
  } else {
    card.appendChild(face);
  }
  turnFaces.set(card, { key, dispose });
}

// --- The turn card ---

/** Build one turn: tinted header (the trigger), plain body (the work), tinted
 *  footer (the outcome ledger). One card type for every turn, so a one-word answer
 *  and a forty-tool-call refactor differ only in how much body they have.
 *
 *  A card is born in the residency the current pass planned for it: a non-resident
 *  turn mounts as a header/footer STUB — no `.turn-body`, no inner reconcile, no
 *  per-block effects — and folds at birth. A resident body is built through the SAME
 *  batched builder the on-demand reveal uses: one slice here, the rest yielded. */
function buildTurn(t: Turn): HTMLElement {
  const card = el("div", { className: "turn" });
  // No `data-outcome` on the CARD: the leading-edge hairline that was this
  // attribute's only reader is gone (29-turns.css). Three surfaces carry the
  // outcome instead, and none of them reads the card: the rail marker at every
  // width the rail is shown, the footer glyph for every outcome EXCEPT
  // `completed` and `running` — one rule hides it for both, because the clean
  // case needs no mark and the footer only says how a turn ENDED — and the header
  // dot only below 48rem, where the tab strip is off-canvas and the transcript is
  // the only place the outcome can be read.
  // The permalink target. `#turn-{n}` addresses a turn from a ledger row, a
  // search hit or the rail.
  card.id = turnAnchorID(t.n);

  const header = buildTurnHeader(headerData(t));
  mountFoldToggle(header, card, t);
  card.appendChild(header);
  card.toggleAttribute("data-running", t.outcome === "running");

  const plan = foldPlan.get(t.id);
  // Born with its fold affordance decided, so the toggle never flashes on a
  // card that does not offer one. The fold pass keeps it current afterwards.
  card.toggleAttribute("data-no-fold", !(plan?.canFold ?? true));
  // The window entry IS `plan.mounted`, so it decides here rather than a
  // whole-turn fallback for a range the presence rule says exists.
  const range = wantedWindow.get(t.id);
  if (range !== undefined) {
    const body = el("div", { className: "turn-body" });
    card.appendChild(body);
    startFirstSlice(body, bodyRows(t, range), t.id);
  } else {
    setCardFolded(card, true);
  }

  mountTurnFooter(card, t);
  // After the footer: Rewind lives inside it, so it must exist first — and the
  // face goes into the footer, so a card born folded builds it here.
  mountRewind(card, t);
  syncTurnFace(card, t);
  syncTurnBodyless(card);
  paintMountedCards = true;

  // A turn the user just sent overrides Reading: they asked for it, so the pin
  // takes them to it even if they were parked further up.
  if (t.trigger !== undefined) {
    scrollToBottom();
  }
  return card;
}

function updateTurn(card: HTMLElement, t: Turn): void {
  const header = card.querySelector<HTMLElement>(":scope > .turn-header");
  if (header !== null) {
    updateTurnHeader(header, headerData(t));
  }
  card.toggleAttribute("data-running", t.outcome === "running");
  // Three bodies this pass keeps its hands off: a stub has none; one with a
  // build still owed is the builder's, because a reconcile over a partial body
  // mounts every remaining row on the frame the yielded builder exists to
  // protect; and a DROPPED range is the fold pass's, about to remove it.
  const body = card.querySelector<HTMLElement>(":scope > .turn-body");
  const range = wantedWindow.get(t.id);
  if (body !== null && range !== undefined && !hasPendingBuild(t.id)) {
    // ONE projection per card per paint: every row is priced by `spacerHeight` over
    // the ordinals it stands in for, so the gate and the reconcile share the list.
    const rows = bodyRows(t, range);
    if (headUnchanged(body, rows)) {
      reconcile(body, rows, bodyRowSpec);
      syncSourceView(body);
    }
  }
  mountTurnFooter(card, t);
  mountRewind(card, t);
  syncTurnBodyless(card);
  syncTurnFace(card, t);
}

/** Whether the wanted rows differ from what `body` holds only at the TAIL. A tail
 *  delta is the STREAMING path — a running workflow appends a row per turn-segment
 *  — and runs free here because it lands below the reader; a HEAD delta is the fold
 *  pass's, which owns the compensation and the deferral. */
function headUnchanged(body: HTMLElement, rows: readonly BodyRow[]): boolean {
  const held: string[] = [];
  for (const child of body.children) {
    const key = child.getAttribute(KEY_ATTR);
    if (key !== null) {
      held.push(key);
    }
  }
  for (let i = 0; i < Math.min(held.length, rows.length); i++) {
    const row = rows[i];
    if (row === undefined || bodyRowSpec.key(row) !== held[i]) {
      return false;
    }
    if (row.kind === "msg" && mountedWindow(row.m.id)?.from !== row.range.from) {
      return false;
    }
  }
  return true;
}

/** Start a stub's body in place, from the turn's current projection: the
 *  stub→resident transition (a search reveal, a failure flip, a live-run attach,
 *  a rewind shrinking the window).
 *
 *  `startFirstSlice` owns how much of it lands on the frame — the same policy
 *  `buildTurn` uses, because this is the same cold build reached from the fold
 *  pass instead of the reconcile. */
function startTurnBody(card: HTMLElement, t: Turn, range: TurnRange): void {
  if (card.querySelector(":scope > .turn-body") !== null) {
    return;
  }
  const body = existingOrNewBody(card);
  if (body === null) {
    return;
  }
  startFirstSlice(body, bodyRows(t, range), t.id);
  syncTurnBodyless(card);
}

/** The 2→3/1→3 transition: drop a card's body DOM and every per-message
 *  resource behind it — the same disposal the card's own removal runs, because
 *  a stub holds exactly what a removed card no longer does.
 *
 *  The turn's OWED SLICES go with it. `applyFoldPass` runs this inside a batch and
 *  calls `drainColdBuilds` on the line after, so an entry left standing would send
 *  the builder straight back to rebuild the body this call just removed — the
 *  exact work residency exists to refuse. The builder guards the same transition
 *  from its own side, for the eviction that lands mid-build. */
function unmountTurnBody(card: HTMLElement): void {
  const body = card.querySelector<HTMLElement>(":scope > .turn-body");
  if (body === null) {
    return;
  }
  const owed = card.getAttribute(KEY_ATTR);
  if (owed !== null) {
    coldBuilds.delete(owed);
  }
  disposeBodyRows(body);
  body.remove();
  syncTurnBodyless(card);
}

// ---------------------------------------------------------------------------
// The on-demand body build (D1).
//
// ONE entry point for every interaction that needs a stub's body NOW rather
// than on some later paint: the fold-toggle click on a stub (below), a rail
// jump onto a stub turn (turn-rail.ts, injected at mount), and the search
// reveal (chat-search.ts, injected at mount). The body is built while the card
// is folded, so nothing the reader can see moves; opening it stays the
// caller's business, which is what keeps the three callers' fold semantics
// apart (a click records an override, a search reveal is transient, a rail
// jump opens nothing).
//
// Heavy cold builds YIELD between block batches so a 300-block turn cannot
// freeze the main thread on one click: `scheduler.yield()` where the platform
// has it, a macrotask hop where it does not. Each batch re-reads the store and
// the DOM, so a full pass that reconciled the body mid-build (its keyed update
// mounts everything) ends the loop instead of double-mounting.
// ---------------------------------------------------------------------------

/** Blocks per synchronous slice of a cold build. The reconcile unit is the
 *  message, so a slice takes whole messages until their block sum reaches
 *  this; one over-budget message is still one slice, because a message row
 *  mounts atomically. */
const BUILD_BATCH_BLOCKS = 32;

/** Blocks one full pass may mount SYNCHRONOUSLY across every body it starts,
 *  refilled by `paint`. A WORK CAP; nothing here measures a frame. No path makes
 *  it bind for the WINDOW's own bodies, whose first slices cannot sum past one
 *  window; a DEMAND grant is outside the budget and can, and that body is then
 *  born EMPTY for `drainColdBuilds` — the behaviour this is kept for. */
const PAINT_SYNC_BLOCKS = RESIDENT_BLOCKS;
let paintSyncBlocks = PAINT_SYNC_BLOCKS;

/** In-flight builds by turn id with the range each is building, so a second caller
 *  joins one that COVERS its request instead of double-appending rows. */
const turnBodyBuilds = new Map<
  string,
  { readonly range: TurnRange; readonly done: Promise<void> }
>();

/** Turns whose cold build got its first slice and owes the rest: added by the
 *  two cold builders, dispatched by `drainColdBuilds` at the end of the pass and
 *  removed when that build settles. So the frame that creates the cards is never
 *  the frame that finishes them. */
const coldBuilds = new Set<string>();

/** Whether `turnID`'s body is mid-build — owed a drain, or in flight in the
 *  yielded loop. The full pass reads it to keep its hands off a partial body
 *  (see `updateTurn`). */
function hasPendingBuild(turnID: string): boolean {
  return coldBuilds.has(turnID) || turnBodyBuilds.has(turnID);
}

/** Take a cold build's FIRST slice, and queue whatever is left.
 *
 *  The one place either cold builder decides what a paint pays on the frame, so
 *  the pass-wide allowance is charged in one place. A pass that has spent it takes
 *  NO slice: the body is born empty and `drainColdBuilds` builds all of it off the
 *  frame, compensated per slice like any other. */
function startFirstSlice(body: HTMLElement, rows: readonly BodyRow[], turnID: string): void {
  if (paintSyncBlocks > 0) {
    paintSyncBlocks -= appendBodyBatch(body, rows, { row: 0, block: 0 }).blocks;
  }
  if (nextBuildPos(body, rows) !== "done") {
    coldBuilds.add(turnID);
  }
}

/** Where the next slice starts. `row` indexes the row list; `block` is how much of
 *  that row's wanted range is already mounted, so a slice can stop INSIDE a row —
 *  which it has to, because one window can sit entirely inside one message. */
interface SlicePos {
  readonly row: number;
  readonly block: number;
}

/** Where the next slice starts, and what this one cost in blocks. */
interface Slice {
  readonly next: SlicePos;
  readonly blocks: number;
}

/** The body's mounted keyed children, by key — the same set `reconcile` collects
 *  before it seats anything. */
function mountedRows(body: HTMLElement): Map<string, HTMLElement> {
  const out = new Map<string, HTMLElement>();
  for (const child of body.children) {
    const key = child.getAttribute(KEY_ATTR);
    if (key !== null) {
      out.set(key, child as HTMLElement);
    }
  }
  return out;
}

/** Mount `row` at its place in `rows`, or extend it where the body holds it.
 *  `reconcile`'s discipline, not `appendChild`: a window extends at the HEAD, so a
 *  row can enter ABOVE rows already mounted, and the target is the first later key
 *  the body holds. */
function placeRow(
  body: HTMLElement,
  held: Map<string, HTMLElement>,
  rows: readonly BodyRow[],
  i: number,
  row: BodyRow,
): void {
  const key = bodyRowSpec.key(row);
  const existing = held.get(key);
  if (existing !== undefined) {
    bodyRowSpec.update?.(existing, row);
    return;
  }
  const node = bodyRowSpec.mount(row);
  node.setAttribute(KEY_ATTR, key);
  let target: HTMLElement | null = null;
  for (let j = i + 1; j < rows.length && target === null; j++) {
    const next = rows[j];
    target = next === undefined ? null : (held.get(bodyRowSpec.key(next)) ?? null);
  }
  body.insertBefore(node, target);
  held.set(key, node);
  // The source view hides the regions it FINDS, so a row the builder mounts under it
  // has to be hidden here or the raw text gets a rendered neighbour.
  syncSourceView(body);
}

/** Mount one slice of `rows` into `body`, starting at `from`. The reconcile's own
 *  mount arm at BLOCK granularity: a 320-ordinal window inside one 580-block
 *  message is one ROW, so a slice that could only break between rows would mount
 *  the whole window on the frame the yielded builder exists to protect. */
function appendBodyBatch(body: HTMLElement, rows: readonly BodyRow[], from: SlicePos): Slice {
  const held = mountedRows(body);
  let blocks = 0;
  let pos = from;
  while (pos.row < rows.length) {
    const row = rows[pos.row];
    if (row === undefined) {
      break;
    }
    if (row.kind === "space") {
      placeRow(body, held, rows, pos.row, row);
      pos = { row: pos.row + 1, block: 0 };
      continue;
    }
    // At least one block per pass, so a slice always makes progress: a blockless
    // message is one row priced at one, exactly as the ordinal space prices it.
    const take = Math.max(
      1,
      Math.min(row.range.to - row.range.from - pos.block, BUILD_BATCH_BLOCKS - blocks),
    );
    const to = Math.min(row.range.to, row.range.from + pos.block + take);
    placeRow(body, held, rows, pos.row, { ...row, range: { from: row.range.from, to } });
    blocks += take;
    pos =
      to >= row.range.to
        ? { row: pos.row + 1, block: 0 }
        : { row: pos.row, block: to - row.range.from };
    if (blocks >= BUILD_BATCH_BLOCKS) {
      break;
    }
  }
  return { next: pos, blocks };
}

/** The first (row, block) the wanted rows do not yet hold, or `done`. Derived from
 *  mounted state rather than carried in the build entry, because a full pass can
 *  reconcile the body whole while this build yields — and then the build must see
 *  the rows as done instead of appending them a second time. */
function nextBuildPos(body: HTMLElement, rows: readonly BodyRow[]): SlicePos | "done" {
  const held = mountedRows(body);
  for (const [i, row] of rows.entries()) {
    if (!held.has(bodyRowSpec.key(row))) {
      return { row: i, block: 0 };
    }
    if (row.kind === "msg") {
      const have = mountedWindow(row.m.id);
      if (have === undefined) {
        return { row: i, block: 0 };
      }
      // The TAIL shortfall only: `appendBodyBatch` reaches a held row through
      // `updateBody`, which renders past `st.window.to` and nothing else, so a head
      // reported here spends a slice mounting nothing. `mountHeadRange` owns it.
      if (have.to < row.range.to) {
        return { row: i, block: Math.max(0, have.to - row.range.from) };
      }
    }
  }
  return "done";
}

/** `card`'s body element, created empty after the header when it has none. Null
 *  only for a card with no header, which is not a card this renderer builds. */
function existingOrNewBody(card: HTMLElement): HTMLElement | null {
  const existing = card.querySelector<HTMLElement>(":scope > .turn-body");
  if (existing !== null) {
    return existing;
  }
  const header = card.querySelector<HTMLElement>(":scope > .turn-header");
  if (header === null) {
    return null;
  }
  const body = el("div", { className: "turn-body" });
  header.after(body);
  return body;
}

/** Finish the bodies this pass could only start. One build per turn, each behind a
 *  yield so the task that created the cards ends first — the builder then re-reads
 *  the store and the DOM per slice, which is what makes a chat switch or a full
 *  pass landing in between end the build instead of double-mounting.
 *
 *  The chat is read HERE rather than passed: every queued turn belongs to the
 *  view that is active when its build starts, and a build for any other chat has
 *  no card to find. */
function drainColdBuilds(): void {
  if (coldBuilds.size === 0) {
    return;
  }
  const chatID = getActiveId();
  for (const id of [...coldBuilds]) {
    // The entry stays until the build SETTLES, which is what keeps
    // `hasPendingBuild` true across the yield — the window a full pass would
    // otherwise walk into and finish the body synchronously. A second drain over
    // the same id joins the in-flight build rather than starting one.
    void yieldToBrowser()
      .then(() => buildOrJoin(chatID, id, wantedWindow.get(id) ?? EMPTY_RANGE))
      .catch((e: unknown) => {
        console.warn("[messages] cold body build failed", e);
      })
      .finally(() => {
        coldBuilds.delete(id);
        // `buildOrJoin`'s own pass ran with this entry still standing, so it refused.
        windowPass();
      });
  }
}

function yieldToBrowser(): Promise<void> {
  const sched = (globalThis as { scheduler?: { yield?: () => Promise<void> } }).scheduler;
  if (sched?.yield !== undefined) {
    return sched.yield.call(sched);
  }
  return new Promise((resolve) => {
    setTimeout(resolve, 0);
  });
}

/** Build the ordinals around `at` in `turnID`'s body, in yielded block batches.
 *  Resolves when the range covering `at` is mounted, or the moment the build stops
 *  being applicable. `at` absent is the turn's HEAD, which a stub shows.
 *
 *  Records the navigation pin, because every caller is a reader interaction: the
 *  fold toggle, a search hit, a rail jump. The drain uses `buildOrJoin` direct. */
export function mountTurnBody(chatID: string, turnID: string, at?: number): Promise<void> {
  // Ordinal 0 IS the turn's head, so the pin always carries a number and the
  // arrival test needs no second rule for a caller that named no ordinal.
  demandPin = { chatID, turnID, at: at ?? 0, until: Date.now() + PIN_GOAL_MS };
  return startDemandBuild(chatID, turnID);
}

/** Build `turnID`'s body for the search walker without claiming the NAVIGATION
 *  pin: it joins the walk's own demand set instead, which is scoped to the reveal
 *  rather than to a deadline. One pin slot cannot serve a loop over N hit turns —
 *  each would overwrite the last, so the loop would do N builds to keep one. */
export function mountTurnBodyForWalk(chatID: string, turnID: string): Promise<void> {
  if (demandWalk?.chatID !== chatID) {
    demandWalk = { chatID, turnIDs: new Set() };
  }
  demandWalk.turnIDs.add(turnID);
  return startDemandBuild(chatID, turnID);
}

/** Release the walk's grants: the reveal has ended. */
export function endWalkReveal(chatID: string): void {
  if (demandWalk?.chatID === chatID) {
    demandWalk = undefined;
  }
}

function startDemandBuild(chatID: string, turnID: string): Promise<void> {
  const t = turnByID.get(turnID);
  // BOUNDED even where the last pass has no projection for this turn: `buildTurnBodyBatches`
  // re-projects per batch, so a whole-turn grant written here can find the turn by its first
  // slice and mount all 700 blocks of it. The head range is the walk's own grant, and the
  // asker's range replaces it on the next pass.
  const want = (t === undefined ? undefined : demandRange(chatID, t)) ?? {
    from: 0,
    to: 2 * OVERSCAN_BLOCKS,
  };
  // Written EARLY, not owned: the builder slices against `wantedWindow` per slice,
  // so a build starting inside this call would resolve with the requested row
  // unmounted. The next pass recomputes it from the asker this caller recorded.
  wantedWindow.set(turnID, want);
  return buildOrJoin(chatID, turnID, want);
}

/** `mountTurnBody` without the pin, joining on COVERAGE rather than identity, or a
 *  second caller resolves with its own row absent. Chaining rather than cancelling,
 *  because that build's range may be the one another caller is waiting on. */
function buildOrJoin(chatID: string, turnID: string, want: TurnRange): Promise<void> {
  const inflight = turnBodyBuilds.get(turnID);
  if (inflight !== undefined) {
    return covers(inflight.range, want)
      ? inflight.done
      : inflight.done.then(() => buildOrJoin(chatID, turnID, want));
  }
  const done = buildTurnBodyBatches(chatID, turnID).finally(() => {
    turnBodyBuilds.delete(turnID);
    // The ONLY pass a HEAD-ward grant gets: the build inserts nothing above its own
    // window, and a rail jump scrolls BEFORE building, so no later event carries it.
    windowPass();
  });
  turnBodyBuilds.set(turnID, { range: want, done });
  return done;
}

async function buildTurnBodyBatches(chatID: string, turnID: string): Promise<void> {
  for (;;) {
    const session = getActive();
    if (session?.id !== chatID) {
      return;
    }
    const root = paintRoot();
    let card: HTMLElement | null = null;
    for (const child of root.children) {
      if (child.getAttribute(KEY_ATTR) === turnID) {
        card = child as HTMLElement;
        break;
      }
    }
    if (card === null) {
      return;
    }
    // Re-projected per batch rather than captured: a page load can reshape the
    // window while a build yields, and the projection is the only truth about
    // what this turn's body holds now.
    const t = projectTurns(session.messages, session.thinking).find((x) => x.id === turnID);
    if (t === undefined) {
      return;
    }
    // The terminal condition beside "no card" and "turn left the projection": the
    // RANGE was revoked while this build yielded, so every row left is one the pass
    // has decided to drop. Both residency sources reach this through `wantedWindow`.
    const want = wantedWindow.get(turnID);
    if (want === undefined) {
      return;
    }
    const body = existingOrNewBody(card);
    if (body === null) {
      return;
    }
    // Recomputed per slice, like the projection above it: the window can move while
    // this yields, and the build must converge on where it went.
    const rows = bodyRows(t, want);
    const at = nextBuildPos(body, rows);
    if (at === "done") {
      syncTurnBodyless(card);
      return;
    }
    // COMPENSATED only where the slice lands ABOVE the reader, read once per slice:
    // under a window a slice BELOW them is the common case, and compensating that
    // drags their view.
    if (body.offsetTop < getScrollEl().scrollTop) {
      preserveReadingPosition(() => {
        appendBodyBatch(body, rows, at);
      }, "content-growth");
    } else {
      appendBodyBatch(body, rows, at);
    }
    const after = nextBuildPos(body, rows);
    if (after === "done") {
      syncTurnBodyless(card);
      return;
    }
    // A slice that left the resume position where it found it mounted nothing, and
    // another over the same rows would too. Spinning holds `hasPendingBuild` true.
    if (after.row === at.row && after.block === at.block) {
      return;
    }
    await yieldToBrowser();
  }
}

/** `.is-bodyless` mirrors "the card ends with an empty body" for CSS: the
 *  header's bottom-edge treatment keys on it (29-turns.css). Called after every
 *  build/update pass, where both facts it encodes settle — reconcile owns the
 *  body's children and mountTurnFooter owns whether a footer follows. */
function syncTurnBodyless(card: HTMLElement): void {
  const body = card.querySelector<HTMLElement>(":scope > .turn-body");
  card.classList.toggle(
    "is-bodyless",
    body !== null && body.firstChild === null && body.nextElementSibling === null,
  );
}

function headerData(t: Turn): TurnHeaderData {
  const request = t.trigger?.content;
  return {
    n: t.n,
    outcome: t.outcome,
    ts: t.ts,
    // An empty prompt is not a request; fall through to the system-trigger
    // rendering rather than showing a blank header band.
    request: request !== undefined && request.trim() !== "" ? request : undefined,
    // Read off the trigger message, where the server stamped them. Not derived
    // from the request text: an image or a document attachment never appears in
    // Content at all, so there is nothing there to parse back out.
    attachments: t.trigger?.attachments ?? [],
  };
}

/** Mount the Rewind action into the turn's FOOTER, once.
 *
 *  In the footer rather than the header because that is where its meaning is
 *  legible: the footer closes the turn, so a button there reads "go back to
 *  this point" — the state right AFTER this turn. In the header it read as
 *  "rewind this turn", which is not what happens; KAS discards the message it
 *  is given plus everything after, so the addressed message is the NEXT turn's
 *  trigger (`t.rewindTo`).
 *
 *  No button on the last turn: there is nothing after it to discard, so it
 *  appears only once a further turn exists. Also none when the next turn has no
 *  user message to address (an agent-initiated turn), because KAS refuses to
 *  revert to anything else. */
function mountRewind(card: HTMLElement, t: Turn): void {
  const footer = card.querySelector<HTMLElement>(":scope > .turn-footer");
  const target = t.rewindTo;
  if (footer === null) {
    return;
  }
  let btn = footer.querySelector<HTMLButtonElement>(":scope > .turn-rewind");
  if (target === undefined) {
    btn?.remove();
    return;
  }
  if (btn === null) {
    btn = el(
      "button",
      {
        className: "turn-rewind",
        type: "button",
        "aria-label": "Rewind to this point",
      },
      "Rewind",
    ) as HTMLButtonElement;
    footer.appendChild(btn);
  }
  // Rebound on every paint: the target moves when a turn is added or removed,
  // and a stale closure would address a message that is no longer next.
  btn.onclick = (): void => {
    void handleRewindClick(target).catch((e: unknown) => {
      console.warn("[messages] rewind failed", e);
    });
  };
  // DISABLED mid-turn, not queued. KAS refuses a revert on a session with a live
  // abortController, so an enabled button during a turn could only produce an
  // error the user cannot act on. Refreshed on every paint because `thinking` is
  // exactly what a paint is reacting to.
  const busy = getActive()?.thinking ?? false;
  btn.disabled = busy;
  btn.title = busy
    ? "Can't rewind while the agent is running \u2014 cancel the turn first"
    : "Rewind to right after this turn, discarding everything that follows";
}

/** Mount / refresh the turn's outcome ledger as the card's last child.
 *
 *  Turn-scoped rather than message-scoped: a turn can hold more than one
 *  assistant message (a mid-turn model switch splits it), and the ledger
 *  describes the TURN, so it sums across them and renders once. */
function mountTurnFooter(card: HTMLElement, t: Turn): void {
  const led = turnLedger(t);
  const data: TurnSummaryData = {
    credits: led.credits,
    elapsedMs: led.elapsedMs,
    changedFiles: led.changedFiles,
    commands: led.commands,
    reads: led.reads,
    models: led.models,
    outcome: t.outcome,
  };
  const existing = card.querySelector<HTMLDivElement>(":scope > .turn-footer");
  // The footer is also where the turn ACTIONS (copy / source / export) and
  // Rewind live, so it stays whenever the turn has settled prose to act on or
  // a rewind target — an unstamped ledger (a turn whose usage never persisted)
  // must not cost the reader the buttons. Ordered so the markdown join only
  // runs for the rare ledger-less turn.
  const keep =
    hasTurnSummary(data) ||
    t.rewindTo !== undefined ||
    (t.outcome !== "running" && turnMarkdown(t).trim() !== "");
  if (!keep) {
    existing?.remove();
    return;
  }
  let footer = existing;
  if (footer === null) {
    footer = buildTurnFooter(data);
    card.appendChild(footer);
  } else {
    updateTurnFooter(footer, data);
  }
  mountTurnFooterActions(footer, card, t);
}

// --- Assistant ---

/** Build an assistant turn. The whole body — text bubbles, reasoning,
 *  tool cards/groups, subagent blocks, todo checklists, plan, turn footer —
 *  is composed by the single block dispatcher (messages-blocks.ts) from the
 *  message's canonical `blocks` array. */
function buildAssistant(m: Message, range: BlockRange): HTMLElement {
  const wrap = el("div", { className: "msg-wrap msg-wrap-assistant" });
  // The transcript only ever renders the active chat (`paint` reads
  // `getActive()`), and the render carries that id because the per-tool signal is
  // keyed on it: the mount and `upsertToolCall` have to name the same chat.
  const chatID = getActiveId();
  buildAssistantBody(wrap, m, chatID, isLikelyLiveStreaming(m), steerMarks(chatID), range);
  return wrap;
}

/** Incremental update: mount newly-arrived blocks + refresh plan/footer.
 *  Per-block and per-tool signals feed streaming deltas straight into the
 *  already-mounted primitives, so this only handles structural growth. */
function updateAssistant(wrap: HTMLElement, m: Message, range: BlockRange): void {
  if (!messageStates.has(m.id)) {
    return;
  }
  const chatID = getActiveId();
  updateAssistantBody(wrap, m, chatID, liveStateOf(m), steerMarks(chatID), range);
}

/** The message's live flag for an update pass, re-promoted when the store now
 *  says the message is streaming. The mount-time judgment freezes on the
 *  message's state, and a misjudgement is cheap to cause — any mid-turn event
 *  that clears the chat's `thinking` flag (a transport gap's eager clear, a
 *  row mounting before the flag lands) froze it settled for the REST of the
 *  turn, so every later thinking block mounted collapsed while actively
 *  streaming. Upward only: the downward transition stays
 *  finalizeStreamingIfNeeded's, which owns the finalize side effects. */
function liveStateOf(m: Message): boolean {
  const state = messageStates.get(m.id);
  if (state === undefined) {
    return false;
  }
  if (!state.streaming && isLikelyLiveStreaming(m)) {
    state.streaming = true;
    streamingIds.add(m.id);
  }
  return state.streaming;
}

/** Finalize a streamed assistant turn: flush every markdown stream + seal
 *  every reasoning trace (via the block dispatcher). The copy/export actions
 *  live in the turn footer and mount on the paint that follows turn end. */
function finalizeTurn(id: string, _root: HTMLElement): void {
  finalizeAssistantBody(id);
}

/** Finalize every mounted message that is no longer live: the still-streaming
 *  turn keeps its caret only while it is the LAST assistant message of a
 *  thinking session; everything else flushes its markdown streams and seals its
 *  reasoning traces. Driven from the same effect
 *  that paints, so it stays consistent with store state.
 *
 *  The population is the union of two live sets, not a walk over every mounted
 *  message: `streamingIds` (messages mounted streaming and not yet finalized —
 *  a live message may carry no bubble at all, so this door cannot be inferred
 *  from the DOM), and the block renderer's `liveRenderIDs` (renders whose caret
 *  has not drained — an earlier finalize `end()`s a bubble and the reveal's
 *  residue keeps the caret past it, and a mid-turn misjudgement can leave a
 *  live bubble on a message recorded `streaming: false`). Re-finalizing is
 *  idempotent, so the second door costs nothing when it overlaps the first. */
function finalizeStreamingIfNeeded(messages: readonly Message[]): void {
  const candidates = new Set<string>(streamingIds);
  for (const id of liveRenderIDs()) {
    candidates.add(id);
  }
  if (candidates.size === 0) {
    return;
  }
  const session = getActive();
  const isThinking = session?.thinking ?? false;
  const lastID = messages[lastAssistantIndex(messages)]?.id;
  // THIS session's rows only: a parked chat's still-streaming tail sits in
  // `streamingIds` too, and finalizing it from another chat's paint would
  // write into the parked view (the freeze) and seal a turn that is not over
  // — its own unpark decides, off the store's state then.
  const own = new Set<string>();
  for (const m of messages) {
    own.add(m.id);
  }
  for (const id of candidates) {
    if (!own.has(id)) {
      continue;
    }
    const st = messageStates.get(id);
    if (st === undefined) {
      continue; // a detached render's id — not this transcript's to finalize
    }
    if (id === lastID && isThinking) {
      continue; // the live tail keeps streaming
    }
    st.streaming = false;
    streamingIds.delete(id);
    finalizeTurn(id, st.el);
    disposeStreamingEffect(id);
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

/** The row wrapper for a top-level assistant bubble.
 *
 *  It carries NO avatar. The Kiro mark used to lead every assistant reply, and
 *  it spent a 24px column plus its gap on identity the card already establishes
 *  — the reply is the only thing in a turn's body that is not a tool card, and
 *  the header band above it is the user's side. That column is prose width now.
 *  The row element stays because the block dispatcher mounts bubbles into it. */
function makeRow(): HTMLDivElement {
  return el("div", { className: "msg-row" }) as HTMLDivElement;
}

async function explainError(errorText: string, toolTitle: string): Promise<string> {
  const d = await explainErrorAction.dispatch({ errorText, context: toolTitle });
  return d?.output ?? "";
}
