// ---------------------------------------------------------------------------
// Progressive collapse: which turns are open.
//
// EXACTLY TWO TURNS OPEN by default — the current turn and the one before the
// user's last input. Two rather than one because the previous turn is usually
// the context for the current request, so folding it forces an expand on nearly
// every read.
//
// Everything older folds to its merged header/footer row. The fold is what pays
// for the turn unit: a resident page of ~23 older turns becomes ~23 one-line
// rows, which is the difference between a transcript and an archive.
//
// THREE THINGS NEVER AUTO-FOLD, and the first is the important one:
//
//   - A turn that FAILED or was INTERRUPTED. Errors are the last thing that
//     should hide themselves.
//   - A turn the user opened by hand. That choice persists per chat, so it
//     survives a reload and a chat switch.
//   - The two newest turns.
//
// Pure and DOM-free: the renderer asks whether a turn is open, and this module
// answers from the projection plus the user's own overrides.
// ---------------------------------------------------------------------------

import * as uiState from "./ui-state.js";
import type { Turn } from "./turns.js";

/** How many trailing turns stay open regardless of anything else. */
const OPEN_TAIL = 2;

/** Per-chat, per-turn overrides: `true` = the user opened it, `false` = the user
 *  folded it. Absent = follow the automatic rule.
 *
 *  Keyed by the turn's opening MESSAGE id rather than its ordinal, because an
 *  ordinal shifts when a rewind drops turns and would then point at a different
 *  turn than the one the reader opened. */
type Overrides = Record<string, boolean>;
const overrides = new Map<string, Overrides>();

/** Turns opened by search rather than by the reader. Not persisted, and dropped
 *  wholesale when the search closes — a search must not permanently rearrange
 *  the transcript as a side effect. */
const searchOpened = new Map<string, Set<string>>();

let loaded = false;

function load(): void {
  if (loaded) {
    return;
  }
  loaded = true;
  // sanitize() guarantees the field exists, so no fallback is needed here.
  const raw = uiState.load().turn_folds;
  for (const [chatID, byTurn] of Object.entries(raw)) {
    overrides.set(chatID, { ...byTurn });
  }
}

function persist(): void {
  const out: Record<string, Overrides> = {};
  for (const [chatID, byTurn] of overrides) {
    if (Object.keys(byTurn).length > 0) {
      out[chatID] = byTurn;
    }
  }
  uiState.save({ turn_folds: out });
}

/** Whether a turn renders open.
 *
 *  `index` and `total` position the turn in the CURRENTLY PROJECTED list, which
 *  is the resident window rather than the session. That is the right frame: the
 *  two open turns are the two the reader is looking at, and a turn scrolled off
 *  the top of history is not one of them. */
export function isTurnOpen(chatID: string, t: Turn, index: number, total: number): boolean {
  load();
  const explicit = overrides.get(chatID)?.[t.id];
  if (explicit !== undefined) {
    return explicit;
  }
  if (searchOpened.get(chatID)?.has(t.id) === true) {
    return true;
  }
  // Sticky failure. Checked BEFORE the tail rule rather than after, so a failed
  // turn stays open no matter how far back it is.
  if (t.outcome === "failed" || t.outcome === "interrupted") {
    return true;
  }
  // A running turn is the one being watched.
  if (t.outcome === "running") {
    return true;
  }
  return index >= total - OPEN_TAIL;
}

/** Record the reader's own choice for a turn. */
export function setTurnOpen(chatID: string, turnID: string, open: boolean): void {
  load();
  let byTurn = overrides.get(chatID);
  if (byTurn === undefined) {
    byTurn = {};
    overrides.set(chatID, byTurn);
  }
  byTurn[turnID] = open;
  persist();
}

// There is no hasTurnOverride accessor. The distinction it was for — a
// hand-opened turn surviving a search close while a search-opened one re-folds —
// is already structural: isTurnOpen consults the persisted overrides BEFORE the
// search set, so an explicit choice outranks a reveal without anyone asking.

/** Open a turn because a search hit is inside it. */
export function openForSearch(chatID: string, turnID: string): void {
  let set = searchOpened.get(chatID);
  if (set === undefined) {
    set = new Set();
    searchOpened.set(chatID, set);
  }
  set.add(turnID);
}

/** Drop every search-opened turn. Returns true when something changed, so the
 *  caller can skip a repaint that would do nothing. */
export function clearSearchOpened(chatID: string): boolean {
  const set = searchOpened.get(chatID);
  if (set === undefined || set.size === 0) {
    return false;
  }
  searchOpened.delete(chatID);
  return true;
}

/** Forget a deleted chat's overrides so the store does not grow without bound. */
export function forgetChatFolds(chatID: string): void {
  load();
  if (overrides.delete(chatID)) {
    persist();
  }
  searchOpened.delete(chatID);
}
