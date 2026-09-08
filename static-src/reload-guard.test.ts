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

const WINDOW_MS = 10_000;
const STABLE_MS = 20_000;

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
