// @vitest-environment happy-dom
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("./api-client.js", () => ({
  apiGet: vi.fn(() => Promise.resolve({})),
  apiPatch: vi.fn(() => Promise.resolve(undefined)),
}));
vi.mock("./save-indicator.js", () => ({
  showSaving: vi.fn(),
  showSaved: vi.fn(),
}));

import { patchSettings } from "./persist.js";
import { apiPatch } from "./api-client.js";

describe("patchSettings debounce coalescing", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("coalesces multiple rapid calls into a single PATCH with merged body", async () => {
    patchSettings({ auto_update: true });
    patchSettings({ debug_logs: true });
    patchSettings({ last_model: "claude" });

    // Advance past debounce.
    await vi.advanceTimersByTimeAsync(350);

    expect(apiPatch).toHaveBeenCalledTimes(1);
    expect(apiPatch).toHaveBeenCalledWith("/api/settings", {
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

    expect(apiPatch).toHaveBeenCalledTimes(1);
    expect(apiPatch).toHaveBeenCalledWith("/api/settings", {
      last_model: "gemini",
    });
  });

  it("property: N rapid calls produce merged result matching Object.assign", async () => {
    // This test verifies the coalescing invariant for a single batch.
    // Each iteration is independent: we flush the timer to complete
    // the previous batch before starting the next.
    const iterations = 50;
    for (let iter = 0; iter < iterations; iter++) {
      vi.clearAllMocks();
      // Generate random patches.
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

      const calls = vi.mocked(apiPatch).mock.calls;
      expect(calls.length).toBe(1);
      expect(calls[0]![1]).toEqual(expected);
    }
  });
});
