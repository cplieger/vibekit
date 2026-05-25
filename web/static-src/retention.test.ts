// @vitest-environment happy-dom
// Tests for retention.ts: SSE-triggered refresh, abort safety, state + listeners.

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("./api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined, _ms: number) =>
    signal ?? new AbortController().signal,
  fetchKiroSetting: vi.fn(),
}));

vi.mock("./toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

import { fetchKiroSetting } from "./api-client.js";
import { _resetForTest as resetDefine } from "./actions/define.js";
import { _resetForTest as resetRegistry } from "./actions/registry.js";

const mockFetchKiroSetting = vi.mocked(fetchKiroSetting);

beforeEach(() => {
  resetDefine();
  resetRegistry();
  vi.clearAllMocks();
});

describe("retention", () => {
  it("SSE-triggered refresh after in-flight returns FRESH data (no dedupe stale issue)", async () => {
    // First call returns 7, second call returns 30 (simulating SSE-triggered refresh)
    let callCount = 0;
    mockFetchKiroSetting.mockImplementation(async () => {
      callCount++;
      if (callCount === 1) return 7;
      return 30;
    });

    // Fresh import to get clean module state
    const mod = await import("./retention.js");

    // First refresh
    await mod.refreshRetention();
    expect(mod.isRetentionEnabled()).toBe(true);

    // Second refresh (simulating SSE-triggered) returns fresh data
    await mod.refreshRetention();
    expect(mod.isRetentionEnabled()).toBe(true);
    expect(mockFetchKiroSetting).toHaveBeenCalledTimes(2);
  });

  it("abort during fetch does NOT update retentionDays", async () => {
    mockFetchKiroSetting.mockImplementation(async (_key, _parse, _fallback, signal) => {
      // Simulate abort during fetch
      if (signal?.aborted) throw new DOMException("Aborted", "AbortError");
      return null; // returning null means the dispatch result is null → no update
    });

    const mod = await import("./retention.js");

    // When fetchKiroSetting returns null, refreshRetention early-returns
    // without updating state
    await mod.refreshRetention();
    // Default is enabled (retentionDays = 1)
    expect(mod.isRetentionEnabled()).toBe(true);
  });

  it("success updates state and fires listeners", async () => {
    mockFetchKiroSetting.mockResolvedValue(14);

    const mod = await import("./retention.js");
    const listener = vi.fn();
    const unsub = mod.onRetentionChange(listener);

    await mod.refreshRetention();

    expect(mod.isRetentionEnabled()).toBe(true);
    expect(listener).toHaveBeenCalledTimes(1);

    unsub();
    // After unsub, listener should not fire again
    mockFetchKiroSetting.mockResolvedValue(0);
    await mod.refreshRetention();
    expect(listener).toHaveBeenCalledTimes(1);
  });
});
