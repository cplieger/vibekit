// Tests for retention.ts: reads the vibekit-owned chat_retention_days from
// /api/settings; state + listeners; the off(0)/forever(-1)/N-days semantics.

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("./api-client.js", () => ({
  apiGetTyped: vi.fn(),
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and this name is imported somewhere in it. No case here calls it.
  apiGet: vi.fn(),
}));

vi.mock("./toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
}));

import { apiGetTyped } from "./api-client.js";
import { settingsPayload } from "./__test-helpers__/settings.js";

// retention.ts reads through the generated decoder, so the double stands in for
// apiGetTyped and returns an already-decoded payload.
const mockApiGet = vi.mocked(apiGetTyped);

// The COMPLETE payload, because that is what the wire carries: GET /api/settings
// resolves every default underneath the stored document, so a fixture supplying
// only the key under test would exercise a response the server cannot produce.
function settings(days: number): ReturnType<typeof settingsPayload> {
  return settingsPayload({ chat_retention_days: days });
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
    expect(mockApiGet).toHaveBeenCalledWith(
      "/api/settings",
      expect.any(Function),
      expect.anything(),
    );
  });

  it("a null response (network failure) leaves the current value in place", async () => {
    let payload: unknown = settings(0);
    mockApiGet.mockImplementation(async () => payload);
    const mod = await import("./retention.js");

    // Move OFF the placeholder first, so the assertion below distinguishes
    // "kept what the server last said" from "the placeholder happens to agree".
    // Asserting enabled straight after a failed first fetch passes either way.
    await mod.refreshRetention();
    expect(mod.isRetentionEnabled()).toBe(false);

    payload = null;
    await mod.refreshRetention();

    expect(mod.isRetentionEnabled()).toBe(false);
  });

  // There is no absent-key case any more, and its subject is what went rather
  // than the test. It asserted a client-side fallback to a mirrored default, and
  // both halves are gone: the server resolves the default into the response, so a
  // payload without the key is a shape the wire cannot produce. It was also
  // passing for the wrong reason — the missing field arrived as `undefined`, and
  // `isRetentionEnabled()` reads `!== 0`, which `undefined` satisfies, so the case
  // would have stayed green with the default deleted.

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

    // subscribe() fires immediately with the current value (the default day
    // count => enabled).
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
