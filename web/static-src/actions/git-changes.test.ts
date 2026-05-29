// @vitest-environment happy-dom
// Tests for actions/git-changes.ts: stage, discard, unstage, pull, push, commit, generateCommitMessage.

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
import { _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry, recentLog } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import * as toast from "../toast.js";

const mockFetch = vi.fn();

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
});

describe("git.stage", () => {
  it("POSTs to /api/git/stage with repo and files", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    const { stage } = await import("./git-changes.js");
    await stage.dispatch({ repo: "myrepo", files: ["src/a.ts", "src/b.ts"] });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/git/stage");
    expect(JSON.parse(opts.body as string)).toEqual({
      repo: "myrepo",
      files: ["src/a.ts", "src/b.ts"],
    });
  });

  it("toasts with file name on single-file failure", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: "no such file" }), { status: 404 }),
    );
    const { stage } = await import("./git-changes.js");
    await stage.dispatch({ repo: "", files: ["README.md"] });
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("README.md"), undefined);
  });

  it("toasts with count on multi-file failure", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "err" }), { status: 500 }));
    const { stage } = await import("./git-changes.js");
    await stage.dispatch({ repo: "", files: ["a.ts", "b.ts", "c.ts"] });
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("3 files"), undefined);
  });
});

describe("git.discard", () => {
  it("is not retryable (destructive)", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: "timeout" }), { status: 500 }),
    );
    const { discard } = await import("./git-changes.js");
    await discard.dispatch({ repo: "", files: ["x.ts"] });
    const log = recentLog();
    expect(log[0]?.status).toBe("error");
    // No retry button (retryable: false)
    expect(toast.error).toHaveBeenCalledWith(expect.any(String), undefined);
  });
});

describe("git.pull", () => {
  it("toasts success with repo name", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    const { pull } = await import("./git-changes.js");
    await pull.dispatch({ repo: "frontend" });
    expect(toast.success).toHaveBeenCalledWith("Pulled frontend");
  });
});

describe("git.push", () => {
  it("toasts success without repo name when empty", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    const { push } = await import("./git-changes.js");
    await push.dispatch({ repo: "" });
    expect(toast.success).toHaveBeenCalledWith("Pushed");
  });

  it("is not retryable (may have succeeded server-side)", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: "rejected" }), { status: 500 }),
    );
    const { push } = await import("./git-changes.js");
    await push.dispatch({ repo: "" });
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("Push failed"), undefined);
  });
});

describe("git.commit", () => {
  it("toasts success on 200", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    const { commit } = await import("./git-changes.js");
    await commit.dispatch({ repo: "", message: "fix: typo" });
    expect(toast.success).toHaveBeenCalledWith("Committed");
  });

  it("error toast includes truncated commit message", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: "nothing to commit" }), { status: 400 }),
    );
    const { commit } = await import("./git-changes.js");
    await commit.dispatch({ repo: "", message: "fix: typo" });
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("fix: typo"), undefined);
  });
});

describe("git.generateCommitMessage", () => {
  it("POSTs to /api/git/commit-message", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ message: "feat: add X" }), { status: 200 }),
    );
    const { generateCommitMessage } = await import("./git-changes.js");
    const r = await generateCommitMessage.dispatch({ repo: "main" });
    expect(r).toEqual({ message: "feat: add X" });
    const [url] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/git/commit-message");
  });
});
