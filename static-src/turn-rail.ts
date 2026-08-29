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
import { jumpTo, scrollableBy } from "./scroll.js";
import { turnAnchorID, type TurnOutcome } from "./turns.js";
import { searchHitTurns } from "./chat-search.js";

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

/** Outcome severity, worst first. A cluster reports its worst member, because a
 *  range containing one failure is a range you want to look at. */
const SEVERITY: Record<TurnOutcome, number> = {
  failed: 6,
  // A refusal is not a malfunction, so it ranks below `failed` — but it is a turn
  // that produced no work, so it outranks every state that did.
  refused: 5,
  interrupted: 4,
  // An end vibekit could not read is not a success, and it is the one state a
  // reader should look at BECAUSE nothing explains it.
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
let chatID = "";
let currentN = 0;
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
/** The turn numbers currently intersecting the viewport. STATE rather than a
 *  per-callback derivation, because an IntersectionObserver callback carries only
 *  the cards whose intersection CHANGED — see `observeTurns`. */
const visible = new Set<number>();

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
  // The rail is responsive and the gap rows are data-dependent, so capacity has
  // to be measured rather than assumed. Re-measure on resize.
  if (typeof ResizeObserver === "function") {
    new ResizeObserver(() => {
      render();
    }).observe(root);
  }
}

/** Hand the rail to a chat, dropping the previous session's index.
 *
 *  Separate from the fetch because an EMPTY chat has to re-point too, and it has
 *  nothing to fetch: its turn count is zero by definition, and a brand-new chat's
 *  id exists nowhere but the tab that minted it, so asking for its turns is a
 *  guaranteed 404. Both halves of skipping this were live defects. A rail still
 *  pointing at the chat before it kept rendering THAT chat's markers over a
 *  conversation with no messages in it — a timeline before the first message. And
 *  `refreshTurnRail` drops a result whose id is not the rail's, so the first
 *  `turn_ended` of a chat the rail had never been handed was discarded, and no
 *  marker appeared until the reader switched away and back. */
export function pointTurnRail(id: string): void {
  if (id === chatID) {
    return;
  }
  // A different chat: drop the previous session's zoom and pending jumps rather
  // than carrying a stale range onto unrelated turns.
  zoom = undefined;
  pending.clear();
  summaries = [];
  currentN = 0;
  // A turn number from the previous chat is a live wrong answer, not merely a
  // stale one: `numberOf` resolves against THIS chat's summaries, so a leftover
  // member would name one of the new chat's turns current.
  visible.clear();
  chatID = id;
  render();
}

/** Point the rail at a chat and (re)load its session-wide index. */
export async function loadTurnRail(id: string): Promise<void> {
  pointTurnRail(id);
  await refreshTurnRail(id);
}

/** Re-fetch the index. Called on load and after a turn ends, which is the only
 *  time the set of turns changes. */
export async function refreshTurnRail(id: string): Promise<void> {
  if (id === "") {
    return;
  }
  const d = await apiGet<{ turns?: TurnSummary[] }>(`/api/chats/${encodeURIComponent(id)}/turns`);
  if (d === null || id !== chatID) {
    // A null is a failed fetch, already logged centrally. Keep whatever the rail
    // is showing: a rail that empties itself on a transient failure is worse
    // than one that is briefly a turn behind.
    return;
  }
  summaries = d.turns ?? [];
  render();
}

export function resetTurnRail(): void {
  chatID = "";
  summaries = [];
  currentN = 0;
  zoom = undefined;
  renderedNavigable = false;
  pending.clear();
  visible.clear();
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
 *  THE ACTIVE TURN IS THE LATEST ONE ON SCREEN — the greatest turn number among
 *  the cards intersecting the viewport. A turn half off the top with a whole turn
 *  below it is the turn the reader has LEFT, so naming it current marks a turn
 *  they have already scrolled past. Position is not consulted at all:
 *  `boundingClientRect.top` was only ever a proxy for order, and `n` is the
 *  order, resolved through the server's own index.
 *
 *  The intersecting set is KEPT across callbacks rather than re-derived from one,
 *  because the entries list is a DELTA: it carries only the cards whose
 *  intersection state changed. A small scroll that brings a turn in above one
 *  already on screen reports the arrival alone, so picking from that callback's
 *  entries — by any rule — would name the higher card. */
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
    // `unobserve` fires NO callback, so a departed card's number would sit in
    // `visible` forever and keep `latest` naming a turn that is no longer
    // mounted. Resolved here rather than remembered per card because the
    // alternative is a number per observed card recomputed every paint, and
    // `numberOf` is a linear scan of the index — that trade is what makes this
    // O(departures) instead of O(turns) per paint. Residual: if the index moved
    // between the observe and the departure the delete misses, which the two
    // chat-level `visible.clear()` sites cover for every case that matters.
    visible.delete(numberOf(c));
  }
  for (const c of next) {
    if (!observed.has(c)) {
      observer.observe(c);
    }
  }
  observed = next;
  if (dropped) {
    // A departure changed the set with no callback behind it, so the pick has to
    // be re-run here or the marker stays on a turn that is no longer mounted.
    pickLatest();
  }
}

/** Fold one DELTA of intersection changes into `visible`, then re-pick.
 *  Module scope so the observer is constructed once. */
function onIntersect(entries: IntersectionObserverEntry[]): void {
  for (const e of entries) {
    const n = numberOf(e.target);
    if (n === 0) {
      // A card whose id is not in the index yet; it cannot be placed.
      continue;
    }
    if (e.isIntersecting) {
      visible.add(n);
    } else {
      visible.delete(n);
    }
  }
  pickLatest();
}

/** Name the latest turn on screen. Shared by the observer callback and the
 *  departure path, because both change `visible` and the rule must not fork.
 *
 *  An EMPTY set leaves `currentN` alone rather than clearing it: between a
 *  departure and the next callback nothing is known to be on screen, and dropping
 *  the marker there would blink it off on every repaint that removes a turn. */
function pickLatest(): void {
  let latest = 0;
  for (const n of visible) {
    if (n > latest) {
      latest = n;
    }
  }
  if (latest !== 0 && latest !== currentN) {
    currentN = latest;
    render();
  }
}

/** A card's absolute turn number, resolved through the server's index so the
 *  rail and the transcript agree even though the store holds only a window. */
function numberOf(card: Element): number {
  const id = card.getAttribute("data-reconcile-key");
  if (id === null) {
    return 0;
  }
  return summaries.find((s) => s.id === id)?.n ?? 0;
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
    nodes.push(zoomOutButton());
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
    const btn = el(
      "button",
      {
        className: "rail-cluster",
        type: "button",
        title: `Turns ${String(row.from)}\u2013${String(row.to)} (${String(row.count)})`,
        "aria-label": `Zoom to turns ${String(row.from)} to ${String(row.to)}`,
      },
      `${String(row.from)}\u2013${String(row.to)}`,
    );
    btn.dataset["outcome"] = row.outcome;
    btn.addEventListener("click", () => {
      zoom = { from: row.from, to: row.to };
      render();
    });
    return btn;
  }
  const s = row.s;
  const btn = el(
    "button",
    {
      className: "rail-marker",
      type: "button",
      // Hover shows the first line of the request — the "I talked about this
      // around turn 5" affordance made real.
      title:
        s.first_line !== undefined && s.first_line !== "" ? s.first_line : `Turn ${String(s.n)}`,
      "aria-label": `Go to turn ${String(s.n)}`,
    },
    String(s.n),
  );
  btn.dataset["outcome"] = s.outcome;
  if (s.n === currentN) {
    btn.dataset["current"] = "";
    btn.setAttribute("aria-current", "true");
  }
  if (s.agent_initiated === true) {
    btn.dataset["trigger"] = "system";
  }
  if (pending.has(s.n)) {
    btn.dataset["pending"] = "";
  }
  // A search hit marks the rail, which is the fastest possible read of WHERE in
  // the session the answer lives — a match in a turn 200 rows up is visible
  // before the reader goes looking for it.
  if (searchHitTurns().has(s.n)) {
    btn.dataset["hit"] = "";
  }
  btn.addEventListener("click", () => {
    void jumpToTurn(s);
  });
  return btn;
}

function zoomOutButton(): HTMLElement {
  const btn = el(
    "button",
    { className: "rail-zoom-out", type: "button", "aria-label": "Show the whole session" },
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

/** Jump to a turn, fetching it first when it is not resident.
 *
 *  The store holds a window, so a marker for an older turn has nothing to scroll
 *  to yet. The marker shows a pending state while the pages load rather than
 *  silently doing nothing, which is what a rail over a paginated store does if
 *  nobody thinks about it. */
async function jumpToTurn(s: TurnSummary): Promise<void> {
  // A turn permalink is addressable, so a ledger row, a run's launch record and
  // a search hit can all link to a precise point.
  const anchor = turnAnchorID(s.n);
  if (scrollToAnchor(anchor)) {
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
      scrollToAnchor(anchor);
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

function scrollToAnchor(anchor: string): boolean {
  const target = document.getElementById(anchor);
  if (target === null) {
    return false;
  }
  // The scroll module owns both halves: it parks the reader (so a streaming turn
  // cannot yank the view back down) and it decides whether this jump moves them
  // off the live edge at all. A one-turn chat that does not overflow cannot
  // scroll, and claiming otherwise raised the `Latest` control over a transcript
  // that had not moved. Same call as find-in-chat's.
  jumpTo(target, { block: "start", behavior: "smooth" });
  return true;
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
