// The transcript's turn rail: one marker per turn on a vertical axis in the chat
// gutter. Position is a function of the turn's own NUMBER and of the session's size,
// and the active turn is a function of scroll offset — `rail-select.ts` and
// `rail-activation.ts` own both arithmetics. This module is the DOM, the click flow
// and the caches around them.

import { el } from "@cplieger/reactive";
import { apiGet } from "./api-client.js";
import {
  atLiveEdgeNow,
  beginSelfScroll,
  endSelfScroll,
  getScrollEl,
  onAttach,
  onContentResize,
  onReaderGesture,
  onTranscriptMutate,
  readingLineOffset,
  scrollableBy,
  scrollToOffset,
} from "./scroll.js";
import { markerLabel, railLabel } from "./rail-labels.js";
import { formatElapsed, isoDuration } from "./strings.js";
import { severityOf } from "./turn-severity.js";
import { projectTurns, turnLedger, WHOLE_SESSION } from "./turns.js";
import { join } from "@cplieger/keyenc";
import { searchHitTurns } from "./chat-search.js";
import { get, turnBaseOf, turnLive } from "./store.js";
import { mergeTurnSets, validateTurnIndex } from "./rail-merge.js";
import type { TurnSummary } from "./rail-merge.js";
import { railAt, railMetrics, railSpan, selectMarkers } from "./rail-select.js";
import { activeTurnAt, buildOffsets } from "./rail-activation.js";
import type { CardTop, TurnOffsets } from "./rail-activation.js";

/** One row of the session-wide turn index. Declared by the module that MERGES the
 *  set, which is a pure leaf, and re-exported here because this is where the rail's
 *  consumers already read it from. */
export type { TurnSummary };

/** How far the transcript must be able to scroll before the rail appears. A
 *  navigator has nothing to offer a conversation the reader can already see whole,
 *  and a threshold rather than `> 0` because a transcript overflowing by a few
 *  pixels would flip the rail on and off as its own content settles.
 *
 *  The number matching `BOTTOM_TOLERANCE_PX` is a coincidence of scale, not a
 *  shared decision — do not collapse the two. */
const MIN_SCROLL_PX = 100;

/** Wall-clock bound on one jump's paging loop, so a store that keeps reporting more
 *  history cannot spin. Deliberately not a page count: `loadUntilResident` is
 *  written for sessions of 8 pages and more, so a small iteration cap would make a
 *  click on an early marker scroll nowhere. */
const PAGE_BUDGET_MS = 4000;

/** How long one scroll is given to settle before the landing is re-measured.
 *  `scrollend` is not universally implemented, so this is the only release path on
 *  an engine without it rather than belt-and-braces. */
const PICK_SETTLE_MS = 1200;

/** At most this many `behavior: "auto"` corrections per jump — never a second smooth
 *  animation, so exactly one is ever in flight. */
const MAX_CORRECTIONS = 6;

/** How far the target's top may sit from the reading line and still count as landed. */
const LANDING_TOLERANCE_PX = 8;

/** Ceiling on the click's own intent, which suppresses offset-driven activation
 *  while the animation runs. DERIVED so it cannot fall inside the two budgets it
 *  brackets: a paged jump spends both in sequence. */
const PICK_BUDGET_MS = PAGE_BUDGET_MS + PICK_SETTLE_MS;

let root: HTMLElement | undefined;
let chatID = "";
/** The pointed chat's index rows, as last fetched or replayed from its record. Held
 *  beside `records` rather than read out of it, because a chat the STORE does not
 *  hold records nothing and would otherwise lose its rail. */
let indexed: TurnSummary[] = [];
let summaries: TurnSummary[] = [];
/** `summaries` indexed by the turn's opening-message id, rebuilt wherever the set is
 *  assigned, so the id-keyed lookups are never a linear scan. */
let summaryByID = new Map<string, TurnSummary>();
/** The session's turn count, which is what `railAt` divides by. */
let total = 0;
/** The turn the scroll offset names. A turn ID rather than a number, because the id
 *  is the turn's identity and the number is a value the index can restate. */
let activeID = "";
/** The turn the READER picked, which outranks `activeID` until they say otherwise.
 *
 *  INVARIANT, enforced at `setTurns`: only ever a turn the marker set carries. The
 *  click reads it off a summary, and the merge drops it when the new set no longer
 *  names it — a rewind truncates the session from a turn footer two clicks away. */
let selectedID: string | undefined;
/** Turn IDs whose jump is waiting on a fetch, so the marker can say so. */
const pending = new Set<string>();

/** One chat's fetched index plus the chat's message count when the request went out,
 *  captured BEFORE the fetch (the same discipline as loadMessages' `knownBefore` — an
 *  answer that raced an append must not claim currency over it). */
interface RailRecord {
  summaries: TurnSummary[];
  atCount: number;
}

/** Session-wide indexes by chat, kept across switches so returning to a loaded chat
 *  paints its rail from memory instead of refetching. `refreshTurnRail` is the one
 *  writer; re-pointing prunes rows the store no longer holds, and
 *  `invalidateTurnRails` drops them all. */
const records = new Map<string, RailRecord>();

/** Whether `id`'s record can stand in for a fetch: present and from the chat's current
 *  message count. The count is the cheap proxy for "a turn started or ended since" —
 *  background SSE ingest moves it while the rail is pointed elsewhere. The index's GET
 *  carries no digest stamp, so a whole-projection reconcile drops the records by hand
 *  through `invalidateTurnRails` instead. */
function recordCurrent(id: string): boolean {
  const r = records.get(id);
  if (r === undefined) {
    return false;
  }
  return r.atCount === get(id)?.message_count;
}

/** Forget every held index: the reconcile body's call, because nothing certifies a
 *  record across a lost stream. Each chat re-reads its index at its next activation;
 *  the one on screen is re-read by the refresh the reconcile ends on. */
export function invalidateTurnRails(): void {
  records.clear();
}

/** Whether there is enough transcript to navigate. Read live at every render rather
 *  than cached from a paint, because the answer changes on window resize too. */
function navigable(): boolean {
  return scrollableBy() > MIN_SCROLL_PX;
}

/** The navigability the last render was built from, so a paint that flips it can
 *  re-render and the overwhelming majority that do not cost one comparison. */
let renderedNavigable = false;

/** Everything the rendered markers were built FROM, so a render that would redraw the
 *  same rail redraws nothing.
 *
 *  `render()` runs on every transcript paint (`setResidentTurns`), which on a streaming
 *  turn is several times a second, and it ends in `root.replaceChildren(...)` over freshly
 *  built nodes. Each marker is a `<button>` and a pending one carries `vk-dot-beat`, so an
 *  unguarded rebuild took focus off a keyboard reader's marker and restarted the beat that
 *  often — the same reader-state loss `exec-view/place.ts` records for a re-seat, reached
 *  by rebuilding instead of moving. A signature is the cheaper half of the two available
 *  fixes: the other is a keyed reconcile of a heterogeneous list whose every node's
 *  geometry moves whenever the window does, which is a redesign of this function rather
 *  than a guard on it.
 *
 *  It must name EVERY input the nodes read, or the rail goes stale, which is worse than
 *  the rebuild. `renderSignature` is that list and is the only place it lives. */
let renderedSig = "";

/** The one writer of the marker set: the resident window merged into the fetched
 *  index, so the newest turn appears the moment its card mounts and the index only
 *  extends the set backwards. Also the one place the reader's pick is reconciled
 *  against that set, because this is the moment the mapping moves. */
function setTurns(): void {
  const session = get(chatID);
  const resident =
    session === undefined
      ? []
      : projectTurns(session.messages, turnLive(session), turnBaseOf(session));
  const base = session === undefined ? WHOLE_SESSION : turnBaseOf(session);
  const merged = mergeTurnSets(resident, indexed, base);
  summaries = merged.turns;
  total = merged.total;
  summaryByID = new Map(summaries.map((s) => [s.id, s]));
  if (selectedID !== undefined && !summaryByID.has(selectedID)) {
    selectedID = undefined;
  }
}

/** Mount the rail into the transcript's positioned outer wrapper. Idempotent. */
export function mountTurnRail(host: HTMLElement): void {
  if (root !== undefined) {
    return;
  }
  root = el("nav", { className: "turn-rail", "aria-label": railLabel(0, 0) });
  host.appendChild(root);
  getScrollEl().addEventListener("scroll", schedulePick, { passive: true });
  // A READER GESTURE revokes the pick; nothing else does. That covers both ways the
  // reader states a position — a scroll, and a request for the live edge, which
  // scrolls through the controller and so fires no reader scroll event.
  onReaderGesture(clearSelection);
  // Mount, unmount and the pagination prepend move every top below them, and
  // `content-visibility: auto` on `.msg-row` makes a card swapping its estimated
  // height for its real one do the same with no DOM change behind it. An unpark
  // restores a scroll position against cards that were re-measured while the view
  // was parked, which is neither of those.
  onTranscriptMutate(repick);
  onContentResize(repick);
  onAttach(repick);
  if (typeof ResizeObserver === "function") {
    new ResizeObserver(scheduleRailRender).observe(root);
  }
}

/** Hand the rail to a chat, dropping the previous session's view state.
 *
 *  Separate from the fetch because an EMPTY chat has to re-point too and has nothing
 *  to fetch. The index itself is NOT view state: the chat's record paints
 *  immediately, which is what makes a switch back to a loaded chat cost no fetch. */
export function pointTurnRail(id: string): void {
  if (id === chatID) {
    return;
  }
  selectedID = undefined;
  pending.clear();
  releaseIntent();
  invalidateJumps();
  // Records for chats the store no longer holds are dead weight (closed tabs,
  // deleted chats), and a re-point is the cheap moment to drop them — including the
  // target's own, so a purged chat renders empty rather than from memory.
  for (const key of records.keys()) {
    if (get(key) === undefined) {
      records.delete(key);
    }
  }
  chatID = id;
  indexed = records.get(id)?.summaries ?? [];
  activeID = "";
  residentCards = [];
  invalidateOffsets();
  setTurns();
  render();
}

/** The activation entry: point the rail at the chat, then fetch its index only when
 *  the chat's record cannot stand in for one. `force` skips that gate — the caller
 *  activating a stale transcript knows the rail is implicated with it. */
export async function loadTurnRail(id: string, opts?: { force?: boolean }): Promise<void> {
  pointTurnRail(id);
  if (opts?.force !== true && recordCurrent(id)) {
    return;
  }
  await refreshTurnRail(id);
}

/** Re-fetch the session-wide index. */
export async function refreshTurnRail(id: string): Promise<void> {
  if (id === "") {
    return;
  }
  // Captured BEFORE the request — see RailRecord. A count the store does not know
  // records nothing: there is no session left to activate against.
  const countAtStart = get(id)?.message_count;
  const d = await apiGet<{ turns?: unknown }>(`/api/chats/${encodeURIComponent(id)}/turns`);
  if (d === null) {
    // A failed fetch, already logged centrally. Keep what the rail is showing and
    // keep the stale record, so the next activation retries instead of trusting it.
    return;
  }
  const { turns } = validateTurnIndex(d.turns ?? []);
  if (countAtStart !== undefined) {
    records.set(id, { summaries: turns, atCount: countAtStart });
  }
  if (id !== chatID) {
    return;
  }
  indexed = turns;
  setTurns();
  render();
  // The index is what turns an already-visible card into a placeable one, and a
  // scroll frame may never arrive on its own.
  repick();
}

export function resetTurnRail(): void {
  chatID = "";
  records.clear();
  indexed = [];
  summaries = [];
  summaryByID = new Map();
  total = 0;
  activeID = "";
  selectedID = undefined;
  renderedNavigable = false;
  pending.clear();
  residentCards = [];
  invalidateOffsets();
  releaseIntent();
  invalidateJumps();
  clearRailTarget();
  if (pickFrame !== 0) {
    cancelAnimationFrame(pickFrame);
    pickFrame = 0;
  }
  if (renderFrame !== 0) {
    cancelAnimationFrame(renderFrame);
    renderFrame = 0;
  }
  render();
}

/** Record which turn cards the transcript holds. Called after every full paint,
 *  which is what makes the offset table's rebuild the paint's own cost rather than a
 *  per-scroll-frame one. */
export function setResidentTurns(cards: Iterable<HTMLElement>): void {
  residentCards = [...cards];
  setTurns();
  render();
  repick();
}

// ---------------------------------------------------------------------------
// Activation
// ---------------------------------------------------------------------------

/** The transcript's turn cards, in paint order. */
let residentCards: HTMLElement[] = [];
/** The cached offset table, rebuilt lazily on the next read. */
let offsets: TurnOffsets | undefined;
/** The scroll-coalesced activation read. */
let pickFrame = 0;
/** The resize-coalesced render, deferred out of the rail's own resize delivery. */
let renderFrame = 0;

function invalidateOffsets(): void {
  offsets = undefined;
}

/** Clear the cached geometry AND re-answer activation. The second half is not
 *  optional: nothing else re-derives the active turn when the table is invalidated
 *  by something that is not a scroll, so without it the mark freezes until the
 *  reader happens to scroll. */
function repick(): void {
  invalidateOffsets();
  schedulePick();
}

/** Defer the resize-driven render one frame, behind a single slot.
 *
 *  IT MAY NOT RUN INSIDE THE RAIL'S OWN RESIZE DELIVERY: it writes `aria-label` and
 *  `replaceChildren` on the OBSERVED element, and `.turn-rail:empty` hides the
 *  element, so the empty/non-empty boundary is a change to the rail's own box — an
 *  observation re-activated at the depth being delivered, which the engine reports as
 *  "ResizeObserver loop completed with undelivered notifications". Every other
 *  `render()` call is a paint or activation path, and stays synchronous. */
function scheduleRailRender(): void {
  if (renderFrame !== 0) {
    return;
  }
  renderFrame = requestAnimationFrame(() => {
    renderFrame = 0;
    render();
    repick();
  });
}

/** Coalesce scroll events into one activation read per frame. */
function schedulePick(): void {
  if (pickFrame !== 0) {
    return;
  }
  pickFrame = requestAnimationFrame(() => {
    pickFrame = 0;
    pick();
  });
}

function pick(): void {
  // A held intent owns the position for the length of its own animation, which
  // crosses every intervening turn on the way.
  if (intentOpen) {
    return;
  }
  const scroller = getScrollEl();
  const next = activeTurnAt(scroller.scrollTop, readOffsets(), readingLineOffset(), {
    clientHeight: scroller.clientHeight,
    atLiveEdge: atLiveEdgeNow(),
  });
  if (next !== "" && next !== activeID) {
    activeID = next;
    render();
  }
}

function readOffsets(): TurnOffsets {
  offsets ??= buildOffsets(cardTops());
  return offsets;
}

/** Measure the resident cards into the scroller's own frame. */
function cardTops(): CardTop[] {
  const out: CardTop[] = [];
  for (const card of residentCards) {
    const key = keyOf(card);
    if (key !== "") {
      out.push({ id: key, top: scrollFrameTop(card) });
    }
  }
  return out;
}

/** A card's top in the SCROLLER's frame, or null for a card the engine reports no
 *  box for. Rects rather than `offsetTop`: `content-visibility: auto` on `.msg-row`
 *  makes the row a containing block, so an offsetParent-relative read returned 0 for
 *  a block whose true position was 2203. */
function scrollFrameTop(card: HTMLElement): number | null {
  if (card.getClientRects().length === 0) {
    return null;
  }
  const scroller = getScrollEl();
  const frame = scroller.getBoundingClientRect();
  return scroller.scrollTop + card.getBoundingClientRect().top - frame.top - scroller.clientTop;
}

/** A card's stable identity: the id of the turn's OPENING MESSAGE, which is both the
 *  transcript's reconcile key and the server index's `TurnSummary.id`. "" for an
 *  element carrying no key, which is not a turn card. */
function keyOf(card: Element): string {
  return card.getAttribute("data-reconcile-key") ?? "";
}

// ---------------------------------------------------------------------------
// Render
// ---------------------------------------------------------------------------

function render(): void {
  if (root === undefined) {
    return;
  }
  renderedNavigable = navigable();
  // No markers means no rail: `.turn-rail:empty` hides the element, which takes the
  // axis line with it, so an unnavigable transcript needs no second mechanism.
  if (summaries.length === 0 || !renderedNavigable) {
    root.setAttribute("aria-label", railLabel(0, total));
    root.replaceChildren();
    // No signature can equal this, so flipping back to navigable re-renders.
    renderedSig = "";
    return;
  }
  const { markerPx, pitchPx } = railMetrics(root);
  const shown = selectMarkers(summaries, root.clientHeight, pitchPx, searchHitTurns());
  // ONE span for the whole render, so two markers cannot disagree about how far down
  // the track the session reaches.
  const span = railSpan(total, root.clientHeight, markerPx);
  // Once per render, not once per marker: the walk is over the whole resident window.
  const elapsed = residentElapsed();
  // Written unconditionally: it is one attribute on the container rather than a node the
  // reader can hold, so it costs nothing and cannot go stale behind the guard below.
  root.setAttribute("aria-label", railLabel(shown.length, total));
  const sig = renderSignature(shown, elapsed, span);
  if (sig === renderedSig) {
    return;
  }
  renderedSig = sig;

  const nodes: HTMLElement[] = [];
  for (const s of shown) {
    nodes.push(markerNode(s, elapsed, span));
  }
  root.replaceChildren(...nodes);
}

/** Every value the rendered nodes read, in one string. See `renderedSig`.
 *
 *  `markerNode` is the whole of it: `s.id`, `s.n`, `s.outcome`, `s.agent_initiated`, the
 *  search-hit and pending flags, its elapsed value, and the three module-level marks
 *  (`selectedID`, `activeID`, `markedID()`) that decide which single marker is filled;
 *  `railAt` folds `total` and `span`. Joined through `keyenc`, not a separator: a turn id
 *  is server-minted text and a collision here is a rail that stops updating. */
function renderSignature(
  shown: readonly TurnSummary[],
  elapsed: ReadonlyMap<string, number>,
  span: number,
): string {
  const hits = searchHitTurns();
  const parts: string[] = [
    String(span),
    String(total),
    String(shown.length),
    selectedID ?? "",
    activeID,
    markedID(),
  ];
  for (const s of shown) {
    parts.push(
      "m",
      s.id,
      String(s.n),
      s.outcome,
      s.agent_initiated === true ? "1" : "0",
      hits.has(s.n) ? "1" : "0",
      pending.has(s.id) ? "1" : "0",
      String(elapsed.get(s.id) ?? -1),
    );
  }
  return join(...parts);
}

/** One turn's marker, positioned by the `--rail-at` fraction one CSS rule consumes.
 *  The SINGLE writer of `data-current` / `data-selected`, and exactly one of the two
 *  is written per render: both take the same filled treatment, so writing both would
 *  claim two positions. */
function markerNode(s: TurnSummary, elapsed: Map<string, number>, span: number): HTMLElement {
  const hit = searchHitTurns().has(s.n);
  const isPending = pending.has(s.id);
  const elapsedMs = elapsed.get(s.id);
  // ONE composer for both channels, and NO native `title`: a UA tooltip misses the
  // styled treatment every other hover uses and publishes no `aria-describedby`.
  const label = markerLabel(s, {
    pending: isPending,
    hit,
    elapsedMs,
  });
  const btn = el(
    "button",
    {
      className: "rail-marker",
      type: "button",
      "data-tooltip": label.tooltip,
      "aria-label": label.ariaLabel,
    },
    String(s.n),
  );
  btn.style.setProperty("--rail-at", String(railAt(s.n, total, span)));
  btn.dataset["outcome"] = s.outcome;
  btn.dataset["severity"] = severityOf(s.outcome);
  if (selectedID === undefined) {
    if (s.id === activeID) {
      btn.dataset["current"] = "";
    }
  } else if (s.id === selectedID) {
    btn.dataset["selected"] = "";
  }
  // One element carries it, and it names the turn the rail is CLAIMING.
  if (s.id === markedID()) {
    btn.setAttribute("aria-current", "true");
  }
  if (s.agent_initiated === true) {
    btn.dataset["trigger"] = "system";
  }
  if (isPending) {
    btn.dataset["pending"] = "";
  }
  // A search hit marks the rail, which is the fastest read of WHERE in the session
  // the answer lives — a match 200 turns up is visible before anyone goes looking.
  if (hit) {
    btn.dataset["hit"] = "";
  }
  // The app's ONE machine-readable duration: the `datetime` and the text are two
  // spellings of one value. No element at all when the store cannot answer.
  if (elapsedMs !== undefined) {
    btn.appendChild(
      el(
        "time",
        { className: "rail-marker-time", datetime: isoDuration(elapsedMs) },
        formatElapsed(elapsedMs),
      ),
    );
  }
  btn.addEventListener("click", () => {
    // BEFORE the jump and unconditionally, which is the whole point: the jump is
    // allowed to do nothing and the click still has to produce a reaction. The id
    // comes off the summary this marker was built from, so the pick's invariant
    // holds at this writer by construction.
    selectedID = s.id;
    holdIntent();
    render();
    void navigateToTurn(s);
  });
  return btn;
}

/** The turn the rail claims the reader is at: their own pick while they hold one,
 *  the scroll-derived turn otherwise. */
function markedID(): string {
  return selectedID ?? activeID;
}

/** Drop the reader's pick and repaint, if there was one to drop. */
function clearSelection(): void {
  releaseIntent();
  if (selectedID === undefined) {
    return;
  }
  selectedID = undefined;
  render();
}

/** Per-turn durations for the turns the STORE holds, keyed by the turn's opening
 *  message id. THE RAIL'S OWN FEED CANNOT ANSWER THIS: the turns index carries no
 *  duration, so the answer is bounded by the paginated window and a turn outside it
 *  gets no slot. */
function residentElapsed(): Map<string, number> {
  const out = new Map<string, number>();
  const messages = get(chatID)?.messages;
  if (messages === undefined) {
    return out;
  }
  // Default base, deliberately: this map is keyed by the turn's opening message id
  // and never reads `n`, so the window's own offset would change nothing here.
  for (const t of projectTurns(messages, false)) {
    const ms = turnLedger(t).elapsedMs;
    if (ms > 0) {
      out.set(t.id, ms);
    }
  }
  return out;
}

// ---------------------------------------------------------------------------
// Navigation
// ---------------------------------------------------------------------------

/** The on-demand body build for a stub turn, injected by messages.ts at mount (a
 *  static import back would cycle). Inert until wired, so a rail built in a test
 *  renders without the transcript. `activeView` scopes the card lookup to the ACTIVE
 *  transcript view: with parked views resident the same key exists once per view. */
let mountTurnBody: (chatID: string, turnID: string) => Promise<void> = () => Promise.resolve();
let activeView: () => HTMLElement | null = () => null;

export function initTurnRailCallbacks(cbs: {
  mountTurnBody: (chatID: string, turnID: string) => Promise<void>;
  activeView?: () => HTMLElement | null;
}): void {
  mountTurnBody = cbs.mountTurnBody;
  if (cbs.activeView !== undefined) {
    activeView = cbs.activeView;
  }
}

/** The jump that owns the scroller. Two markers clicked inside `PAGE_BUDGET_MS` are
 *  two operations in flight, and the SUPERSEDED one may not act: closing the epoch
 *  from its `finally` hands the reader to the live edge mid-flight, and its own
 *  corrections would write the scroller against a landing nobody asked for. */
let jumpGeneration = 0;

function ownsJump(gen: number): boolean {
  return gen === jumpGeneration;
}

/** Supersede every jump in flight, so one cannot tear down state that has since
 *  become another chat's. */
function invalidateJumps(): void {
  jumpGeneration++;
}

/** Whether the click's intent is suppressing offset-driven activation. */
let intentOpen = false;
let intentTimer = 0;

function holdIntent(): void {
  releaseIntent();
  intentOpen = true;
  intentTimer = window.setTimeout(() => {
    intentTimer = 0;
    releaseIntent();
  }, PICK_BUDGET_MS);
}

function releaseIntent(): void {
  if (intentTimer !== 0) {
    clearTimeout(intentTimer);
    intentTimer = 0;
  }
  intentOpen = false;
}

/** Jump to a turn: page it in when it is not resident, build its body, scroll once,
 *  then correct the landing until the turn's top sits on the reading line.
 *
 *  ONE branch point, at the paging step. Everything after it runs on both paths —
 *  the body build included, because its completion applies a scroller write inside
 *  `preserveReadingPosition`, which mid-animation would redirect the scroll. */
async function navigateToTurn(s: TurnSummary, behavior = jumpBehavior()): Promise<void> {
  // A second click on a turn that is already paging IS that jump, not another one:
  // claiming a generation here would supersede the operation this click is waiting
  // on, so the page would land and nothing would scroll to it.
  if (pending.has(s.id)) {
    return;
  }
  const gen = ++jumpGeneration;
  try {
    if (turnCard(s.id) === null && !(await pageIn(s))) {
      return;
    }
    if (!ownsJump(gen)) {
      return;
    }
    // A rejected build still scrolls: the body arrives on a later paint.
    await mountTurnBody(chatID, s.id).catch(() => undefined);
    if (!ownsJump(gen)) {
      return;
    }
    await nextFrame();
    if (!ownsJump(gen)) {
      return;
    }
    const card = turnCard(s.id);
    if (card === null) {
      return;
    }
    // The epoch opens HERE rather than at the paging step, so its own backstop can
    // never expire inside `PAGE_BUDGET_MS` and leave the scroll, the corrections and
    // the release running with no epoch at all.
    beginSelfScroll();
    const landing = landingFor(card);
    if (landing === null) {
      return;
    }
    scrollToOffset(landing, behavior);
    markRailTarget(card);
    await correctLanding(card, gen);
  } finally {
    // Per id and unconditional: a superseded jump still owns the marker it set
    // pending. The epoch and the pick belong to whoever owns the scroller, and the
    // epoch closes from here because one left open suspends pagination and silences
    // every reader gesture indefinitely.
    pending.delete(s.id);
    if (ownsJump(gen)) {
      endSelfScroll();
      releaseIntent();
      render();
      schedulePick();
    }
  }
}

/** Where the scroller has to sit for `card`'s top to land on the reading line. */
function landingFor(card: HTMLElement): number | null {
  const top = scrollFrameTop(card);
  return top === null ? null : top - readingLineOffset();
}

/** Page history in until the turn is resident, marking the turn while it waits. */
async function pageIn(s: TurnSummary): Promise<boolean> {
  if (pending.has(s.id)) {
    return false;
  }
  pending.add(s.id);
  render();
  return loadUntilResident(s, Date.now() + PAGE_BUDGET_MS);
}

/** Page backwards until the target turn's opening message is in the store. Two
 *  termination conditions and a wall-clock bound: the store reports no more history,
 *  a page made no progress, or the budget is spent. */
async function loadUntilResident(s: TurnSummary, deadline: number): Promise<boolean> {
  const [{ getActive }, { loadMessages }] = await Promise.all([
    import("./store.js"),
    import("./store-load.js"),
  ]);
  for (;;) {
    const session = getActive();
    if (session?.id !== chatID) {
      return false;
    }
    if (session.messages.some((m) => m.id === s.id)) {
      return true;
    }
    if (!session.has_more) {
      return false;
    }
    if (Date.now() > deadline) {
      // A budget spent on a store that still reports more history is a regression
      // signal rather than an ordinary end of the session.
      console.warn("turn rail: paging budget spent before the turn became resident", s.n);
      return false;
    }
    const oldest = session.messages[0];
    if (oldest === undefined) {
      return false;
    }
    await loadMessages(chatID, oldest.id);
    const after = getActive();
    if (after === undefined || after.messages[0]?.id === oldest.id) {
      return false;
    }
  }
}

/** Re-measure after each settle and jump the remainder. Never a second smooth
 *  animation: a correction closes the gap the first one could not see, because the
 *  page load and the body build moved the target after it was aimed at. */
async function correctLanding(card: HTMLElement, gen: number): Promise<void> {
  for (let i = 0; i < MAX_CORRECTIONS; i++) {
    await settled();
    if (!ownsJump(gen)) {
      return;
    }
    const landing = landingFor(card);
    if (landing === null) {
      return;
    }
    if (Math.abs(getScrollEl().scrollTop - landing) <= LANDING_TOLERANCE_PX) {
      return;
    }
    scrollToOffset(landing, "auto");
  }
  console.warn("turn rail: the landing did not settle within the correction budget");
}

/** Resolve on `scrollend` or at `PICK_SETTLE_MS`, whichever comes first. */
function settled(): Promise<void> {
  return new Promise<void>((resolve) => {
    const scroller = getScrollEl();
    let timer = 0;
    const finish = (): void => {
      if (timer !== 0) {
        clearTimeout(timer);
        timer = 0;
      }
      scroller.removeEventListener("scrollend", finish);
      resolve();
    };
    timer = window.setTimeout(finish, PICK_SETTLE_MS);
    scroller.addEventListener("scrollend", finish, { once: true });
  });
}

function jumpBehavior(): ScrollBehavior {
  const reduced =
    typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;
  return reduced ? "auto" : "smooth";
}

/** The mounted card for the turn whose opening message is `id`, or null when it is
 *  not resident. Scoped to the ACTIVE transcript view, because the reconcile key
 *  repeats once per resident view and a document-wide query answers in document
 *  order. The document fallback keeps the rail fixtures working unscoped. */
function turnCard(id: string): HTMLElement | null {
  if (id === "") {
    return null;
  }
  const selector = `[data-reconcile-key="${CSS.escape(id)}"]`;
  const view = activeView();
  return (view ?? document).querySelector<HTMLElement>(selector);
}

/** How long the landing card wears its ring. Long enough to be seen, short enough
 *  not to read as a persistent selected state — the marker carries that. */
const RAIL_TARGET_MS = 1000;

/** The card currently wearing `data-rail-target`, and the timer that removes it.
 *  Single-slot: a second click has to reset the first's timer, or the earlier
 *  deadline strips the ring off the card the reader just landed on. */
let railTarget: HTMLElement | undefined;
let railTargetTimer = 0;

/** Flash the ring on the card a jump landed on. A click on a turn already on screen
 *  moves only the marker, and a reader watching the TRANSCRIPT would see nothing.
 *
 *  `outline` only in the stylesheet, never `border` or `padding`: this fires on a
 *  card mid-transcript and must shift no layout. */
function markRailTarget(card: HTMLElement): void {
  clearRailTarget();
  railTarget = card;
  card.dataset["railTarget"] = "";
  railTargetTimer = window.setTimeout(() => {
    railTargetTimer = 0;
    clearRailTarget();
  }, RAIL_TARGET_MS);
}

function clearRailTarget(): void {
  if (railTargetTimer !== 0) {
    clearTimeout(railTargetTimer);
    railTargetTimer = 0;
  }
  if (railTarget !== undefined) {
    delete railTarget.dataset["railTarget"];
    railTarget = undefined;
  }
}

function nextFrame(): Promise<void> {
  return new Promise((resolve) => {
    if (typeof requestAnimationFrame === "function") {
      requestAnimationFrame(() => {
        resolve();
      });
      return;
    }
    setTimeout(resolve, 0);
  });
}
