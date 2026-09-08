// ---------------------------------------------------------------------------
// The reload bound: count this tab's rapid reloads and, past a threshold, boot
// REDUCED.
//
// transport.ts's backoff ramp escalates within ONE document — `lastBackoffMs`
// starts at 0 on every fresh document — so a page that crashes and reloads every
// ~1.5s retries at full rate forever, and each of those boots pays for a connect
// snapshot, a transcript paint and four fetch-only fan-outs. This module is the
// persisted counter that bounds it, and it is deliberately blind to WHY the page
// reloaded: whatever the cause, the loop costs the same.
//
// A LEAF: it imports nothing, so every consumer can read the verdict without a
// cycle back through the composition root.
// ---------------------------------------------------------------------------

/** Where the count lives — and why the key is NOT in `ls-keys.ts`.
 *
 *  `sessionStorage` because the scope wanted is exactly ONE TAB across reloads:
 *  it survives a reload and dies with the tab, so a crash loop in one tab says
 *  nothing about the next one. That also puts it outside `clearDeviceKeys()`,
 *  whose whole contract is the localStorage sweep — a sign-out reload must not
 *  reset a crash count — so this key does not belong in that file's list. */
const KEY = "vibekit.reload-guard";

/** How long a run of boots reads as one loop.
 *
 *  Measured cadence of the crash under investigation is ~1.5s, so a real loop
 *  crosses the threshold in ~3s while a person double-reloading in frustration
 *  rarely reaches it. */
const WINDOW_MS = 10_000;

/** Which boot inside the window is reduced: the THIRD.
 *
 *  Two is a reload someone asked for. If a person does reach three inside the
 *  window, what they get is a banner and a smaller connect rather than a broken
 *  app, so the cost of being wrong here is bounded. */
const THRESHOLD = 3;

/** How long a page has to stay alive to clear the count. A page that lived this
 *  long was not in a 1.5s loop. */
const STABLE_MS = 20_000;

/** What this tab remembers: how many boots, and when the run started. */
interface GuardRecord {
  readonly n: number;
  readonly first: number;
}

/** The verdict, computed on the first `bootMode()` call and memoised so every
 *  consumer of this document agrees. */
let mode: "full" | "reduced" | undefined;

let count = 0;
let stabilityTimer: number | undefined;

/** The stored record, or null for anything that is not one.
 *
 *  A `sessionStorage` that THROWS (Safari private mode, storage denied) answers
 *  null like an absent record, which resolves to `full` below: a guard that
 *  cannot read its own count must not be the thing that breaks the boot. */
function readRecord(): GuardRecord | null {
  try {
    const raw = sessionStorage.getItem(KEY);
    if (raw === null) {
      return null;
    }
    const parsed: unknown = JSON.parse(raw);
    if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
      return null;
    }
    const rec = parsed as Record<string, unknown>;
    const n = rec["n"];
    const first = rec["first"];
    if (typeof n !== "number" || typeof first !== "number") {
      return null;
    }
    if (!Number.isFinite(n) || !Number.isFinite(first) || n < 1) {
      return null;
    }
    return { n, first };
  } catch {
    return null;
  }
}

function writeRecord(rec: GuardRecord): void {
  try {
    sessionStorage.setItem(KEY, JSON.stringify(rec));
  } catch {
    /* denied storage: this boot is counted nowhere, so it stays full */
  }
}

/** Whether this document boots with everything or with the smallest set that
 *  still reads a chat.
 *
 *  Computed and MEMOISED on the first call, which is what lets the four
 *  suppression sites and the banner read it independently and agree. The call
 *  also RECORDS this boot, so it must run once per document however many
 *  consumers ask. */
export function bootMode(): "full" | "reduced" {
  if (mode !== undefined) {
    return mode;
  }
  const now = Date.now();
  const prev = readRecord();
  const elapsed = prev === null ? 0 : now - prev.first;
  // A negative elapsed is a clock that moved backwards, which is a fresh run
  // rather than an in-window boot: the stored `first` describes no window here.
  const next: GuardRecord =
    prev === null || elapsed < 0 || elapsed > WINDOW_MS
      ? { n: 1, first: now }
      : { n: prev.n + 1, first: prev.first };
  writeRecord(next);
  count = next.n;
  mode = next.n >= THRESHOLD ? "reduced" : "full";
  return mode;
}

/** How many boots this run has counted, this one included. What the banner names
 *  to the reader. */
export function reloadCount(): number {
  bootMode();
  return count;
}

/** Forget the run.
 *
 *  Two callers: the stability timer, and the banner's own "Start in full mode",
 *  which clears and reloads. It deliberately does NOT re-decide `mode` — the
 *  suppressions have already been applied to this document, and a verdict that
 *  changed underneath them would leave the boot half reduced. */
export function clearReloadGuard(): void {
  count = 0;
  try {
    sessionStorage.removeItem(KEY);
  } catch {
    /* nothing stored, nothing to clear */
  }
}

/** Arm the stability clear: a page still alive in 20s was not in a loop.
 *
 *  Idempotent, so the boot may call it without tracking whether it already has. */
export function noteBootAlive(): void {
  if (stabilityTimer !== undefined) {
    return;
  }
  stabilityTimer = window.setTimeout(() => {
    stabilityTimer = undefined;
    clearReloadGuard();
  }, STABLE_MS);
}
