// @vitest-environment happy-dom
// Targeted tests for registry.ts performance changes (C5F2):
// tombstone eviction, Set-based listener iteration, pendingFor/recentLog
// filtering, _resetForTest completeness.
import { describe, it, expect, vi, beforeEach } from "vitest";
import {
  record,
  subscribe,
  recentLog,
  pendingFor,
  pendingCount,
  pendingForAny,
  isPending,
  _resetForTest,
} from "./registry.js";
import type { ActionInstance } from "./types.js";

function makeInstance(overrides: Partial<ActionInstance> = {}): ActionInstance {
  return {
    id: `id-${Math.random().toString(36).slice(2)}`,
    name: "test.action",
    status: "pending",
    args: {},
    dispatchedAt: Date.now(),
    startedAt: Date.now(),
    ...overrides,
  };
}

beforeEach(() => {
  _resetForTest();
});

// ===========================================================================
// Tombstone eviction + compaction
// ===========================================================================

describe("tombstone eviction", () => {
  it("evicts oldest non-pending entry when log exceeds MAX_LOG_SIZE (200)", () => {
    // Fill with 200 completed entries
    for (let i = 0; i < 200; i++) {
      record(makeInstance({ id: `a-${i}`, status: "success" }));
    }
    expect(recentLog()).toHaveLength(200);

    // 201st entry triggers eviction of the oldest non-pending
    record(makeInstance({ id: "overflow", status: "success" }));
    expect(recentLog()).toHaveLength(200);
  });

  it("preserves pending entries during soft eviction", () => {
    // Fill with 200 pending entries
    for (let i = 0; i < 200; i++) {
      record(makeInstance({ id: `p-${i}`, status: "pending" }));
    }
    // 201st entry: no non-pending to evict, so liveCount goes to 201
    record(makeInstance({ id: "extra", status: "pending" }));
    expect(recentLog()).toHaveLength(201);
    expect(pendingCount()).toBe(201);
  });

  it("hard cap (1000) force-evicts pending entries and decrements pendingCount", () => {
    // Fill to hard cap with pending entries
    for (let i = 0; i < 1000; i++) {
      record(makeInstance({ id: `h-${i}`, status: "pending" }));
    }
    expect(pendingCount()).toBe(1000);

    // 1001st triggers hard eviction of oldest (pending) entry
    record(makeInstance({ id: "hard-overflow", status: "pending" }));
    expect(pendingCount()).toBe(1000);
    expect(recentLog()).toHaveLength(1000);
  });

  it("compaction splices leading nulls when head > 256", () => {
    // Create 260 entries then evict them to create tombstones
    for (let i = 0; i < 260; i++) {
      record(makeInstance({ id: `c-${i}`, status: "success" }));
    }
    // Now add enough new entries to evict all 260 originals
    // Each new entry evicts one old one. After 260 more, all originals are gone.
    for (let i = 0; i < 260; i++) {
      record(makeInstance({ id: `d-${i}`, status: "success" }));
    }
    // recentLog should still work correctly (no stale references)
    const log = recentLog();
    expect(log.length).toBeLessThanOrEqual(200);
    // All entries should be non-null (tombstones filtered)
    for (const entry of log) {
      expect(entry).not.toBeNull();
    }
  });
});

// ===========================================================================
// Listener iteration safety (Set-based)
// ===========================================================================

describe("listener iteration", () => {
  it("listener can unsubscribe itself during notification", () => {
    const calls: string[] = [];
    const unsub = subscribe(() => {
      calls.push("self");
      unsub();
    });
    subscribe(() => calls.push("other"));

    record(makeInstance());
    expect(calls).toContain("self");
    expect(calls).toContain("other");

    // Second record: self-unsubscribed listener should not fire
    calls.length = 0;
    record(makeInstance());
    expect(calls).toEqual(["other"]);
  });

  it("listener that unsubscribes a later listener prevents it from firing", () => {
    // This documents the behavioral difference from the old spread approach.
    // With Set iteration, deleting a not-yet-visited entry skips it.
    const calls: string[] = [];
    let unsubB: (() => void) | undefined;

    subscribe(() => {
      calls.push("A");
      unsubB?.();
    });
    unsubB = subscribe(() => {
      calls.push("B");
    });

    record(makeInstance());
    // B may or may not fire depending on Set insertion order.
    // Per ES spec, Set iterates in insertion order, so A fires first,
    // removes B, and B is skipped.
    expect(calls).toEqual(["A"]);
  });

  it("listener added during iteration fires for the current event", () => {
    // This is the documented "acceptable semantic change" — new
    // subscribers added mid-iteration ARE visited by Set iteration.
    const calls: string[] = [];
    subscribe(() => {
      calls.push("first");
      // Add a new listener during iteration
      subscribe(() => calls.push("dynamic"));
    });

    record(makeInstance());
    // The dynamic listener fires for this same event
    expect(calls).toContain("first");
    expect(calls).toContain("dynamic");
  });

  it("throwing listener does not prevent other listeners from firing", () => {
    const calls: string[] = [];
    const consoleSpy = vi.spyOn(console, "error").mockImplementation(() => {});

    subscribe(() => { throw new Error("boom"); });
    subscribe(() => calls.push("survived"));

    record(makeInstance());
    expect(calls).toEqual(["survived"]);
    expect(consoleSpy).toHaveBeenCalledWith(
      "[actions] registry listener threw",
      expect.any(Error),
    );
    consoleSpy.mockRestore();
  });
});

// ===========================================================================
// pendingFor / pendingForAny with tombstones
// ===========================================================================

describe("pendingFor with tombstones", () => {
  it("skips tombstoned (evicted) entries", () => {
    // Fill log to trigger eviction
    for (let i = 0; i < 200; i++) {
      record(makeInstance({ id: `x-${i}`, name: "other", status: "success" }));
    }
    // Add a pending entry for our target action
    record(makeInstance({ id: "target", name: "my.action", status: "pending" }));
    // The pending entry should survive eviction (soft cap preserves pending)
    expect(pendingFor("my.action")).toHaveLength(1);
    expect(pendingFor("my.action")[0]!.id).toBe("target");
  });

  it("returns empty when all matching entries are completed", () => {
    record(makeInstance({ id: "done", name: "my.action", status: "success" }));
    expect(pendingFor("my.action")).toHaveLength(0);
  });
});

describe("pendingForAny", () => {
  it("returns true when at least one named action is pending", () => {
    record(makeInstance({ id: "a1", name: "chat.send", status: "pending" }));
    record(makeInstance({ id: "a2", name: "chat.delete", status: "success" }));
    expect(pendingForAny(["chat.send", "chat.delete"])).toBe(true);
  });

  it("returns false when no named actions are pending", () => {
    record(makeInstance({ id: "a1", name: "chat.send", status: "success" }));
    expect(pendingForAny(["chat.send"])).toBe(false);
  });

  it("returns false for empty names array", () => {
    record(makeInstance({ id: "a1", name: "chat.send", status: "pending" }));
    expect(pendingForAny([])).toBe(false);
  });
});

// ===========================================================================
// recentLog tombstone filtering
// ===========================================================================

describe("recentLog", () => {
  it("never returns null entries", () => {
    for (let i = 0; i < 250; i++) {
      record(makeInstance({ id: `r-${i}`, status: "success" }));
    }
    const log = recentLog();
    for (const entry of log) {
      expect(entry).not.toBeNull();
      expect(entry).not.toBeUndefined();
    }
  });

  it("returns entries in insertion order", () => {
    record(makeInstance({ id: "first", status: "success" }));
    record(makeInstance({ id: "second", status: "success" }));
    record(makeInstance({ id: "third", status: "success" }));
    const log = recentLog();
    expect(log.map(e => e.id)).toEqual(["first", "second", "third"]);
  });
});

// ===========================================================================
// _resetForTest completeness
// ===========================================================================

describe("_resetForTest", () => {
  it("clears all state including pendingCount and listeners", () => {
    const listener = vi.fn();
    subscribe(listener);
    record(makeInstance({ id: "pre", status: "pending" }));
    expect(pendingCount()).toBe(1);
    expect(listener).toHaveBeenCalledTimes(1);

    _resetForTest();
    listener.mockClear();

    expect(recentLog()).toHaveLength(0);
    expect(pendingCount()).toBe(0);
    expect(pendingFor("test.action")).toHaveLength(0);

    // Listener should have been cleared by reset
    record(makeInstance({ id: "post", status: "pending" }));
    expect(listener).not.toHaveBeenCalled();
  });
});

// ===========================================================================
// State transition correctness (pendingN tracking)
// ===========================================================================

describe("pendingN tracking", () => {
  it("increments on new pending, decrements on transition to success", () => {
    record(makeInstance({ id: "t1", status: "pending" }));
    expect(pendingCount()).toBe(1);

    record(makeInstance({ id: "t1", status: "success", completedAt: Date.now() }));
    expect(pendingCount()).toBe(0);
  });

  it("handles pending -> error transition", () => {
    record(makeInstance({ id: "t2", status: "pending" }));
    expect(pendingCount()).toBe(1);

    record(makeInstance({ id: "t2", status: "error", error: { message: "fail" } }));
    expect(pendingCount()).toBe(0);
  });

  it("handles pending -> cancelled transition", () => {
    record(makeInstance({ id: "t3", status: "pending" }));
    record(makeInstance({ id: "t3", status: "cancelled" }));
    expect(pendingCount()).toBe(0);
  });

  it("does not double-decrement on repeated terminal transitions", () => {
    record(makeInstance({ id: "t4", status: "pending" }));
    record(makeInstance({ id: "t4", status: "success" }));
    record(makeInstance({ id: "t4", status: "success" })); // idempotent
    expect(pendingCount()).toBe(0);
  });

  it("handles non-pending to pending transition (unusual but valid)", () => {
    record(makeInstance({ id: "t5", status: "success" }));
    expect(pendingCount()).toBe(0);
    // Transition back to pending (e.g. retry)
    record(makeInstance({ id: "t5", status: "pending" }));
    expect(pendingCount()).toBe(1);
  });
});

// ===========================================================================
// pendingByName index (isPending + integrity)
// ===========================================================================

describe("pendingByName index", () => {
  it("isPending returns true for pending action, false after completion", () => {
    record(makeInstance({ id: "p1", name: "chat.send", status: "pending" }));
    expect(isPending("chat.send")).toBe(true);

    record(makeInstance({ id: "p1", name: "chat.send", status: "success" }));
    expect(isPending("chat.send")).toBe(false);
  });

  it("isPending returns false for unknown action name", () => {
    expect(isPending("nonexistent")).toBe(false);
  });

  it("tracks multiple pending instances of the same name", () => {
    record(makeInstance({ id: "a1", name: "file.upload", status: "pending" }));
    record(makeInstance({ id: "a2", name: "file.upload", status: "pending" }));
    expect(isPending("file.upload")).toBe(true);
    expect(pendingFor("file.upload")).toHaveLength(2);

    record(makeInstance({ id: "a1", name: "file.upload", status: "success" }));
    expect(isPending("file.upload")).toBe(true);
    expect(pendingFor("file.upload")).toHaveLength(1);

    record(makeInstance({ id: "a2", name: "file.upload", status: "error", error: { message: "fail" } }));
    expect(isPending("file.upload")).toBe(false);
    expect(pendingFor("file.upload")).toHaveLength(0);
  });

  it("retry (terminal→pending) re-adds to pendingByName", () => {
    record(makeInstance({ id: "r1", name: "git.push", status: "pending" }));
    record(makeInstance({ id: "r1", name: "git.push", status: "error", error: { message: "timeout" } }));
    expect(isPending("git.push")).toBe(false);

    // Retry: same id goes back to pending
    record(makeInstance({ id: "r1", name: "git.push", status: "pending" }));
    expect(isPending("git.push")).toBe(true);
    expect(pendingFor("git.push")).toHaveLength(1);
    expect(pendingFor("git.push")[0]!.id).toBe("r1");
  });

  it("pending→pending re-record does not duplicate in index", () => {
    record(makeInstance({ id: "d1", name: "chat.send", status: "pending" }));
    record(makeInstance({ id: "d1", name: "chat.send", status: "pending" }));
    expect(pendingFor("chat.send")).toHaveLength(1);
    expect(pendingCount()).toBe(1);
  });

  it("hard-cap eviction removes from pendingByName", () => {
    for (let i = 0; i < 1000; i++) {
      record(makeInstance({ id: `hc-${i}`, name: "bulk.op", status: "pending" }));
    }
    expect(isPending("bulk.op")).toBe(true);

    // 1001st triggers hard eviction of oldest pending entry
    record(makeInstance({ id: "hc-overflow", name: "bulk.op", status: "pending" }));
    // The evicted entry (hc-0) should be removed from pendingByName
    expect(pendingCount()).toBe(1000);
    const pending = pendingFor("bulk.op");
    expect(pending.find(e => e.id === "hc-0")).toBeUndefined();
  });

  it("_resetForTest clears pendingByName", () => {
    record(makeInstance({ id: "z1", name: "action.a", status: "pending" }));
    record(makeInstance({ id: "z2", name: "action.b", status: "pending" }));
    expect(isPending("action.a")).toBe(true);
    expect(isPending("action.b")).toBe(true);

    _resetForTest();

    expect(isPending("action.a")).toBe(false);
    expect(isPending("action.b")).toBe(false);
  });
});
