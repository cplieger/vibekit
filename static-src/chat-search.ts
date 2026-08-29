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

/** One server-side match. Mirrors chat.SearchHit. */
export interface SearchHit {
  message_id: string;
  /** The matched turn's opening message id — what the fold state keys on. */
  turn_message_id: string;
  excerpt: string;
  role: string;
  turn: number;
  offset: number;
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
 *  count what it marked, and it prunes at every `aria-hidden` subtree — which is
 *  every collapsed delegate card and workflow step row, since `createDisclosure`
 *  writes `aria-hidden` + `inert` on a closed region — plus every closed
 *  reasoning `<details>`, and it never reaches a non-resident page at all. The
 *  server searches the chat FILE, so it sees all of it. Reporting only the DOM
 *  number let the overlay print "No matches" in the same tick the server had
 *  answered that the text occurs N times, which is the data-loss case this
 *  module's own header says the pre-pass exists to prevent. */
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
  for (const h of hits) {
    if (h.turn_message_id !== "") {
      openForSearch(chatID, h.turn_message_id);
    }
  }
  // Nudge the renderer so the reveal takes effect before the DOM walker runs.
  bumpMessages(chatID);
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
    bumpMessages(chatID);
  }
}
