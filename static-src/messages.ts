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
  getActive,
  getActiveId,
  watchActiveId,
  messagesVersionOf,
  renderCauseOf,
  steerMarks,
} from "./store.js";
import { clearStreamingSig, clearReasoningSig, clearAllBlockSigs } from "./store-signals.js";
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
} from "./scroll.js";
import {
  buildTurnHeader,
  updateTurnHeader,
  initTurnHeaderCallbacks,
  clickFoldsTurn,
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
  turnFoldSummary,
  turnRunIDs,
  type Turn,
} from "./turns.js";
import { peekRunState, runIsLive } from "./run-store.js";
import { isTurnOpen, setTurnOpen } from "./fold-state.js";
import { wireRowToggle } from "./disclosure-row.js";
import { searchHitCount } from "./chat-search.js";
import {
  mountTurnRail,
  observeTurns,
  resetTurnRail,
  loadTurnRail,
  pointTurnRail,
} from "./turn-rail.js";
import {
  buildAssistantBody,
  updateAssistantBody,
  finalizeAssistantBody,
  disposeAssistantBody,
  resetBlockRenders,
  refreshGroupHeader,
  refreshMessageCard,
  liveRenderIDs,
  openContainerKeys,
  initBlockRenderer,
  getLiveAnchor,
} from "./messages-blocks.js";
import { explainError as explainErrorAction } from "./actions/messages.js";
import { rewindChat } from "./actions/rewind.js";
import { confirm as confirmDialog } from "./confirm.js";
import { disposeAllToolEffects, initToolCallbacks } from "./messages-tools.js";
import { buildEvent, updateEvent, buildSystemFallback } from "./messages-events.js";
import {
  attachTurnActions,
  initTurnActionCallbacks,
  copyWithFeedback,
} from "./messages-turn-actions.js";
import { syncCodeReferences } from "./code-refs.js";
import { syncRefusal, setRefusalRewindHandler } from "./refusal.js";

// ---------------------------------------------------------------------------
// Public re-exports
// ---------------------------------------------------------------------------

export { getScrollEl, scrollToBottom, setLoadMore, resetScrollState };
// Re-exported for the same reason the scroll helpers are: this module owns the
// rail (it mounts it and feeds it the painted cards), so chat.ts reaching the
// rail THROUGH here keeps ownership in one place instead of two modules driving
// the same surface.
export { loadTurnRail, pointTurnRail };

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
 *  holds the element's ordinary state, so a stale one changes nothing. */
export function fadeInTranscript(): void {
  messagesEl.removeAttribute("data-chat-entry");
  void messagesEl.offsetWidth;
  messagesEl.setAttribute("data-chat-entry", "");
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
  const isChatSwitch = lastActiveId !== session.id;
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
  // The placeholder and the conversation may never share this container, and the
  // rule is enforced HERE because this is the line where content lands. Two
  // reasons it cannot be left to the activation's continuation, which removes the
  // skeleton one microtask later: reconcile inserts the newest turn AFTER any
  // unkeyed sibling, so a skeleton still mounted at this point ends up sitting
  // above the whole conversation rather than below it; and "no frame is painted
  // between two microtasks" is a timing property of one call order, not an
  // invariant of the renderer. Only when there is something to replace it with —
  // an empty turn list is a chat still loading, which is what the placeholder is
  // for. Unkeyed pagination furniture (`load-more-indicator`, the load-more
  // skeleton) is deliberately untouched: that one is mounted BESIDE real turns on
  // purpose.
  if (turns.length > 0) {
    document.getElementById(CHAT_SKELETON_ID)?.remove();
  }
  reconcile(messagesEl, turns, turnSpec);
  // ONE walk over the container's children builds the card list every full-pass
  // consumer shares — the rail's observer and the fold pass. Filtered because
  // unkeyed furniture (load-more, skeletons) lives beside the cards.
  const cards: HTMLElement[] = [];
  for (const child of messagesEl.children) {
    if (child.classList.contains("turn")) {
      cards.push(child as HTMLElement);
    }
  }
  // Tell the rail which cards exist so it can track the turn in view. Re-run per
  // full pass because the set changes as pages load and turns arrive.
  observeTurns(cards);
  applyFoldPass(session.id, turns, cards);
  finalizeStreamingIfNeeded(session.messages);
  reachableBlocks = blockCount(session.messages);
  refreshResumeLabel();
  lastNewestId = session.messages[session.messages.length - 1]?.id;
  lastActiveId = session.id;
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
  // judgment frozen on the message's state.
  const live = messageStates.get(msgID)?.streaming ?? false;
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
  streamingIds.clear();
  resetScrollState();
  resetTurnRail();
  reconcile(messagesEl, [] as Turn[], turnSpec);
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
 * Fold every turn that should be folded, and unfold every turn that should not.
 *
 * DEFERRED WHILE READING and COMPENSATED WHEN APPLIED, both mandatory. A fold
 * removes hundreds of pixels rather than tens, so content vanishing from above
 * the reader through no action of their own is the failure mode this guards —
 * which is why §3.4 makes the helper an obligation rather than a nicety.
 *
 * Runs on every paint because eligibility changes with every new turn: the turn
 * that was second-newest becomes third-newest and folds.
 */
function applyFoldPass(
  chatID: string,
  turns: readonly Turn[],
  cards: readonly HTMLElement[],
): void {
  const wanted = new Map<string, boolean>();
  const hits = new Map<string, number>();
  for (const [i, t] of turns.entries()) {
    // A turn holding a live workflow run stays open however far back it is; see
    // isTurnOpen. `peekRunState` rather than `runState`, because this pass runs
    // inside no effect and a tracked read here would subscribe the whole fold pass
    // to every run on screen. The run ids come from the projection-time cache;
    // the derivation stays turns.ts's.
    const liveRun = (turnRunIDsCache.get(t.id) ?? turnRunIDs(t)).some((id) =>
      runIsLive(peekRunState(id)),
    );
    wanted.set(t.id, isTurnOpen(chatID, t, i, turns.length, liveRun));
    hits.set(t.id, searchHitCount(t.n));
  }
  const changes: (() => void)[] = [];
  for (const card of cards) {
    const id = card.getAttribute(KEY_ATTR);
    if (id === null) {
      continue;
    }
    setHitCount(card, hits.get(id) ?? 0);
    const open = wanted.get(id) ?? true;
    const folded = card.hasAttribute("data-folded");
    if (open === !folded) {
      continue;
    }
    changes.push(() => {
      setCardFolded(card, !open);
    });
  }
  if (changes.length === 0) {
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
    const open = card.hasAttribute("data-folded");
    const chatID = getActiveId();
    if (chatID !== "") {
      setTurnOpen(chatID, t.id, open);
    }
    // Applied immediately and compensated: this is the reader's own action, so
    // it is not deferred, but it still must not move what they are looking at.
    preserveReadingPosition(() => {
      setCardFolded(card, !open);
    }, "content-growth");
  });
  // The band activates that button, so folding a turn is not a 16x16 target.
  // Copy, the show-more, an attachment pill and a linkified path inside the
  // request all keep their own click — `wireRowToggle` skips a control by kind.
  // The band activates that button, so folding a turn is no longer a 16x16
  // target. Where the surface STOPS is `clickFoldsTurn`'s call, next to the
  // header it describes; the cursor and hover fill in 29-turns.css mark exactly
  // the region it admits, in both states.
  wireRowToggle(header, btn, {
    skip: (target) => !clickFoldsTurn(target, card.hasAttribute("data-folded")),
  });
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
  if (folded) {
    card.setAttribute("data-folded", "");
  } else {
    card.removeAttribute("data-folded");
  }
  const header = card.querySelector<HTMLElement>(":scope > .turn-header");
  header?.setAttribute("aria-expanded", folded ? "false" : "true");
}

// --- The turn card ---

/** Build one turn: tinted header (the trigger), plain body (the work), tinted
 *  footer (the outcome ledger). One card type for every turn — a one-word
 *  answer and a forty-tool-call refactor are the same object, differing only
 *  in how much body they have. Density comes from type scale, not from
 *  structural variation. */
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

  const body = el("div", { className: "turn-body" });
  card.appendChild(body);
  reconcile(body, t.body, messageSpec);

  mountTurnFooter(card, t);
  // After the footer: Rewind lives inside it, so it must exist first.
  mountRewind(card, t);
  syncTurnBodyless(card);

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
  const body = card.querySelector<HTMLElement>(":scope > .turn-body");
  if (body !== null) {
    reconcile(body, t.body, messageSpec);
  }
  mountTurnFooter(card, t);
  mountRewind(card, t);
  syncTurnBodyless(card);
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
    foldSummary: turnFoldSummary(t),
  };
  const existing = card.querySelector<HTMLDivElement>(":scope > .turn-footer");
  // A turn with a rewind target keeps its footer even when the ledger is empty:
  // the footer is where Rewind lives, and an unstamped ledger (a turn whose
  // usage never persisted) must not cost the reader the action.
  if (!hasTurnSummary(data) && t.rewindTo === undefined) {
    existing?.remove();
    return;
  }
  if (existing === null) {
    card.appendChild(buildTurnFooter(data));
  } else {
    updateTurnFooter(existing, data);
  }
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
  const state = messageStates.get(m.id);
  if (state === undefined) {
    return;
  }
  const chatID = getActiveId();
  updateAssistantBody(wrap, m, chatID, state.streaming, steerMarks(chatID));
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

/** Finalize every mounted message that is no longer live: the still-streaming
 *  turn keeps its caret only while it is the LAST assistant message of a
 *  thinking session; everything else flushes its markdown streams, seals its
 *  reasoning traces and gains the turn-actions row. Driven from the same effect
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
  for (const id of candidates) {
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
