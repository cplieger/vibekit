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
import { emitMessages } from "./store.js";

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

export function searchHitTurns(): ReadonlySet<number> {
  return hitTurns;
}

export function searchHitCount(turn: number): number {
  return countsByTurn.get(turn) ?? 0;
}

/**
 * Run the server search and reveal every turn that holds a hit.
 *
 * Returns the hits so a caller can report a count that is TRUE for the session,
 * not for the resident window. Revealing before the DOM pass is the whole point
 * of the ordering: the walker prunes hidden subtrees, so a folded turn's hit is
 * invisible to it until the fold is lifted.
 */
export async function runServerSearch(chatID: string, query: string): Promise<SearchHit[]> {
  if (chatID === "" || query.trim() === "") {
    resetServerSearch(chatID);
    return [];
  }
  const d = await apiGet<{ hits?: SearchHit[] }>(
    `/api/chats/${encodeURIComponent(chatID)}/search?q=${encodeURIComponent(query)}`,
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
  emitMessages();
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
  if (chatID !== "" && clearSearchOpened(chatID)) {
    emitMessages();
  }
}
