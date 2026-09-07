// ---------------------------------------------------------------------------
// The timeline rail — numbered turn markers on a vertical axis, in the gutter
// immediately beside the transcript's right edge (29-turns.css derives that
// offset from the column's own cap, so the rail travels with the cards instead
// of sitting at the window's border).
//
// Three reasons it carries NUMBERS rather than being a decorative spine (an
// earlier left-hand spine was deleted, not kept alongside):
//
//  - Numbers are how people remember a session. "I talked about that around
//    turn 5" is a real memory; "it was about 60% of the way down" is not.
//  - It is a progress read-out. Session length becomes visible at a glance,
//    which a scrollbar over a virtualised, collapsing transcript cannot
//    honestly convey.
//  - A rail carrying numbers earns its column; a decorative one does not.
//
// IT SPANS THE WHOLE SESSION, WHICH IS WHY IT HAS ITS OWN FETCH. The transcript
// store is a paginated window — `maybeLoadMore` exists because older turns are
// not resident — so a rail assembled from resident turns would GROW markers as
// the reader scrolled up, which is precisely the progress read-out it claims to
// be. `GET /api/chats/{id}/turns` is the cheap session-wide index that makes the
// claim true, and the server owns turn numbering so both sides cannot disagree
// about what "turn 14" means.
//
// Time shows as space: a long pause between turns inserts a gap marker, so an
// overnight break is visible on the rail itself rather than being invisible in a
// message list that has no home for time.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { apiGet } from "./api-client.js";
import { jumpTo, scrollableBy, getScrollEl, onReaderGesture } from "./scroll.js";
import { clusterLabel, markerLabel, zoomOutLabel } from "./rail-labels.js";
import { severityOf } from "./turn-severity.js";
import type { TurnOutcome } from "./turns.js";
import { searchHitTurns } from "./chat-search.js";
import { get, syncEpoch } from "./store.js";

/** One row of the session-wide turn index. Mirrors vibekit.TurnSummary. */
export interface TurnSummary {
  id: string;
  first_line?: string;
  outcome: TurnOutcome;
  n: number;
  ts: number;
  agent_initiated?: boolean;
}

/** A pause longer than this earns a gap marker. Twenty minutes is the point at
 *  which a break stops being a pause in one sitting and starts being a seam
 *  between two: short enough to catch a lunch break, long enough that ordinary
 *  thinking time never trips it. */
const GAP_THRESHOLD_MS = 20 * 60 * 1000;

/** The minimum hit area for one marker, in CSS pixels. WCAG 2.5.8 asks for
 *  24x24 or equivalent spacing. */
const MARKER_MIN_PX = 24;

/** What one row actually COSTS: its hit area plus the gap below it (`--sp-1` in
 *  29-turns.css, which is also the axis's only visible stretch). This is the
 *  number the capacity arithmetic divides by, and it is not the same as the hit
 *  area — dividing by 24 over-counted every rail by one row per twelve, because
 *  the gap between markers was spent but never charged.
 *
 *  Exported so the tests measure rails in the unit production lays them out in,
 *  rather than keeping a second copy of the pitch that can drift from this one. */
export const ROW_PITCH_PX = MARKER_MIN_PX + 4;

/** Outcome severity as a total ORDER, worst first. A cluster reports its worst
 *  member, because a range containing one failure is a range you want to look at.
 *
 *  A RANK IS NOT A GRADE, which is why this cannot be derived from `severityOf`:
 *  that table's four buckets carry no order, and three outcomes share `broken`
 *  while two share `stopped`. So this stays, and it stays hand-written.
 *
 *  What it MAY NOT DO is contradict the hue partition, and it did: `interrupted`
 *  used to rank BELOW `unknown`, so a cluster holding an interrupted turn and an
 *  unknown one painted the neutral ink of an unreadable end while the interrupted
 *  turn's own marker painted red. A cluster must not read calmer than its worst
 *  member. Every `broken` outcome therefore outranks every `stopped` one here. */
const SEVERITY: Record<TurnOutcome, number> = {
  failed: 6,
  // A refusal is not a malfunction, so it ranks below `failed` — but it is a turn
  // that produced no work, so it outranks every state that did.
  refused: 5,
  // BROKEN, and above `unknown` for that reason: the hue partition paints it red,
  // so a cluster containing it may not fall through to a neutral range.
  interrupted: 4,
  // An end vibekit could not read is not a success, and it is the one state a
  // reader should look at BECAUSE nothing explains it — but it is graded `stopped`,
  // so it sits below the three broken outcomes.
  unknown: 3,
  cancelled: 2,
  running: 1,
  completed: 0,
};

/** A rendered rail row: either one turn, a range of turns, or a time gap. */
type Row =
  | { kind: "turn"; s: TurnSummary }
  | { kind: "cluster"; from: number; to: number; outcome: TurnOutcome; count: number }
  | { kind: "gap"; ms: number };

let root: HTMLElement | undefined;
let summaries: TurnSummary[] = [];
/** `summaries` indexed by the turn's opening-message id — `numberOf` resolves
 *  a card per observer delta, so the lookup must not be a linear scan of the
 *  index. Rebuilt wherever `summaries` is assigned. */
let summaryByID = new Map<string, TurnSummary>();

/** One chat's fetched index plus what the world looked like when the request
 *  went out: the sync epoch and the chat's message count, both captured BEFORE
 *  the fetch (the same discipline as loadMessages' epochAtStart — an answer
 *  that raced a gap or an append must not claim currency over it). */
interface RailRecord {
  summaries: TurnSummary[];
  epoch: number;
  atCount: number;
}

/** Session-wide indexes by chat, kept across switches so returning to a loaded
 *  chat paints its rail from memory instead of refetching. `refreshTurnRail`
 *  is the one writer; re-pointing prunes rows the store no longer holds. */
const records = new Map<string, RailRecord>();

/** Whether `id`'s record can stand in for a fetch: present, from the current
 *  sync epoch, and from the chat's current message count. The count is the
 *  cheap proxy for "a turn started or ended since" — background SSE ingest
 *  moves it while the rail is pointed elsewhere. */
function recordCurrent(id: string): boolean {
  const r = records.get(id);
  if (r === undefined) {
    return false;
  }
  return r.epoch === syncEpoch() && r.atCount === get(id)?.message_count;
}

let chatID = "";
let currentN = 0;
/** The turn the READER picked, held by its OPENING-MESSAGE ID, which outranks the
 *  scroll-derived `currentN` until they say otherwise.
 *
 *  THIS IS THE FIX FOR A CLICK THAT DID NOTHING. `currentN` had exactly one writer
 *  — the geometry pick — so clicking a marker for a turn already fully on screen
 *  produced no observable change anywhere: `scrollIntoView` is a no-op when the
 *  target is where the reader already is, so no scroll event fired, no intersection
 *  changed, no pick ran and no render happened. And because dominance is by visible
 *  PIXELS, with turns 2 and 3 both on screen the taller one keeps the mark, so the
 *  rail actively contradicted the reader's own choice.
 *
 *  BY ID, not by number, for the reason `visible` is keyed that way: the id is the
 *  turn's identity and the number is a derived value the index can restate. A
 *  rewind truncates the session and a server-side renumbering moves the numbers
 *  under a held pick, so a number-keyed pick either names a turn that no longer
 *  exists — leaving the rail with NO position marked on any row, since `render`
 *  withholds `data-current` while a pick stands — or, worse, silently names a
 *  different turn than the one clicked.
 *
 *  INVARIANT, enforced at both writers: this is only ever a turn the index carries.
 *  The click reads it off a summary, and `setSummaries` drops it when the new index
 *  no longer names it.
 *
 *  Two attributes rather than one, because they mean different things and the
 *  stylesheet has to be able to tell them apart: `data-current` stays the
 *  scroll-derived position, `data-selected` is the intent. `aria-current` moves to
 *  whichever is marked — exactly one element carries it, which is what that
 *  attribute means.
 *
 *  RECORDED CONSEQUENCE: while a selection is held the rail stops tracking a
 *  streaming turn. That is the point — the intent is supposed to win — and any
 *  reader gesture hands tracking straight back, a request for the live edge
 *  included. */
let selectedID: string | undefined;
/** The range the rail is zoomed into, set by clicking a cluster. */
let zoom: { from: number; to: number } | undefined;
let observer: IntersectionObserver | undefined;
/** The cards `observer` is currently watching, so a repaint can observe the
 *  ARRIVALS and unobserve the DEPARTURES instead of tearing the observer down.
 *  Must be cleared wherever `observer` is dropped: a `disconnect()` unobserves
 *  every target, so a stale set would make the next pass skip re-observing. */
let observed = new Set<Element>();
/** Turn numbers whose jump is waiting on a fetch, so the marker can say so. */
const pending = new Set<number>();
/** Mounted cards currently intersecting the transcript viewport, keyed by the
 *  card's OPENING-MESSAGE ID — its `data-reconcile-key`, the identity that never
 *  changes — with the absolute turn number resolved at pick time.
 *
 *  The map persists across observer callbacks because an IntersectionObserver
 *  callback is a DELTA, not a complete visible set. The element stays here so
 *  every scroll frame can measure exact visible height; storing the last observer
 *  ratio would go stale while two cards remain intersecting.
 *
 *  KEYED BY ID RATHER THAN BY NUMBER, and that is the fix for a marker stuck on
 *  the previous turn for a whole session. The number is not known until the
 *  session index carries the card's id, and the index is refetched at only three
 *  moments — turn end, chat activation, a transport gap — none of them turn
 *  START. So for the entire duration of a running turn the newest card resolves
 *  to no number, and `onIntersect` used to DISCARD every entry for it. Nothing
 *  recovered: the observer reports membership CHANGES only, `observeTurns` keeps
 *  the card in `observed` so it is never re-observed, and the pick re-measures
 *  only what is already in this map. The card entered it only by leaving the
 *  viewport and coming back, so on the common shape — the newest turn fully on
 *  screen and staying there — the rail marked the PREVIOUS turn active until the
 *  reader scrolled. Storing the element under a key that is knowable now and
 *  resolving the number per frame is the same discipline `pickDominant` already
 *  applies to geometry, for the same reason: a remembered derived value goes
 *  stale, and this one is derived from a fetch that has not landed yet. */
const visible = new Map<string, Element>();

/** Treat subpixel geometry as a tie so fractional layout cannot make the marker
 *  flicker between two otherwise equal cards. */
const VISIBILITY_TIE_PX = 1;

/** The scroll-coalesced dominant-turn measurement. */
let pickFrame = 0;

/** How far the transcript must be able to scroll before the rail appears.
 *
 *  The rail is a NAVIGATOR, so it has nothing to offer a conversation the reader
 *  can already see whole: on a one-turn chat it was a column of one digit beside
 *  a transcript with nowhere to go. A threshold rather than a bare `> 0` because
 *  a transcript overflowing by a few pixels is not one anybody navigates, and
 *  because a scrollable-by-2px transcript would otherwise flip the rail on and
 *  off as its own content settles.
 *
 *  This is the rail's POLICY and lives here; scroll.ts only measures. The number
 *  matching BOTTOM_TOLERANCE_PX is a coincidence of scale, not a shared decision
 *  — do not collapse the two. */
const MIN_SCROLL_PX = 100;

/** Whether there is enough transcript to navigate.
 *
 *  Read live at every render rather than cached from a paint, because the answer
 *  changes on window resize too and the rail's own ResizeObserver is what catches
 *  that — a flag written by the transcript's paint path would be stale exactly
 *  when the viewport is what moved. */
function navigable(): boolean {
  return scrollableBy() > MIN_SCROLL_PX;
}

/** The navigability the last render was built from, so a paint that flips it can
 *  re-render and the overwhelming majority that do not cost one comparison. */
let renderedNavigable = false;

/** The one writer of `summaries`, so the id index can never drift from it — and the
 *  one place the reader's pick is reconciled against that index, for the same
 *  reason: this is the moment the mapping moves.
 *
 *  A pick the new index does not carry is DROPPED rather than left to resolve to
 *  nothing. The producer is a rewind, which truncates the session from the turn
 *  footer two clicks away from a marker; without the drop the rail marks no
 *  position on any row — not a wrong one, none — because `rowNode` withholds
 *  `data-current` while a pick stands and matches `data-selected` on a turn that is
 *  gone. */
function setSummaries(next: TurnSummary[]): void {
  summaries = next;
  summaryByID = new Map(next.map((s) => [s.id, s]));
  if (selectedID !== undefined && !summaryByID.has(selectedID)) {
    selectedID = undefined;
  }
}

/** Mount the rail into the transcript's positioned outer wrapper. Idempotent. */
export function mountTurnRail(host: HTMLElement): void {
  if (root !== undefined) {
    return;
  }
  root = el("nav", {
    className: "turn-rail",
    "aria-label": "Turn timeline",
  });
  host.appendChild(root);
  // IntersectionObserver reports membership changes, not every scroll step.
  // Re-measure the members once per animation frame while the transcript moves
  // so two cards that remain intersecting can exchange dominance accurately.
  getScrollEl().addEventListener("scroll", schedulePick, { passive: true });
  // A READER GESTURE revokes the selection; nothing else does. That covers both
  // ways the reader states a position — a scroll, and a request for the live edge
  // (the resume control, End, and a turn they just sent, all of which scroll
  // through the controller and so produce no reader scroll event).
  //
  // Not `onReadingStateChange`: that fires on state TRANSITIONS, and a reader
  // scrolling within Following never transitions, so the gesture that should hand
  // tracking back would not. And deliberately not `pickDominant` noticing a
  // different dominant turn: a streaming turn's own growth moves dominance with no
  // gesture behind it at all, which would drop the selection while the reader sits
  // perfectly still.
  onReaderGesture(clearSelection);
  // The rail is responsive and the gap rows are data-dependent, so capacity has
  // to be measured rather than assumed. A resize also changes visible-height
  // geometry, so re-pick the current turn in the same callback.
  if (typeof ResizeObserver === "function") {
    new ResizeObserver(() => {
      render();
      schedulePick();
    }).observe(root);
  }
}

/** Hand the rail to a chat, dropping the previous session's view state.
 *
 *  Separate from the fetch because an EMPTY chat has to re-point too, and it has
 *  nothing to fetch: its turn count is zero by definition, and a brand-new chat's
 *  id exists nowhere but the tab that minted it, so asking for its turns is a
 *  guaranteed 404. Both halves of skipping this were live defects. A rail still
 *  pointing at the chat before it kept rendering THAT chat's markers over a
 *  conversation with no messages in it — a timeline before the first message. And
 *  `refreshTurnRail` drops a result whose id is not the rail's, so the first
 *  `turn_ended` of a chat the rail had never been handed was discarded, and no
 *  marker appeared until the reader switched away and back.
 *
 *  The index itself is NOT view state: the chat's record paints immediately,
 *  which is what makes a switch back to a loaded chat cost zero fetches. Whether
 *  the record is also CURRENT is `loadTurnRail`'s question, not this one's. */
export function pointTurnRail(id: string): void {
  if (id === chatID) {
    return;
  }
  // A different chat: drop the previous session's zoom, selection and pending
  // jumps rather than carrying a stale range onto unrelated turns.
  zoom = undefined;
  selectedID = undefined;
  pending.clear();
  // Records for chats the store no longer holds are dead weight (closed tabs,
  // deleted chats), and a re-point is the cheap moment to drop them — including
  // the target's own, so a purged chat renders empty rather than from memory.
  for (const key of records.keys()) {
    if (get(key) === undefined) {
      records.delete(key);
    }
  }
  setSummaries(records.get(id)?.summaries ?? []);
  currentN = 0;
  // A turn number from the previous chat is a live wrong answer, not merely a
  // stale one: `numberOf` resolves against THIS chat's summaries, so a leftover
  // member would name one of the new chat's turns current.
  visible.clear();
  chatID = id;
  render();
}

/** The activation entry: point the rail at the chat, then fetch its index only
 *  when the chat's record cannot stand in for one. `force` skips that gate —
 *  the caller activating a stale transcript (gap, eviction, first load) knows
 *  the rail is implicated with it, and by the time the messages heal lands the
 *  session reads fresh again, so the verdict cannot be re-derived here. */
export async function loadTurnRail(id: string, opts?: { force?: boolean }): Promise<void> {
  pointTurnRail(id);
  if (opts?.force !== true && recordCurrent(id)) {
    return;
  }
  await refreshTurnRail(id);
}

/** Re-fetch the index. Called on load and after a turn ends, which is the only
 *  time the set of turns changes. */
export async function refreshTurnRail(id: string): Promise<void> {
  if (id === "") {
    return;
  }
  // Both captured BEFORE the request — see RailRecord. A count the store does
  // not know (the chat was removed, or was never seeded) records nothing: there
  // is no session left to activate against, and pruning would drop the row.
  const epochAtStart = syncEpoch();
  const countAtStart = get(id)?.message_count;
  const d = await apiGet<{ turns?: TurnSummary[] }>(`/api/chats/${encodeURIComponent(id)}/turns`);
  if (d === null) {
    // A null is a failed fetch, already logged centrally. Keep whatever the rail
    // is showing — a rail that empties itself on a transient failure is worse
    // than one that is briefly a turn behind — and keep the stale record, so the
    // next activation retries instead of trusting it.
    return;
  }
  const turns = d.turns ?? [];
  if (countAtStart !== undefined) {
    records.set(id, { summaries: turns, epoch: epochAtStart, atCount: countAtStart });
  }
  if (id !== chatID) {
    // A background chat's index (a turn ended while the rail points elsewhere):
    // recorded above so its next activation paints from memory, not painted now.
    return;
  }
  setSummaries(turns);
  render();
  // AFTER the render, because the pick renders again only when it moves the
  // marker. The index is what turns an already-visible card into a placeable one,
  // and no other trigger is coming: an IntersectionObserver reports membership
  // CHANGES, and a card sitting still on screen has none. Without this the
  // just-ended turn's marker waits for a scroll frame that may never arrive.
  pickDominant();
}

export function resetTurnRail(): void {
  chatID = "";
  setSummaries([]);
  records.clear();
  currentN = 0;
  selectedID = undefined;
  zoom = undefined;
  renderedNavigable = false;
  pending.clear();
  visible.clear();
  clearRailTarget();
  if (pickFrame !== 0) {
    cancelAnimationFrame(pickFrame);
    pickFrame = 0;
  }
  observer?.disconnect();
  observer = undefined;
  // `disconnect` unobserves every target, so the set has to go with it or the
  // next pass would treat those cards as already observed and skip them.
  observed.clear();
  render();
}

/** Observe the mounted turn cards so the rail knows which turn is in view.
 *
 *  Called after EVERY transcript paint, which is what makes the cost shape the
 *  point. This used to `disconnect()` and construct a brand-new
 *  IntersectionObserver over every card on every call, while claiming to be
 *  "cheap because it re-observes the same elements rather than rebuilding
 *  anything" — the opposite of what it did. Nothing about the observer varies
 *  between paints; only the target set does. So the observer is built once and
 *  this diffs the set, observing arrivals and unobserving departures.
 *
 *  Dropping the rebuild is what forces the departure handling below: a fresh
 *  observer reported every target it was given, so clearing `visible` each pass
 *  was safe and the callbacks refilled it. A persistent observer reports nothing
 *  for a target it already watches, so that clear would empty the set with no
 *  callback coming to refill it, and the next partial report — which is what a
 *  small scroll produces — would be the whole set and name the arrival.
 *
 *  THE ACTIVE TURN IS THE DOMINANT ONE ON SCREEN: the card occupying the most
 *  vertical pixels of the transcript viewport. A sliver never beats a full
 *  card in either direction. A fully visible footer breaks an equal-height tie,
 *  then the later turn does. Geometry is measured from the live card and
 *  scroller on each scroll frame rather than remembered from observer entries,
 *  because two cards can remain intersecting while their visible heights trade.
 *
 *  The intersecting map is KEPT across callbacks rather than re-derived from one,
 *  because the entries list is a DELTA: it carries only the cards whose
 *  intersection state changed. A small scroll can alter one entry while every
 *  incumbent remains absent from the callback; dropping them would turn one
 *  partial notification into the whole visible set. */
export function observeTurns(cards: Iterable<HTMLElement>): void {
  // Content just changed, so the transcript may have crossed the navigable
  // threshold in either direction. The IntersectionObserver below cannot cover
  // this: it renders only when the turn IN VIEW changes, and a streaming turn
  // grows the transcript past the threshold without ever changing that — so the
  // rail would stay hidden until the reader happened to scroll.
  if (navigable() !== renderedNavigable) {
    render();
  }
  if (typeof IntersectionObserver !== "function") {
    return;
  }
  observer ??= new IntersectionObserver(onIntersect, { threshold: 0 });
  const next = new Set<Element>(cards);
  let dropped = false;
  for (const c of observed) {
    if (next.has(c)) {
      continue;
    }
    observer.unobserve(c);
    dropped = true;
    // `unobserve` fires NO callback, so a departed card would stay in the
    // geometry map forever and could keep winning after its DOM left. Delete it
    // here; the chat-level `visible.clear()` sites cover every index reset.
    //
    // By KEY, for the reason the map is keyed that way: deleting by resolved
    // number silently deleted key 0 for any card the index does not name yet,
    // leaving the real entry in the map to keep winning from a detached node.
    const key = keyOf(c);
    if (key !== "") {
      visible.delete(key);
    }
  }
  for (const c of next) {
    if (!observed.has(c)) {
      observer.observe(c);
    }
  }
  observed = next;
  if (dropped) {
    // A departure changed the map with no callback behind it, so the pick has to
    // be re-run here or the marker stays on a turn that is no longer mounted.
    pickDominant();
  }
  // A paint can change card heights without changing intersection membership
  // (fold, tool output, streaming text), so remeasure on the next frame too.
  schedulePick();
}

/** Fold one DELTA of intersection changes into `visible`, then re-pick.
 *  Module scope so the observer is constructed once.
 *
 *  UNCONDITIONAL: a card whose id the index does not carry yet is recorded all the
 *  same, because this callback is the only notification it will ever get and the
 *  number it is missing arrives later on a different channel. See `visible`. */
function onIntersect(entries: IntersectionObserverEntry[]): void {
  for (const e of entries) {
    const key = keyOf(e.target);
    if (key === "") {
      // No reconcile key at all: not a turn card, so there is nothing to place it
      // by, now or later.
      continue;
    }
    if (e.isIntersecting) {
      visible.set(key, e.target);
    } else {
      visible.delete(key);
    }
  }
  pickDominant();
}

/** Coalesce transcript scroll events into one geometry read per frame. */
function schedulePick(): void {
  if (pickFrame !== 0) {
    return;
  }
  pickFrame = requestAnimationFrame(() => {
    pickFrame = 0;
    pickDominant();
  });
}

/** Name the turn occupying the most vertical pixels of the transcript viewport.
 *
 *  Absolute visible height is the stable answer for both reported failure
 *  directions: an older sliver cannot beat a full lower turn, and a newer
 *  sliver cannot win merely because its number is larger. If heights tie within
 *  one CSS pixel, a fully visible footer wins; if that ties too, the later turn
 *  wins so a viewport of several short cards settles on its last one.
 *
 *  An EMPTY map leaves `currentN` alone rather than clearing it: between a
 *  departure and the next observer callback nothing is known to be on screen,
 *  and clearing the marker there would blink it off during ordinary repaint. */
function pickDominant(): void {
  const viewportRect = getScrollEl().getBoundingClientRect();
  const viewportTop = viewportRect.height > 0 ? viewportRect.top : 0;
  const viewportBottom = viewportRect.height > 0 ? viewportRect.bottom : window.innerHeight;
  let bestN = 0;
  let bestPixels = -1;
  let bestFooter = false;

  for (const [key, card] of visible) {
    // Resolved per frame, never remembered: the card was recorded before the
    // session index knew its number, so an unresolvable candidate is SKIPPED here
    // and stays in the map, ready for the frame after the index lands.
    const n = summaryByID.get(key)?.n ?? 0;
    if (n === 0) {
      continue;
    }
    const rect = card.getBoundingClientRect();
    const pixels = Math.max(
      0,
      Math.min(rect.bottom, viewportBottom) - Math.max(rect.top, viewportTop),
    );
    if (pixels <= 0) {
      continue;
    }
    const footer = footerFullyVisible(card, viewportTop, viewportBottom);
    const clearlyLarger = pixels > bestPixels + VISIBILITY_TIE_PX;
    const tied = Math.abs(pixels - bestPixels) <= VISIBILITY_TIE_PX;
    const winsTie = tied && (footer !== bestFooter ? footer : n > bestN);
    if (clearlyLarger || winsTie) {
      bestN = n;
      bestPixels = pixels;
      bestFooter = footer;
    }
  }

  if (bestN !== 0 && bestN !== currentN) {
    currentN = bestN;
    render();
  }
}

function footerFullyVisible(card: Element, viewportTop: number, viewportBottom: number): boolean {
  const footer = card.querySelector<HTMLElement>(":scope > .turn-footer");
  if (footer === null) {
    return false;
  }
  const rect = footer.getBoundingClientRect();
  return (
    rect.height > 0 &&
    rect.top >= viewportTop - VISIBILITY_TIE_PX &&
    rect.bottom <= viewportBottom + VISIBILITY_TIE_PX
  );
}

/** A card's stable identity: the id of the turn's OPENING MESSAGE, which is both
 *  the transcript's reconcile key (`messages.ts` `turnSpec.key`) and the server
 *  index's `TurnSummary.id`. That shared value is the one join between the rail's
 *  session-absolute numbering and the transcript's window-local numbering, and it
 *  is used in BOTH directions: here to place a visible card on the rail, and in
 *  `turnCard` to find the card a marker jumps to.
 *
 *  "" for an element carrying no key, which is not a turn card. */
function keyOf(card: Element): string {
  return card.getAttribute("data-reconcile-key") ?? "";
}

// ---------------------------------------------------------------------------
// Layout
// ---------------------------------------------------------------------------

/** Build the rows for the current state.
 *
 *  Exported for tests: the capacity arithmetic is the part most likely to be got
 *  wrong, and driving it through the DOM would mean asserting on pixels. */
export function railRows(
  all: TurnSummary[],
  railHeightPx: number,
  range?: { from: number; to: number },
): Row[] {
  const turns = range === undefined ? all : all.filter((s) => s.n >= range.from && s.n <= range.to);
  if (turns.length === 0) {
    return [];
  }
  const gaps = countGaps(turns);
  // The threshold is COMPUTED, never a constant: the rail's height is responsive
  // and the gap rows are data-dependent, so any hardcoded turn count would be
  // wrong at some viewport. A rail of 900px fits 32 conforming markers, fewer
  // once the gaps below take their rows.
  const capacity = Math.max(1, Math.floor(railHeightPx / ROW_PITCH_PX) - gaps);
  if (turns.length <= capacity) {
    return withGaps(turns);
  }
  return cluster(turns, capacity);
}

function countGaps(turns: TurnSummary[]): number {
  let n = 0;
  for (let i = 1; i < turns.length; i++) {
    const prev = turns[i - 1];
    const cur = turns[i];
    if (prev !== undefined && cur !== undefined && cur.ts - prev.ts > GAP_THRESHOLD_MS) {
      n++;
    }
  }
  return n;
}

/** Interleave gap markers between turns far apart in real time. */
function withGaps(turns: TurnSummary[]): Row[] {
  const rows: Row[] = [];
  for (let i = 0; i < turns.length; i++) {
    const cur = turns[i];
    if (cur === undefined) {
      continue;
    }
    const prev = turns[i - 1];
    if (prev !== undefined) {
      const delta = cur.ts - prev.ts;
      if (delta > GAP_THRESHOLD_MS) {
        rows.push({ kind: "gap", ms: delta });
      }
    }
    rows.push({ kind: "turn", s: cur });
  }
  return rows;
}

/** Compress turns into at most `capacity` range markers.
 *
 *  Geometric compression, not typographic. An earlier draft promised "every
 *  marker still exists and is still clickable" while labelling only every fifth
 *  number — which cannot hold: at 300 turns in a 900px rail each marker owns
 *  3px, so a per-turn hit area is a false promise however many dots are painted.
 *  Direct per-turn targets survive exactly as long as they fit, and beyond that
 *  a cluster shows its range and its worst outcome, and zooms the rail when
 *  clicked. */
function cluster(turns: TurnSummary[], capacity: number): Row[] {
  const per = Math.ceil(turns.length / capacity);
  const rows: Row[] = [];
  for (let i = 0; i < turns.length; i += per) {
    const chunk = turns.slice(i, i + per);
    const first = chunk[0];
    const last = chunk[chunk.length - 1];
    if (first === undefined || last === undefined) {
      continue;
    }
    let worst: TurnOutcome = "completed";
    for (const s of chunk) {
      if (SEVERITY[s.outcome] > SEVERITY[worst]) {
        worst = s.outcome;
      }
    }
    rows.push({ kind: "cluster", from: first.n, to: last.n, outcome: worst, count: chunk.length });
  }
  return rows;
}

// ---------------------------------------------------------------------------
// Render
// ---------------------------------------------------------------------------

function render(): void {
  if (root === undefined) {
    return;
  }
  root.dataset["zoomed"] = zoom === undefined ? "" : "on";
  renderedNavigable = navigable();
  // No rows means no rail: `.turn-rail:empty` hides the element, which takes the
  // axis line with it, so an unnavigable transcript needs no second mechanism.
  if (summaries.length === 0 || !renderedNavigable) {
    root.replaceChildren();
    return;
  }
  const rows = railRows(summaries, root.clientHeight || fallbackHeight(), zoom);
  const nodes: HTMLElement[] = [];
  if (zoom !== undefined) {
    nodes.push(zoomOutButton(zoom));
  }
  for (const row of rows) {
    nodes.push(rowNode(row));
  }
  root.replaceChildren(...nodes);
}

/** A height to reason about before the rail has been laid out (first paint, and
 *  every environment without layout). Deliberately generous: over-estimating
 *  capacity renders per-turn markers that may not all fit, which the next
 *  measured render corrects, while under-estimating would cluster a short
 *  session that never needed it. */
function fallbackHeight(): number {
  return 600;
}

function rowNode(row: Row): HTMLElement {
  if (row.kind === "gap") {
    return el("div", { className: "rail-gap", "aria-hidden": "true" }, formatGap(row.ms));
  }
  if (row.kind === "cluster") {
    // A cluster holding the reader's position is marked too. Past capacity —
    // roughly 32 rows on a 900px rail — EVERY turn is inside a cluster, so
    // without this the rail showed no current position at all on a long session,
    // and the same held for any zoom range excluding `currentN`.
    const containsCurrent = markedN() >= row.from && markedN() <= row.to;
    const label = clusterLabel(row, { containsCurrent });
    const btn = el(
      "button",
      {
        className: "rail-cluster",
        type: "button",
        "data-tooltip": label.tooltip,
        "aria-label": label.ariaLabel,
      },
      `${String(row.from)}\u2013${String(row.to)}`,
    );
    btn.dataset["outcome"] = row.outcome;
    btn.dataset["severity"] = severityOf(row.outcome);
    if (containsCurrent) {
      btn.dataset["current"] = "";
    }
    btn.addEventListener("click", () => {
      zoom = { from: row.from, to: row.to };
      // A range is not a turn, so zooming into one cannot stand as a pick of any
      // turn inside it.
      selectedID = undefined;
      render();
    });
    return btn;
  }
  const s = row.s;
  const hit = searchHitTurns().has(s.n);
  const isPending = pending.has(s.n);
  // ONE composer for both channels, and NO native `title`: a UA tooltip misses the
  // styled `.uip-tooltip` treatment every other hover in the app uses, and it
  // publishes no `aria-describedby`, so it reached mouse users only.
  const label = markerLabel(s, { pending: isPending, hit });
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
  btn.dataset["outcome"] = s.outcome;
  btn.dataset["severity"] = severityOf(s.outcome);
  // EXACTLY ONE of the two, on exactly one marker: the rail claims ONE position, so
  // the scroll-derived mark is withheld while the reader holds a pick. Writing both
  // would paint two filled markers, and demoting the loser in CSS needed three
  // attribute selectors and a specificity this stylesheet's own ceilings refuse.
  // They stay separate attributes because they answer different questions — where
  // the scroll puts you, versus which turn you chose.
  if (selectedID === undefined) {
    if (s.n === currentN) {
      btn.dataset["current"] = "";
    }
  } else if (s.id === selectedID) {
    btn.dataset["selected"] = "";
  }
  // One element carries it, and it names the turn the rail is CLAIMING — the
  // reader's pick when they have made one, the dominant turn otherwise.
  if (s.n === markedN()) {
    btn.setAttribute("aria-current", "true");
  }
  if (s.agent_initiated === true) {
    btn.dataset["trigger"] = "system";
  }
  if (isPending) {
    btn.dataset["pending"] = "";
  }
  // A search hit marks the rail, which is the fastest possible read of WHERE in
  // the session the answer lives — a match in a turn 200 rows up is visible
  // before the reader goes looking for it.
  if (hit) {
    btn.dataset["hit"] = "";
  }
  btn.addEventListener("click", () => {
    // BEFORE the jump and unconditionally, which is the whole point: the jump is
    // allowed to do nothing, and the click still has to produce a reaction. The id
    // comes off the summary this row was built from, which is what makes the pick's
    // invariant hold at this writer: it is in the index by construction.
    selectedID = s.id;
    render();
    void jumpToTurn(s);
  });
  return btn;
}

/** The turn the rail claims the reader is at: their own pick while they hold one,
 *  the dominant turn otherwise.
 *
 *  The pick's number is RESOLVED rather than remembered, the same discipline
 *  `pickDominant` applies, so a renumbering moves the mark with the turn instead of
 *  leaving it on whatever now wears that number. The fallback is unreachable while
 *  the pick's invariant holds (`setSummaries` drops a pick the index has lost); it
 *  degrades to the scroll-derived position rather than to no position at all, which
 *  is the failure this whole shape exists to prevent. */
function markedN(): number {
  if (selectedID === undefined) {
    return currentN;
  }
  return summaryByID.get(selectedID)?.n ?? currentN;
}

/** Drop the reader's pick and repaint, if there was one to drop. */
function clearSelection(): void {
  if (selectedID === undefined) {
    return;
  }
  selectedID = undefined;
  render();
}

/** The row that leaves a zoomed range. Takes the range as a parameter rather than
 *  reading `zoom`, so the one caller's own narrowing is what proves a range exists —
 *  this row is rendered only while the rail is zoomed. */
function zoomOutButton(range: { from: number; to: number }): HTMLElement {
  const label = zoomOutLabel(range);
  const btn = el(
    "button",
    {
      className: "rail-zoom-out",
      type: "button",
      "data-tooltip": label.tooltip,
      "aria-label": label.ariaLabel,
    },
    "all",
  );
  btn.addEventListener("click", () => {
    zoom = undefined;
    render();
  });
  return btn;
}

/** `2 hours later`. Coarse on purpose — the point is that a seam exists, not
 *  how many minutes it was. */
function formatGap(ms: number): string {
  const days = Math.floor(ms / 86_400_000);
  if (days >= 1) {
    return `${String(days)}d`;
  }
  const hours = Math.floor(ms / 3_600_000);
  if (hours >= 1) {
    return `${String(hours)}h`;
  }
  return `${String(Math.floor(ms / 60_000))}m`;
}

// ---------------------------------------------------------------------------
// Navigation
// ---------------------------------------------------------------------------

/** The on-demand body build for a stub turn, injected by messages.ts at mount
 *  (a static import back would cycle: messages.ts imports this module to mount
 *  the rail). Inert until wired, so a rail built in a test renders without the
 *  transcript. `activeView` scopes the anchor lookup to the ACTIVE transcript
 *  view: turn anchors are `#turn-{n}`, and with parked views resident the same
 *  id exists once per view, so a document-wide lookup could land on a hidden
 *  card. */
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

/** Jump to a turn, fetching it first when it is not resident.
 *
 *  The store holds a window, so a marker for an older turn has nothing to scroll
 *  to yet. The marker shows a pending state while the pages load rather than
 *  silently doing nothing, which is what a rail over a paginated store does if
 *  nobody thinks about it.
 *
 *  Either way the landing turn may be a tier-3 STUB (pagination lands stubs,
 *  and an old resident turn unmounts past the warm window), so the jump runs
 *  the same on-demand body build the fold toggle uses. AFTER the scroll: the
 *  build happens under a folded card, so it changes no height the jump could
 *  care about, and the jump itself stays instant. The turn is NOT opened —
 *  a jump onto a resident turn today lands on its folded row, and this keeps
 *  that exactly.
 *
 *  THE TARGET IS RESOLVED BY MESSAGE ID, NOT BY `#turn-{n}`, and that is the fix
 *  for a click landing on the wrong turn. There are two numbering spaces both
 *  spelled `turn-{n}`: `TurnSummary.n` is SESSION-ABSOLUTE (the server owns it —
 *  `internal/vibekit/turns.go`) while a card's `id` is WINDOW-LOCAL (`Turn.n`, an
 *  ordinal within the paginated store — `turns.ts`). So addressing the card by the
 *  marker's number landed on the card whose WINDOW ordinal matched, missing by
 *  exactly the number of turns paged out — zero on a short chat and growing with
 *  every page loaded, which is why it read as intermittent. It also swallowed the
 *  fetch: a wrong-but-resident card resolved, so an off-window turn scrolled to a
 *  neighbour instead of paging history in, and the pending marker never appeared
 *  for the one case it exists for.
 *
 *  `keyOf` already joined the two spaces in the other direction. This is the same
 *  join, so the rail now has ONE mapping between them and runs it both ways. */
async function jumpToTurn(s: TurnSummary): Promise<void> {
  const resident = turnCard(s.id);
  if (resident !== null) {
    scrollToCard(resident);
    markRailTarget(resident);
    await mountTurnBody(chatID, s.id);
    return;
  }
  if (pending.has(s.n)) {
    return;
  }
  pending.add(s.n);
  render();
  try {
    const loaded = await loadUntilResident(s);
    if (loaded) {
      // One frame for the appended cards to lay out before scrolling to one.
      await nextFrame();
      // Re-resolved rather than remembered: the card did not exist when this
      // jump started, and pagination is what created it.
      const landed = turnCard(s.id);
      if (landed !== null) {
        scrollToCard(landed);
        markRailTarget(landed);
      }
      await mountTurnBody(chatID, s.id);
    }
  } finally {
    pending.delete(s.n);
    render();
  }
}

/** Page backwards until the target turn's opening message is in the store.
 *  Bounded: a session with 400 turns is 8 pages of 50, and the loop stops the
 *  moment the store reports no more history, so a target that can never arrive
 *  terminates instead of spinning. */
async function loadUntilResident(s: TurnSummary): Promise<boolean> {
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
    const oldest = session.messages[0];
    if (oldest === undefined) {
      return false;
    }
    await loadMessages(chatID, oldest.id);
    const after = getActive();
    // No progress means another page cannot help; stop rather than loop.
    if (after === undefined || after.messages[0]?.id === oldest.id) {
      return false;
    }
  }
}

/** The mounted card for the turn whose opening message is `id`, or null when it is
 *  not resident.
 *
 *  Scoped to the ACTIVE transcript view, because `data-reconcile-key` repeats once
 *  per resident view under the multiplexer and a document-wide query answers in
 *  document order — which can be a PARKED view's card. The document fallback keeps
 *  the rail fixtures, and the pre-multiplexer boot instant, working unscoped. */
function turnCard(id: string): HTMLElement | null {
  if (id === "") {
    return null;
  }
  const selector = `[data-reconcile-key="${CSS.escape(id)}"]`;
  const root = activeView();
  return (root ?? document).querySelector<HTMLElement>(selector);
}

/** How long the landing card wears its ring. Long enough to be seen after an
 *  instant scroll, short enough not to read as a persistent selected state — the
 *  rail's own marker is what carries that. */
const RAIL_TARGET_MS = 1000;

/** The card currently wearing `data-rail-target`, and the timer that removes it.
 *  Module-level and single-slot: a second click has to reset the first's timer, or
 *  the earlier deadline would strip the ring off the card the reader just landed
 *  on. */
let railTarget: HTMLElement | undefined;
let railTargetTimer = 0;

/** Flash the ring on the card a jump landed on.
 *
 *  A click on a turn already fully on screen scrolls nowhere, so the marker's own
 *  `data-selected` is the only thing that moves — and a reader watching the
 *  TRANSCRIPT rather than the rail would see nothing at all. The ring is what
 *  answers "which one did I just pick" on the surface they are reading.
 *
 *  `outline`/`box-shadow` only in the stylesheet, never `border` or `padding`: this
 *  fires on a card mid-transcript and must shift no layout. */
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

function scrollToCard(target: HTMLElement): void {
  // The scroll module owns both halves: it parks the reader (so a streaming turn
  // cannot yank the view back down) and it decides whether this jump moves them
  // off the live edge at all. A one-turn chat that does not overflow cannot
  // scroll, and claiming otherwise raised the `Latest` control over a transcript
  // that had not moved.
  //
  // INSTANT, and deliberately not find-in-chat's `smooth`. A smooth scroll freezes
  // its target at flight start, so the fold batch a landing releases never moves
  // it; and its ~50 intermediate events carry no self-scroll marker, so scroll.ts
  // reads every one as a reader gesture — which arms the user-scroll debounce, re-
  // derives the reading state, and (since item 5) revokes the selection the click
  // just made. That is the same mechanism behind the measured `2600 against a real
  // maximum of 4600` the resume control already fixed by going instant.
  jumpTo(target, { block: "start", behavior: "instant" });
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
