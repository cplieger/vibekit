// ---------------------------------------------------------------------------
// One shared owner of the /api/forges poll.
//
// Before this, THREE modules fetched that endpoint and each kept its answer
// private: git-badge.ts polled it every 15s and reduced the response to a badge
// colour, git-prs-tab.ts fetched it again at the head of every PR fan-out, and
// forge-auth.ts fetched it twice more on its own gestures. The same answer was
// on the wire up to three times, and nothing a consumer learned was readable by
// another — the shape git-status-store.ts exists to end.
//
// This store owns the timer and the payload; consumers subscribe or read
// through. It adds NO new server call and NO second timer: the badge's poll
// MOVED here.
//
// Two accessors rather than one, because two questions get asked. A consumer
// PAINTING wants whatever is known now and a repaint when that changes
// (onForgeChange + currentForges). A consumer about to ACT on the list has to
// have one, so it reads through (ensureForges), which returns the cached payload
// or awaits the first fetch.
//
// Nothing here takes an AbortSignal, and that is a property of sharing rather
// than an omission: one consumer navigating away must not abort a fetch two
// others are waiting on. Callers guard staleness after the await instead, which
// is what they already did with their own generation counters.
// ---------------------------------------------------------------------------

import { pollAction } from "./actions/index.js";
import { listForges, type ForgesListResponse } from "./actions/forge-list.js";
import { onSSE } from "./bus.js";
import { signal, subscribe } from "@cplieger/reactive";
import type { ConfiguredForge, ForgeKind } from "./wire/types.gen.js";

/** Re-exported so a consumer imports the payload shape from the store that owns
 *  it rather than from the action file underneath. */
export type { ForgesListResponse };

/** Poll cadence, unchanged from the badge's own. pollAction pauses while the
 *  document is hidden and refreshes on focus, so this is a ceiling not a floor. */
const POLL_INTERVAL_MS = 15_000;

/** The last successful payload, or null before the first one lands. A signal so
 *  consumers repaint on every poll without each holding a copy. */
const state = signal<ForgesListResponse | null>(null);

/** True when the most recent fetch failed. Distinct from a null payload, which
 *  only means nothing has arrived yet: the Sources tab offers a Retry for a
 *  failure and the badge paints a failure red, while neither should react to a
 *  load still in flight. */
const failed = signal(false);

let started = false;

/** Start the poll and the invalidation listener. Idempotent — several init
 *  paths reach it. */
export function initForgeStore(): void {
  if (started) {
    return;
  }
  started = true;
  // A connection change (PAT login, OAuth completion, disconnect, probe) is the
  // only thing that moves this data other than time, and the server broadcasts
  // it. Without this the badge waited up to 15s to notice a sign-out.
  onSSE("forges_changed", () => {
    void refreshForges();
  });
  pollAction(listForges, undefined, {
    interval: POLL_INTERVAL_MS,
    onSuccess: apply,
  });
}

function apply(d: ForgesListResponse | null): void {
  if (d === null) {
    failed.value = true;
    return;
  }
  failed.value = false;
  state.value = d;
}

/** Fetch now and publish the result. Deduped with any in-flight poll tick, so
 *  calling it from several consumers at once costs one request.
 *
 *  This is what a consumer with a REASON to distrust the cache calls: the
 *  Sources tab after a login or a sign-out, and the SSE listener above. */
export async function refreshForges(): Promise<ForgesListResponse | null> {
  const d = await listForges.dispatch(undefined);
  apply(d);
  return d;
}

/** The forge list, fetching once if nothing has landed yet.
 *
 *  For a consumer that cannot proceed without a list: the PR fan-out reads the
 *  connected forges to know which repos to ask about, and an empty answer would
 *  render as "no connected forges" rather than as "not loaded yet". A payload
 *  already in hand is returned as-is, which is the whole point — the fan-out is
 *  the expensive caller and it should not add a round trip of its own. */
export async function ensureForges(): Promise<ForgesListResponse | null> {
  const current = state.peek();
  if (current !== null) {
    return current;
  }
  return refreshForges();
}

/** Subscribe to store changes. Fires immediately with the current value. */
export function onForgeChange(fn: () => void): () => void {
  return subscribe(state, fn);
}

/** The configured forges known now, empty before the first successful load. */
export function currentForges(): readonly ConfiguredForge[] {
  return state.value?.forges ?? [];
}

/** Which forge kinds offer the browser-based device flow. */
export function oauthByKind(): Partial<Record<ForgeKind, boolean>> {
  return state.value?.oauth ?? {};
}

/** True when the last fetch failed. See the `failed` signal for why this is not
 *  the same question as an empty list. */
export function forgeLoadFailed(): boolean {
  return failed.value;
}
