// Tests for files.ts: createFile, createFolder, renameFile, deleteFilesBatch, upload.
import { describe, it, expect, vi, beforeEach } from "vitest";

import { resetActionFramework, headerValue } from "./__test-helpers__/action-test-setup.js";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

vi.mock("../api-client.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  apiGet: undefined,
  // Reached through tabs.ts -> tabs-sync.ts, whose `GET /api/tabs` is the only
  // read in the projection. Nothing under test lists tabs, so the name only has
  // to exist for real-ESM linking.
  apiGetTyped: undefined,
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
}));

vi.mock("../upload.js", () => ({
  uploadFiles: vi.fn(),
}));

import { getActionLog as recentLog } from "./index.js";
import * as toast from "../toast.js";
import { uploadFiles } from "../upload.js";

const mockFetch = vi.fn();

beforeEach(() => {
  resetActionFramework();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
});

describe("files.create_file", () => {
  it("POSTs touch action to /api/files/action", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    const { createFile } = await import("./files.js");
    await createFile.dispatch({ dir: "/src", name: "index.ts" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/files/action");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body as string)).toEqual({ action: "touch", path: "/src/index.ts" });
  });

  it("retries on network error", async () => {
    vi.useFakeTimers();
    mockFetch
      .mockRejectedValueOnce(new TypeError("Failed to fetch"))
      .mockResolvedValueOnce(new Response(JSON.stringify({}), { status: 200 }));
    const { createFile } = await import("./files.js");
    const p = createFile.dispatch({ dir: "/", name: "a.ts" });
    await vi.advanceTimersByTimeAsync(300);
    await p;
    expect(mockFetch).toHaveBeenCalledTimes(2);
    vi.useRealTimers();
  });

  it("toasts error on failure", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "exists" }), { status: 409 }));
    const { createFile } = await import("./files.js");
    await createFile.dispatch({ dir: "/", name: "x" });
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("create file"), undefined);
  });
});

describe("files.create_folder", () => {
  it("POSTs mkdir action to /api/files/action", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    const { createFolder } = await import("./files.js");
    await createFolder.dispatch({ dir: "/project", name: "utils" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/files/action");
    expect(JSON.parse(opts.body as string)).toEqual({ action: "mkdir", path: "/project/utils" });
  });
});

describe("files.rename", () => {
  it("POSTs rename action with original and new name", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    const { renameFile } = await import("./files.js");
    await renameFile.dispatch({ dir: "/src", original: "old.ts", newName: "new.ts" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/files/action");
    expect(JSON.parse(opts.body as string)).toEqual({
      action: "rename",
      path: "/src/old.ts",
      name: "new.ts",
    });
  });

  it("includes Idempotency-Key header", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    const { renameFile } = await import("./files.js");
    await renameFile.dispatch({ dir: "/", original: "a", newName: "b" });
    expect(headerValue(mockFetch.mock.calls[0]![1], "idempotency-key")).toEqual(expect.any(String));
  });

  it("toasts error with truncated filename on failure", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "perm" }), { status: 403 }));
    const { renameFile } = await import("./files.js");
    await renameFile.dispatch({ dir: "/", original: "my-file.ts", newName: "x" });
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("my-file.ts"), undefined);
  });
});

describe("files.delete (batch)", () => {
  it("sends parallel delete requests for each file", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    const listEl = document.createElement("div");
    const { deleteFilesBatch } = await import("./files.js");
    await deleteFilesBatch.dispatch({ dir: "/src", names: ["a.ts", "b.ts"], listEl });
    expect(mockFetch).toHaveBeenCalledTimes(2);
    const bodies = mockFetch.mock.calls.map((c) => JSON.parse(c[1].body as string));
    expect(bodies).toContainEqual({ action: "delete", path: "/src/a.ts" });
    expect(bodies).toContainEqual({ action: "delete", path: "/src/b.ts" });
  });

  it("applies optimistic exit class and rolls back on failure", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    const listEl = document.createElement("div");
    const row = document.createElement("div");
    row.dataset["name"] = "target.ts";
    listEl.appendChild(row);

    const { deleteFilesBatch } = await import("./files.js");
    await deleteFilesBatch.dispatch({ dir: "/", names: ["target.ts"], listEl });
    // After rollback, the exit class should be removed
    expect(row.classList.contains("fb-row-exiting")).toBe(false);
  });

  it("is not retryable", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    const listEl = document.createElement("div");
    const { deleteFilesBatch } = await import("./files.js");
    await deleteFilesBatch.dispatch({ dir: "/", names: ["x"], listEl });
    const log = recentLog();
    expect(log[0]?.status).toBe("error");
  });
});

describe("files.upload", () => {
  it("calls uploadFiles and toasts success with count", async () => {
    vi.mocked(uploadFiles).mockImplementation(({ onComplete }) => {
      onComplete!(["a.ts", "b.ts"]);
    });
    const { upload } = await import("./files.js");
    const files = {
      length: 2,
      item: () => null,
      0: new File([""], "a.ts"),
      1: new File([""], "b.ts"),
    } as unknown as FileList;
    const r = await upload.dispatch({ files, targetDir: "/uploads" });
    expect(r).toEqual(["a.ts", "b.ts"]);
    expect(toast.success).toHaveBeenCalledWith("Uploaded 2 files");
  });

  it("toasts error on upload failure", async () => {
    vi.mocked(uploadFiles).mockImplementation(({ onError }) => {
      onError!("Network error", []);
    });
    const { upload } = await import("./files.js");
    const files = { length: 1, item: () => null, 0: new File([""], "x") } as unknown as FileList;
    const r = await upload.dispatch({ files, targetDir: "/" });
    expect(r).toBeNull();
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("Upload failed"), undefined);
  });

  it("carries the partial batch to the caller, which is not rolled back", async () => {
    vi.mocked(uploadFiles).mockImplementation(({ onError }) => {
      onError!("3 of 5 uploaded, then big.zip failed: upload too large", [
        "/workspace/uploads/a.txt",
        "/workspace/uploads/b.txt",
        "/workspace/uploads/c.txt",
      ]);
    });
    const { upload, partialUploadOf } = await import("./files.js");
    const files = { length: 5, item: () => null } as unknown as FileList;
    let seen: string[] = [];
    await upload.dispatch(
      { files, targetDir: "/workspace/uploads" },
      {
        onError: (err) => {
          seen = partialUploadOf(err.cause);
        },
      },
    );
    expect(seen).toEqual([
      "/workspace/uploads/a.txt",
      "/workspace/uploads/b.txt",
      "/workspace/uploads/c.txt",
    ]);
    expect(toast.error).toHaveBeenCalledWith(
      expect.stringContaining("then big.zip failed"),
      undefined,
    );
  });
});

describe("partialUploadOf", () => {
  it("returns nothing for every other failure shape, so callers need no branch", async () => {
    const { partialUploadOf } = await import("./files.js");
    expect(partialUploadOf(undefined)).toEqual([]);
    expect(partialUploadOf(null)).toEqual([]);
    expect(partialUploadOf(new Error("boom"))).toEqual([]);
    expect(partialUploadOf({ uploaded: "not-a-list" })).toEqual([]);
    expect(partialUploadOf({ uploaded: ["a", 7, "b"] })).toEqual(["a", "b"]);
  });
});

// ---------------------------------------------------------------------------
// Composite idempotency / dedupe keys (keyenc `join`).
//
// These keys travel as the `Idempotency-Key` HTTP header, which the Go
// middleware treats as opaque — it never parses or builds one — so only
// within-client injectivity matters here.
// ---------------------------------------------------------------------------

describe("files.rename idempotency key", () => {
  async function keyFor(args: { dir: string; original: string; newName: string }): Promise<string> {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    const { renameFile } = await import("./files.js");
    await renameFile.dispatch(args);
    const key = headerValue(mockFetch.mock.calls[0]![1], "idempotency-key");
    mockFetch.mockReset();
    resetActionFramework();
    return key ?? "";
  }

  it("distinguishes two renames the old '->' form collapsed", async () => {
    // THE live defect this adoption fixes. "->" is a legal filename sequence,
    // so `files.rename:${dir}/${original}->${newName}` gave both of these
    // renames the key "files.rename:/w/a->b->c". The Go idempotency middleware
    // then replayed the first cached 200 and the second rename silently never
    // happened for the 5-minute TTL.
    const a = { dir: "/w", original: "a", newName: "b->c" };
    const b = { dir: "/w", original: "a->b", newName: "c" };
    const oldKey = (x: typeof a): string => `files.rename:${x.dir}/${x.original}->${x.newName}`;
    expect(oldKey(a)).toBe(oldKey(b));

    expect(await keyFor(a)).not.toBe(await keyFor(b));
  });

  it("emits verbatim components for ordinary names", async () => {
    expect(await keyFor({ dir: "/src", original: "old.ts", newName: "new.ts" })).toBe(
      "files.rename:/src:old.ts:new.ts",
    );
  });

  it("separates the directory from the original name", async () => {
    // The old form joined dir and original with "/", so ("/a", "b/c") and
    // ("/a/b", "c") produced one key for two different files.
    const x = await keyFor({ dir: "/a", original: "b/c", newName: "z" });
    const y = await keyFor({ dir: "/a/b", original: "c", newName: "z" });
    expect(x).not.toBe(y);
  });
});

describe("files.create idempotency keys", () => {
  async function createKey(dir: string, name: string): Promise<string> {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    const { createFile } = await import("./files.js");
    await createFile.dispatch({ dir, name });
    const key = headerValue(mockFetch.mock.calls[0]![1], "idempotency-key");
    mockFetch.mockReset();
    resetActionFramework();
    return key ?? "";
  }

  it("distinguishes a slash in the name from a slash in the directory", async () => {
    const oldKey = (dir: string, name: string): string => `files.create:${dir}/${name}`;
    expect(oldKey("/a", "b/c")).toBe(oldKey("/a/b", "c"));
    expect(await createKey("/a", "b/c")).not.toBe(await createKey("/a/b", "c"));
  });

  it("emits verbatim components for ordinary input", async () => {
    expect(await createKey("/src", "index.ts")).toBe("files.create:/src:index.ts");
  });

  it("create_folder keys live in their own namespace", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    const { createFolder } = await import("./files.js");
    await createFolder.dispatch({ dir: "/src", name: "index.ts" });
    expect(headerValue(mockFetch.mock.calls[0]![1], "idempotency-key")).toBe(
      "files.create_folder:/src:index.ts",
    );
  });
});

describe("files.delete dedupe key", () => {
  /** Dispatch a batch delete and report whether fetch ran (a deduped second
   *  dispatch is folded into the first in-flight promise and never fetches). */
  async function dispatchBoth(first: string[], second: string[]): Promise<number> {
    mockFetch.mockImplementation(
      () =>
        new Promise((resolve) => {
          setTimeout(() => {
            resolve(new Response(JSON.stringify({}), { status: 200 }));
          }, 0);
        }),
    );
    const { deleteFilesBatch } = await import("./files.js");
    const listEl = document.createElement("div");
    const a = deleteFilesBatch.dispatch({ dir: "/w", names: first, listEl });
    const b = deleteFilesBatch.dispatch({ dir: "/w", names: second, listEl });
    await Promise.all([a, b]);
    return mockFetch.mock.calls.length;
  }

  it("does not collapse the batch [\u201ca,b\u201d] into the batch [\u201ca\u201d,\u201cb\u201d]", async () => {
    // A "," -joined list cannot tell one filename containing a comma from two
    // filenames. Nested through its own join, the two batches differ, so both
    // dispatches really run (3 fetches: 1 + 2) instead of the second being
    // folded into the first.
    const oldKey = (names: string[]): string => `files.delete:${names.slice().sort().join(",")}`;
    expect(oldKey(["a,b"])).toBe(oldKey(["a", "b"]));

    expect(await dispatchBoth(["a,b"], ["a", "b"])).toBe(3);
  });

  it("still dedupes an identical batch regardless of order", async () => {
    expect(await dispatchBoth(["a", "b"], ["b", "a"])).toBe(2);
  });
});
