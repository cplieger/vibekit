// @vitest-environment happy-dom
import { describe, it, expect, beforeEach } from "vitest";

import { _resetForTest as resetRegistry, record } from "./registry.js";
import {
  initErrorTail,
  getRecentErrors,
  clearRecentErrors,
  _resetForTest as resetTail,
} from "./error-tail.js";
import type { ActionInstance } from "./types.js";

beforeEach(() => {
  resetRegistry();
  resetTail();
  try { localStorage.clear(); } catch { /* */ }
});

function makeInstance(over: Partial<ActionInstance> = {}): ActionInstance {
  return {
    id: over.id ?? "id-" + Math.random().toString(36).slice(2, 8),
    name: over.name ?? "t.action",
    status: over.status ?? "error",
    args: over.args ?? {},
    startedAt: over.startedAt ?? 1000,
    completedAt: over.completedAt ?? 1010,
    ...(over.result !== undefined ? { result: over.result } : {}),
    ...(over.error !== undefined ? { error: over.error } : {}),
  } as ActionInstance;
}

describe("persisted error tail", () => {
  it("starts empty", () => {
    expect(getRecentErrors()).toEqual([]);
  });

  it("persists error instances after init", () => {
    initErrorTail();
    record(
      makeInstance({
        name: "save.do",
        error: { message: "disk full", code: "ENOSPC", status: 500 },
      }),
    );
    const tail = getRecentErrors();
    expect(tail.length).toBe(1);
    expect(tail[0]?.name).toBe("save.do");
    expect(tail[0]?.message).toBe("disk full");
    expect(tail[0]?.code).toBe("ENOSPC");
    expect(tail[0]?.status).toBe(500);
  });

  it("ignores non-error transitions", () => {
    initErrorTail();
    record(makeInstance({ name: "ok.do", status: "success", error: undefined }));
    record(makeInstance({ name: "p.do", status: "pending", error: undefined } as Partial<ActionInstance>));
    record(makeInstance({ name: "c.do", status: "cancelled", error: undefined }));
    expect(getRecentErrors()).toEqual([]);
  });

  it("most-recent first", () => {
    initErrorTail();
    record(makeInstance({ name: "first", error: { message: "1" } }));
    record(makeInstance({ name: "second", error: { message: "2" } }));
    const tail = getRecentErrors();
    expect(tail[0]?.name).toBe("second");
    expect(tail[1]?.name).toBe("first");
  });

  it("caps at 20 entries", () => {
    initErrorTail();
    for (let i = 0; i < 30; i++) {
      record(makeInstance({ name: `e.${String(i)}`, error: { message: String(i) } }));
    }
    expect(getRecentErrors().length).toBe(20);
    // Newest first → most recent (e.29) should be at index 0.
    expect(getRecentErrors()[0]?.name).toBe("e.29");
  });

  it("clearRecentErrors empties the list", () => {
    initErrorTail();
    record(makeInstance({ name: "x", error: { message: "boom" } }));
    expect(getRecentErrors().length).toBe(1);
    clearRecentErrors();
    expect(getRecentErrors().length).toBe(0);
  });

  it("survives missing optional fields", () => {
    initErrorTail();
    record(makeInstance({ name: "min", error: { message: "minimal" } }));
    const tail = getRecentErrors();
    expect(tail[0]?.message).toBe("minimal");
    expect(tail[0]?.code).toBeUndefined();
    expect(tail[0]?.status).toBeUndefined();
  });

  it("teardown fn stops persistence", () => {
    const stop = initErrorTail();
    stop();
    record(makeInstance({ name: "after-stop", error: { message: "x" } }));
    expect(getRecentErrors()).toEqual([]);
  });

  it("recovers from corrupt localStorage", () => {
    localStorage.setItem("vk.errors", "{not json");
    expect(getRecentErrors()).toEqual([]);
    initErrorTail();
    record(makeInstance({ name: "fresh", error: { message: "f" } }));
    expect(getRecentErrors()[0]?.name).toBe("fresh");
  });

  it("filters non-conforming entries from existing storage", () => {
    localStorage.setItem("vk.errors", JSON.stringify([
      { name: "valid", at: 1, message: "ok" },
      { not: "valid" },
      "string-entry",
      null,
    ]));
    const tail = getRecentErrors();
    expect(tail.length).toBe(1);
    expect(tail[0]?.name).toBe("valid");
  });

  it("re-init replaces previous subscription (no double-write)", () => {
    initErrorTail();
    initErrorTail();
    record(makeInstance({ name: "once", error: { message: "1" } }));
    expect(getRecentErrors().length).toBe(1);
  });
});
