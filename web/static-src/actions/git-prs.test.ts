// @vitest-environment happy-dom
// Tests for mergePR and closePR optimistic + rollback.

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
import { _resetForTest as resetCleanup } from "./cleanup.js";
import { bindPRState } from "../git-prs-state.js";
import { mergePR, closePR } from "./git-prs.js";

const mockFetch = vi.fn();

function makeGroups() {
  return [{
    forge_id: "gh1", forge_kind: "github", forge_host: "github.com",
    owner: "org", name: "repo", full_name: "org/repo",
    prs: [
      { number: 10, title: "PR 10", state: "open", source_branch: "f10", target_branch: "main" },
      { number: 5, title: "PR 5", state: "open", source_branch: "f5", target_branch: "main" },
      { number: 3, title: "PR 3", state: "open", source_branch: "f3", target_branch: "main" },
    ],
  }];
}

const paint = vi.fn();

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  mockFetch.mockReset();
  paint.mockReset();
  vi.stubGlobal("fetch", mockFetch);
  const groups = makeGroups();
  bindPRState({ groups, paint });
});

const prArgs = { forge_id: "gh1", owner: "org", name: "repo", pr_number: 5 };

describe("mergePR optimistic + rollback", () => {
  it("removes PR from group optimistically", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await mergePR.dispatch(prArgs);
    expect(paint).toHaveBeenCalled();
  });

  it("reinserts PR on failure", async () => {
    const groups = makeGroups();
    bindPRState({ groups, paint });
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    await mergePR.dispatch(prArgs);
    const prs = groups[0]!.prs;
    expect(prs.some((p) => p.number === 5)).toBe(true);
  });

  it("preserves PR ordering after rollback", async () => {
    const groups = makeGroups();
    bindPRState({ groups, paint });
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    await mergePR.dispatch(prArgs);
    const numbers = groups[0]!.prs.map((p) => p.number);
    expect(numbers).toEqual([10, 5, 3]);
  });
});

describe("closePR optimistic + rollback", () => {
  it("removes PR from group optimistically", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await closePR.dispatch(prArgs);
    expect(paint).toHaveBeenCalled();
  });

  it("reinserts PR on failure", async () => {
    const groups = makeGroups();
    bindPRState({ groups, paint });
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    await closePR.dispatch(prArgs);
    const prs = groups[0]!.prs;
    expect(prs.some((p) => p.number === 5)).toBe(true);
  });

  it("preserves PR ordering after rollback", async () => {
    const groups = makeGroups();
    bindPRState({ groups, paint });
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    await closePR.dispatch(prArgs);
    const numbers = groups[0]!.prs.map((p) => p.number);
    expect(numbers).toEqual([10, 5, 3]);
  });
});
