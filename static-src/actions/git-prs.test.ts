// @vitest-environment happy-dom
// Tests for mergePR and closePR optimistic + rollback.

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,

  apiGet: vi.fn(),
  apiPost: vi.fn(),
}));
import { bindPRPaint, setPRGroups } from "../git-prs-state.js";
import type { GitRepoGroup } from "../git-types.js";
import * as toast from "../toast.js";
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { mergePR, closePR, createPR } from "./git-prs.js";

const mockFetch = vi.fn();

function makeGroups(): GitRepoGroup[] {
  return [
    {
      forge_id: "gh1",
      forge_kind: "github",
      forge_host: "github.com",
      owner: "org",
      name: "repo",
      full_name: "org/repo",
      prs: [
        { number: 10, title: "PR 10", state: "open", source_branch: "f10", target_branch: "main" },
        { number: 5, title: "PR 5", state: "open", source_branch: "f5", target_branch: "main" },
        { number: 3, title: "PR 3", state: "open", source_branch: "f3", target_branch: "main" },
      ],
    },
  ];
}

const paint = vi.fn();

beforeEach(() => {
  resetActionFramework();
  mockFetch.mockReset();
  paint.mockReset();
  vi.stubGlobal("fetch", mockFetch);
  bindPRPaint(paint);
  setPRGroups(makeGroups());
});

const prArgs = { forge_id: "gh1", owner: "org", name: "repo", pr_number: 5 };

describe("mergePR optimistic + rollback", () => {
  it("removes PR from group on success (stays removed, no rollback)", async () => {
    const groups = makeGroups();
    setPRGroups(groups);
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await mergePR.dispatch(prArgs);
    // The merged PR (#5) is gone and the survivors keep their order.
    expect(groups[0]!.prs.map((p) => p.number)).toEqual([10, 3]);
    expect(paint).toHaveBeenCalled();
  });

  it("reinserts PR on failure", async () => {
    const groups = makeGroups();
    setPRGroups(groups);
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    await mergePR.dispatch(prArgs);
    const prs = groups[0]!.prs;
    expect(prs.some((p) => p.number === 5)).toBe(true);
  });

  it("preserves PR ordering after rollback", async () => {
    const groups = makeGroups();
    setPRGroups(groups);
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    await mergePR.dispatch(prArgs);
    const numbers = groups[0]!.prs.map((p) => p.number);
    expect(numbers).toEqual([10, 5, 3]);
  });
});

describe("closePR optimistic + rollback", () => {
  it("removes PR from group on success (stays removed, no rollback)", async () => {
    const groups = makeGroups();
    setPRGroups(groups);
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await closePR.dispatch(prArgs);
    expect(groups[0]!.prs.map((p) => p.number)).toEqual([10, 3]);
    expect(paint).toHaveBeenCalled();
  });

  it("reinserts PR on failure", async () => {
    const groups = makeGroups();
    setPRGroups(groups);
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    await closePR.dispatch(prArgs);
    const prs = groups[0]!.prs;
    expect(prs.some((p) => p.number === 5)).toBe(true);
  });

  it("preserves PR ordering after rollback", async () => {
    const groups = makeGroups();
    setPRGroups(groups);
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    await closePR.dispatch(prArgs);
    const numbers = groups[0]!.prs.map((p) => p.number);
    expect(numbers).toEqual([10, 5, 3]);
  });
});

describe("createPR", () => {
  const createArgs = {
    forge_id: "gh1",
    owner: "org",
    name: "repo",
    source_branch: "feature",
    target_branch: "main",
    title: "Add feature",
    body: "Closes #1",
    draft: false,
  };

  it("POSTs to the repo prs endpoint with the PR body and returns the result", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ number: 42 }), { status: 200 }));
    const r = await createPR.dispatch(createArgs);
    expect(r).toEqual({ number: 42 });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/forges/gh1/repos/org/repo/prs");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body as string)).toEqual({
      source_branch: "feature",
      target_branch: "main",
      title: "Add feature",
      body: "Closes #1",
      draft: false,
    });
  });

  it("does not toast on failure (error: false — the create dialog shows inline status)", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: "branch exists" }), { status: 500 }),
    );
    await createPR.dispatch(createArgs);
    expect(toast.error).not.toHaveBeenCalled();
  });
});
