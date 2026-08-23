// Tests for retention.ts: reads the vibekit-owned chat_retention_days from
// /api/settings; state + listeners; the off(0)/forever(-1)/N-days semantics.

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("./api-client.js", () => ({
  apiGet: vi.fn(),
}));

vi.mock("./toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
}));

import { apiGet } from "./api-client.js";

const mockApiGet = vi.mocked(apiGet);

// Return the /api/settings shape retention.ts reads.
function settings(days: number | undefined): { chat_retention_days?: number } {
  return days === undefined ? {} : { chat_retention_days: days };
}

beforeEach(() => {
  vi.resetModules();
  vi.clearAllMocks();
});

describe("retention", () => {
  it("reads chat_retention_days from /api/settings on refresh", async () => {
    mockApiGet.mockResolvedValue(settings(14));
    const mod = await import("./retention.js");

    await mod.refreshRetention();

    expect(mod.isRetentionEnabled()).toBe(true);
    expect(mockApiGet).toHaveBeenCalledWith("/api/settings", expect.anything());
  });

  it("a null response (network failure) leaves the current value in place", async () => {
    mockApiGet.mockResolvedValue(null);
    const mod = await import("./retention.js");

    await mod.refreshRetention();

    // Default is 1 (enabled); a failed fetch must not flip it off.
    expect(mod.isRetentionEnabled()).toBe(true);
  });

  it("a missing field falls back to the default (1 day, enabled)", async () => {
    mockApiGet.mockResolvedValue(settings(undefined));
    const mod = await import("./retention.js");

    await mod.refreshRetention();

    expect(mod.isRetentionEnabled()).toBe(true);
  });

  it("forever (-1) counts as enabled (archive on close, History shown)", async () => {
    mockApiGet.mockResolvedValue(settings(-1));
    const mod = await import("./retention.js");

    await mod.refreshRetention();

    expect(mod.isRetentionEnabled()).toBe(true);
  });

  it("off (0) is the only disabled state; subscribe fires on 0<->N flips", async () => {
    let next = 0;
    mockApiGet.mockImplementation(async () => settings(next));
    const mod = await import("./retention.js");
    const listener = vi.fn();
    const unsub = mod.onRetentionChange(listener);

    // subscribe() fires immediately with the current value (default 1 => enabled).
    expect(listener).toHaveBeenCalledTimes(1);
    expect(mod.isRetentionEnabled()).toBe(true);

    // N -> 0: fires on change, retention now disabled.
    next = 0;
    await mod.refreshRetention();
    expect(listener).toHaveBeenCalledTimes(2);
    expect(mod.isRetentionEnabled()).toBe(false);

    // 0 -> N: fires on change, retention re-enabled.
    next = 14;
    await mod.refreshRetention();
    expect(listener).toHaveBeenCalledTimes(3);
    expect(mod.isRetentionEnabled()).toBe(true);

    unsub();
    // After unsub, listener should not fire again.
    next = 0;
    await mod.refreshRetention();
    expect(listener).toHaveBeenCalledTimes(3);
  });
});
