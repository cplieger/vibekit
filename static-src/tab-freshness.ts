// Whether a tab's view still shows server state, for all nine kinds.
//
// It must never import `store.js` or `tabs.js`: a convenience helper over either
// (`viewStaleForSession(s)`, `refreshActive()`) closes a cycle.
import { join } from "@cplieger/keyenc";

import type { TabKind } from "./types.js";

/** Counts transport replay gaps. Module state rather than a signal: read at
 *  activation and fetch time, never rendered. */
let syncEpochCount = 0;

/** Whether every mutation of a kind's data reaches this client as an event.
 *
 *  A `true` entry is an optimisation and its incomplete form fails toward
 *  FETCHING: skipping a refresh needs both the entry and a ledger record at the
 *  current epoch, and only a loader writes a record. */
const EVENT_COVERED: Readonly<Record<TabKind, boolean>> = {
  chat: true,
  subagent: true,
  editor: false,
  run: false,
  settings: false,
  git: false,
  files: false,
  history: false,
  docs: false,
};

/** Per subject: the epoch its last answered load went out under. */
const ledger = new Map<string, number>();

/** @internal The node test's collision case. Every exported function takes
 *  `(kind, ref)`, so no writer composes a key. */
export function subjectKey(kind: TabKind, ref: string): string {
  return join(kind, ref);
}

export function syncEpoch(): number {
  return syncEpochCount;
}

/** Every view loaded under the old epoch is a claim this client can no longer
 *  support. Bumped BEFORE any heal starts, so a fetch already in flight captured
 *  the old number and stays stale. */
export function bumpSyncEpoch(): void {
  syncEpochCount++;
}

/** Record a load that ANSWERED. The epoch is a PARAMETER and there is no
 *  overload that reads it here: capturing it at settle time would let an answer
 *  that raced a gap claim currency over it. */
export function noteLoaded(kind: TabKind, ref: string, epoch: number): void {
  ledger.set(subjectKey(kind, ref), epoch);
}

/** Drop a view's record. TWO callers, both in `store.ts`: `removeChat` (the
 *  subject is gone) and `evictChatMessages` (the window is gone). NOT called from
 *  `tabs.ts` — a closed tab's subject still exists, and dropping its record would
 *  make reopening it pay a full message-window GET for nothing. */
export function forgetView(kind: TabKind, ref: string): void {
  ledger.delete(subjectKey(kind, ref));
}

/** Drop EVERY view's record: no load this client has answered can be vouched for any
 *  more, so each view pays one refresh at its next activation.
 *
 *  Distinct from `bumpSyncEpoch`, and the difference is what an in-flight fetch means.
 *  A gap says frames were LOST, so an answer computed before it is not entitled to
 *  claim currency and the epoch strands it. A PAGE RESUME says only that real time
 *  passed unobserved: nothing was dropped, and a request that spanned the suspension
 *  is answered from the server's current state, so stranding it would buy a second
 *  fetch for nothing. This is the reason the resume path calls THIS rather than the
 *  epoch — and it needs to call something, because a record at the current epoch reads
 *  FRESH forever and an in-app tab switch to such a view costs zero fetches. */
export function forgetAllViews(): void {
  ledger.clear();
}

/** Must this view fetch? The ONE freshness question. */
export function viewStale(kind: TabKind, ref: string): boolean {
  return !EVENT_COVERED[kind] || ledger.get(subjectKey(kind, ref)) !== syncEpochCount;
}

/** Test isolation only; production never resets. */
export function _resetForTest(): void {
  syncEpochCount = 0;
  ledger.clear();
}
