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
import { mergePR, closePR, createPR, armAutoMerge, reopenPR, rerunChecks } from "./git-prs.js";

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
const mergeArgs = { ...prArgs, head_sha: "" };
const rerunArgs = { ...prArgs, head_sha: "" };

/** The request URL of the Nth fetch the action framework issued. */
function requestURL(call = 0): string {
  return mockFetch.mock.calls[call]![0] as string;
}

describe("mergePR optimistic + rollback", () => {
  it("removes PR from group on success (stays removed, no rollback)", async () => {
    const groups = makeGroups();
    setPRGroups(groups);
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await mergePR.dispatch(mergeArgs);
    // The merged PR (#5) is gone and the survivors keep their order.
    expect(groups[0]!.prs.map((p) => p.number)).toEqual([10, 3]);
    expect(paint).toHaveBeenCalled();
  });

  it("reinserts PR on failure", async () => {
    const groups = makeGroups();
    setPRGroups(groups);
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    await mergePR.dispatch(mergeArgs);
    const prs = groups[0]!.prs;
    expect(prs.some((p) => p.number === 5)).toBe(true);
  });

  it("preserves PR ordering after rollback", async () => {
    const groups = makeGroups();
    setPRGroups(groups);
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    await mergePR.dispatch(mergeArgs);
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

describe("merge head-commit pin", () => {
  it("sends the head SHA so the forge refuses a moved branch", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await mergePR.dispatch({ ...prArgs, head_sha: "aaaaaaa1111" });
    expect(requestURL()).toBe("/api/forges/gh1/repos/org/repo/prs/5/merge?head_sha=aaaaaaa1111");
  });

  it("omits the pin when the forge reported no head SHA", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await mergePR.dispatch(mergeArgs);
    expect(requestURL()).toBe("/api/forges/gh1/repos/org/repo/prs/5/merge");
  });

  it("never asks for auto-merge on a plain merge", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await mergePR.dispatch({ ...prArgs, head_sha: "aaaaaaa1111" });
    expect(requestURL()).not.toContain("auto=");
  });
});

describe("armAutoMerge", () => {
  it("arms on the merge route with auto=1 and the same pin", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await armAutoMerge.dispatch({ ...prArgs, head_sha: "aaaaaaa1111" });
    const url = requestURL();
    expect(url.startsWith("/api/forges/gh1/repos/org/repo/prs/5/merge?")).toBe(true);
    const q = new URLSearchParams(url.slice(url.indexOf("?")));
    expect(q.get("auto")).toBe("1");
    expect(q.get("head_sha")).toBe("aaaaaaa1111");
    expect(mockFetch.mock.calls[0]![1].method).toBe("POST");
  });

  // Arming does not merge, so the row must stay put: an optimistic remove
  // here would show the PR gone while the forge is still holding it.
  it("does not optimistically remove the PR", async () => {
    const groups = makeGroups();
    setPRGroups(groups);
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await armAutoMerge.dispatch({ ...prArgs, head_sha: "" });
    expect(groups[0]!.prs.map((p) => p.number)).toEqual([10, 5, 3]);
  });
});

describe("reopenPR", () => {
  it("POSTs the reopen route", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await reopenPR.dispatch(prArgs);
    expect(requestURL()).toBe("/api/forges/gh1/repos/org/repo/prs/5/reopen");
    expect(mockFetch.mock.calls[0]![1].method).toBe("POST");
  });

  it("leaves the groups alone (a reopened PR is not in the open list)", async () => {
    const groups = makeGroups();
    setPRGroups(groups);
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await reopenPR.dispatch(prArgs);
    expect(groups[0]!.prs.map((p) => p.number)).toEqual([10, 5, 3]);
  });
});

describe("rerunChecks", () => {
  // The pin is the point of this action, not a decoration: the row's check
  // chip is the folded state of one commit, and without the SHA the server
  // resolved the run from the mutable branch and could re-run an older
  // commit's CI, deployment side effects included.
  it("sends the head SHA the row was rendered from", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await rerunChecks.dispatch({ ...prArgs, head_sha: "aaaaaaa1111" });
    expect(requestURL()).toBe("/api/forges/gh1/repos/org/repo/prs/5/rerun?head_sha=aaaaaaa1111");
    expect(mockFetch.mock.calls[0]![1].method).toBe("POST");
  });

  it("omits the pin when the forge reported no head SHA", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await rerunChecks.dispatch(rerunArgs);
    expect(requestURL()).toBe("/api/forges/gh1/repos/org/repo/prs/5/rerun");
  });

  it("never asks for auto-merge on the rerun route", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await rerunChecks.dispatch({ ...prArgs, head_sha: "aaaaaaa1111" });
    expect(requestURL()).not.toContain("auto=");
  });

  // The chip flips only once the forge says so, so nothing is mutated
  // locally on the way out.
  it("does not touch the groups", async () => {
    const groups = makeGroups();
    setPRGroups(groups);
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await rerunChecks.dispatch(rerunArgs);
    expect(groups[0]!.prs.map((p) => p.number)).toEqual([10, 5, 3]);
  });

  it("toasts the failure (a 501 from an unsupported forge is a real answer)", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: "not supported", code: "not_supported" }), {
        status: 501,
      }),
    );
    const res = await rerunChecks.dispatch(rerunArgs);
    expect(res).toBeNull();
    expect(toast.error).toHaveBeenCalled();
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
