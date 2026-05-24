// @vitest-environment happy-dom
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("./api-client.js", () => ({
  apiGet: vi.fn(() => Promise.resolve({})),
  apiPatch: vi.fn(() => Promise.resolve(undefined)),
  withTimeout: (_signal: AbortSignal | undefined, _ms: number) => AbortSignal.timeout(30000),
  API_TIMEOUT_MS: 30000,
}));
vi.mock("./save-indicator.js", () => ({
  showSaving: vi.fn(),
  showSaved: vi.fn(),
  showError: vi.fn(),
}));
vi.mock("./toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

import { patchSettings } from "./persist.js";

describe("patchSettings debounce coalescing", () => {
  let fetchSpy: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    vi.useFakeTimers();
    vi.clearAllMocks();
    fetchSpy = vi.fn(() => Promise.resolve(new Response("{}", { status: 200 })));
    vi.stubGlobal("fetch", fetchSpy);
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("coalesces multiple rapid calls into a single PATCH with merged body", async () => {
    patchSettings({ auto_update: true });
    patchSettings({ debug_logs: true });
    patchSettings({ last_model: "claude" });

    // Advance past debounce.
    await vi.advanceTimersByTimeAsync(350);

    expect(fetchSpy).toHaveBeenCalledTimes(1);
    const [url, init] = fetchSpy.mock.calls[0]!;
    expect(url).toBe("/api/settings");
    expect(init.method).toBe("PATCH");
    expect(JSON.parse(init.body as string)).toEqual({
      auto_update: true,
      debug_logs: true,
      last_model: "claude",
    });
  });

  it("last-writer-wins for same key across rapid calls", async () => {
    patchSettings({ last_model: "gpt4" });
    patchSettings({ last_model: "claude" });
    patchSettings({ last_model: "gemini" });

    await vi.advanceTimersByTimeAsync(350);

    expect(fetchSpy).toHaveBeenCalledTimes(1);
    const [, init] = fetchSpy.mock.calls[0]!;
    expect(JSON.parse(init.body as string)).toEqual({
      last_model: "gemini",
    });
  });

  it("property: N rapid calls produce merged result matching Object.assign", async () => {
    // NOTE: This property test uses Math.random for fuzzing. Failures may not
    // be reproducible without seeding. Log `iter` on failure to narrow down.
    const iterations = 50;
    for (let iter = 0; iter < iterations; iter++) {
      fetchSpy.mockClear();
      const keys = ["auto_update", "debug_logs", "last_model"] as const;
      const patches: Record<string, unknown>[] = [];
      const count = 1 + Math.floor(Math.random() * 5);
      for (let i = 0; i < count; i++) {
        const patch: Record<string, unknown> = {};
        for (const k of keys) {
          if (Math.random() > 0.5) {
            patch[k] = k === "last_model" ? ["a", "b", "c"][Math.floor(Math.random() * 3)] : Math.random() > 0.5;
          }
        }
        if (Object.keys(patch).length > 0) {
          patches.push(patch);
        }
      }
      if (patches.length === 0) continue;

      for (const patch of patches) {
        patchSettings(patch as Parameters<typeof patchSettings>[0]);
      }
      await vi.advanceTimersByTimeAsync(350);

      const expected: Record<string, unknown> = {};
      for (const patch of patches) {
        Object.assign(expected, patch);
      }

      const calls = fetchSpy.mock.calls;
      expect(calls.length).toBe(1);
      const [, init] = calls[0]!;
      expect(JSON.parse(init.body as string)).toEqual(expected);
    }
  });
});
