// @vitest-environment happy-dom
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("./api-client.js", () => ({
  apiGet: vi.fn(() => Promise.resolve({})),
  withTimeout: (_signal: AbortSignal | undefined, _ms: number) => AbortSignal.timeout(30000),
  API_TIMEOUT_MS: 30000,
}));
vi.mock("./save-indicator.js", () => ({
  showSaving: vi.fn(),
  showSaved: vi.fn(),
  showError: vi.fn(),
}));
vi.mock("./toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
}));

import { patchSettings, initSettingsTracking, __testResetTracking } from "./persist.js";

describe("patchSettings debounce coalescing", () => {
  let fetchSpy: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    vi.useFakeTimers();
    vi.clearAllMocks();
    fetchSpy = vi.fn(() => Promise.resolve(new Response("{}", { status: 200 })));
    vi.stubGlobal("fetch", fetchSpy);
    // Reset the dedup tracker so tests don't bleed state between
    // each other. Without this, patchSettings({last_model: "claude"})
    // in test N+1 would be filtered out as a duplicate of test N's
    // value.
    __testResetTracking();
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
      // Each iteration is an independent batch; reset the dedup
      // tracker so values from prior iterations don't filter out
      // patches in this one.
      __testResetTracking();
      // Generate random patches.
      const keys = ["auto_update", "debug_logs", "last_model"] as const;
      const patches: Record<string, unknown>[] = [];
      const count = 1 + Math.floor(Math.random() * 5);
      for (let i = 0; i < count; i++) {
        const patch: Record<string, unknown> = {};
        for (const k of keys) {
          if (Math.random() > 0.5) {
            patch[k] =
              k === "last_model"
                ? ["a", "b", "c"][Math.floor(Math.random() * 3)]
                : Math.random() > 0.5;
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

describe("patchSettings no-op dedup", () => {
  let fetchSpy: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    vi.useFakeTimers();
    vi.clearAllMocks();
    fetchSpy = vi.fn(() => Promise.resolve(new Response("{}", { status: 200 })));
    vi.stubGlobal("fetch", fetchSpy);
    __testResetTracking();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("skips PATCH when the value matches the seeded server state (page-reload bootstrap case)", async () => {
    initSettingsTracking({ shell_policy: "safe_commands", last_model: "claude" });
    // Simulate the bootstrap fire from onSelectionChange: same shell_policy
    // value the server already has.
    patchSettings({ shell_policy: "safe_commands" });
    await vi.advanceTimersByTimeAsync(350);
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it("PATCHes only the changed key when bootstrap and a real change overlap", async () => {
    initSettingsTracking({ shell_policy: "safe_commands", last_model: "claude" });
    // shell_policy unchanged, last_model changed.
    patchSettings({ shell_policy: "safe_commands" });
    patchSettings({ last_model: "gemini" });
    await vi.advanceTimersByTimeAsync(350);
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    expect(JSON.parse((fetchSpy.mock.calls[0]![1] as RequestInit).body as string)).toEqual({
      last_model: "gemini",
    });
  });

  it("the second identical patch in a session is also filtered", async () => {
    patchSettings({ last_model: "claude" });
    await vi.advanceTimersByTimeAsync(350);
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    fetchSpy.mockClear();
    patchSettings({ last_model: "claude" });
    await vi.advanceTimersByTimeAsync(350);
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it("PATCHes again when the value changes back after a different value", async () => {
    initSettingsTracking({ last_model: "claude" });
    patchSettings({ last_model: "gemini" });
    await vi.advanceTimersByTimeAsync(350);
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    fetchSpy.mockClear();
    // Round-tripping back to "claude" IS a change (current is "gemini").
    patchSettings({ last_model: "claude" });
    await vi.advanceTimersByTimeAsync(350);
    expect(JSON.parse((fetchSpy.mock.calls[0]![1] as RequestInit).body as string)).toEqual({
      last_model: "claude",
    });
  });

  it("array-valued settings dedup correctly", async () => {
    initSettingsTracking({ trust_tools: ["a", "b", "c"] });
    patchSettings({ trust_tools: ["a", "b", "c"] });
    await vi.advanceTimersByTimeAsync(350);
    expect(fetchSpy).not.toHaveBeenCalled();
    // Different order is treated as different (we use deterministic JSON).
    patchSettings({ trust_tools: ["c", "b", "a"] });
    await vi.advanceTimersByTimeAsync(350);
    expect(JSON.parse((fetchSpy.mock.calls[0]![1] as RequestInit).body as string)).toEqual({
      trust_tools: ["c", "b", "a"],
    });
  });

  it("does not fire showSaving when every key in the patch is filtered", async () => {
    initSettingsTracking({ shell_policy: "safe_commands" });
    const { showSaving } = await import("./save-indicator.js");
    patchSettings({ shell_policy: "safe_commands" });
    await vi.advanceTimersByTimeAsync(350);
    expect(showSaving).not.toHaveBeenCalled();
  });

  it("resolvers fire even when showSaved throws (try/finally guarantee)", async () => {
    const { showSaved } = await import("./save-indicator.js");
    const consoleErr = vi.spyOn(console, "error").mockImplementation(() => undefined);
    vi.mocked(showSaved).mockImplementation(() => {
      throw new Error("indicator boom");
    });
    const p = patchSettings({ last_model: "opus" });
    await vi.advanceTimersByTimeAsync(350);
    // The promise should still resolve (resolvers fire in finally block).
    const result = await p;
    expect(result).not.toBeUndefined();
    expect(consoleErr).toHaveBeenCalled();
    consoleErr.mockRestore();
    vi.mocked(showSaved).mockReset();
  });
});
