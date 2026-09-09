// The reload bound. What is under test is the COUNT across documents, so every
// case boots a fresh module instance against one shared store: that is what a
// reload is, and module state re-evaluated is the only honest way to model it
// (`vi.resetModules()` does not re-evaluate a module in Browser Mode).
//
// The store is stubbed with `vi.stubGlobal`, never `vi.spyOn(Storage.prototype,
// ...)`, which is hollow once another test in the file has run.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type * as Guard from "./reload-guard.js";

/** A `Storage`-shaped map, so a case can read back what production wrote without
 *  restating the key it wrote it under. */
function fakeStore(): { readonly entries: Map<string, string> } & Storage {
  const entries = new Map<string, string>();
  return {
    entries,
    getItem: (k: string) => entries.get(k) ?? null,
    setItem: (k: string, v: string) => {
      entries.set(k, v);
    },
    removeItem: (k: string) => {
      entries.delete(k);
    },
    clear: () => {
      entries.clear();
    },
    key: (i: number) => [...entries.keys()][i] ?? null,
    get length() {
      return entries.size;
    },
  };
}

/** A store that refuses every operation: Safari private mode, or storage denied. */
function throwingStore(): Storage {
  const deny = (): never => {
    throw new DOMException("denied", "SecurityError");
  };
  return {
    getItem: deny,
    setItem: deny,
    removeItem: deny,
    clear: deny,
    key: deny,
    get length(): number {
      return deny();
    },
  };
}

/** One boot. A busted specifier is what mints a new module instance; the `.ts`
 *  extension is load-bearing for coverage attribution. */
let seq = 0;
async function boot(): Promise<typeof Guard> {
  seq++;
  return (await import(/* @vite-ignore */ `./reload-guard.ts?boot=${seq}`)) as typeof Guard;
}

/** `n` boots, `gap` apart, each one ASKING for its verdict — which is what records
 *  it, exactly as a document does on its first consumer. Returns the last instance.
 *
 *  A helper rather than a loop per case, because the two loop cases below differ only
 *  in how many boots they run. */
async function bootRun(n: number, gap: number): Promise<typeof Guard> {
  let guard = await boot();
  guard.bootMode();
  for (let i = 2; i <= n; i++) {
    vi.advanceTimersByTime(gap);
    guard = await boot();
    guard.bootMode();
  }
  return guard;
}

const WINDOW_MS = 10_000;
const STABLE_MS = 20_000;
/** The measured cadence of the crash the bound was built for. */
const LOOP_GAP_MS = 1500;

let store: ReturnType<typeof fakeStore>;

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-09-10T12:00:00Z"));
  store = fakeStore();
  vi.stubGlobal("sessionStorage", store);
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("the reload guard counts one tab's boots", () => {
  it("increments across reloads inside the window", async () => {
    expect((await boot()).reloadCount()).toBe(1);
    vi.advanceTimersByTime(1500);
    expect((await boot()).reloadCount()).toBe(2);
    vi.advanceTimersByTime(1500);
    expect((await boot()).reloadCount()).toBe(3);
  });

  it("starts again once the window has passed", async () => {
    expect((await boot()).reloadCount()).toBe(1);
    vi.advanceTimersByTime(WINDOW_MS + 1);
    const second = await boot();
    expect(second.reloadCount()).toBe(1);
    expect(second.bootMode()).toBe("full");
  });

  it("reduces on the THIRD boot and not the second", async () => {
    expect((await boot()).bootMode()).toBe("full");
    vi.advanceTimersByTime(1500);
    expect((await boot()).bootMode()).toBe("full");
    vi.advanceTimersByTime(1500);
    const third = await boot();
    expect(third.bootMode()).toBe("reduced");
    expect(third.reloadCount()).toBe(3);
  });

  it("stays reduced through a SUSTAINED loop, past the window's own length", async () => {
    // Eight boots 1500ms apart: the run lasts 10.5s, past WINDOW_MS. Anchored on the
    // run's first boot it is the EIGHTH that crosses — t=10500 against the 10000
    // window, where the seventh is still inside it at t=9000 with n=7 — so the eighth
    // reset to 1 and read FULL: a full boot, with every suppression lifted, in the
    // middle of the loop the bound exists to end. The assertion below is on that
    // eighth boot, which is why the red check reads `['full', 1]`.
    const latest = await bootRun(8, LOOP_GAP_MS);

    expect([latest.bootMode(), latest.reloadCount()]).toEqual(["reduced", 8]);
  });

  it("does not cap the count, so a long loop reports what it cost", async () => {
    const latest = await bootRun(30, LOOP_GAP_MS);

    // The banner names this number, so a cap would understate it. 30 boots is 45s of
    // reloading, which the anchored window would have reset four times over.
    expect([latest.bootMode(), latest.reloadCount()]).toEqual(["reduced", 30]);
  });

  it("reads a record another build wrote before the window slid", async () => {
    // A tab that picks up a new bundle mid-loop: the retired anchor is the only stamp
    // it has, so it is read as the previous boot. Costs that one tab a wrong gap,
    // never the count.
    store.setItem("vibekit.reload-guard", JSON.stringify({ n: 2, first: Date.now() }));

    const guard = await boot();

    expect([guard.bootMode(), guard.reloadCount()]).toEqual(["reduced", 3]);
  });

  it("answers every consumer of one document the same way", async () => {
    // The record is written once per document however many consumers ask, or the
    // four suppression sites would each count their own boot.
    const first = await boot();
    expect(first.bootMode()).toBe("full");
    expect(first.bootMode()).toBe("full");
    expect(first.reloadCount()).toBe(1);
    const second = await boot();
    expect(second.reloadCount()).toBe(2);
  });
});

describe("the count is cleared", () => {
  it("by a page that stays alive, and by nothing else at that age", async () => {
    // Ageing out is not a REMOVAL: the record is still there, which is what makes
    // the assertion below about the timer rather than about the window.
    (await boot()).bootMode();
    expect(store.entries.size).toBe(1);
    vi.advanceTimersByTime(STABLE_MS);
    expect(store.entries.size).toBe(1);

    const second = await boot();
    second.bootMode();
    second.noteBootAlive();
    vi.advanceTimersByTime(STABLE_MS);
    expect(store.entries.size).toBe(0);
  });

  it("by clearReloadGuard, inside the window", async () => {
    (await boot()).bootMode();
    vi.advanceTimersByTime(1500);
    const second = await boot();
    expect(second.reloadCount()).toBe(2);

    second.clearReloadGuard();
    expect(store.entries.size).toBe(0);
    expect(second.reloadCount()).toBe(0);

    // Still inside the window, so a fresh boot reading 1 is the clear rather than
    // an expiry.
    vi.advanceTimersByTime(500);
    expect((await boot()).reloadCount()).toBe(1);
  });
});

describe("a store it cannot use", () => {
  it("degrades to full rather than throwing", async () => {
    vi.stubGlobal("sessionStorage", throwingStore());
    for (let i = 0; i < 4; i++) {
      const guard = await boot();
      expect(guard.bootMode()).toBe("full");
      expect(guard.reloadCount()).toBe(1);
      vi.advanceTimersByTime(1500);
    }
  });

  it("treats bytes nothing wrote as no count at all", async () => {
    store.setItem("vibekit.reload-guard", "not json");
    expect((await boot()).reloadCount()).toBe(1);
    store.setItem("vibekit.reload-guard", JSON.stringify({ n: "many", first: 0 }));
    expect((await boot()).reloadCount()).toBe(1);
  });
});
