// The rapid-reload counter: count this tab's reloads and, past a threshold, boot
// REDUCED. transport.ts's backoff ramp escalates within ONE document, so a page
// crashing and reloading every ~1.5s retries at full rate forever, and each of those
// boots pays for a connect snapshot and a transcript paint. This module CHEAPENS a
// run's later boots rather than ending it, blind to WHY the page reloaded: whatever
// the cause, the loop costs the same.

/** Where the count lives, and why the key is NOT in `ls-keys.ts`: `sessionStorage`
 *  scopes it to ONE TAB across reloads, so a crash loop in one tab says nothing about
 *  the next. That also puts it outside `clearDeviceKeys()`, whose contract is the
 *  localStorage sweep, and a sign-out reload must not reset a crash count. */
const KEY = "vibekit.reload-guard";

/** The longest QUIET between two boots that still reads as one loop.
 *
 *  A GAP rather than the age of the run, which is what lets a run last: the measured
 *  cadence is ~1.5s, so a real loop crosses the threshold in ~3s and STAYS in it for
 *  as long as it keeps reloading. Anchored on the run's first boot instead, the EIGHTH
 *  boot of a 1.5s loop aged past this and reset the count to 1, mid-run. */
const WINDOW_MS = 10_000;

/** Which boot inside the window is reduced: the THIRD. Two is a reload someone asked
 *  for; a person who does reach three gets a banner and a smaller connect rather than
 *  a broken app, so the cost of being wrong here is bounded. */
const THRESHOLD = 3;

/** How long a page has to stay alive to clear the count. A page that lived this
 *  long was not in a 1.5s loop. */
const STABLE_MS = 20_000;

/** What this tab remembers: how many boots, and when the LAST one was. The last
 *  rather than the first, because the window is a gap test measured against the
 *  previous boot. */
interface GuardRecord {
  readonly n: number;
  readonly last: number;
}

/** The verdict, computed on the first `bootMode()` call and memoised so every
 *  consumer of this document agrees. */
let mode: "full" | "reduced" | undefined;

let count = 0;
let stabilityTimer: number | undefined;

/** The stored record, or null for anything that is not one. A `sessionStorage` that
 *  THROWS (Safari private mode, storage denied) answers null like an absent record and
 *  resolves to `full`: a guard that cannot read its count must not break the boot. */
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
    // `first` is the retired anchor, read as the previous boot for the one tab that
    // picks up a new bundle mid-loop: the gap it yields is wrong by however long the
    // run had been going, which costs that tab one full boot rather than a reset.
    const last = rec["last"] ?? rec["first"];
    if (typeof n !== "number" || typeof last !== "number") {
      return null;
    }
    // The count is deliberately UNCAPPED: the banner names it to the reader, so a cap
    // would make a sustained loop understate what it cost them.
    if (!Number.isFinite(n) || !Number.isFinite(last) || n < 1) {
      return null;
    }
    return { n, last };
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

/** Whether this document boots with everything or with the smallest set that still
 *  reads a chat. MEMOISED on the first call, which is what lets the suppression sites
 *  and the banner read it independently and agree; the call also RECORDS this boot, so
 *  it must run once per document however many consumers ask. */
export function bootMode(): "full" | "reduced" {
  if (mode !== undefined) {
    return mode;
  }
  const now = Date.now();
  const prev = readRecord();
  // The QUIET since the previous boot, never the age of the run: a loop that keeps
  // booting keeps its own run alive, and only real quiet ends it.
  const gap = prev === null ? 0 : now - prev.last;
  // A negative gap is a clock that moved backwards, which is a fresh run rather than
  // an in-window boot: the stored stamp describes no window here.
  const n = prev === null || gap < 0 || gap > WINDOW_MS ? 1 : prev.n + 1;
  // One stamp for both arms, so the advance cannot be forgotten on the increment path:
  // a record that counts up without moving its stamp ages out mid-loop.
  const next: GuardRecord = { n, last: now };
  writeRecord(next);
  count = next.n;
  mode = next.n >= THRESHOLD ? "reduced" : "full";
  return mode;
}

/** How many boots this run has counted, this one included. UNBOUNDED: the banner names
 *  it, and a cap would understate what a sustained loop cost the reader. */
export function reloadCount(): number {
  bootMode();
  return count;
}

/** Forget the run. Deliberately does NOT re-decide `mode`: the suppressions have
 *  already been applied to this document, and a verdict that changed underneath them
 *  would leave the boot half reduced. */
export function clearReloadGuard(): void {
  count = 0;
  try {
    sessionStorage.removeItem(KEY);
  } catch {
    /* nothing stored, nothing to clear */
  }
}

/** Arm the stability clear: a page still alive in 20s was not in a loop. Idempotent, so
 *  the boot may call it without tracking whether it already has. */
export function noteBootAlive(): void {
  if (stabilityTimer !== undefined) {
    return;
  }
  stabilityTimer = window.setTimeout(() => {
    stabilityTimer = undefined;
    clearReloadGuard();
  }, STABLE_MS);
}
