// ---------------------------------------------------------------------------
// The sync half of the tab projection: which frames reach the strip, in what
// order, and when a frame means "ask again".
//
// Every test here is against a FAKE target that is little more than a Set, which
// is the point of the split: the three version rules, the arrival-order queue and
// the stale-snapshot guard are decidable without a row, a spec or a document. What
// they defend is not decidable by inspection — each one has a defect behind it
// that shipped, or that an adversarial review reached before it could.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, vi } from "vitest";
import type { TabList, TabSubject, TabsChangedPayload } from "./types.js";

/** The collection GET's answers, queued so a test can arrange a stale one behind
 *  a fresh one. `null` is the unreachable-endpoint case. */
const listing = {
  answers: [] as (TabList | null)[],
  calls: 0,
};

vi.mock("./api-client.js", () => ({
  apiGetTyped: vi.fn((path: string) => {
    expect(path).toBe("/api/tabs");
    listing.calls++;
    return Promise.resolve(listing.answers.shift() ?? null);
  }),
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  apiGet: vi.fn(),
}));

const {
  registerTabsTarget,
  tabsVersion,
  markLocalOp,
  ingestTabsChanged,
  listTabs,
  whenOpen,
  permute,
  _resetTabsSyncForTest,
} = await import("./tabs-sync.js");

// --- The fake target ---

interface Applied {
  delta: TabsChangedPayload;
  local: boolean;
}

/** A projection reduced to what the rules actually consult: a set of ids, plus a
 *  log of everything it was told. Membership is maintained by hand from the
 *  frames, exactly as tabs.ts will, so `whenOpen` is exercised against a real
 *  transition rather than a stub that always answers true. */
class FakeTarget {
  ids: string[] = [];
  applied: Applied[] = [];
  resets: string[][] = [];

  reset = (tabs: readonly TabSubject[]): void => {
    this.ids = tabs.map((t) => t.id);
    this.resets.push([...this.ids]);
  };

  apply = (delta: TabsChangedPayload, local: boolean): void => {
    this.applied.push({ delta, local });
    if (delta.changed !== undefined && !this.ids.includes(delta.changed.id)) {
      this.ids.push(delta.changed.id);
    }
    for (const id of delta.removed_ids ?? []) {
      this.ids = this.ids.filter((held) => held !== id);
    }
    if (delta.order !== undefined) {
      this.ids = permute(this.ids, (id) => id, delta.order);
    }
  };

  has = (id: string): boolean => this.ids.includes(id);
}

function subject(id: string, over: Partial<TabSubject> = {}): TabSubject {
  return { id, kind: "chat", ref: `c-${id}`, parent: "", pinned: false, owns: true, ...over };
}

function list(version: number, ...ids: string[]): TabList {
  return { tabs: ids.map((id) => subject(id)), version };
}

/** Let the drain's microtasks run. Every path through it is either synchronous or
 *  a single awaited fetch, so one macrotask turn is enough and a fixed sleep would
 *  only make the suite slower. */
async function settle(): Promise<void> {
  await new Promise((r) => setTimeout(r, 0));
}

let t: FakeTarget;

beforeEach(() => {
  _resetTabsSyncForTest();
  listing.answers = [];
  listing.calls = 0;
  t = new FakeTarget();
  registerTabsTarget(t);
});

// --- The three version rules ---

describe("the version rules", () => {
  it("applies a frame exactly one past the local version", async () => {
    ingestTabsChanged({ changed: subject("a"), order: ["a"], version: 1 });
    await settle();
    expect(t.applied).toHaveLength(1);
    expect(tabsVersion()).toBe(1);
    expect(t.has("a")).toBe(true);
  });

  it("ignores a frame at or below the local version", async () => {
    ingestTabsChanged({ changed: subject("a"), order: ["a"], version: 1 });
    ingestTabsChanged({ changed: subject("b"), order: ["a", "b"], version: 2 });
    await settle();
    expect(tabsVersion()).toBe(2);

    // A redelivery of both, which is what a reconnect replay looks like.
    ingestTabsChanged({ changed: subject("a"), order: ["a"], version: 1 });
    ingestTabsChanged({ removed_ids: ["b"], order: ["a"], version: 2 });
    await settle();
    expect(t.applied).toHaveLength(2);
    expect(t.has("b")).toBe(true);
    expect(tabsVersion()).toBe(2);
  });

  it("re-lists on a skipped version instead of applying the frame", async () => {
    listing.answers.push(list(7, "a", "b", "c"));
    ingestTabsChanged({ changed: subject("z"), order: ["z"], version: 6 });
    await settle();
    // The frame itself never reached the projection: a delta on top of a set we
    // do not hold cannot be applied, and its `order` names one tab out of three.
    expect(t.applied).toHaveLength(0);
    expect(listing.calls).toBe(1);
    expect(t.ids).toEqual(["a", "b", "c"]);
    expect(tabsVersion()).toBe(7);
  });

  it("keeps queued frames across a gap and applies the ones the snapshot misses", async () => {
    listing.answers.push(list(7, "a"));
    // 6 is a gap from 0. 7 is covered by the snapshot. 8 is genuinely newer.
    ingestTabsChanged({ changed: subject("gap"), order: ["gap"], version: 6 });
    ingestTabsChanged({ changed: subject("covered"), order: ["covered"], version: 7 });
    ingestTabsChanged({ changed: subject("after"), order: ["a", "after"], version: 8 });
    await settle();
    expect(listing.calls).toBe(1);
    // Only the frame past the snapshot applied. The other two fell out through
    // rule 1, which is what makes NOT clearing the queue safe.
    expect(t.applied.map((a) => a.delta.version)).toEqual([8]);
    expect(t.ids).toEqual(["a", "after"]);
    expect(tabsVersion()).toBe(8);
  });

  it("does not advance the version when the projection is told nothing", async () => {
    // A frame the rules discard must leave the watermark alone AND must not reach
    // the projection. Asserting only on the watermark is not enough: an
    // implementation that advances first and tests afterwards leaves the number
    // right and applies the frame anyway, so the apply log is what pins it.
    ingestTabsChanged({ changed: subject("a"), order: ["a"], version: 1 });
    await settle();
    ingestTabsChanged({ changed: subject("b"), order: ["a", "b"], version: 1 });
    await settle();
    expect(tabsVersion()).toBe(1);
    ingestTabsChanged({ changed: subject("b"), order: ["a", "b"], version: 2 });
    await settle();
    expect(t.applied.map((a) => a.delta.version)).toEqual([1, 2]);
    expect(t.has("b")).toBe(true);
  });
});

// --- Arrival order ---

describe("the apply queue", () => {
  it("applies frames in arrival order through one drain", async () => {
    for (let v = 1; v <= 5; v++) {
      ingestTabsChanged({ changed: subject(`t${String(v)}`), version: v });
    }
    await settle();
    expect(t.applied.map((a) => a.delta.version)).toEqual([1, 2, 3, 4, 5]);
  });

  it("re-tests a second gap frame after the first re-list instead of listing twice", async () => {
    // Both frames are past local+1 on arrival, but the drain is SERIALIZED: the
    // first gap's re-list completes before the second frame is dequeued, so the
    // second falls out through rule 1. This pins the serialization, not the
    // coalescing — see the test below for that.
    listing.answers.push(list(9, "a"));
    listing.answers.push(list(9, "a"));
    ingestTabsChanged({ changed: subject("x"), version: 5 });
    ingestTabsChanged({ changed: subject("y"), version: 6 });
    await settle();
    expect(listing.calls).toBe(1);
    expect(t.resets).toHaveLength(1);
  });

  it("joins a re-list already in flight rather than issuing a second GET", async () => {
    // Boot's read and a gap-driven re-list are the pair that genuinely overlap:
    // the strip is assembled over several awaits, and an event arriving mid-boot
    // detects a gap because boot has not established a version yet. Two GETs would
    // put two snapshots in a race to reset the same projection.
    let release = (): void => {
      /* replaced by the promise below */
    };
    const held = new Promise<void>((r) => {
      release = r;
    });
    const { apiGetTyped } = await import("./api-client.js");
    vi.mocked(apiGetTyped).mockImplementationOnce(async () => {
      listing.calls++;
      await held;
      return list(9, "a") as never;
    });

    const boot = listTabs();
    ingestTabsChanged({ changed: subject("x"), version: 5 });
    await settle();
    expect(listing.calls).toBe(1);
    release();
    await boot;
    await settle();
    expect(t.resets).toHaveLength(1);
  });

  it("evaluates a frame that arrives DURING a re-list against the listed version", async () => {
    let release = (): void => {
      /* replaced by the promise below */
    };
    const held = new Promise<void>((r) => {
      release = r;
    });
    listing.answers.push(null); // placeholder, replaced below
    const { apiGetTyped } = await import("./api-client.js");
    vi.mocked(apiGetTyped).mockImplementationOnce(async () => {
      listing.calls++;
      await held;
      return list(4, "a") as never;
    });

    ingestTabsChanged({ changed: subject("gap"), version: 3 });
    await settle();
    expect(listing.calls).toBe(1);
    // Arrives while the GET is in the air, and is only decidable afterwards.
    ingestTabsChanged({ changed: subject("late"), order: ["a", "late"], version: 5 });
    release();
    await settle();
    expect(t.ids).toEqual(["a", "late"]);
    expect(tabsVersion()).toBe(5);
  });
});

// --- The stale snapshot ---

describe("a stale snapshot", () => {
  it("does not remove a tab a committed frame already gave us", async () => {
    // The open commits version 4 here; the GET that was already in flight
    // describes version 3 and does not name the tab.
    ingestTabsChanged({ changed: subject("mine"), order: ["mine"], version: 1 });
    ingestTabsChanged({ changed: subject("b"), order: ["mine", "b"], version: 2 });
    ingestTabsChanged({ changed: subject("c"), order: ["mine", "b", "c"], version: 3 });
    ingestTabsChanged({ changed: subject("d"), order: ["mine", "b", "c", "d"], version: 4 });
    await settle();
    expect(tabsVersion()).toBe(4);

    listing.answers.push(list(3, "b", "c"));
    await listTabs();
    expect(t.has("mine")).toBe(true);
    expect(t.has("d")).toBe(true);
    expect(t.resets).toHaveLength(0);
    expect(tabsVersion()).toBe(4);
  });

  it("adopts a snapshot at or above the local version", async () => {
    ingestTabsChanged({ changed: subject("a"), order: ["a"], version: 1 });
    await settle();
    listing.answers.push(list(1, "a", "elsewhere"));
    await listTabs();
    expect(t.ids).toEqual(["a", "elsewhere"]);
  });

  it("leaves the projection alone when the collection cannot be read", async () => {
    ingestTabsChanged({ changed: subject("a"), order: ["a"], version: 1 });
    await settle();
    listing.answers.push(null);
    await listTabs();
    expect(t.ids).toEqual(["a"]);
    expect(tabsVersion()).toBe(1);
  });
});

// --- Order is a permutation ---

describe("order is a permutation, never a membership statement", () => {
  it("keeps a tab the order does not name, at the END of the strip", () => {
    expect(permute(["a", "b", "unnamed", "c"], (id) => id, ["c", "a", "b"])).toEqual([
      "c",
      "a",
      "b",
      "unnamed",
    ]);
  });

  it("never puts an unnamed tab at position 0", () => {
    // The failure this pins: an implementation that seeds `next` with the
    // leftovers, or that appends the named ids to the existing array, lands the
    // unnamed one first — which puts a tab the server has said nothing about
    // ahead of the strip the reader arranged.
    const out = permute(["unnamed", "a", "b"], (id) => id, ["b", "a"]);
    expect(out[0]).not.toBe("unnamed");
    expect(out).toEqual(["b", "a", "unnamed"]);
  });

  it("keeps several unnamed tabs in the relative order they already had", () => {
    expect(permute(["x", "a", "y", "b"], (id) => id, ["b", "a"])).toEqual(["b", "a", "x", "y"]);
  });

  it("ignores an id the order names but the projection does not hold", () => {
    expect(permute(["a"], (id) => id, ["ghost", "a"])).toEqual(["a"]);
  });

  it("survives an applied frame whose order omits a tab we hold", async () => {
    ingestTabsChanged({ changed: subject("a"), order: ["a"], version: 1 });
    ingestTabsChanged({ changed: subject("b"), order: ["a", "b"], version: 2 });
    await settle();
    // Another device reorders while holding a set that does not include "b" —
    // which cannot happen against a correct server, and is exactly why the client
    // must not read it as a close.
    ingestTabsChanged({ order: ["a"], version: 3 });
    await settle();
    expect(t.has("b")).toBe(true);
    expect(t.ids).toEqual(["a", "b"]);
  });
});

// --- Interleavings ---

describe("whenOpen, both interleavings", () => {
  it("resolves at once when the tab is already held (event-first, and created:false)", async () => {
    ingestTabsChanged({ changed: subject("a"), order: ["a"], version: 1 });
    await settle();
    let resolved = false;
    void whenOpen("a").then(() => {
      resolved = true;
    });
    await settle();
    expect(resolved).toBe(true);
  });

  it("waits for the frame when the response landed first", async () => {
    let resolved = false;
    void whenOpen("a").then(() => {
      resolved = true;
    });
    await settle();
    expect(resolved).toBe(false);
    ingestTabsChanged({ changed: subject("a"), order: ["a"], version: 1 });
    await settle();
    expect(resolved).toBe(true);
  });

  it("resolves a waiter that a SNAPSHOT satisfies rather than an event", async () => {
    // The gap path has to release waiters too, or an open whose frame was the one
    // that got dropped hangs until its timeout even though the re-list brought the
    // tab in.
    let resolved = false;
    void whenOpen("a").then(() => {
      resolved = true;
    });
    listing.answers.push(list(3, "a"));
    await listTabs();
    await settle();
    expect(resolved).toBe(true);
  });

  it("resolves rather than rejects when no frame ever arrives", async () => {
    vi.useFakeTimers();
    try {
      let resolved = false;
      void whenOpen("never", 50).then(() => {
        resolved = true;
      });
      await vi.advanceTimersByTimeAsync(60);
      expect(resolved).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });

  it("resolves every caller waiting on one id", async () => {
    const seen: number[] = [];
    void whenOpen("a").then(() => seen.push(1));
    void whenOpen("a").then(() => seen.push(2));
    ingestTabsChanged({ changed: subject("a"), order: ["a"], version: 1 });
    await settle();
    expect(seen).toEqual([1, 2]);
  });
});

// --- Op correlation ---

describe("op correlation", () => {
  it("marks a frame local when this device minted its op", async () => {
    markLocalOp("op-1");
    ingestTabsChanged({ removed_ids: ["a"], order: [], version: 1, op_id: "op-1" });
    await settle();
    expect(t.applied[0]?.local).toBe(true);
  });

  it("marks another device's frame remote", async () => {
    markLocalOp("op-mine");
    ingestTabsChanged({ removed_ids: ["a"], order: [], version: 1, op_id: "op-theirs" });
    await settle();
    expect(t.applied[0]?.local).toBe(false);
  });

  it("marks a frame with no op remote", async () => {
    ingestTabsChanged({ removed_ids: ["a"], order: [], version: 1 });
    await settle();
    expect(t.applied[0]?.local).toBe(false);
  });

  it("claims local authorship at most once per op", async () => {
    // One frame per committed mutation, so a second frame carrying the same op is
    // a duplicate. Claiming it twice would re-run a teardown dispatch.
    markLocalOp("op-1");
    ingestTabsChanged({ removed_ids: ["a"], order: [], version: 1, op_id: "op-1" });
    ingestTabsChanged({ removed_ids: ["b"], order: [], version: 2, op_id: "op-1" });
    await settle();
    expect(t.applied.map((a) => a.local)).toEqual([true, false]);
  });
});
