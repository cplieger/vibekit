// ---------------------------------------------------------------------------
// Transcript search: the server pre-pass that makes the DOM search honest.
//
// The overlay in find-in-chat.ts highlights and navigates in the DOM, which is
// the right place for those — marks, `aria-live` counts and scroll-into-view all
// need real nodes. What the DOM cannot do is ENUMERATE, and it had three blind
// spots doing it: non-resident pages, resident rows whose
// `content-visibility: auto` reports invisible while rendering is skipped, and
// hidden or collapsed subtrees. Progressive collapse would have added a fourth
// and turned a search miss into silent data loss.
//
// So enumeration asks the server (session-wide, no window), and the result is
// used to make the DOM able to answer: reveal the turns that hold hits, then let
// the existing walker highlight them.
// ---------------------------------------------------------------------------

import { apiGet } from "./api-client.js";
import { openForSearch, clearSearchOpened } from "./fold-state.js";
import { bumpMessages } from "./store.js";

/** The on-demand body build for a revealed stub turn, injected by messages.ts
 *  at mount (a static import back would cycle: messages.ts imports this module
 *  for the folded rows' hit counts). Inert until wired. */
let buildRevealedTurn: (chatID: string, turnID: string) => Promise<void> = () => Promise.resolve();

export function initSearchRevealBuilder(
  cb: (chatID: string, turnID: string) => Promise<void>,
): void {
  buildRevealedTurn = cb;
}

/** Which span of a message a hit landed in. `message` is the filter-only kind:
 *  a query with filters and no free text locates the MESSAGE, not a span in it. */
export type SegmentKind = "content" | "reasoning" | "tool_title" | "tool_output" | "message";

/** One server-side match. Mirrors chat.SearchHit — HAND-MAINTAINED, not
 *  wiregen (the generated namespace's SearchHit name is taken by the tools
 *  type); chat-search.node.test.ts pins the two against one shared fixture.
 *
 *  Position is segment-relative: `offset` indexes runes inside the one segment
 *  named by `segment_kind` + `block_index`, never a concatenation of the
 *  message. */
export interface SearchHit {
  /** The matched segment's block position in the message's chronological
   *  blocks array. Absent for messages persisted before blocks existed and for
   *  `message`-kind hits. */
  block_index?: number;
  message_id: string;
  /** The matched turn's opening message id — what the fold state keys on. */
  turn_message_id: string;
  excerpt: string;
  role: string;
  segment_kind: SegmentKind;
  /** Subtask id of the agent that produced the matched segment; absent for the
   *  top-level agent. What lets navigation open the right delegate's chain. */
  agent_subtask_id?: string;
  turn: number;
  /** Rune offset of the match inside its segment. */
  offset: number;
  /** The segment's rune length: the denominator for a relative position,
   *  carried so the client never re-derives the server's segmentation. Zero
   *  for `message`-kind hits. */
  segment_len: number;
}

/** The turn numbers holding hits for the current query, for the timeline rail
 *  and the folded rows' match counts. */
let hitTurns = new Set<number>();
/** Hits per turn number, so a folded row can advertise what is inside it rather
 *  than hiding it. */
let countsByTurn = new Map<number, number>();
/** Every hit the server reported for the current query, session-wide.
 *
 *  Kept as its own number rather than summed from `countsByTurn` on demand: the
 *  map's keys are turn numbers, so summing it would answer the same question by a
 *  longer route and would silently change meaning if a hit ever arrived without a
 *  resolvable turn. */
let hitTotal = 0;

export function searchHitTurns(): ReadonlySet<number> {
  return hitTurns;
}

export function searchHitCount(turn: number): number {
  return countsByTurn.get(turn) ?? 0;
}

/** How many matches the server found in the WHOLE conversation for the current
 *  query, or 0 when no server search is standing.
 *
 *  This is the figure the counter needs and did not have. The DOM pass can only
 *  count what it marked, and three populations are unreachable to it: every
 *  `aria-hidden` subtree (a collapsed delegate card, since `createDisclosure`
 *  writes `aria-hidden` + `inert` on a closed region, plus every closed reasoning
 *  `<details>`), every non-resident page, and every WORKFLOW STEP's text, which
 *  `messages-blocks.ts` drops outright rather than hiding. The server searches the
 *  chat FILE, so it sees all of it. Reporting only the DOM number let the overlay
 *  print "No matches" in the same tick the server had answered that the text
 *  occurs N times, which is the data-loss case this module's own header says the
 *  pre-pass exists to prevent. */
export function searchHitTotal(): number {
  return hitTotal;
}

/**
 * Run the server search and reveal every turn that holds a hit.
 *
 * Returns the hits so a caller can report a count that is TRUE for the session,
 * not for the resident window. Revealing before the DOM pass is the whole point
 * of the ordering: the walker prunes hidden subtrees, so a folded turn's hit is
 * invisible to it until the fold is lifted.
 */
export async function runServerSearch(
  chatID: string,
  query: string,
  caseSensitive = false,
): Promise<SearchHit[]> {
  if (chatID === "" || query.trim() === "") {
    resetServerSearch(chatID);
    return [];
  }
  // `case=1` only when asked. The server treats an absent parameter as
  // insensitive, so the default stays the behaviour it has always had.
  const flag = caseSensitive ? "&case=1" : "";
  const d = await apiGet<{ hits?: SearchHit[] }>(
    `/api/chats/${encodeURIComponent(chatID)}/search?q=${encodeURIComponent(query)}${flag}`,
  );
  // A null is a failed fetch, already logged centrally. Leave the previous
  // reveal in place rather than collapsing turns out from under a reader
  // mid-search.
  if (d === null) {
    return [];
  }
  const hits = d.hits ?? [];

  hitTurns = new Set<number>();
  countsByTurn = new Map<number, number>();
  hitTotal = hits.length;
  for (const h of hits) {
    hitTurns.add(h.turn);
    countsByTurn.set(h.turn, (countsByTurn.get(h.turn) ?? 0) + 1);
  }

  // Open by the turn's OPENING message id, which the server resolves and sends
  // alongside the matched one. Neither substitute works: a hit often lands on an
  // assistant message inside the turn, and the turn NUMBER is session-absolute
  // on the wire but window-relative in the client's projection.
  const revealTurns = new Set<string>();
  for (const h of hits) {
    if (h.turn_message_id !== "") {
      revealTurns.add(h.turn_message_id);
    }
  }
  for (const id of revealTurns) {
    openForSearch(chatID, id);
  }
  // A revealed turn may be a tier-3 stub whose body text the DOM walker cannot
  // mark until it exists. Build each one through the transcript's on-demand
  // entry point BEFORE the repaint below — the builds land under still-folded
  // cards (invisible), yield between block batches, and must complete before
  // this function resolves because the caller re-runs the walker on resolution.
  for (const id of revealTurns) {
    await buildRevealedTurn(chatID, id);
  }
  // Nudge the renderer so the reveal takes effect before the DOM walker runs.
  // A reveal changes which turns are open and mounted: `shape`, stated.
  bumpMessages(chatID, "shape");
  return hits;
}

/** Drop the reveal and the hit marks.
 *
 *  A search must not permanently rearrange the transcript as a side effect, so
 *  turns opened BY SEARCH re-fold here. Turns the reader opened by hand carry a
 *  persisted override and are left alone. */
export function resetServerSearch(chatID: string): void {
  hitTurns = new Set<number>();
  countsByTurn = new Map<number, number>();
  hitTotal = 0;
  if (chatID !== "" && clearSearchOpened(chatID)) {
    // The re-fold is a shape change too: turns the reveal opened fold back,
    // and the ones it mounted past the warm window unmount.
    bumpMessages(chatID, "shape");
  }
}

/**
 * Reveal ONE hit's turn on demand: open it for search, build its body if it is
 * a stub, and repaint. What hit NAVIGATION runs before it can select anything,
 * mirroring `runServerSearch`'s reveal per turn — needed again there because a
 * hit can be paged in AFTER the search ran (its turn arrived as a folded stub
 * the original reveal never saw), and a reader can re-fold a revealed turn and
 * then step onto its hit. Idempotent on an already-revealed turn.
 */
export async function revealHitTurn(chatID: string, hit: SearchHit): Promise<void> {
  if (chatID === "" || hit.turn_message_id === "") {
    return;
  }
  openForSearch(chatID, hit.turn_message_id);
  await buildRevealedTurn(chatID, hit.turn_message_id);
  // Same stated cause as the search-wide reveal: which turns are open and
  // mounted changed. `shape`.
  bumpMessages(chatID, "shape");
}
