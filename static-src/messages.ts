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
import { effect, el } from "@cplieger/reactive";
import { reconcile, KEY_ATTR, type ReconcileSpec } from "./reconcile.js";
import { CHAT_SKELETON_ID } from "./skeleton.js";
import { $ } from "./dom.js";
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
import { isTurnOpen, setTurnOpen, TURNS_WARM } from "./fold-state.js";
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
  const rows = view.el.querySelectorAll<HTMLElement>(`.turn-body > [${KEY_ATTR}]`);
  for (const row of rows) {
    const key = row.getAttribute(KEY_ATTR);
    if (key !== null) {
      disposeMessage(key);
    }
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
    buildAssistantBody(row, m, session.id, live, steerMarks(session.id));
    syncCodeReferences(row, m);
    syncRefusal(row, m);
  }
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
  // The two navigation surfaces that can land on a tier-3 stub call the same
  // on-demand build this module's own fold toggle uses. Injected — both
  // modules are imported BY this one, so a static import back would cycle.
  initTurnRailCallbacks({ mountTurnBody, activeView: activeTranscriptView });
  initSearchRevealBuilder(mountTurnBody);
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
    void messagesVersionOf(id).value;
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
  void root.offsetWidth;
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

/** What this pass wants per turn, on the two independent axes (D1): the fold
 *  policy's open/closed, and the tier derivation's mountedness
 *  (`open || distance < TURNS_WARM`). Computed per full pass BEFORE the
 *  reconcile so a new card can be born in its final state, and applied to
 *  existing cards by `applyFoldPass` — transitions run through the fold pass,
 *  never inside the reconcile. */
interface FoldPlan {
  open: boolean;
  mounted: boolean;
  /** Whether the header offers the fold at all. False for the newest turn
   *  (nothing after it to get back to), a running turn, and a turn whose fold
   *  would hide nothing — the card carries `data-no-fold` and the toggle
   *  disappears. */
  canFold: boolean;
}
const foldPlan = new Map<string, FoldPlan>();

function computeFoldPlan(chatID: string, turns: readonly Turn[]): void {
  foldPlan.clear();
  turnByID.clear();
  for (const [i, t] of turns.entries()) {
    turnByID.set(t.id, t);
    const distance = turns.length - 1 - i;
    const hides = turnFoldHides(t);
    // A hides-nothing turn stays OPEN while its body is warm: its face would
    // be identical to the body, so an auto-fold buys nothing and its animation
    // reads as "something happened, nothing changed". Beyond the warm window
    // it stubs like any other turn — the stub face IS the body's content for
    // this class, so the swap is invisible.
    const open = isTurnOpen(chatID, t, i, turns.length) || (!hides && distance < TURNS_WARM);
    foldPlan.set(t.id, {
      open,
      mounted: open || distance < TURNS_WARM,
      canFold: hides && i < turns.length - 1 && t.outcome !== "running",
    });
  }
}

/** The latest projection per turn id, refreshed each fold-plan pass. The fold
 *  toggle reads it, because its bound closure holds the BUILD-time turn and a
 *  face built from that would show a stale body. */
const turnByID = new Map<string, Turn>();

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
  computeFoldPlan(session.id, turns);
  paintMountedCards = false;
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
  // consumer shares — the rail's observer and the fold pass. Filtered because
  // unkeyed furniture (load-more, skeletons) lives beside the cards.
  const cards: HTMLElement[] = [];
  for (const child of root.children) {
    if (child.classList.contains("turn")) {
      cards.push(child as HTMLElement);
    }
  }
  // Tell the rail which cards exist so it can track the turn in view. Re-run per
  // full pass because the set changes as pages load and turns arrive.
  observeTurns(cards);
  applyFoldPass(turns, cards);
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
    const rows = card.querySelectorAll<HTMLElement>(`:scope > .turn-body > [${KEY_ATTR}]`);
    for (const row of rows) {
      const key = row.getAttribute(KEY_ATTR);
      if (key !== null) {
        disposeMessage(key);
      }
    }
  },
};

const messageSpec: ReconcileSpec<Message> = {
  key: (m) => m.id,
  mount: (m) => {
    const node = buildMessage(m);
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
  update: (el, m) => {
    updateMessage(el, m);
    if (m.role === "assistant") {
      syncCodeReferences(el, m);
      syncRefusal(el, m);
    }
  },
  onRemove: (_el, key) => {
    disposeMessage(key);
  },
};

/** Drop every per-message resource for `key`. Called from the body reconcile's
 *  onRemove, and from the turn reconcile's onRemove for each of a discarded
 *  card's rows — a removed card's inner list never reconciles again, so its
 *  own onRemove would never fire. */
function disposeMessage(key: string): void {
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

/** Build one message of a turn's BODY. A user message never reaches here:
 *  projectTurns promotes it to its turn's header, so the body holds only what
 *  the trigger caused. An unexpected role still renders as a plain system row
 *  rather than vanishing from the transcript. */
function buildMessage(m: Message): HTMLElement {
  switch (m.role) {
    case "assistant":
      return buildAssistant(m);
    case "event":
      return buildEvent(m) ?? buildSystemFallback(m);
    case "user":
      return buildSystemFallback(m);
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

/**
 * Fold every turn that should be folded, and unfold every turn that should not
 * — and apply the tier derivation's MOUNTEDNESS the same way (D1): a turn the
 * fold holds open or within the warm window keeps its body; everything older
 * is a header/footer stub whose body DOM does not exist.
 *
 * DEFERRED WHILE READING and COMPENSATED WHEN APPLIED, both mandatory. A fold
 * removes hundreds of pixels rather than tens, so content vanishing from above
 * the reader through no action of their own is the failure mode this guards —
 * which is why §3.4 makes the helper an obligation rather than a nicety. A
 * 2→3 unmount removes the same pixels a fold does, so it rides the same
 * deferred, compensated batch. A body BUILD is the one transition applied
 * synchronously here: it happens under a folded card (`display: none`), so it
 * moves nothing the reader can see — which is what lets a search reveal build
 * a stub's body in the paint that revealed it.
 *
 * Runs on every paint because eligibility changes with every new turn: the turn
 * that was second-newest becomes third-newest and folds, and the turn that was
 * fifth-newest leaves the warm window and unmounts.
 */
function applyFoldPass(turns: readonly Turn[], cards: readonly HTMLElement[]): void {
  const hits = new Map<string, number>();
  const byID = new Map<string, Turn>();
  for (const t of turns) {
    hits.set(t.id, searchHitCount(t.n));
    byID.set(t.id, t);
  }
  const changes: (() => void)[] = [];
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
    const mounted = card.querySelector(":scope > .turn-body") !== null;
    if (wantMounted && !mounted && t !== undefined) {
      if (folded) {
        // Hidden build: the card is folded, so the body lands at zero height.
        mountTurnBodySync(card, t);
      } else {
        // A card mid-deferral (its fold is still queued) is visible, so its
        // build moves content; it joins the compensated batch instead.
        changes.push(() => {
          if (card.isConnected) {
            mountTurnBodySync(card, t);
          }
        });
      }
    } else if (!wantMounted && mounted) {
      changes.push(() => {
        if (card.isConnected) {
          unmountTurnBody(card);
        }
      });
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
    changes.push(() => {
      setCardFolded(card, !open);
      if (t !== undefined) {
        syncTurnFace(card, t);
      }
    });
  }
  if (changes.length === 0) {
    // Born-folded cards queue nothing, so the pagination chain below still
    // needs its trigger restored when this pass mounted cards.
    if (paintMountedCards) {
      fillViewport();
    }
    return;
  }
  deferWhileReading(() => {
    preserveReadingPosition(() => {
      for (const fn of changes) {
        fn();
      }
    }, "content-growth");
    // Folding can starve its own pagination trigger — once the resident turns
    // fold, the page can be shorter than the viewport, and then there is no
    // overflow, no scroll event and no fetch. Restore the trigger.
    fillViewport();
  });
}

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
 *  footer (the outcome ledger). One card type for every turn — a one-word
 *  answer and a forty-tool-call refactor are the same object, differing only
 *  in how much body they have. Density comes from type scale, not from
 *  structural variation.
 *
 *  A card is born in the residency the current pass planned for it (D1): a
 *  tier-3 turn mounts as a header/footer STUB — no `.turn-body`, no inner
 *  reconcile, no per-block effects — and folds at birth, which removes nothing
 *  because the card did not exist a frame ago. Everything else builds its body
 *  exactly as before, and its fold state stays `applyFoldPass`'s business, so
 *  an open card's mount is byte-identical to the pre-tier renderer's. */
function buildTurn(t: Turn): HTMLElement {
  const card = el("div", { className: "turn" });
  // No `data-outcome` on the CARD: the outcome is carried by the header dot,
  // the footer glyph and the rail marker, and the leading-edge hairline that
  // was this attribute's only reader is gone (29-turns.css).
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
  if (plan === undefined || plan.mounted) {
    const body = el("div", { className: "turn-body" });
    card.appendChild(body);
    reconcile(body, t.body, messageSpec);
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

  // A new user turn pops the reader back to the bottom. scrollToBottom() does
  // an explicit RAF-paced scroll that lands on the new card immediately
  // (suppressScroll would have blocked the auto-scroll for the very turn that
  // just arrived).
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
  // A stub has no body element, so the outer reconcile renders whatever
  // mountedness the fold pass last applied; only the fold pass and the
  // on-demand build change it.
  const body = card.querySelector<HTMLElement>(":scope > .turn-body");
  if (body !== null) {
    reconcile(body, t.body, messageSpec);
  }
  mountTurnFooter(card, t);
  mountRewind(card, t);
  syncTurnBodyless(card);
  syncTurnFace(card, t);
}

/** Build a card's body in place, from the turn's current projection. The
 *  3→1/3→2 transition (search reveal, failure flip, live-run attach, a rewind
 *  shrinking the window) — and the synchronous half of the on-demand build. */
function mountTurnBodySync(card: HTMLElement, t: Turn): void {
  if (card.querySelector(":scope > .turn-body") !== null) {
    return;
  }
  const header = card.querySelector<HTMLElement>(":scope > .turn-header");
  if (header === null) {
    return;
  }
  const body = el("div", { className: "turn-body" });
  header.after(body);
  reconcile(body, t.body, messageSpec);
  syncTurnBodyless(card);
}

/** The 2→3/1→3 transition: drop a card's body DOM and every per-message
 *  resource behind it — the same disposal the card's own removal runs, because
 *  a stub holds exactly what a removed card no longer does. */
function unmountTurnBody(card: HTMLElement): void {
  const body = card.querySelector<HTMLElement>(":scope > .turn-body");
  if (body === null) {
    return;
  }
  const rows = body.querySelectorAll<HTMLElement>(`:scope > [${KEY_ATTR}]`);
  for (const row of rows) {
    const key = row.getAttribute(KEY_ATTR);
    if (key !== null) {
      disposeMessage(key);
    }
  }
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

/** In-flight builds by turn id, so a second caller joins the first instead of
 *  double-appending rows. */
const turnBodyBuilds = new Map<string, Promise<void>>();

function yieldToBrowser(): Promise<void> {
  const sched = (globalThis as { scheduler?: { yield?: () => Promise<void> } }).scheduler;
  if (sched?.yield !== undefined) {
    return sched.yield.call(sched);
  }
  return new Promise((resolve) => {
    setTimeout(resolve, 0);
  });
}

/** Build `turnID`'s body on demand, in yielded block batches. Resolves when
 *  the body is complete (or the moment the build stops being applicable: the
 *  chat switched, the card left the DOM, the turn left the projection).
 *  Idempotent while in flight, a no-op on an already-mounted turn. */
export function mountTurnBody(chatID: string, turnID: string): Promise<void> {
  const existing = turnBodyBuilds.get(turnID);
  if (existing !== undefined) {
    return existing;
  }
  const build = buildTurnBodyBatches(chatID, turnID).finally(() => {
    turnBodyBuilds.delete(turnID);
  });
  turnBodyBuilds.set(turnID, build);
  return build;
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
    let body = card.querySelector<HTMLElement>(":scope > .turn-body");
    if (body === null) {
      const header = card.querySelector<HTMLElement>(":scope > .turn-header");
      if (header === null) {
        return;
      }
      body = el("div", { className: "turn-body" });
      header.after(body);
    }
    const have = body.querySelectorAll(`:scope > [${KEY_ATTR}]`).length;
    if (have >= t.body.length) {
      syncTurnBodyless(card);
      return;
    }
    let blocks = 0;
    let i = have;
    while (i < t.body.length) {
      const m = t.body[i];
      if (m === undefined) {
        break;
      }
      // The reconcile's own mount arm, replayed: same builder, same key
      // attribute, appended in order onto rows that are already in order.
      const node = messageSpec.mount(m);
      node.setAttribute(KEY_ATTR, messageSpec.key(m));
      body.appendChild(node);
      blocks += Math.max(1, (m.blocks ?? []).length);
      i++;
      if (blocks >= BUILD_BATCH_BLOCKS && i < t.body.length) {
        break;
      }
    }
    if (i >= t.body.length) {
      syncTurnBodyless(card);
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
function buildAssistant(m: Message): HTMLElement {
  const wrap = el("div", { className: "msg-wrap msg-wrap-assistant" });
  // The transcript only ever renders the active chat (`paint` reads
  // `getActive()`), and the render carries that id because the per-tool signal is
  // keyed on it: the mount and `upsertToolCall` have to name the same chat.
  const chatID = getActiveId();
  buildAssistantBody(wrap, m, chatID, isLikelyLiveStreaming(m), steerMarks(chatID));
  return wrap;
}

/** Incremental update: mount newly-arrived blocks + refresh plan/footer.
 *  Per-block and per-tool signals feed streaming deltas straight into the
 *  already-mounted primitives, so this only handles structural growth. */
function updateAssistant(wrap: HTMLElement, m: Message): void {
  if (!messageStates.has(m.id)) {
    return;
  }
  const chatID = getActiveId();
  updateAssistantBody(wrap, m, chatID, liveStateOf(m), steerMarks(chatID));
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
