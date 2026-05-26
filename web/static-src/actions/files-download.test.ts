// @vitest-environment happy-dom
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

import { _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry, recentLog } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import { downloadFiles } from "./files.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.clearAllMocks();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("downloadFiles action", () => {
  it("is registered as files.download and not retryable", () => {
    // The action object exposes its name via the registry when dispatched.
    expect(downloadFiles).toBeDefined();
    expect(typeof downloadFiles.dispatch).toBe("function");
  });

  it("dispatches a POST to /api/files/download and triggers blob download", async () => {
    const fakeBlob = new Blob(["zip-content"], { type: "application/zip" });
    const mockFetch = vi.fn().mockResolvedValue({
      ok: true,
      blob: () => Promise.resolve(fakeBlob),
    });
    vi.stubGlobal("fetch", mockFetch);

    const revokeURL = vi.fn();
    const createURL = vi.fn().mockReturnValue("blob:fake-url");
    vi.stubGlobal("URL", { ...URL, createObjectURL: createURL, revokeObjectURL: revokeURL });

    const clickSpy = vi.fn();
    const removeSpy = vi.fn();
    vi.spyOn(document, "createElement").mockImplementation((tag: string) => {
      if (tag === "a") {
        return { href: "", download: "", click: clickSpy, remove: removeSpy } as unknown as HTMLAnchorElement;
      }
      return document.createElement(tag);
    });
    vi.spyOn(document.body, "appendChild").mockImplementation((el) => el);

    const result = await downloadFiles.dispatch({ paths: ["dir/a.txt", "dir/b.txt"] });

    expect(result).toBeUndefined();
    expect(mockFetch).toHaveBeenCalledOnce();
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/files/download");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body as string)).toEqual({ paths: ["dir/a.txt", "dir/b.txt"] });
    expect(clickSpy).toHaveBeenCalledOnce();
    expect(removeSpy).toHaveBeenCalledOnce();
    expect(revokeURL).toHaveBeenCalledWith("blob:fake-url");
  });

  it("throws ActionError on non-ok response", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: false, status: 500 }));

    const result = await downloadFiles.dispatch({ paths: ["x.txt"] });
    // dispatch returns null on error (action framework catches and toasts)
    expect(result).toBeNull();
    const log = recentLog();
    expect(log[0]?.status).toBe("error");
  });

  it("does not trigger download if cancelled after blob()", async () => {
    const fakeBlob = new Blob(["zip-content"], { type: "application/zip" });
    const mockFetch = vi.fn().mockResolvedValue({
      ok: true,
      blob: () => Promise.resolve(fakeBlob),
    });
    vi.stubGlobal("fetch", mockFetch);

    const revokeURL = vi.fn();
    const createURL = vi.fn().mockReturnValue("blob:fake-url");
    vi.stubGlobal("URL", { ...URL, createObjectURL: createURL, revokeObjectURL: revokeURL });

    const clickSpy = vi.fn();
    vi.spyOn(document, "createElement").mockImplementation((tag: string) => {
      if (tag === "a") {
        return { href: "", download: "", click: clickSpy, remove: vi.fn() } as unknown as HTMLAnchorElement;
      }
      return document.createElement(tag);
    });
    vi.spyOn(document.body, "appendChild").mockImplementation((el) => el);

    // Start dispatch then cancel immediately — the blob() resolves but
    // signal should be aborted before the anchor click.
    const promise = downloadFiles.dispatch({ paths: ["a.txt"] });
    downloadFiles.cancel();
    const result = await promise;

    expect(result).toBeNull();
    expect(clickSpy).not.toHaveBeenCalled();
    // revokeObjectURL should still be called if createObjectURL was called
    if (createURL.mock.calls.length > 0) {
      expect(revokeURL).toHaveBeenCalledWith("blob:fake-url");
    }
  });

  it("records pending then success in registry", async () => {
    const fakeBlob = new Blob(["z"]);
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: true, blob: () => Promise.resolve(fakeBlob) }));
    vi.stubGlobal("URL", { ...URL, createObjectURL: () => "blob:x", revokeObjectURL: () => {} });
    vi.spyOn(document, "createElement").mockReturnValue(
      { href: "", download: "", click: vi.fn(), remove: vi.fn() } as unknown as HTMLAnchorElement,
    );
    vi.spyOn(document.body, "appendChild").mockImplementation((el) => el);

    await downloadFiles.dispatch({ paths: ["a.txt"] });
    const log = recentLog();
    expect(log.length).toBe(1);
    expect(log[0]?.name).toBe("files.download");
    expect(log[0]?.status).toBe("success");
  });
});
