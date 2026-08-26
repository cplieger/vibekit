// ---------------------------------------------------------------------------
// A per-chat localStorage map, bounded by chat count with oldest-first eviction.
//
// Two readers own state that is the VIEWER's rather than the workspace's — which
// turns this reader has open (fold-state.ts) and which notices this reader has
// acknowledged (banner-stack.ts) — so neither may travel to another device. Both
// used to live in the server-owned arrangement, where a phone dismissing a banner
// silenced the desktop and a fold on one screen rearranged a transcript someone
// else was reading.
//
// It exists as one module because the part worth getting right is not the read or
// the write, it is the BOUND. Nothing purges these now: the manual cleanup paths
// that ran on chat_deleted existed only because the state was global, and a
// per-chat map keyed by an id the server may purge at any time has no event that
// says "forget this one". So the map caps the number of CHATS it tracks and evicts
// the oldest, which needs the two halves to agree — the write has to re-insert the
// chat it touched at the end for "oldest" to mean anything, and JavaScript's
// insertion-ordered object keys are what make that free.
//
// A chat evicted from here loses its folds and its dismissals, which is the
// correct degradation: both are re-derivable by the reader making the gesture
// again, and the alternative is an unbounded store.
// ---------------------------------------------------------------------------

/** How many chats one of these maps tracks.
 *
 *  Sized from the strip rather than from a guess: the live instance's arrangement
 *  held seven tabs, so a hundred chats is well past anything a reader is moving
 *  between and still a few kilobytes at most. The old server-side bounds were 500
 *  chats for folds and 200 flat keys for dismissals; both were decode walls for a
 *  document a hostile client wrote, where this is a bound on a reader's own history.
 */
const MAX_CHATS = 100;

/** Read the whole map, dropping anything that is not the expected shape.
 *
 *  Validated per ENTRY rather than as a document, for the reason the server-side
 *  sanitizer did it that way: hand-edited or stale data must not reach a
 *  renderer's open/closed decision, and the honest failure is a missing fold
 *  rather than a blank transcript. */
export function readPerChat<T>(
  key: string,
  valid: (v: unknown) => T | undefined,
): Record<string, T> {
  const out: Record<string, T> = {};
  try {
    const raw = localStorage.getItem(key);
    if (raw === null) {
      return out;
    }
    const parsed: unknown = JSON.parse(raw);
    if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
      return out;
    }
    for (const [chatID, value] of Object.entries(parsed as Record<string, unknown>)) {
      if (chatID === "") {
        continue;
      }
      const ok = valid(value);
      if (ok !== undefined) {
        out[chatID] = ok;
      }
    }
  } catch {
    // Disabled storage, a quota failure, or bytes nothing wrote. An arrangement
    // of folds is not data worth reporting a failure over.
  }
  return out;
}

/** Write `value` for `chatID`, evicting the oldest-touched chats past the cap.
 *
 *  The map is rebuilt as an ENTRY LIST rather than mutated, so the touched chat
 *  lands last: object key order is insertion order, so a re-insert is what makes
 *  the eviction below oldest-first rather than arbitrary. A value the caller
 *  considers empty is a DELETE — keeping an empty record would spend a slot on a
 *  chat with nothing to remember and evict one that has something. */
export function writePerChat<T>(
  key: string,
  map: Record<string, T>,
  chatID: string,
  value: T | undefined,
): void {
  if (chatID === "") {
    return;
  }
  const kept = Object.entries(map).filter(([id]) => id !== chatID);
  if (value !== undefined) {
    kept.push([chatID, value]);
  }
  // Trim from the FRONT: the touched chat was just re-inserted at the end, so the
  // head of this list is the least recently written.
  const next = Object.fromEntries(kept.slice(Math.max(0, kept.length - MAX_CHATS)));
  try {
    localStorage.setItem(key, JSON.stringify(next));
  } catch {
    // ignore quota / disabled storage
  }
}

/** @internal Test seam: the cap, so a test states it in the unit production uses
 *  rather than restating the number. */
export const MAX_TRACKED_CHATS = MAX_CHATS;
