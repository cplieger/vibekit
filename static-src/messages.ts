// The transcript SHELL, and the multiplexer: `#messages` holds one `.transcript-view`
// per resident chat, the active one live and the parked ones frozen. One effect watches
// the active chat id plus that chat's messages version and reconciles into the active
// view by message id. Assistant BODIES are composed entirely by messages-blocks.ts.

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
  turnBaseOf,
  turnLive,
} from "./store.js";
import { clearBlockSigsFor } from "./store-signals.js";
import { releaseClampsIn } from "./clamp-text.js";
import { effect, el, touch } from "@cplieger/reactive";
import { reconcile, KEY_ATTR, type ReconcileSpec } from "./reconcile.js";
import { CHAT_SKELETON_ID } from "./skeleton.js";
import { $, forceReflow } from "./dom.js";
import {
  getScrollEl,
  scrollToBottom,
  resetScrollState,
  setLoadMore,
  deferWhileReading,
  preserveReadingPosition,
  fillViewport,
  onReadingStateChange,
  onReaderGesture,
  onViewportChange,
  setAnchorProvider,
  setResumeLabel,
  readingState,
  jumpTo,
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
  earnsTurnFooter,
  type TurnSummaryData,
} from "./fundamentals/turn-footer.js";
import {
  projectTurns,
  turnLedger,
  turnAnchorID,
  turnFaceProse,
  turnFailureText,
  turnFoldHides,
  type Turn,
} from "./turns.js";
import { severityOf } from "./turn-severity.js";
import { ICON_REWIND } from "./icons.js";
import { buildAssistantBubble } from "./fundamentals/text-bubble.js";
import { isTurnOpen, isTurnRevealed, setTurnOpen } from "./fold-state.js";
import {
  planResidency,
  sliceTurn,
  supersededMessages,
  turnCost,
  turnOrdinalOf,
  OVERSCAN_BLOCKS,
  RESIDENT_BLOCKS,
  type BlockRange,
  type ResidencyAnchor,
  type TurnRange,
} from "./block-window.js";
import { forgetHeights, recordRowHeight, spacerHeight } from "./block-heights.js";
import { wireRowToggle } from "./disclosure-row.js";
import { initSearchRevealBuilder, searchHitCount } from "./chat-search.js";
import {
  mountTurnRail,
  setResidentTurns,
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
  initBlockRenderer,
  getLiveAnchor,
  mountedWindow,
  mountHeadRange,
  dropHead,
  dropTail,
  geometrySkipped,
  setSupersededMessages,
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
import { buildSteerNote } from "./fundamentals/steer-note.js";
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

// --- Public re-exports ---

export { getScrollEl, setLoadMore };
// This module owns the rail, so chat.ts reaches it through here rather than
// driving the same surface itself.
export { loadTurnRail, pointTurnRail };

// --- Module state ---

const messagesEl = $.messages;

// --- The transcript multiplexer ---
//
// Exactly one resident view carries `.is-active` and the scroller's observers. A parked
// view keeps its DOM, renders and `messageStates` rows with every writer that could
// reach that DOM paused: the store keeps ingesting, only rendering is frozen.

/** How many PARKED views stay resident (the active view is not counted).
 *  Past this, the least-recently-used parked view runs the real dispose. */
export const PARKED_VIEWS = 3;

/** One resident chat view. The saved-at-park fields are meaningful only while
 *  `parked` is true. There is no view→message index: that is a store read
 *  (`viewMessages`). */
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
  /** Message ids live-streaming when the view parked. Resume rebuilds these
   *  bodies fresh even if they have settled: their binding effects were
   *  disposed, so they missed every update that landed while parked. */
  pausedStreaming: Set<string>;
}

/** Resident views by chat id. Iteration order is the LRU order: activation
 *  re-inserts, so the first parked entry is the eviction candidate. */
const views = new Map<string, ChatView>();
let activeView: ChatView | null = null;

/** The active view's element, for consumers mounting transcript furniture.
 *  Null when no chat view is active (boot, the last-tab window). */
export function activeTranscriptView(): HTMLElement | null {
  return activeView?.el ?? null;
}

/** A resident view's element for `chatID` — active or parked — or null. */
export function transcriptViewFor(chatID: string): HTMLElement | null {
  return views.get(chatID)?.el ?? null;
}

/** Reveal a run's card in `chatID`'s transcript. Returns whether it landed.
 *
 *  BEST-EFFORT: mountedness is the whole condition and this never unfolds, so a card
 *  outside the paginated window or inside a stub answers false and the caller keeps
 *  only the tab. SCOPED to that chat's view, because `.run-card[data-run]` repeats
 *  once per resident view and `document.querySelector` can answer with a parked one. */
export function revealRunCard(chatID: string, workflowID: string): boolean {
  if (workflowID === "") {
    return false;
  }
  const view = transcriptViewFor(chatID);
  if (view === null) {
    return false;
  }
  const card = view.querySelector<HTMLElement>(`.run-card[data-run="${CSS.escape(workflowID)}"]`);
  if (card === null) {
    return false;
  }
  // `jumpTo`, not a raw `scrollIntoView`: it owns whether the jump parks the reader.
  jumpTo(card, { block: "start", behavior: "smooth" });
  return true;
}

/** Where paint reconciles: the active view, or the bare multiplexer before any
 *  view exists (the boot instant; nothing renders turns there). */
function paintRoot(): HTMLElement {
  return activeView?.el ?? messagesEl;
}

/** The messages this view has mounted: the session's messages ∩ `messageStates`.
 *  A store read, so no second index is maintained. */
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
  const created = view === undefined;
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
  if (created) {
    // AFTER the attach, and that ordering is the whole point: `resetScrollState`
    // ends in `setLoadMore(null, false)`, and every indicator lookup inside the
    // controller is scoped to the ATTACHED `viewEl`. Run before the attach it
    // stripped the OUTGOING view's own pagination button — a wrong-view mutation
    // that a parked view then carried back with a control missing.
    //
    // Only for a view this call CREATED: an existing view's furniture is its own,
    // restored by the attach above, and resetting it would take away the pagination
    // the previous activation wired up.
    resetScrollState();
  }
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

/** The REAL per-view dispose: every message row's disposal, the chat's tool effects
 *  through their composite keys, and the container's removal. LRU eviction, chat
 *  close/delete and `teardownAll` all run this — never a bare empty reconcile, which
 *  would strip render state while leaving parked DOM behind. */
export function disposeChatView(chatID: string): void {
  const view = views.get(chatID);
  if (view === undefined) {
    return;
  }
  for (const body of view.el.querySelectorAll<HTMLElement>(".turn-body")) {
    disposeBodyRows(body);
  }
  // Every clamp in the view, in one sweep: the single per-view dispose close, delete,
  // eviction and `teardownAll` all run, so it covers a whole chat's headers without
  // counting them. `parkView` deliberately does NOT release — a parked view keeps its
  // clamps and re-measures on unpark.
  releaseClampsIn(view.el);
  // AFTER the row disposal, which records one last height per row. The measurement
  // cache is the one per-message store an unmount deliberately KEEPS, so a view
  // going away is where its numbers stop standing for anything on screen.
  forgetHeights(get(chatID)?.messages.map((m) => m.id) ?? []);
  disposeToolEffectsForChat(chatID, view.el);
  view.el.remove();
  views.delete(chatID);
  if (activeView === view) {
    activeView = null;
    lastActiveId = undefined;
  }
}

/** Pause one message: dispose its live-binding effects, finish its reveals, and
 *  suspend its run cards and tool-card effects through the owning view's composite
 *  keys — which also stops the duration ticker. `renders` maps, DOM and
 *  message-lifetime bookkeeping stay. */
function pauseMessage(view: ChatView, m: Message): void {
  disposeStreamingEffect(m.id);
  pauseAssistantBody(m.id);
  suspendToolEffectsFor(
    view.chatID,
    (m.tool_calls ?? []).map((tc) => tc.id),
    view.el,
  );
}

/** Resume one message after the catch-up paint. A settled message needs only what
 *  pause suspended re-armed, `syncMountedText` having already trued grown text. One
 *  STREAMING at park rebuilds its body fresh instead: its binding effects were
 *  disposed, so an update that landed while parked is only guaranteed to appear
 *  through a fresh render of the current store. */
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
  releaseClampsIn(row);
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

/** Per-message streaming effect cleanups, disposed on turn end as well as on unmount.
 *  Separate from `bindUnbinds` so a tool card's loading-state binding survives a turn
 *  end, which is not the end of that card. */
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

/** IDs of messages newly appended at the end since the last paint. Two mounts read it
 *  for the same reason — the entry animation and a sent turn's live-edge pin — and both
 *  must stay silent for a replay or a prepend, which the reader did not cause. */
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
  // Scrolling does not repaint; it re-windows. TWO hooks: the gesture says the READER
  // moved, the viewport change is the settled frame to measure in. `onViewportChange`
  // fires for every scroll whoever wrote it, this module's own compensation included,
  // and the anchor comes off scroll position — so alone it lets the pass feed itself.
  onReaderGesture(noteReaderMoved);
  onViewportChange(windowPass);
  // The tool layer sits below this module, so the two facts only the multiplexer
  // knows arrive injected: whether a chat's view is parked, and that a RESIDENT
  // view's chat must not have its messages evicted out from under the DOM.
  // Registered here rather than in app.ts because the view registry lives in this
  // module and this module already imports store.js, so routing through app.ts
  // would add an indirection with no cycle to break.
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
 *  Reuses `data-chat-entry` rather than a second motion vocabulary, and the
 *  attribute stays this module's to write. Remove, reflow, re-add is what RESTARTS
 *  the animation — a second call mid-flight otherwise does nothing — and it is left
 *  in place afterwards because the `both` fill holds the element's ordinary state.
 *  Targets the ACTIVE view, so a parked sibling cannot replay it on unpark. */
export function fadeInTranscript(): void {
  const root = paintRoot();
  root.removeAttribute("data-chat-entry");
  forceReflow(root);
  root.setAttribute("data-chat-entry", "");
}

// ---------------------------------------------------------------------------
// The follow model's two client-side obligations (§3.4).
// ---------------------------------------------------------------------------

/** Blocks the reader can REACH, which is what the resume chip counts. A DELEGATED
 *  block is a member of the parent message's `blocks` array and is DROPPED by
 *  `placeBlock`, so it adds no document height in any fold state and counting one
 *  would promise a distance that does not exist. The test is the STAMP, not a fold. */
function blockCount(msgs: readonly Message[]): number {
  let n = 0;
  for (const m of msgs) {
    for (const b of m.blocks ?? []) {
      if ((b.agent_subtask_id ?? "") === "") {
        n++;
      }
    }
  }
  return n;
}

/** Blocks present when the reader last entered Reading. */
let followBaseline = 0;

/** The last FULL pass's reachable-block count. Chunk- and tool-cause paints cannot add
 *  a REACHABLE block (a block this transcript draws arrives as `shape`), so the walk
 *  runs once per full pass rather than once per streamed delta. */
let reachableBlocks = 0;

function initFollowModel(): void {
  // Following pins to the ACTIVE TEXT BLOCK, not the document bottom: otherwise a
  // 400-line diff card rendering below the streamed sentence scrolls it off the top.
  // WHICH bubble is the anchor registry's call (messages-blocks.ts owns `.streaming`
  // and the delegate boxes); a registry read, not a selector walk, because the
  // follow path runs per frame.
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

/** What this pass wants per turn, on the two independent axes: the fold policy's
 *  open/closed (`fold-state.ts`) and residency's mountedness (`block-window.ts`).
 *  Computed BEFORE the reconcile so a new card is born in its final state; existing
 *  cards go through `applyFoldPass`, never through the reconcile. */
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

/** turn id → the ordinals that turn's body may hold NOW: the union of the plan's WINDOW
 *  and the reader's own DEMAND. TWO WRITERS, the second writing only what the first
 *  re-derives. `has()` IS `mounted`, hence the per-pass CLEAR beside `foldPlan`. */
const wantedWindow = new Map<string, TurnRange>();

/** A range covering nothing, at a turn's first ordinal: what a turn holding NO
 *  ordinal gets, so `.is-bodyless` has a body to mark. Never a turn that HAS
 *  ordinals — see the range decision in `computeFoldPlan`. */
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
  // The block dispatcher's turn-scope input, installed with the rest of this pass's
  // projection: a `MsgRender` is per message and cannot see its own neighbours.
  setSupersededMessages(supersededMessages(turns));
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
    // A turn holding NO ordinal is bodied whatever the budget says: `.is-bodyless` needs
    // a body element to mark. One WITH ordinals is a STUB instead, because an empty range
    // emits the whole-turn tail spacer and no row — 23,988px of nothing, measured on a
    // 400-block turn.
    const range = merged ?? (turnCost(t).blocks === 0 ? EMPTY_RANGE : undefined);
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
  // A REVEALED turn is a standing request the budget may not take back: letting the
  // clock expire under one dropped the body the reader had just opened. The deadline
  // still governs a grant they can ARRIVE at (`clearArrivedPin` is its ordinary
  // release), and the single pin slot bounds the standing case to one turn.
  if (
    pin?.chatID === chatID &&
    pin.turnID === t.id &&
    (Date.now() <= pin.until || isTurnRevealed(chatID, t.id))
  ) {
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

// The anchor ladder. COORDINATES: every level compares `el.offsetTop` against
// `scrollTop`, valid only because `#messages-wrap` is `position: absolute` and nothing
// below it is positioned — adding `position: relative` to a card type breaks this
// silently. PICK: the last entry at or above the viewport top, or the first, descending
// only while the entry's own box still holds that top.

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
  // `blockOrdinal` answers a MESSAGE-LOCAL index and this owes a TURN ordinal; the two
  // coincide only for a turn's first message. Joined by the decoder that defines the
  // space, never by adding `base` here.
  const local = blockOrdinal(m, [...blocksEl.children] as HTMLElement[], top);
  return local === undefined ? base : (turnOrdinalOf(t, m.id, local) ?? base);
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

/** Whether the current full pass mounted at least one new card. A pass whose cards were
 *  born folded queues no changes, so `fillViewport` needs this door: without it a page
 *  of born-folded stubs leaves the viewport unfilled and no scroll event follows. */
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
  // those get the entry animation. Chat-switches, paginated prepends and
  // refetched windows are silent (no animation).
  appendNewIds.clear();
  staggerIndex.clear();
  // A FETCHED window is a replay whatever its rows look like, and only the paint's CAUSE
  // can say so: a cold open paints on `setActive` before `loadMessages` resolves, so its
  // post-fetch paint is not a chat switch and recorded no tail — indistinguishable from
  // a first prompt by the array alone.
  const replayed = isChatSwitch || flushed?.cause === "load";
  if (!replayed) {
    // Where the arrivals start. An UNSET tail means the previous paint of this
    // same chat had no messages at all, so every message here arrived since. With
    // a fetched window excluded above, that is the reader's first prompt in a
    // fresh chat — the one appended-tail paint with no tail behind it. A tail the
    // array no longer carries (a rewind truncated past it) names no arrivals.
    let from = 0;
    if (lastNewestId !== undefined) {
      // Reverse scan: lastNewestId is always near the tail (set at end of
      // previous paint), so scanning backward is O(1) amortized.
      from = -1;
      for (let i = session.messages.length - 1; i >= 0; i--) {
        if (session.messages[i]?.id === lastNewestId) {
          from = i + 1;
          break;
        }
      }
    }
    if (from >= 0) {
      for (let i = from; i < session.messages.length; i++) {
        const id = session.messages[i]?.id;
        if (id !== undefined) {
          appendNewIds.add(id);
        }
      }
    }
  } else if (isChatSwitch) {
    // Cascade the last 8 on chat-switch so they stagger rather than flashing in
    // together. Not for a fetched window, whose rows replace ones already on screen.
    const total = session.messages.length;
    for (let i = Math.max(0, total - 8); i < total; i++) {
      const id = session.messages[i]?.id;
      if (id !== undefined) {
        staggerIndex.set(id, total - 1 - i);
      }
    }
  }
  // With the window's own base, so the ordinals are SESSION-ABSOLUTE: this is a
  // page, so a base-less scan would number turn 1 of the window as turn 1 of the
  // session and every card's number would move as older pages arrived.
  const turns = projectTurns(session.messages, turnLive(session), turnBaseOf(session));
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
  // The placeholder never coexists with content, dropped HERE because this is the
  // line where content lands (`vibekit-ui.md` "A SKELETON MAY ONLY PAINT OVER AN
  // EMPTY CONTAINER" owns why the activation's continuation is too late). Only with
  // something to replace it: an empty turn list is a chat still loading. Scoped to
  // THIS view, and the load-more furniture is deliberately untouched — that one
  // mounts BESIDE real turns.
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
  setResidentTurns(cards);
  applyFoldPass(session.id, turns, cards, false);
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

/** Whether the READER has stated a position since the last scroll-driven pass. Only the
 *  reader may move the window: this module's own compensation goes through the
 *  controller's marked write and publishes no gesture, so the pass cannot schedule
 *  itself. */
let readerMoved = false;

function noteReaderMoved(): void {
  readerMoved = true;
}

/** Re-window on a settled scroll frame the READER caused. Not a paint: the projection
 *  is the last full pass's, and only residency can change. The `openable` filter is
 *  RECOMPUTED rather than carried, because a fold toggle between two frames changes it.
 *  `fromScroll` false is a build settling: its own reason to re-window, no gesture. */
function windowPass(fromScroll = true): void {
  if (inWindowPass) {
    return;
  }
  if (fromScroll) {
    if (!readerMoved) {
      return;
    }
    readerMoved = false;
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
    applyFoldPass(session.id, turns, turnCards(paintRoot()), true);
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
  for (let i = session.messages.length - 1; i >= 0; i--) {
    if (session.messages[i]?.id === msgID) {
      msg = session.messages[i];
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
  return refreshMessageCard(msgID, msg, session.id, live, steerMarks(session.id));
}

/**
 * The confirmation shown before a rewind.
 *
 * THE CONFIRM IS THE ONLY GUARD, so it states the LOSSES rather than describing an
 * operation: the addressed turn and everything after it leave the transcript, the
 * files roll back to KAS's snapshots, and there is no undo. It surfaces what is
 * being rewound FROM so that cost is legible first; field reads stay defensive.
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
 * Confirm the rewind and dispatch it.
 *
 * REFUSED MID-TURN, not queued: KAS throws on a session with a live
 * abortController, so a button offered during a turn could only produce an error
 * the user cannot act on. `mountRewind` disables it; this is the second gate.
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

/** The multiplexer-wide teardown: the REAL per-view dispose applied to every resident
 *  view, then the shared surfaces (scroll, rail) and the module-global belts for state
 *  no view owns. Runs on page unload; the close, delete and eviction paths dispose per
 *  view instead. Exported for the op-set tests. */
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

// --- Reconcile specs ---
//
// Two levels, both keyed: TURNS by the turn's opening message id, then each card's
// `.turn-body` over that turn's messages. The nesting is safe because reconcile only
// considers children carrying its key attribute, so a card's unkeyed header and
// footer are invisible to the inner pass.

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
    // No face teardown: the face holds only prose, so it has no effect to stop,
    // its element leaves with the card, and the WeakMap entry dies with it.
    //
    // Dispose the body's messages: the inner reconcile never runs again for a
    // removed card, so its onRemove would not fire on its own.
    const body = card.querySelector<HTMLElement>(":scope > .turn-body");
    if (body !== null) {
      disposeBodyRows(body);
    }
    // The header's request-text clamp goes with the card: a rewind truncation
    // drops a card while its view lives, so `disposeChatView` never runs for it.
    releaseClampsIn(card);
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

/** The rows a body may hold over `range`: the ONE function turning a range into a row
 *  list, and where the spacers are born. A spacer standing in for at least one ordinal is
 *  floored at 1px, so ordinals behind it are never priced out of the document (reachable
 *  on an all-`padBlocks` turn). One standing in for NO ordinal is not emitted. */
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
  onRemove: (el, key) => {
    if (!isSpacerKey(key)) {
      disposeMessage(key);
    }
    // A steer note's clamp lives inside the row, so it leaves with it.
    releaseClampsIn(el);
  },
};

function isSpacerKey(key: string): boolean {
  return key === SPACER_HEAD_KEY || key === SPACER_TAIL_KEY;
}

/** What each mounted row measured, keyed by reconcile key. Filled by
 *  `withMeasuredRows` and empty outside it: a height is only true for the pass that
 *  read it. */
let measuredRowHeights: ReadonlyMap<string, number> = new Map();

/** Measure the rows `keys` names in ONE read-only pass, then run the mutation that drops
 *  them. `reconcile` runs `onRemove` between `el.remove()` calls, so `disposeMessage`'s
 *  layout read costs one forced reflow PER row there and one for the whole pass here.
 *  Callers pass DEPARTING keys only: a survivor's height is never read back. */
function withMeasuredRows(body: ParentNode, keys: ReadonlySet<string>, mutate: () => void): void {
  const outer = measuredRowHeights;
  measuredRowHeights = measureRows(body, keys);
  try {
    mutate();
  } finally {
    measuredRowHeights = outer;
  }
}

function measureRows(body: ParentNode, keys: ReadonlySet<string>): Map<string, number> {
  const measured = new Map<string, number>();
  for (const row of body.querySelectorAll<HTMLElement>(`:scope > [${KEY_ATTR}]`)) {
    const key = row.getAttribute(KEY_ATTR);
    if (key === null || !keys.has(key) || geometrySkipped(row)) {
      continue;
    }
    measured.set(key, row.offsetHeight);
  }
  return measured;
}

/** The MESSAGE keys `body` holds, in mount order. The keys half of a reconcile's
 *  before-picture, read with no layout property touched. */
function mountedRowKeys(body: ParentNode): string[] {
  const held: string[] = [];
  for (const row of body.querySelectorAll<HTMLElement>(`:scope > [${KEY_ATTR}]`)) {
    const key = row.getAttribute(KEY_ATTR);
    if (key !== null && !isSpacerKey(key)) {
      held.push(key);
    }
  }
  return held;
}

/** The ONE way a body's rows are reconciled: departing rows measured first, mutated
 *  second. A reconcile that drops NOTHING reads no layout at all — the streaming path,
 *  where a measurement pass would force a reflow per frame for a cache nobody reads. */
function reconcileBody(body: HTMLElement, rows: readonly BodyRow[]): void {
  const departing = departingKeys(body, rows);
  if (departing.size === 0) {
    reconcile(body, rows, bodyRowSpec);
    return;
  }
  withMeasuredRows(body, departing, () => {
    reconcile(body, rows, bodyRowSpec);
  });
}

/** The message keys this reconcile will REMOVE: what `body` holds minus what `rows`
 *  wants. Answered from keys alone, so asking costs no layout. */
function departingKeys(body: ParentNode, rows: readonly BodyRow[]): Set<string> {
  const wanted = new Set(rows.map((row) => bodyRowSpec.key(row)));
  const departing = new Set<string>();
  for (const key of mountedRowKeys(body)) {
    if (!wanted.has(key)) {
      departing.add(key);
    }
  }
  return departing;
}

/** Dispose every MESSAGE row of `body`. A spacer key names no message and owns
 *  nothing, so the three walkers that hand keys to `disposeMessage` route through
 *  here rather than each carrying the test. */
function disposeBodyRows(body: ParentNode): void {
  // Every row here departs, so the departing set IS the mounted set.
  const keys = mountedRowKeys(body);
  withMeasuredRows(body, new Set(keys), () => {
    for (const key of keys) {
      disposeMessage(key);
    }
  });
}

/** Drop every per-message resource for `key`. Called from the body reconcile's
 *  onRemove, and from the turn reconcile's onRemove for each of a discarded
 *  card's rows — a removed card's inner list never reconciles again, so its
 *  own onRemove would never fire. */
function disposeMessage(key: string): void {
  // The spacer holds the height `withMeasuredRows` measured, never a DOM read: this runs
  // mid-mutation. No entry and a detached row both leave the recorded height alone.
  const held = mountedWindow(key);
  const px = measuredRowHeights.get(key);
  if (held !== undefined && px !== undefined && px > 0) {
    recordRowHeight(key, held, px);
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

/** Build one message of a turn's BODY over `range`. The one user row that reaches here is
 *  a STEER, which joins the turn already running rather than opening one; a PROMPT is
 *  promoted to its turn's header. An unexpected role renders as a plain system row. */
function buildMessage(m: Message, range: BlockRange): HTMLElement {
  switch (m.role) {
    case "assistant":
      return buildAssistant(m, range);
    case "event":
      return buildEvent(m) ?? buildSystemFallback(m);
    case "user":
      return m.user_kind === "steer" ? buildPersistedSteerNote(m) : buildSystemFallback(m);
  }
}

/** The note for a steer's DURABLE row — what a page reload rebuilds it from.
 *
 *  Every fact comes off the row; nothing is inferred (invariant 1). `steer_state`
 *  ABSENT means not known — the whole legacy population, plus every row the
 *  session/load replay writes, since KAS's log records a steer without saying
 *  whether the model consumed it — and it reads as the NEUTRAL note. Reading it as
 *  not-delivered would label a correction the agent may have acted on as missed.
 *  `ack` is omitted, the one fidelity loss against the live mark this supersedes. */
function buildPersistedSteerNote(m: Message): HTMLElement {
  const dropped = m.steer_state === "dropped";
  const text = m.content ?? "";
  return buildSteerNote({
    text,
    // Absent means the user's, which is what this note assumed before the row
    // carried the field at all.
    origin: m.steer_origin ?? "user",
    dropped,
  });
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
  chatID: string,
  turns: readonly Turn[],
  cards: readonly HTMLElement[],
  immediate: boolean,
): void {
  const hits = new Map<string, number>();
  const byID = new Map<string, Turn>();
  for (const t of turns) {
    // `countsByTurn` is keyed by `SearchHit.turn`, which the server computes over the
    // WHOLE message array — so this join is only honest now that `t.n` is
    // session-absolute. Window-local, it looked up an absolute key and a folded row's
    // match count read 0 (or another turn's) on any chat long enough to page.
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
      if (!collectWindowMove(chatID, changes, card, body, t, side, sideOf)) {
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
  chatID: string,
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
  // The PASS's own chat, threaded down from its caller, never `getActiveId()`: these
  // marks are consumed inside closures that run at a later frame boundary, so an
  // ambient read hands a chat switch inside the deferral the wrong chat's marks.
  const marks = steerMarks(chatID);
  for (const [msgID, want] of sliceTurn(t, range)) {
    const m = t.body.find((x) => x.id === msgID);
    const have = mountedWindow(msgID);
    const row = m === undefined ? null : messageStates.get(msgID)?.el;
    if (m === undefined || have === undefined || row === null || row === undefined) {
      continue; // absent, or about to be mounted whole by the rows reconcile
    }
    // A folded body is `block-size: 0`, so a row inside one reports offsetTop 0 and
    // `sideOf` answers "head" whatever the card's real position. The card's own side
    // is measured on a `.turn` in the view this pass paints, which is the active one.
    const rowSide = (): "head" | "tail" => (geometrySkipped(row) ? side : sideOf(row));
    if (want.from < have.from) {
      changes.push({
        side: rowSide(),
        fn: () => {
          if (!stale()) {
            mountHeadRange(m, want, liveStateOf(m), marks);
          }
        },
      });
    } else if (want.from > have.from) {
      changes.push({
        side: rowSide(),
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
        reconcileBody(body, rows);
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

/** Wire the header's fold toggle. The click RECORDS the reader's choice, which outranks
 *  the two-newest rule and persists per chat, so the next paint cannot undo it. */
function mountFoldToggle(header: HTMLElement, card: HTMLElement, t: Turn): void {
  const btn = header.querySelector<HTMLButtonElement>(":scope > .turn-fold-toggle");
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
      // Opening a STUB: its body does not exist yet, so build it hidden, then unfold
      // through the same compensated write a resident toggle uses and declare the
      // shape change. All in this interaction, so nothing waits on a later paint.
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
  // The band activates that button, so folding a turn is the WHOLE header rather than
  // a 16x16 target, matching the tool and delegate cards. `wireRowToggle` is what
  // keeps a nested control's own click, and a drag that selects the prompt.
  wireRowToggle(header, btn);
}

/** Show how many search hits a turn holds, so scanning the folded list tells the
 *  reader which turns are worth opening before they open any. */
function setHitCount(card: HTMLElement, n: number): void {
  const header = card.querySelector<HTMLElement>(":scope > .turn-header");
  if (header === null) {
    return;
  }
  const badge = header.querySelector<HTMLElement>(":scope > .turn-badge > .turn-hit-count");
  if (badge === null) {
    return;
  }
  badge.textContent = n > 0 ? String(n) : "";
  // The badge sits out of flow inside a FIXED first-line reserve, and the count
  // is the one member that renders only while a search runs — so without
  // widening that reserve it would paint over the prompt's first line. Written
  // by the renderer rather than matched with `:has()`, for the reason
  // `.is-bodyless` is: a streaming card mutates every frame and `:has()`
  // charges each recalc for relational matching.
  if (n > 0) {
    header.dataset["hits"] = "";
  } else {
    delete header.dataset["hits"];
  }
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
    ?.querySelector<HTMLButtonElement>(":scope > .turn-fold-toggle")
    ?.setAttribute("aria-expanded", folded ? "false" : "true");
}

// --- The collapsed turn's FACE ---
//
// A collapsed turn is input + output in the OPEN layout: the header carries the
// request as when open, a face slots in where the body was with the turn's final
// answer prose in full, and the ledger footer stays below it.
//
// The face carries NO run card: a live run's persistent surface is the composer
// band's run bar (`run-bar.ts`), which survives both the fold and a reload.

/** Face bookkeeping per CARD element: the content key, which detects a change the
 *  face has to be rebuilt for. No dispose half — with only prose in it the face
 *  holds no effect, and its element leaves with the card. */
const turnFaces = new WeakMap<HTMLElement, string>();

function faceKey(t: Turn): string {
  // `t.outcome` stays in the key: an outcome flip is exactly when the turn's final
  // prose block can be superseded by text of the same length.
  return `${t.outcome}|${String(turnFaceProse(t).length)}`;
}

function disposeTurnFace(card: HTMLElement): void {
  if (!turnFaces.has(card)) {
    return;
  }
  turnFaces.delete(card);
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
  if (turnFaces.get(card) === key) {
    return;
  }
  disposeTurnFace(card);
  const face = el("div", { className: "turn-face" });
  const prose = turnFaceProse(t);
  if (prose !== "") {
    const bubble = buildAssistantBubble(prose, false);
    bubble.root.classList.add("turn-face-prose");
    face.appendChild(bubble.root);
  }
  // No failure text here: `syncTurnNotice` carries it at card level, for both folds.
  if (face.childElementCount === 0) {
    // Nothing to show: the ledger row alone carries the fold, as before.
    turnFaces.set(card, key);
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
  turnFaces.set(card, key);
}

/** Mount or refresh the turn's failure notice: one card-level row saying why a turn
 *  that did not end cleanly ended that way.
 *
 *  A card-level child before the footer, NOT a row in `.turn-body`: the body is a
 *  keyed reconcile over MESSAGES, a tier-3 stub has no body element, and the notice
 *  must survive residency. One mount point serves both fold states, because
 *  `syncTurnFace` early-returns for an unfolded card. Tinted by SEVERITY, so a
 *  cancel reads as `stopped` rather than as a failure. Idempotent. */
function syncTurnNotice(card: HTMLElement, t: Turn): void {
  const text = turnFailureText(t);
  const existing = card.querySelector<HTMLElement>(":scope > .turn-notice");
  if (text === "") {
    existing?.remove();
    return;
  }
  const severity = severityOf(t.outcome);
  if (existing !== null) {
    if (existing.textContent !== text) {
      existing.textContent = text;
    }
    existing.dataset["severity"] = severity;
    existing.dataset["outcome"] = t.outcome;
    return;
  }
  const notice = el("div", { className: "turn-notice" }, text);
  notice.dataset["severity"] = severity;
  // The outcome beside the severity, for the ONE stated hue exception the four
  // other outcome surfaces in 29-turns.css already carry: `unknown` keeps a
  // neutral ink because an unreadable end has no honest hue, and severity alone
  // cannot express it (`unknown` and `cancelled` are one severity, two inks).
  notice.dataset["outcome"] = t.outcome;
  // `role="status"` rather than `alert`: the turn has already ended by the time
  // this mounts, so it is a result to be read in course, not an interruption. A
  // repaint would re-announce an `alert` on every pass.
  notice.setAttribute("role", "status");
  const footer = card.querySelector<HTMLElement>(":scope > .turn-footer");
  if (footer !== null) {
    footer.before(notice);
  } else {
    card.appendChild(notice);
  }
}

// --- The turn card ---

/** Build one turn: tinted header (the trigger), plain body (the work), tinted
 *  footer (the outcome ledger). One card type for every turn, so a one-word answer
 *  and a forty-tool-call refactor differ only in how much body they have.
 *
 *  Born in the residency this pass planned: a non-resident turn mounts as a STUB (no
 *  `.turn-body`, no inner reconcile, no per-block effects) and folds at birth. A
 *  resident body goes through the same batched builder the reveal uses. */
function buildTurn(t: Turn): HTMLElement {
  const card = el("div", { className: "turn" });
  // The anchor, session-absolute now that the paint pass supplies the window's base
  // (`turnAnchorID` in turns.ts owns why it is still not a working permalink). The
  // rail keeps joining on the reconcile key — parked views keep their cards, so this
  // id exists once per resident view.
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
  syncTurnNotice(card, t);
  syncTurnFace(card, t);
  syncTurnBodyless(card);
  paintMountedCards = true;

  // A turn the reader just sent overrides Reading. BOTH conditions, because a user
  // trigger alone does not mean they asked for anything NOW: this mount also runs
  // for a chat-switch replay, a refetched window and a prepend, and the pin
  // publishes a reader gesture that would revoke the rail's own pick
  // (`vibekit-client.md` "The timeline rail").
  if (t.trigger !== undefined && appendNewIds.has(t.id)) {
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
      reconcileBody(body, rows);
      syncSourceView(body);
    }
  }
  mountTurnFooter(card, t);
  mountRewind(card, t);
  syncTurnNotice(card, t);
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
 *  `startFirstSlice` owns how much lands on the frame — the same policy `buildTurn`
 *  uses, this being the same cold build reached from the fold pass. */
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

/** The 2→3/1→3 transition: drop a card's body DOM and every per-message resource
 *  behind it — the same disposal the card's own removal runs.
 *
 *  The turn's OWED SLICES go with it: `applyFoldPass` calls `drainColdBuilds` on the
 *  next line, so an entry left standing would send the builder straight back to
 *  rebuild the body this call just removed. The builder guards the same transition
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

// --- The on-demand body build ---
//
// ONE entry point for every interaction needing a stub's body NOW; built while the
// card is FOLDED, so nothing visible moves, and OPENING it stays the caller's
// business, which keeps the three callers' fold semantics apart. A cold build YIELDS
// between batches (`scheduler.yield()`, else a macrotask) so a 300-block turn cannot
// freeze the main thread on one click; each batch re-reads the store and the DOM, so
// a pass that reconciled the body mid-build ends the loop instead of double-mounting.

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
 *  The one place either cold builder decides what a paint pays on the frame. A pass
 *  that has spent the allowance takes NO slice: the body is born empty and
 *  `drainColdBuilds` builds all of it off the frame. */
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
 *  yield so the task that created the cards ends first; the builder re-reads the store
 *  and the DOM per slice, so a chat switch or full pass landing in between ends the
 *  build instead of double-mounting. The chat is read HERE rather than passed: a build
 *  for any other chat has no card to find. */
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
        windowPass(false);
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
 *  Records the navigation pin, every caller being a reader interaction; the drain
 *  uses `buildOrJoin` direct. */
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
    windowPass(false);
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
    // what this turn's body holds now. With the base for the same reason the paint
    // pass uses it: two projections of one window must not disagree about `n`.
    const t = projectTurns(session.messages, turnLive(session), turnBaseOf(session)).find(
      (x) => x.id === turnID,
    );
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
 *  The footer rather than the header because that is where its meaning is legible:
 *  KAS discards the message it is given plus everything after, so the addressed
 *  message is the NEXT turn's trigger (`t.rewindTo`) and the button means "go back
 *  to the state after this turn". Hence none on the last turn, and none when the
 *  next turn has no user message to address. */
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
      svgTemplate(ICON_REWIND)(),
      // Desktop only: 29-turns.css drops the word at the width the footer's own
      // actions collapse, and the `aria-label` above is the name once it is gone.
      el("span", { className: "turn-rewind-label" }, "Rewind"),
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
  // The CONSEQUENCE, in the channel `aria-label` is not: the name carries the
  // action. `data-tooltip` rather than `title` reaches this button even while it
  // is disabled — `web.md` "A DISABLED control still fires hover events".
  btn.setAttribute(
    "data-tooltip",
    busy
      ? "Can't rewind while the agent is running \u2014 cancel the turn first"
      : "Rewind to right after this turn, discarding everything that follows",
  );
}

/** How long after the previous turn's start this one began, from the projection the
 *  current paint is reconciling.
 *
 *  UNDEFINED when this window holds no predecessor, which is a different fact from a
 *  gap of zero and must not be folded into it: the rail draws its seam from the same
 *  pair, and a turn opening a paged window genuinely has no measurable gap. Clamped
 *  at zero because a clock that ran backwards is not a negative pause. */
function gapBefore(t: Turn): number | undefined {
  const i = lastTurns.findIndex((x) => x.id === t.id);
  const prev = i > 0 ? lastTurns[i - 1] : undefined;
  return prev === undefined ? undefined : Math.max(0, t.ts - prev.ts);
}

/** Mount / refresh the turn's outcome ledger as the card's last child.
 *
 *  Turn-scoped rather than message-scoped: a turn can hold more than one
 *  assistant message (a mid-turn model switch splits it), and the ledger
 *  describes the TURN, so it sums across them and renders once. */
function mountTurnFooter(card: HTMLElement, t: Turn): void {
  const led = turnLedger(t);
  const since = gapBefore(t);
  const data: TurnSummaryData = {
    credits: led.credits,
    elapsedMs: led.elapsedMs,
    changedFiles: led.changedFiles,
    commands: led.commands,
    reads: led.reads,
    models: led.models,
    outcome: t.outcome,
    toolMs: led.toolMs,
    kindCounts: led.kindCounts,
    delegateCount: led.delegateCount,
    delegateMs: led.delegateMs,
    startedAt: led.startedAt,
    endedAt: led.endedAt,
    stopReasonRaw: led.stopReasonRaw,
    truncated: led.truncated,
    // Spread rather than assigned, because `exactOptionalPropertyTypes` separates an
    // absent field from one holding `undefined` — which is the distinction above.
    // LAST, so the conditional spread cannot be overwritten by a later key.
    ...(since === undefined ? {} : { sinceMs: since }),
  };
  const existing = card.querySelector<HTMLDivElement>(":scope > .turn-footer");
  // ONE predicate, in the footer's own module: the two extra reasons a turn card's
  // footer survives an unstamped ledger used to sit here as a second expression
  // beside `hasTurnSummary`, so the same question had two answers. The markdown join
  // is still ordered last, so it runs only for a ledger-less turn.
  const keep = earnsTurnFooter(data, {
    rewindable: t.rewindTo !== undefined,
    settledProse: t.outcome !== "running" && turnMarkdown(t).trim() !== "",
  });
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

/** Build an assistant turn. The whole body is composed by the single block dispatcher
 *  (messages-blocks.ts) from the message's canonical `blocks` array. */
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

/** The message's live flag for an update pass, re-promoted when the store now says
 *  the message is streaming: the mount-time judgment freezes, and any mid-turn event
 *  clearing `thinking` froze it settled for the REST of the turn, so later thinking
 *  blocks mounted collapsed while streaming. UPWARD ONLY — the downward transition is
 *  `finalizeStreamingIfNeeded`'s, which owns the side effects. */
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

/** Finalize a streamed assistant turn: flush every markdown stream + SETTLE every
 *  reasoning trace (via the block dispatcher) — the label and the pulse, folding
 *  nothing. A fold is POSITIONAL: a successor being posted is what seals a trace, so
 *  the trace nothing followed stays expanded past turn end. The copy/export actions
 *  live in the turn footer and mount on the paint that follows turn end. */
function finalizeTurn(id: string, _root: HTMLElement): void {
  finalizeAssistantBody(id);
}

/** Finalize every mounted message that is no longer live: the still-streaming turn
 *  keeps its caret only while it is the LAST assistant message of a thinking
 *  session; everything else flushes its markdown and SETTLES its reasoning traces,
 *  which flips the label and drops the pulse without collapsing anything — only a
 *  successor arriving after a trace folds it.
 *
 *  The population is the UNION of two live sets rather than a walk over every
 *  mounted message: `streamingIds` (a live message may carry no bubble at all, so
 *  this door cannot be inferred from the DOM) and `liveRenderIDs` (a caret the
 *  reveal's residue keeps past an earlier `end()`). Re-finalizing is idempotent. */
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
 *  No avatar: the card already establishes identity (`vibekit-ui.md` "There are no
 *  bubbles"). The row element stays because the block dispatcher mounts into it. */
function makeRow(): HTMLDivElement {
  return el("div", { className: "msg-row" }) as HTMLDivElement;
}

async function explainError(errorText: string, toolTitle: string): Promise<string> {
  const d = await explainErrorAction.dispatch({ errorText, context: toolTitle });
  return d?.output ?? "";
}
