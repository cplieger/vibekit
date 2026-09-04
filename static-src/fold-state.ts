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
// DISCLOSURE ONLY. Whether a turn's body is MOUNTED is `block-window.ts`'s
// question, decided on a block and tool-card budget; this module decides whether
// a mounted body is shown. A turn residency excluded renders folded whatever the
// rule here says, because it has no body to open — and `isTurnRevealed` is how
// the reader's own request pins it back into residency.
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

import { LS_TURN_FOLDS_KEY } from "./ls-keys.js";
import { readPerChat, writePerChat } from "./per-chat-store.js";
import type { Turn } from "./turns.js";

/** How many trailing turns stay open regardless of anything else. ONE: a turn
 *  auto-collapses when the next turn starts, and not before. */
const OPEN_TAIL = 1;

// There is no TURNS_WARM. It was a second trailing-turn count answering
// MOUNTEDNESS, and a turn count is not a paint cost — `block-window.ts` owns
// residency now, in blocks and tool cards. This module answers disclosure only.

/** Per-chat, per-turn overrides: `true` = the user opened it, `false` = the user
 *  folded it. Absent = follow the automatic rule.
 *
 *  Keyed by the turn's opening MESSAGE id rather than its ordinal, because an
 *  ordinal shifts when a rewind drops turns and would then point at a different
 *  turn than the one the reader opened.
 *
 *  PER-DEVICE, in localStorage. It used to be `ui-state.turn_folds`, shared with
 *  every other device, and that failed the arrangement's own test: a fold is a
 *  disclosure state, so sharing it meant one screen rearranged a transcript
 *  someone else was reading. Bounded by chat count with oldest-first eviction,
 *  because nothing purges it any more — the `forgetChatFolds` call on chat_deleted
 *  existed only because the state was global. */
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
  for (const [chatID, byTurn] of Object.entries(readPerChat(LS_TURN_FOLDS_KEY, validOverrides))) {
    overrides.set(chatID, byTurn);
  }
}

/** Validate one chat's overrides, dropping any entry that is not a boolean.
 *
 *  Per entry rather than per chat: hand-edited or stale bytes must not reach the
 *  renderer's open/closed decision, and the honest failure is one fold falling
 *  back to the automatic rule rather than a whole chat losing its overrides. */
function validOverrides(v: unknown): Overrides | undefined {
  if (typeof v !== "object" || v === null || Array.isArray(v)) {
    return undefined;
  }
  const out: Overrides = {};
  for (const [turnID, open] of Object.entries(v as Record<string, unknown>)) {
    if (turnID !== "" && typeof open === "boolean") {
      out[turnID] = open;
    }
  }
  return Object.keys(out).length > 0 ? out : undefined;
}

/** Persist ONE chat's overrides, which is what lets the store evict by chat.
 *
 *  A whole-document write would have nothing to re-insert, so "oldest" could not
 *  mean anything and the eviction order would be whatever the map happened to
 *  hold. An empty record is written as a DELETE so a chat with nothing to remember
 *  does not occupy a slot. */
function persist(chatID: string): void {
  const byTurn = overrides.get(chatID);
  const value = byTurn !== undefined && Object.keys(byTurn).length > 0 ? byTurn : undefined;
  writePerChat(LS_TURN_FOLDS_KEY, Object.fromEntries(overrides), chatID, value);
}

/** Whether a turn renders open.
 *
 *  `index` and `total` position the turn in the CURRENTLY PROJECTED list, which
 *  is the resident window rather than the session. That is the right frame: the
 *  two open turns are the two the reader is looking at, and a turn scrolled off
 *  the top of history is not one of them.
 *
 *  `hasLiveRun` is passed rather than derived, for the same reason `pendingAsk` is
 *  passed into `tabStatusFor`: the answer needs the run store and the message list,
 *  and this module must stay a pure fold-state rule that knows about neither. */
export function isTurnOpen(chatID: string, t: Turn, index: number, total: number): boolean {
  load();
  // A running turn is the one being watched, and it CANNOT be collapsed — the
  // rule outranks even an explicit override, so a stale recorded fold cannot
  // hide a live stream.
  if (t.outcome === "running") {
    return true;
  }
  // The newest turn is the one being read and CANNOT be collapsed — its toggle
  // is hidden, and the rule sits ABOVE the overrides so a fold recorded against
  // it by an earlier build, or against a turn a rewind made newest, cannot
  // strand the tail closed with no control left to reopen it.
  if (index >= total - OPEN_TAIL) {
    return true;
  }
  const explicit = overrides.get(chatID)?.[t.id];
  if (explicit !== undefined) {
    return explicit;
  }
  if (searchOpened.get(chatID)?.has(t.id) === true) {
    return true;
  }
  // No sticky-open for failed turns and no sticky-open for live runs: the
  // collapsed face carries both (the error text as the turn's output, and a
  // duplicate run card above the prose), so folding hides neither.
  return false;
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
  persist(chatID);
}

// There is no hasTurnOverride accessor. The distinction it was for — a
// hand-opened turn surviving a search close while a search-opened one re-folds —
// is already structural: isTurnOpen consults the persisted overrides BEFORE the
// search set, so an explicit choice outranks a reveal without anyone asking.

/** Whether the reader ASKED for this turn's body — by opening it, or by landing a
 *  search on it.
 *
 *  A different question from `isTurnOpen`, which also answers true for the newest
 *  turn and a running one by rule. This one is only ever the reader's own request,
 *  which is what makes it the right pin for residency (`block-window.ts`): a
 *  budget may fold a turn nobody asked for, and may not take back one somebody
 *  did. A recorded FOLD reads false, like no record at all. */
export function isTurnRevealed(chatID: string, turnID: string): boolean {
  load();
  return overrides.get(chatID)?.[turnID] === true || searchOpened.get(chatID)?.has(turnID) === true;
}

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

// There is no forgetChatFolds. It existed only because the overrides lived in one
// global blob where an entry per deleted chat accumulated forever, and its one
// caller was the chat_deleted handler. Per-chat storage bounds itself by chat count
// with oldest-first eviction (per-chat-store.ts), so a purged or deleted chat needs
// nobody to tell this module about it.

/** Drop the in-memory copy of the persisted document, so the next read reloads it.
 *
 *  Production caller: the sign-out sweep (`boot.ts` `forgetDeviceState`). Deleting
 *  the localStorage key alone does not forget anything — `persist` rewrites the whole
 *  document out of this map, so the next fold after a sign-out would put the previous
 *  user's folds straight back. Also the reset a test that drives two boots needs. */
export function resetFoldState(): void {
  overrides.clear();
  searchOpened.clear();
  loaded = false;
}
