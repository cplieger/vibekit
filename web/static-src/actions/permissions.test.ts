// @vitest-environment happy-dom
// Tests for addRule and removeRule optimistic + rollback.

import { describe, it, expect, vi, beforeEach } from "vitest";

import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";

vi.mock("../toast.js", () => import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()));

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
}));

import { addRule, removeRule, type CommandRule } from "./permissions.js";

const mockFetch = vi.fn();

function makeRules(): CommandRule[] {
  return [
    { pattern: "npm *", mode: "allow", priority: 1, created_at: 1000 },
    { pattern: "rm -rf *", mode: "deny", priority: 2, created_at: 1001 },
  ];
}

beforeEach(() => {
  resetActionFramework();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
});

describe("addRule optimistic + rollback", () => {
  it("adds new rule optimistically", async () => {
    let rules = makeRules();
    const setRules = vi.fn((next: CommandRule[]) => {
      rules = next;
    });
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await addRule.dispatch({
      pattern: "git *",
      mode: "allow",
      priority: 3,
      rules,
      setRules,
      getCurrentRules: () => rules,
    });
    // setRules called with array containing the new rule
    expect(setRules).toHaveBeenCalled();
    const optimisticCall = setRules.mock.calls[0]![0];
    expect(optimisticCall.some((r) => r.pattern === "git *")).toBe(true);
  });

  it("rolls back new rule on failure", async () => {
    let rules = makeRules();
    const setRules = vi.fn((next: CommandRule[]) => {
      rules = next;
    });
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    await addRule.dispatch({
      pattern: "git *",
      mode: "allow",
      priority: 3,
      rules,
      setRules,
      getCurrentRules: () => rules,
    });
    // Last setRules call is the rollback — filters out the new pattern
    const lastCall = setRules.mock.calls[setRules.mock.calls.length - 1]![0];
    expect(lastCall.some((r) => r.pattern === "git *")).toBe(false);
  });

  it("updates existing rule optimistically", async () => {
    let rules = makeRules();
    const setRules = vi.fn((next: CommandRule[]) => {
      rules = next;
    });
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await addRule.dispatch({
      pattern: "npm *",
      mode: "deny",
      priority: 5,
      rules,
      setRules,
      getCurrentRules: () => rules,
    });
    const optimisticCall = setRules.mock.calls[0]![0];
    const updated = optimisticCall.find((r) => r.pattern === "npm *");
    expect(updated?.mode).toBe("deny");
    expect(updated?.priority).toBe(5);
  });

  it("rolls back updated rule on failure (restores original)", async () => {
    let rules = makeRules();
    const original = makeRules();
    const setRules = vi.fn((next: CommandRule[]) => {
      rules = next;
    });
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    await addRule.dispatch({
      pattern: "npm *",
      mode: "deny",
      priority: 5,
      rules,
      setRules,
      getCurrentRules: () => rules,
    });
    // Rollback should restore the previous version of "npm *"
    const lastCall = setRules.mock.calls[setRules.mock.calls.length - 1]![0];
    const restored = lastCall.find((r) => r.pattern === "npm *");
    expect(restored?.mode).toBe(original[0]!.mode);
    expect(restored?.priority).toBe(original[0]!.priority);
  });
});

describe("removeRule optimistic + rollback", () => {
  it("removes rule optimistically", async () => {
    let rules = makeRules();
    const setRules = vi.fn((next: CommandRule[]) => {
      rules = next;
    });
    mockFetch.mockResolvedValue(new Response("", { status: 204 }));
    await removeRule.dispatch({ pattern: "npm *", rules, setRules, getCurrentRules: () => rules });
    const optimisticCall = setRules.mock.calls[0]![0];
    expect(optimisticCall.some((r) => r.pattern === "npm *")).toBe(false);
  });

  it("reinserts rule on failure", async () => {
    let rules = makeRules();
    const setRules = vi.fn((next: CommandRule[]) => {
      rules = next;
    });
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    await removeRule.dispatch({ pattern: "npm *", rules, setRules, getCurrentRules: () => rules });
    // Rollback re-adds previousRule to current rules
    const lastCall = setRules.mock.calls[setRules.mock.calls.length - 1]![0];
    expect(lastCall.some((r) => r.pattern === "npm *")).toBe(true);
  });
});
