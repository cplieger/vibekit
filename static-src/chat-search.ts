// ---------------------------------------------------------------------------
// Transcript search: the server pre-pass that makes the DOM search honest.
//
// find-in-chat.ts highlights and lands in the DOM, which needs real nodes. What
// the DOM cannot do is ENUMERATE: non-resident pages, rows `content-visibility`
// has skipped, and hidden or collapsed subtrees are all invisible to a walker.
// So enumeration asks the server (session-wide, no window), and a collapse hides
// nothing because BOTH halves are the server's: the count, and the step list
// Enter walks (find-in-chat.ts's `stepOrder`). The reveal below lifts a folded
// turn so the walker can MARK its hit; it is a convenience for the landing.
//
// The answer is the server's whole envelope (`SearchResult`, tally included),
// handed to the caller as decoded. This module keeps only what other surfaces
// read off it: the hit turns for the rail and the folded rows.
// ---------------------------------------------------------------------------

import { apiGetTyped } from "./api-client.js";
import { openForSearch, clearSearchOpened } from "./fold-state.js";
import { bumpMessages } from "./store.js";
import { decodeSearchResult } from "./wire/decoders.gen.js";
import type { Hit, SearchResult } from "./wire/types.gen.js";

/** The answer to a question nobody asked (no chat, a blank query): nothing read,
 *  nothing matched, nothing cut. */
const EMPTY_ANSWER: SearchResult = { matches: [], scanned: 0, matched: 0, truncated: false };

/** The on-demand body build for ONE hit's turn, injected by messages.ts at mount (a
 *  static import back would cycle: messages.ts imports this module for the folded rows'
 *  hit counts). Inert until wired. The hit's BLOCK crosses rather than its turn-block
 *  ordinal, which is a fact of the residency projection on the other side. */
let buildRevealedTurn: (
  chatID: string,
  turnID: string,
  messageID: string,
  blockIndex?: number,
) => Promise<void> = () => Promise.resolve();

/** The same build for the search-WIDE loop, whose grant is scoped to the reveal
 *  rather than to the one navigation the reader is making. */
let buildWalkTurn: (chatID: string, turnID: string) => Promise<void> = () => Promise.resolve();

/** Release every grant the loop above took, in the chat it took them in. */
let endWalkReveal: (chatID: string) => void = () => undefined;

export function initSearchRevealBuilder(
  reveal: (chatID: string, turnID: string, messageID: string, blockIndex?: number) => Promise<void>,
  forWalk: (chatID: string, turnID: string) => Promise<void>,
  endWalk: (chatID: string) => void,
): void {
  buildRevealedTurn = reveal;
  buildWalkTurn = forWalk;
  endWalkReveal = endWalk;
}

/** The turn numbers holding hits for the current query, for the timeline rail
 *  and the folded rows' match counts. */
let hitTurns = new Set<number>();
/** Hits per turn number, so a folded row can advertise what is inside it rather
 *  than hiding it. */
let countsByTurn = new Map<number, number>();
/** The chat the standing search ran in, so its reveal is released where it was taken:
 *  the close path names whichever chat is ACTIVE, and a chat switch with the find box
 *  open closes against the new one. */
let searchedChatID = "";

export function searchHitTurns(): ReadonlySet<number> {
  return hitTurns;
}

export function searchHitCount(turn: number): number {
  return countsByTurn.get(turn) ?? 0;
}

/** Run the server search and reveal every turn holding a hit BEFORE the DOM
 *  pass: the walker prunes hidden subtrees, so a folded turn's hit is invisible
 *  to it until the fold is lifted.
 *
 *  Three answers. An envelope is the server's; `EMPTY_ANSWER` answers an empty
 *  question (no chat, blank query); `null` means the FETCH failed, so the caller
 *  keeps what was standing rather than claiming "no matches". */
export async function runServerSearch(
  chatID: string,
  query: string,
  caseSensitive = false,
): Promise<SearchResult | null> {
  if (chatID === "" || query.trim() === "") {
    resetServerSearch();
    return EMPTY_ANSWER;
  }
  // `case=1` only when asked. The server treats an absent parameter as
  // insensitive, so the default stays the behaviour it has always had.
  const flag = caseSensitive ? "&case=1" : "";
  const d = await apiGetTyped(
    `/api/chats/${encodeURIComponent(chatID)}/search?q=${encodeURIComponent(query)}${flag}`,
    decodeSearchResult,
  );
  // A null is a failed fetch or a reply the decoder refused, already logged
  // centrally. Leave the previous reveal in place rather than collapsing turns
  // out from under a reader mid-search. `null` travels OUT for the same reason:
  // the caller's own standing answer, cursor and ownership are what keep the
  // reader's walk whole across the failure.
  if (d === null) {
    return null;
  }
  const hits = d.matches;

  hitTurns = new Set<number>();
  countsByTurn = new Map<number, number>();
  searchedChatID = chatID;
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
  // A revealed turn may be a STUB whose body text the DOM walker cannot
  // mark until it exists. Build each one through the transcript's on-demand
  // entry point BEFORE the repaint below — the builds land under still-folded
  // cards (invisible), yield between block batches, and must complete before
  // this function resolves because the caller re-runs the walker on resolution.
  for (const id of revealTurns) {
    await buildWalkTurn(chatID, id);
  }
  // Nudge the renderer so the reveal takes effect before the DOM walker runs.
  // A reveal changes which turns are open and mounted: `shape`, stated.
  bumpMessages(chatID, "shape");
  return d;
}

/** Drop the reveal and the hit marks.
 *
 *  A search must not permanently rearrange the transcript as a side effect, so turns opened
 *  BY SEARCH re-fold here. Turns the reader opened by hand carry a persisted override and are
 *  left alone. Keyed on the chat this module SEARCHED and takes no chat argument: the box
 *  closes AFTER a tab change has moved the active id, so an active-keyed teardown re-folded
 *  nothing and left the searched chat's turns open. */
export function resetServerSearch(): void {
  hitTurns = new Set<number>();
  countsByTurn = new Map<number, number>();
  const searched = searchedChatID;
  searchedChatID = "";
  // Unconditional: the reveal is over whatever the fold set says. Inside the
  // branch below the grants would outlive a `searchOpened` some other path
  // emptied first, with no gesture left to end them.
  endWalkReveal(searched);
  if (searched !== "" && clearSearchOpened(searched)) {
    // The re-fold is a shape change too: turns the reveal opened fold back, and
    // the ones it pinned resident past the paint's block budget unmount
    // (`block-window.ts`).
    bumpMessages(searched, "shape");
  }
}

/**
 * Reveal ONE hit's turn on demand: open it for search, build the body around the
 * hit's own block, and repaint. What hit NAVIGATION runs before it can select anything,
 * mirroring `runServerSearch`'s reveal per turn — needed again there because a
 * hit can be paged in AFTER the search ran (its turn arrived as a folded stub
 * the original reveal never saw), and a reader can re-fold a revealed turn and
 * then step onto its hit. Idempotent on an already-revealed turn.
 */
export async function revealHitTurn(chatID: string, hit: Hit): Promise<void> {
  if (chatID === "" || hit.turn_message_id === "") {
    return;
  }
  openForSearch(chatID, hit.turn_message_id);
  await buildRevealedTurn(chatID, hit.turn_message_id, hit.message_id, hit.block_index);
  // Same stated cause as the search-wide reveal: which turns are open and
  // mounted changed. `shape`.
  bumpMessages(chatID, "shape");
}
