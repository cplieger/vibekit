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

import { describe, it, expect, beforeEach, afterEach, vi, type Mock } from "vitest";
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
  permute,
  beginAdopt,
  adoptCommitted,
  beginRemove,
  removeCommitted,
  opFailed,
  opTimedOut,
  removesPending,
  setOnRemovesSettled,
  _resetTabsSyncForTest,
} = await import("./tabs-sync.js");

// --- The fake target ---

interface Applied {
  delta: TabsChangedPayload;
  local: boolean;
}

/** A projection reduced to what the rules actually consult: a set of ids, plus a
 *  log of everything it was told. Membership is maintained by hand from the
 *  frames, exactly as tabs.ts does, so the overlay rules are exercised against a
 *  real transition rather than a stub. */
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

// --- The pending-op machine ---

/** A remove op's callbacks, spied. The capture itself is the caller's business
 *  (task: the optimistic close), so the machine sees ids and closures only. */
function removeSpies(): { onConfirm: Mock<() => void>; rollback: Mock<() => void> } {
  return { onConfirm: vi.fn<() => void>(), rollback: vi.fn<() => void>() };
}

describe("pending adopt: the transition table, both orders", () => {
  it("response-first: paints nothing here, holds for the frame, retires on its echo", async () => {
    // Local watermark at 1 so the committed version (2) is genuinely ahead.
    ingestTabsChanged({ changed: subject("a"), order: ["a"], version: 1 });
    await settle();

    markLocalOp("op-open");
    beginAdopt("op-open");
    // Transition 3: the response commits at v2; the frame has not landed, so the
    // op holds in confirmed-awaiting-frame — and the WATERMARK DOES NOT MOVE:
    // the version is carried to the machine, never adopted by the event rules.
    adoptCommitted("op-open", subject("b"), 2, true);
    expect(tabsVersion()).toBe(1);

    // The echo. Transition 2 retires the op; the frame applies normally.
    ingestTabsChanged({ changed: subject("b"), order: ["a", "b"], version: 2, op_id: "op-open" });
    await settle();
    expect(t.applied.at(-1)?.local).toBe(true);
    expect(tabsVersion()).toBe(2);

    // Retired: a stale-but-adoptable snapshot no longer merges the subject back,
    // which is the observable difference between pending and settled.
    listing.answers.push(list(2, "a"));
    await listTabs();
    expect(t.resets.at(-1)).toEqual(["a"]);
  });

  it("frame-first: the echo retires the op, and the late response is ignored", async () => {
    markLocalOp("op-open");
    beginAdopt("op-open");
    ingestTabsChanged({ changed: subject("b"), order: ["b"], version: 1, op_id: "op-open" });
    await settle();
    expect(t.applied[0]?.local).toBe(true);

    // The response lands after the frame. Retired-op immunity: filling a settled
    // op must not resurrect it as an overlay source.
    adoptCommitted("op-open", subject("b"), 1, true);
    listing.answers.push(list(1));
    await listTabs();
    // The snapshot omits "b" at the local version, and nothing merges it back.
    expect(t.resets.at(-1)).toEqual([]);
  });

  it("retires immediately when the frame already advanced the watermark past the commit", async () => {
    // Event-first without correlation: the frame carried no op this device
    // recognizes (or correlation was consumed by a duplicate), and the response
    // arrives with committedVersion <= local. Transition 3's immediate arm.
    beginAdopt("op-open");
    ingestTabsChanged({ changed: subject("b"), order: ["b"], version: 1 });
    await settle();
    adoptCommitted("op-open", subject("b"), 1, true);

    // Settled: a version-1 snapshot without "b" is adopted un-overlaid.
    listing.answers.push(list(1));
    await listTabs();
    expect(t.resets.at(-1)).toEqual([]);
  });

  it("retires immediately on created:false, because no frame is coming", async () => {
    ingestTabsChanged({ changed: subject("a"), order: ["a"], version: 1 });
    await settle();
    beginAdopt("op-open");
    // An idempotent open answers the CURRENT version with created:false. The
    // committed version equals local here, but the semantic arm is what this
    // pins: nothing was committed, so nothing will ever correlate.
    adoptCommitted("op-open", subject("a"), 1, false);
    listing.answers.push(list(1, "a"));
    await listTabs();
    expect(t.resets.at(-1)).toEqual(["a"]);
  });

  it("absorbs an uncorrelated frame that reaches the committed version (transition 4)", async () => {
    // The echo lost its correlation (its op_id claimed by a duplicate, or the
    // dispatch deduped onto another caller's), but the watermark reaches the
    // committed version anyway: the mutation is part of what the projection now
    // holds, so the op is absorbed — and for a remove that is OBSERVABLE, because
    // absorption is what releases the deferred teardown.
    const { onConfirm, rollback } = removeSpies();
    beginRemove("op-close", { id: "a", capturedTabIDs: ["a"], onConfirm, rollback });
    removeCommitted("op-close", ["a"], 2);
    expect(onConfirm).not.toHaveBeenCalled();

    ingestTabsChanged({ changed: subject("x"), order: ["x"], version: 1 });
    ingestTabsChanged({ removed_ids: ["a"], order: ["x"], version: 2, op_id: "op-other" });
    await settle();
    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(rollback).not.toHaveBeenCalled();
    expect(removesPending()).toBe(false);
  });

  it("merges an adopted subject back into a snapshot from before the commit", async () => {
    // The re-list race: the GET was in flight when the open committed v2, so the
    // answer describes v1 — at or above local (adoptable), below the commit. The
    // adopted row must survive it.
    ingestTabsChanged({ changed: subject("a"), order: ["a"], version: 1 });
    await settle();
    beginAdopt("op-open");
    adoptCommitted("op-open", subject("b"), 2, true);

    listing.answers.push(list(1, "a"));
    await listTabs();
    expect(t.resets.at(-1)).toEqual(["a", "b"]);
    // The snapshot did not advance past the commit, so the op is STILL pending
    // and the next frame settles it the ordinary way.
    ingestTabsChanged({ changed: subject("b"), order: ["a", "b"], version: 2, op_id: "op-open" });
    await settle();
    expect(tabsVersion()).toBe(2);
  });

  it("does not advance the watermark from a response, ever", async () => {
    beginAdopt("op-open");
    adoptCommitted("op-open", subject("b"), 7, true);
    expect(tabsVersion()).toBe(0);
    // A frame at v1 is still the next mutation — a response-adopted 7 would have
    // made it read as stale, which is the defect the rule exists to prevent.
    ingestTabsChanged({ changed: subject("a"), order: ["a"], version: 1 });
    await settle();
    expect(t.applied).toHaveLength(1);
    expect(tabsVersion()).toBe(1);
  });
});

describe("pending remove: the transition table, both orders", () => {
  it("response-first: holds for the frame, then confirms once on its echo", async () => {
    ingestTabsChanged({ changed: subject("a"), order: ["a"], version: 1 });
    await settle();
    const { onConfirm, rollback } = removeSpies();
    markLocalOp("op-close");
    beginRemove("op-close", { id: "a", capturedTabIDs: ["a"], onConfirm, rollback });

    removeCommitted("op-close", ["a"], 2);
    // Committed but not yet framed: the teardown stays deferred.
    expect(onConfirm).not.toHaveBeenCalled();
    expect(removesPending()).toBe(true);

    ingestTabsChanged({ removed_ids: ["a"], order: [], version: 2, op_id: "op-close" });
    await settle();
    expect(t.applied.at(-1)?.local).toBe(true);
    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(rollback).not.toHaveBeenCalled();
    expect(removesPending()).toBe(false);
  });

  it("frame-first: the echo confirms in awaiting-response, and the late response is ignored", async () => {
    ingestTabsChanged({ changed: subject("a"), order: ["a"], version: 1 });
    await settle();
    const { onConfirm, rollback } = removeSpies();
    markLocalOp("op-close");
    beginRemove("op-close", { id: "a", capturedTabIDs: ["a"], onConfirm, rollback });

    ingestTabsChanged({ removed_ids: ["a"], order: [], version: 2, op_id: "op-close" });
    await settle();
    expect(onConfirm).toHaveBeenCalledTimes(1);

    // The response arrives after the frame: retired-op immunity, no second run.
    removeCommitted("op-close", ["a"], 2);
    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(rollback).not.toHaveBeenCalled();
  });

  it("confirms a close whose response the frame already covered (localVersion >= committedVersion)", async () => {
    const { onConfirm, rollback } = removeSpies();
    beginRemove("op-close", { id: "a", capturedTabIDs: ["a"], onConfirm, rollback });
    // The echo lost its correlation (swept, or claimed by a duplicate): the
    // frame still advances the watermark to the committed version, and the
    // response's immediate arm settles the op.
    ingestTabsChanged({ removed_ids: ["a"], order: [], version: 1 });
    await settle();
    removeCommitted("op-close", ["a"], 1);
    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(rollback).not.toHaveBeenCalled();
  });

  it("reads closed:[] as SEMANTIC confirmation of absence, however far behind the client is", async () => {
    // The client-behind no-frame close: another device closed the tab; the
    // server sits at v+1 while this client sits at v. The close commits nothing,
    // so no frame is coming — and the empty list is the whole answer, whatever
    // any version comparison says.
    ingestTabsChanged({ changed: subject("a"), order: ["a"], version: 1 });
    await settle();
    const { onConfirm, rollback } = removeSpies();
    beginRemove("op-close", { id: "a", capturedTabIDs: ["a"], onConfirm, rollback });
    removeCommitted("op-close", [], 2);
    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(rollback).not.toHaveBeenCalled();
    expect(removesPending()).toBe(false);
    // And the watermark still has not moved: only an event advances it.
    expect(tabsVersion()).toBe(1);
  });

  it("rolls back on a DEFINITIVE failure, exactly once", async () => {
    const { onConfirm, rollback } = removeSpies();
    beginRemove("op-close", { id: "a", capturedTabIDs: ["a"], onConfirm, rollback });
    opFailed("op-close");
    expect(rollback).toHaveBeenCalledTimes(1);
    expect(onConfirm).not.toHaveBeenCalled();
    expect(removesPending()).toBe(false);
    // Retired: a repeat failure signal does nothing.
    opFailed("op-close");
    expect(rollback).toHaveBeenCalledTimes(1);
  });

  it("never rolls back an op a frame already confirmed (retired-op immunity)", async () => {
    ingestTabsChanged({ changed: subject("a"), order: ["a"], version: 1 });
    await settle();
    const { onConfirm, rollback } = removeSpies();
    markLocalOp("op-close");
    beginRemove("op-close", { id: "a", capturedTabIDs: ["a"], onConfirm, rollback });
    ingestTabsChanged({ removed_ids: ["a"], order: [], version: 2, op_id: "op-close" });
    await settle();
    expect(onConfirm).toHaveBeenCalledTimes(1);

    // The dispatch reports failure AFTER the frame confirmed the mutation — a
    // retry raced the commit, or the response was lost and re-answered an error.
    // Rolling back now would revert a close the collection holds.
    opFailed("op-close");
    expect(rollback).not.toHaveBeenCalled();
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });
});

describe("a remove that times out: the verifying state", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  /** Drain microtasks under fake timers (settle() uses a real setTimeout). */
  async function flushMicrotasks(): Promise<void> {
    await vi.advanceTimersByTimeAsync(0);
  }

  it("neither restores nor retires: the removal stays applied and the op verifies", async () => {
    const { onConfirm, rollback } = removeSpies();
    beginRemove("op-close", { id: "a", capturedTabIDs: ["a"], onConfirm, rollback });
    opTimedOut("op-close");
    expect(rollback).not.toHaveBeenCalled();
    expect(onConfirm).not.toHaveBeenCalled();
    expect(removesPending()).toBe(true);
    await flushMicrotasks();
    expect(listing.calls).toBe(0);
  });

  it("re-lists once per backoff tick while the list keeps failing", async () => {
    const { onConfirm, rollback } = removeSpies();
    beginRemove("op-close", { id: "a", capturedTabIDs: ["a"], onConfirm, rollback });
    opTimedOut("op-close");

    // Every answer is null (unreachable). Each tick asks exactly once, and a
    // failed list settles nothing.
    await vi.advanceTimersByTimeAsync(1_000);
    expect(listing.calls).toBe(1);
    await vi.advanceTimersByTimeAsync(2_000);
    expect(listing.calls).toBe(2);
    await vi.advanceTimersByTimeAsync(4_000);
    expect(listing.calls).toBe(3);
    expect(removesPending()).toBe(true);
    expect(rollback).not.toHaveBeenCalled();
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it("restores when an authoritative list still holds the row", async () => {
    ingestTabsChanged({ changed: subject("a"), order: ["a"], version: 1 });
    await flushMicrotasks();
    const { onConfirm, rollback } = removeSpies();
    beginRemove("op-close", { id: "a", capturedTabIDs: ["a"], onConfirm, rollback });
    opTimedOut("op-close");

    listing.answers.push(list(1, "a"));
    await vi.advanceTimersByTimeAsync(1_000);
    expect(rollback).toHaveBeenCalledTimes(1);
    expect(onConfirm).not.toHaveBeenCalled();
    expect(removesPending()).toBe(false);
    // The row is back through the reset itself — restore rides the snapshot.
    expect(t.resets.at(-1)).toEqual(["a"]);
    // Timer canceled on settlement: no further re-list ever fires.
    const calls = listing.calls;
    await vi.advanceTimersByTimeAsync(60_000);
    expect(listing.calls).toBe(calls);
  });

  it("confirms silently when an authoritative list no longer holds the row", async () => {
    ingestTabsChanged({ changed: subject("a"), order: ["a"], version: 1 });
    await flushMicrotasks();
    const { onConfirm, rollback } = removeSpies();
    beginRemove("op-close", { id: "a", capturedTabIDs: ["a"], onConfirm, rollback });
    opTimedOut("op-close");

    listing.answers.push(list(2, "b"));
    await vi.advanceTimersByTimeAsync(1_000);
    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(rollback).not.toHaveBeenCalled();
    expect(removesPending()).toBe(false);
  });

  it("is settled by a matching frame ahead of any tick", async () => {
    ingestTabsChanged({ changed: subject("a"), order: ["a"], version: 1 });
    await flushMicrotasks();
    const { onConfirm, rollback } = removeSpies();
    markLocalOp("op-close");
    beginRemove("op-close", { id: "a", capturedTabIDs: ["a"], onConfirm, rollback });
    opTimedOut("op-close");

    ingestTabsChanged({ removed_ids: ["a"], order: [], version: 2, op_id: "op-close" });
    await flushMicrotasks();
    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(rollback).not.toHaveBeenCalled();
    // Settlement canceled the verify timer: no re-list ever runs.
    await vi.advanceTimersByTimeAsync(60_000);
    expect(listing.calls).toBe(0);
  });

  it("ignores a STALE list: it is not authoritative evidence in either direction", async () => {
    // The projection is ahead of the snapshot (mechanism 3 discards it). A
    // discarded list must not restore OR confirm — the op keeps verifying.
    ingestTabsChanged({ changed: subject("a"), order: ["a"], version: 1 });
    ingestTabsChanged({ changed: subject("b"), order: ["a", "b"], version: 2 });
    await flushMicrotasks();
    const { onConfirm, rollback } = removeSpies();
    beginRemove("op-close", { id: "a", capturedTabIDs: ["a"], onConfirm, rollback });
    opTimedOut("op-close");

    listing.answers.push(list(1, "a"));
    await vi.advanceTimersByTimeAsync(1_000);
    expect(rollback).not.toHaveBeenCalled();
    expect(onConfirm).not.toHaveBeenCalled();
    expect(removesPending()).toBe(true);
  });

  it("keeps one timer per verifying remove, each settled independently", async () => {
    ingestTabsChanged({ changed: subject("a"), order: ["a"], version: 1 });
    ingestTabsChanged({ changed: subject("b"), order: ["a", "b"], version: 2 });
    await flushMicrotasks();
    const a = removeSpies();
    const b = removeSpies();
    beginRemove("op-a", {
      id: "a",
      capturedTabIDs: ["a"],
      onConfirm: a.onConfirm,
      rollback: a.rollback,
    });
    beginRemove("op-b", {
      id: "b",
      capturedTabIDs: ["b"],
      onConfirm: b.onConfirm,
      rollback: b.rollback,
    });
    opTimedOut("op-a");
    opTimedOut("op-b");

    // One authoritative list settles BOTH: "a" still open (restore), "b" gone
    // (confirm). Two answers queued because the two ticks each issue a GET and
    // the first retires both ops before the second resolves.
    listing.answers.push(list(3, "a"));
    listing.answers.push(list(3, "a"));
    await vi.advanceTimersByTimeAsync(1_000);
    expect(a.rollback).toHaveBeenCalledTimes(1);
    expect(b.onConfirm).toHaveBeenCalledTimes(1);
    expect(removesPending()).toBe(false);
  });
});

describe("the overlay rules", () => {
  it("suppresses a changed-upsert for a tab id a pending remove captured", async () => {
    // A pin committed before the close, delivered after the gesture: the frame
    // must not resurrect the visually removed row. The version still advances —
    // only the upsert is withheld.
    ingestTabsChanged({ changed: subject("a"), order: ["a"], version: 1 });
    await settle();
    const { onConfirm, rollback } = removeSpies();
    beginRemove("op-close", { id: "a", capturedTabIDs: ["a"], onConfirm, rollback });

    ingestTabsChanged({ changed: subject("a", { pinned: true }), order: ["a"], version: 2 });
    await settle();
    expect(tabsVersion()).toBe(2);
    expect(t.applied.at(-1)?.delta.changed).toBeUndefined();
    // The frame's other statements pass through untouched.
    expect(t.applied.at(-1)?.delta.order).toEqual(["a"]);
  });

  it("keys the suppression by TAB ID, never by ref: a remote reopen paints unsuppressed", async () => {
    // A remote device closed the chat and reopened it: the reopen mints a NEW
    // tab id for the SAME (kind, ref). Suppression keyed by ref would blank the
    // new row; keyed by the captured ids it paints.
    ingestTabsChanged({ changed: subject("a"), order: ["a"], version: 1 });
    await settle();
    const { onConfirm, rollback } = removeSpies();
    beginRemove("op-close", { id: "a", capturedTabIDs: ["a"], onConfirm, rollback });

    ingestTabsChanged({
      changed: { ...subject("fresh"), ref: "c-a" },
      order: ["fresh"],
      version: 2,
    });
    await settle();
    expect(t.applied.at(-1)?.delta.changed?.id).toBe("fresh");
    expect(t.has("fresh")).toBe(true);
  });

  it("lets a pending adopt OVERRIDE the suppression for its ref: open wins locally", async () => {
    // The local reopen inside the window: the server has not processed the close
    // yet, so the reopen answers the SAME tab id the remove captured. The adopt
    // op is what says "the user wants this row back".
    ingestTabsChanged({ changed: subject("a"), order: ["a"], version: 1 });
    await settle();
    const { onConfirm, rollback } = removeSpies();
    beginRemove("op-close", { id: "a", capturedTabIDs: ["a"], onConfirm, rollback });
    beginAdopt("op-reopen");
    adoptCommitted("op-reopen", subject("a"), 3, true);

    ingestTabsChanged({ changed: subject("a"), order: ["a"], version: 2 });
    await settle();
    expect(t.applied.at(-1)?.delta.changed?.id).toBe("a");
  });

  it("filters a pending remove's captured ids out of a snapshot", async () => {
    // A re-list raced the close: the GET answers a set from before the commit,
    // and adopting it verbatim would resurrect the removed rows.
    ingestTabsChanged({ changed: subject("a"), order: ["a"], version: 1 });
    await settle();
    const { onConfirm, rollback } = removeSpies();
    beginRemove("op-close", { id: "a", capturedTabIDs: ["a", "child"], onConfirm, rollback });

    listing.answers.push(list(1, "a", "child", "b"));
    await listTabs();
    expect(t.resets.at(-1)).toEqual(["b"]);
  });

  it("stops filtering once the snapshot reaches the remove's committed version", async () => {
    // A snapshot AT or PAST the commit already reflects the close server-side,
    // so the overlay has nothing left to hide — and the op is absorbed by it
    // (transition 4 over a snapshot).
    ingestTabsChanged({ changed: subject("a"), order: ["a"], version: 1 });
    await settle();
    const { onConfirm, rollback } = removeSpies();
    beginRemove("op-close", { id: "a", capturedTabIDs: ["a"], onConfirm, rollback });
    removeCommitted("op-close", ["a"], 2);

    listing.answers.push(list(2, "b"));
    await listTabs();
    expect(t.resets.at(-1)).toEqual(["b"]);
    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(removesPending()).toBe(false);
  });
});

describe("the removes-settled notification", () => {
  it("fires when the LAST pending remove settles, on any settlement path", async () => {
    const settled = vi.fn();
    setOnRemovesSettled(settled);
    const a = removeSpies();
    const b = removeSpies();
    beginRemove("op-a", {
      id: "a",
      capturedTabIDs: ["a"],
      onConfirm: a.onConfirm,
      rollback: a.rollback,
    });
    beginRemove("op-b", {
      id: "b",
      capturedTabIDs: ["b"],
      onConfirm: b.onConfirm,
      rollback: b.rollback,
    });

    // First settles (semantic confirm) — one remove still pending, no signal.
    removeCommitted("op-a", [], 5);
    expect(settled).not.toHaveBeenCalled();
    // Second settles (definitive failure) — the set is empty, the slot fires.
    opFailed("op-b");
    expect(settled).toHaveBeenCalledTimes(1);
  });

  it("does not fire for an adopt settling", () => {
    const settled = vi.fn();
    setOnRemovesSettled(settled);
    beginAdopt("op-open");
    adoptCommitted("op-open", subject("a"), 1, false);
    expect(settled).not.toHaveBeenCalled();
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

  it("reads a frame matching a pending op as local, mark or no mark", async () => {
    // The machine's map is itself proof of local authorship: an op is only ever
    // registered at this device's own dispatch site.
    beginAdopt("op-create");
    ingestTabsChanged({ changed: subject("a"), order: ["a"], version: 1, op_id: "op-create" });
    await settle();
    expect(t.applied[0]?.local).toBe(true);
  });
});
