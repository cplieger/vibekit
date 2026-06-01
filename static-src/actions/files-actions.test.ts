// @vitest-environment happy-dom
// Tests for files.ts: createFile, createFolder, renameFile, deleteFilesBatch, upload.
import { describe, it, expect, vi, beforeEach } from "vitest";

import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
}));

vi.mock("../upload.js", () => ({
  uploadFiles: vi.fn(),
}));

import { recentLog } from "./registry.js";
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
    const headers = mockFetch.mock.calls[0]![1].headers as Record<string, string>;
    expect(headers["Idempotency-Key"]).toEqual(expect.any(String));
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
      onError!("Network error");
    });
    const { upload } = await import("./files.js");
    const files = { length: 1, item: () => null, 0: new File([""], "x") } as unknown as FileList;
    const r = await upload.dispatch({ files, targetDir: "/" });
    expect(r).toBeNull();
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("Upload failed"), undefined);
  });
});
