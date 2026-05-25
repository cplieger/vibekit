// @vitest-environment happy-dom
// Tests for addRuleAction and removeRuleAction optimistic + rollback.

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
}));

import { _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry } from "./registry.js";
import { addRuleAction, removeRuleAction, type CommandRule } from "./permissions.js";

const mockFetch = vi.fn();

function makeRules(): CommandRule[] {
  return [
    { pattern: "npm *", mode: "allow", priority: 1, created_at: 1000 },
    { pattern: "rm -rf *", mode: "deny", priority: 2, created_at: 1001 },
  ];
}

beforeEach(() => {
  resetDefine();
  resetRegistry();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
});

describe("addRuleAction optimistic + rollback", () => {
  it("adds new rule optimistically", async () => {
    const rules = makeRules();
    const setRules = vi.fn();
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await addRuleAction.dispatch({ pattern: "git *", mode: "allow", priority: 3, rules, setRules });
    // setRules called with array containing the new rule
    expect(setRules).toHaveBeenCalled();
    const optimisticCall = setRules.mock.calls[0]![0] as CommandRule[];
    expect(optimisticCall.some((r) => r.pattern === "git *")).toBe(true);
  });

  it("rolls back new rule on failure", async () => {
    const rules = makeRules();
    const setRules = vi.fn();
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    await addRuleAction.dispatch({ pattern: "git *", mode: "allow", priority: 3, rules, setRules });
    // Last setRules call is the rollback — filters out the new pattern
    const lastCall = setRules.mock.calls[setRules.mock.calls.length - 1]![0] as CommandRule[];
    expect(lastCall.some((r) => r.pattern === "git *")).toBe(false);
  });

  it("updates existing rule optimistically", async () => {
    const rules = makeRules();
    const setRules = vi.fn();
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await addRuleAction.dispatch({ pattern: "npm *", mode: "deny", priority: 5, rules, setRules });
    const optimisticCall = setRules.mock.calls[0]![0] as CommandRule[];
    const updated = optimisticCall.find((r) => r.pattern === "npm *");
    expect(updated?.mode).toBe("deny");
    expect(updated?.priority).toBe(5);
  });

  it("rolls back updated rule on failure (restores original array)", async () => {
    const rules = makeRules();
    const setRules = vi.fn();
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    await addRuleAction.dispatch({ pattern: "npm *", mode: "deny", priority: 5, rules, setRules });
    // Rollback for existing rule calls setRules([...rules]) — the original snapshot
    const lastCall = setRules.mock.calls[setRules.mock.calls.length - 1]![0] as CommandRule[];
    expect(lastCall.length).toBe(rules.length);
  });
});

describe("removeRuleAction optimistic + rollback", () => {
  it("removes rule optimistically", async () => {
    const rules = makeRules();
    const setRules = vi.fn();
    mockFetch.mockResolvedValue(new Response("", { status: 204 }));
    await removeRuleAction.dispatch({ pattern: "npm *", rules, setRules });
    const optimisticCall = setRules.mock.calls[0]![0] as CommandRule[];
    expect(optimisticCall.some((r) => r.pattern === "npm *")).toBe(false);
  });

  it("reinserts rule on failure", async () => {
    const rules = makeRules();
    const setRules = vi.fn();
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    await removeRuleAction.dispatch({ pattern: "npm *", rules, setRules });
    // Rollback calls setRules([...rules]) — restoring original
    const lastCall = setRules.mock.calls[setRules.mock.calls.length - 1]![0] as CommandRule[];
    expect(lastCall.length).toBe(rules.length);
  });
});
