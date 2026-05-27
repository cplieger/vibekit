// @vitest-environment happy-dom
// Tests for kiro-config.ts: cancel + dispatch freshness, render on success, error path.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("./toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
}));

vi.mock("./icons.js", async (importOriginal) => {
  const orig = await importOriginal<Record<string, string>>();
  return orig;
});

vi.mock("./send-state.js", () => ({
  setLastError: vi.fn(),
  setSSEStatus: vi.fn(),
  onSendStateChange: vi.fn(() => () => {}),
  getSendState: vi.fn(() => ({ kind: "idle" })),
}));

vi.mock("./api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined, _ms: number) =>
    signal ?? new AbortController().signal,
  apiGet: vi.fn(),
}));

vi.mock("./editor-openers.js", () => ({ openFile: vi.fn() }));
vi.mock("./dom.js", () => {
  const div = document.createElement("div");
  return { $: { kiroConfigList: div } };
});

import { apiGet } from "./api-client.js";
import { _resetForTest as resetDefine } from "./actions/define.js";
import { _resetForTest as resetRegistry } from "./actions/registry.js";
import { $ } from "./dom.js";

const mockApiGet = vi.mocked(apiGet);

beforeEach(() => {
  vi.useFakeTimers();
  resetDefine();
  resetRegistry();
  mockApiGet.mockReset();
  $.kiroConfigList.replaceChildren();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("kiro-config", () => {
  it("cancel + dispatch produces a fresh fetch each time", async () => {
    let callCount = 0;
    mockApiGet.mockImplementation(async () => {
      callCount++;
      return { items: [{ name: `item${callCount}`, path: "/a", type: "steering" }] };
    });

    const { loadKiroConfig } = await import("./kiro-config.js");

    // First call
    loadKiroConfig();
    // Immediately cancel + re-dispatch (simulates rapid user interaction)
    loadKiroConfig();

    // Wait for async resolution
    await vi.advanceTimersByTimeAsync(0);
    expect(callCount).toBeGreaterThanOrEqual(2);
  });

  it("render runs on success", async () => {
    mockApiGet.mockResolvedValue({
      items: [{ name: "my-doc", path: "/workspace/.kiro/steering/env.md", type: "steering" }],
    });

    const { loadKiroConfig } = await import("./kiro-config.js");
    loadKiroConfig();

    await vi.advanceTimersByTimeAsync(0);
    expect($.kiroConfigList.children.length).toBeGreaterThan(0);
    expect($.kiroConfigList.textContent).toContain("Steering docs");
    expect($.kiroConfigList.textContent).toContain("my-doc");
  });

  it("error path works", async () => {
    mockApiGet.mockResolvedValue(null);

    const { loadKiroConfig } = await import("./kiro-config.js");
    loadKiroConfig();

    // Advance through retries (2 retries: 300ms + 600ms)
    await vi.advanceTimersByTimeAsync(1000);
    expect($.kiroConfigList.children.length).toBeGreaterThan(0);
    expect($.kiroConfigList.textContent).toContain("Failed to load config");
  });
});
