// @vitest-environment happy-dom
// Tests for actions/editor.ts: saveFile, fetchAgentLines, suggestResolution.

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
vi.mock("../transport.js", async (importOriginal) => {
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  const orig = await importOriginal<typeof import("../transport.js")>();
  return { ...orig, send: vi.fn() };
});

vi.mock("../editor-types.js", () => ({
  routeForPath: (path: string) => ({ writeURL: `/api/file?path=${encodeURIComponent(path)}` }),
}));

import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { setWorkspaceRoot, _resetForTest as resetWorkspace } from "../workspace.js";
import * as api from "../api-client.js";

const mockFetch = vi.fn();

beforeEach(() => {
  resetActionFramework();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
  resetWorkspace();
});

describe("editor.save_file", () => {
  it("PUTs file content to the write URL", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    const { saveFile } = await import("./editor.js");
    const r = await saveFile.dispatch({ path: "src/main.ts", content: "console.log('hi')" });
    expect(r).toEqual({ ok: true });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/file?path=src%2Fmain.ts");
    expect(opts.method).toBe("PUT");
    expect(JSON.parse(opts.body as string)).toEqual({ content: "console.log('hi')" });
  });

  it("suppresses error toast (error: false)", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: "disk full" }), { status: 500 }),
    );
    const { saveFile } = await import("./editor.js");
    const { error: toastError } = await import("../toast.js");
    await saveFile.dispatch({ path: "x.ts", content: "" });
    expect(toastError).not.toHaveBeenCalled();
  });
});

// The inverse IS the done-when for N4: per-hunk resolution is gone because
// KAS decides per ACTION (a multi-file rename shares one toolCallId), so a
// merged-text reply had no addressable target.
describe("editor.resolve_partial", () => {
  it("no longer exists", async () => {
    const mod = await import("./editor.js");
    expect(mod).not.toHaveProperty("resolvePendingPartial");
  });
});

describe("editor.fetch_agent_lines", () => {
  it("GETs file changes with chat_id and path params", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ changes: [{ start_line: 1, end_line: 5 }] }), { status: 200 }),
    );
    const { fetchAgentLines } = await import("./editor.js");
    const r = await fetchAgentLines.dispatch({ chatID: "c1", path: "src/a.ts" });
    expect(r).toEqual({ changes: [{ start_line: 1, end_line: 5 }] });
    const [url] = mockFetch.mock.calls[0]!;
    expect(url).toContain("/api/file-changes");
    expect(url).toContain("chat_id=c1");
    expect(url).toContain("path=src%2Fa.ts");
  });
});

describe("editor.suggest_resolution", () => {
  it("POSTs to /api/utility/resolve-conflict", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ output: "resolved" }), { status: 200 }),
    );
    const { suggestResolution } = await import("./editor.js");
    const r = await suggestResolution.dispatch({ ours: "a", theirs: "b", context: "merge" });
    expect(r).toEqual({ output: "resolved" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/utility/resolve-conflict");
    expect(opts.method).toBe("POST");
  });
});

// ---------------------------------------------------------------------------
// load_diff: the two sides speak different path languages, and conflating them
// broke every git diff in the editor, in BOTH directions.
//
//   /api/file      container-ABSOLUTE, resolved against the granted-roots
//                  allow-list; a relative path is denied 403.
//   /api/git/show  workspace-relative (or repo-relative with an explicit repo);
//                  validateFilePath refuses a leading "/", so an absolute path
//                  is a 400.
//
// One spelling sent to both meant whichever endpoint disagreed returned null and
// the whole diff failed with a message naming neither side.
// ---------------------------------------------------------------------------
describe("editor.load_diff", () => {
  /** Answer each GET by URL prefix, so the two sides can differ per case. */
  function answer(map: Record<string, unknown>): void {
    vi.mocked(api.apiGet).mockImplementation((url: string) => {
      for (const [prefix, body] of Object.entries(map)) {
        if (url.startsWith(prefix)) {
          return Promise.resolve(body);
        }
      }
      throw new Error(`unexpected GET ${url}`);
    });
  }

  const SHOW = "/api/git/show";
  const FILE = "/api/file?";

  it("sends the workspace-relative path to git show and the absolute one to the file route", async () => {
    expect.assertions(2);
    setWorkspaceRoot("/workspace");
    answer({ [SHOW]: { content: "base" }, [FILE]: { content: "work" } });
    const { loadDiff } = await import("./editor.js");
    await loadDiff.dispatch({ path: "/workspace/sub/a.go", repo: "", ref: "HEAD" }).outcome;
    const urls = vi.mocked(api.apiGet).mock.calls.map((c) => String(c[0]));
    expect(urls).toContain("/api/git/show?path=sub%2Fa.go&ref=HEAD");
    expect(urls).toContain("/api/file?path=%2Fworkspace%2Fsub%2Fa.go");
  });

  it("passes the path through untouched when the caller names a repo", async () => {
    // With an explicit repo the caller already holds the repo-relative path.
    expect.assertions(1);
    setWorkspaceRoot("/workspace");
    answer({ [SHOW]: { content: "base" }, [FILE]: { content: "work" } });
    const { loadDiff } = await import("./editor.js");
    await loadDiff.dispatch({ path: "static-src/a.ts", repo: "vibekit", ref: "HEAD" }).outcome;
    const urls = vi.mocked(api.apiGet).mock.calls.map((c) => String(c[0]));
    expect(urls).toContain("/api/git/show?path=static-src%2Fa.ts&ref=HEAD&repo=vibekit");
  });

  it("captions the base pane with the ref when git holds a revision", async () => {
    expect.assertions(3);
    setWorkspaceRoot("/workspace");
    answer({ [SHOW]: { content: "base" }, [FILE]: { content: "work" } });
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/a.go", repo: "", ref: "HEAD" }).outcome;
    expect(o.status).toBe("success");
    if (o.status !== "success") {
      return;
    }
    expect(o.value.baseLabel).toBe("HEAD");
    expect(o.value.oldContent).toBe("base");
  });

  it("captions a file outside every repo 'not in git' rather than HEAD", async () => {
    // An empty pane captioned HEAD claims HEAD holds the file and holds it
    // empty. This pairs with internal/git's KindNotInRepo, which exists so a
    // real git failure cannot render as "this file is brand new".
    expect.assertions(3);
    setWorkspaceRoot("/workspace");
    answer({ [SHOW]: { error: "not_in_repo" }, [FILE]: { content: "work" } });
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/a.go", repo: "", ref: "HEAD" }).outcome;
    expect(o.status).toBe("success");
    if (o.status !== "success") {
      return;
    }
    expect(o.value.baseLabel).toBe("not in git");
    expect(o.value.newContent).toBe("work");
  });

  it("fails on a real git error rather than rendering an all-add diff", async () => {
    expect.assertions(2);
    setWorkspaceRoot("/workspace");
    answer({
      [SHOW]: { error: "show_failed", detail: "fatal: bad object" },
      [FILE]: { content: "work" },
    });
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/a.go", repo: "", ref: "HEAD" }).outcome;
    expect(o.status).toBe("error");
    if (o.status !== "error") {
      return;
    }
    expect(o.error.message).toContain("fatal: bad object");
  });

  it("names the side that died, not 'base/new'", async () => {
    expect.assertions(2);
    setWorkspaceRoot("/workspace");
    answer({ [SHOW]: { content: "base" }, [FILE]: null });
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/a.go", repo: "", ref: "HEAD" }).outcome;
    expect(o.status).toBe("error");
    if (o.status !== "error") {
      return;
    }
    expect(o.error.message).toContain("working copy");
  });

  it("names the base side when git show is unreachable", async () => {
    expect.assertions(2);
    setWorkspaceRoot("/workspace");
    answer({ [SHOW]: null, [FILE]: { content: "work" } });
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/a.go", repo: "", ref: "HEAD" }).outcome;
    expect(o.status).toBe("error");
    if (o.status !== "error") {
      return;
    }
    expect(o.error.message).toContain("HEAD");
  });
});
