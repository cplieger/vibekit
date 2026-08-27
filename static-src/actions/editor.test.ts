// Tests for actions/editor.ts: saveFile, fetchAgentLines, suggestResolution.

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

// Every name in `api-client.ts`, because Browser Mode links ESM for real: a name
// any module in this graph imports has to exist on the mock even when nothing
// here calls it. Four names sufficed until the tab projection widened the graph
// and `apiGetTyped` started being reached, and the symptom was not a missing
// export. The `transport.js` mock below calls `importOriginal()`, which threw on
// the broken link and killed the browser page, so the run reported a closed
// connection rather than naming what it could not find. That is worth knowing
// before debugging the next one.
//
// Listed rather than spread from `vi.importActual`: the real module's own graph
// reaches `transport.js`, which this file also mocks, so importing it for real
// inside a factory is circular and dies the same opaque way.
vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
  apiGet: vi.fn(),
  apiPost: vi.fn(),
  apiDelete: vi.fn(),
  apiGetTyped: vi.fn(),
  apiPostTyped: vi.fn(),
  apiPutOrError: vi.fn(),
  apiGetOrError: vi.fn(),
  fetchKiroSetting: vi.fn(),
}));
// Every name in `transport.ts`, listed rather than spread from `importOriginal`.
// This mock used to call it, and that is what turned a broken ESM link elsewhere
// in the graph into an opaque "browser connection was closed": the factory threw
// while resolving the real module, which kills the page instead of naming the
// export it could not find. Listing is duller and it fails legibly.
//
// Only `send` needs to be inert; the four id minters are real because a test that
// asserts on a request wants a real id in it, and `init` is never called here.
vi.mock("../transport.js", () => ({
  send: vi.fn(),
  init: vi.fn(),
  markHydrated: vi.fn(),
  newMessageID: () => "m-test",
  newRequestID: () => "r-test",
  newOpID: () => "op-test",
}));

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
//
// The two sides also go through DIFFERENT client helpers, which is why they are
// mocked separately below: the working copy needs `apiGetOrError` because two of
// its failure statuses are answers about a changed file (404 deleted, 415 binary)
// rather than failures to read one.
// ---------------------------------------------------------------------------
describe("editor.load_diff", () => {
  const SHOW = "/api/git/show";
  const FILE = "/api/file?";

  /** Stage both sides. `show` is git's answer body (or null for unreachable);
   *  `file` is the working copy's HTTP status plus its body, because the status
   *  is what the action branches on. */
  function answer(show: unknown, file: { status: number; data?: unknown }): void {
    vi.mocked(api.apiGet).mockImplementation((url: string) => {
      if (!url.startsWith(SHOW)) {
        throw new Error(`unexpected GET ${url}`);
      }
      return Promise.resolve(show);
    });
    vi.mocked(api.apiGetOrError).mockImplementation((url: string) => {
      if (!url.startsWith(FILE)) {
        throw new Error(`unexpected GET ${url}`);
      }
      return Promise.resolve({
        ok: file.status >= 200 && file.status < 300,
        status: file.status,
        data: file.data ?? null,
        error: "",
      });
    });
  }

  const ok = (content: string): { status: number; data: unknown } => ({
    status: 200,
    data: { content },
  });

  /** Every URL either side was asked for. */
  function urls(): string[] {
    return [
      ...vi.mocked(api.apiGet).mock.calls.map((c) => String(c[0])),
      ...vi.mocked(api.apiGetOrError).mock.calls.map((c) => String(c[0])),
    ];
  }

  it("sends the workspace-relative path to git show and the absolute one to the file route", async () => {
    expect.assertions(2);
    setWorkspaceRoot("/workspace");
    answer({ content: "base" }, ok("work"));
    const { loadDiff } = await import("./editor.js");
    await loadDiff.dispatch({ path: "/workspace/sub/a.go", repo: "", ref: "HEAD" }).outcome;
    expect(urls()).toContain("/api/git/show?path=sub%2Fa.go&ref=HEAD");
    expect(urls()).toContain("/api/file?path=%2Fworkspace%2Fsub%2Fa.go");
  });

  it("passes the path through untouched when the caller names a repo", async () => {
    // With an explicit repo the caller already holds the repo-relative path.
    expect.assertions(1);
    setWorkspaceRoot("/workspace");
    answer({ content: "base" }, ok("work"));
    const { loadDiff } = await import("./editor.js");
    await loadDiff.dispatch({ path: "static-src/a.ts", repo: "vibekit", ref: "HEAD" }).outcome;
    expect(urls()).toContain("/api/git/show?path=static-src%2Fa.ts&ref=HEAD&repo=vibekit");
  });

  it("captions both panes for an ordinary change", async () => {
    expect.assertions(4);
    setWorkspaceRoot("/workspace");
    answer({ content: "base" }, ok("work"));
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/a.go", repo: "", ref: "HEAD" }).outcome;
    expect(o.status).toBe("success");
    if (o.status !== "success") {
      return;
    }
    expect(o.value.baseLabel).toBe("HEAD");
    expect(o.value.workingLabel).toBe("working tree");
    expect(o.value.oldContent).toBe("base");
  });

  it("captions a file outside every repo 'not in git' rather than HEAD", async () => {
    // An empty pane captioned HEAD claims HEAD holds the file and holds it
    // empty. This pairs with internal/git's KindNotInRepo, which exists so a
    // real git failure cannot render as "this file is brand new".
    expect.assertions(3);
    setWorkspaceRoot("/workspace");
    answer({ error: "not_in_repo" }, ok("work"));
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/a.go", repo: "", ref: "HEAD" }).outcome;
    expect(o.status).toBe("success");
    if (o.status !== "success") {
      return;
    }
    expect(o.value.baseLabel).toBe("not in git");
    expect(o.value.newContent).toBe("work");
  });

  it("renders a deleted file as an all-deletions diff captioned 'deleted'", async () => {
    // A 404 from the file route is the CHANGE, not a failure to read it: the
    // working copy is gone, so the diff is every line of the base removed. The
    // git panel's changed-file list is full of these, and collapsing the status
    // to null failed the whole diff for one.
    expect.assertions(4);
    setWorkspaceRoot("/workspace");
    answer({ content: "gone\n" }, { status: 404 });
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/a.go", repo: "", ref: "HEAD" }).outcome;
    expect(o.status).toBe("success");
    if (o.status !== "success") {
      return;
    }
    expect(o.value.oldContent).toBe("gone\n");
    expect(o.value.newContent).toBe("");
    expect(o.value.workingLabel).toBe("deleted");
  });

  it("says a binary file has no text diff instead of rendering the blob", async () => {
    expect.assertions(2);
    setWorkspaceRoot("/workspace");
    answer({ content: "\u0000\u0001" }, { status: 415 });
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/logo.png", repo: "", ref: "HEAD" })
      .outcome;
    expect(o.status).toBe("success");
    if (o.status !== "success") {
      return;
    }
    expect(o.value.error).toContain("binary");
  });

  it("fails on a real git error rather than rendering an all-add diff", async () => {
    expect.assertions(2);
    setWorkspaceRoot("/workspace");
    answer({ error: "show_failed", detail: "fatal: bad object" }, ok("work"));
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/a.go", repo: "", ref: "HEAD" }).outcome;
    expect(o.status).toBe("error");
    if (o.status !== "error") {
      return;
    }
    expect(o.error.message).toContain("fatal: bad object");
  });

  it("names the side that died, not 'base/new'", async () => {
    // A 500 is a genuine read failure, unlike the 404 and 415 above.
    expect.assertions(2);
    setWorkspaceRoot("/workspace");
    answer({ content: "base" }, { status: 500 });
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
    answer(null, ok("work"));
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/a.go", repo: "", ref: "HEAD" }).outcome;
    expect(o.status).toBe("error");
    if (o.status !== "error") {
      return;
    }
    expect(o.error.message).toContain("HEAD");
  });
});
