// @vitest-environment happy-dom
// Tests for kiro-config.ts: cancel + dispatch freshness, render on success,
// error path, keyboard-operable rows, and the settings_updated live-refetch.

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
  onSendStateChange: vi.fn(() => () => {
    /* noop */
  }),
  getSendState: vi.fn(() => ({ kind: "idle" })),
}));

vi.mock("./api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined, _ms: number) =>
    signal ?? new AbortController().signal,
  apiGet: vi.fn(),
}));

vi.mock("./editor-openers.js", () => ({ openFile: vi.fn() }));
vi.mock("./transport.js", () => ({ send: vi.fn() }));
vi.mock("./dom.js", () => {
  const div = document.createElement("div");
  return { $: { kiroConfigList: div } };
});

// Capture the settings_updated handler so the live-refetch wiring is testable
// (mirrors the specs.test.ts bus mock — the factory runs lazily at import time,
// by which point the `let` is initialised).
let settingsUpdatedHandler: (() => void) | undefined;
vi.mock("./bus.js", () => ({
  onSSE: (type: string, fn: () => void) => {
    if (type === "settings_updated") {
      settingsUpdatedHandler = fn;
    }
    return () => {
      /* unsubscribe noop */
    };
  },
}));

import { apiGet } from "./api-client.js";
import { openFile } from "./editor-openers.js";
import { configure } from "@cplieger/actions";
import { $ } from "./dom.js";
import * as toast from "./toast.js";

const mockApiGet = vi.mocked(apiGet);

beforeEach(() => {
  vi.useFakeTimers();
  configure({
    success: (msg) => {
      toast.success(msg);
    },
    error: (msg, retry) => {
      toast.error(msg, retry);
    },
  });
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

  it("renders keyboard-operable rows that open the file on Enter", async () => {
    vi.mocked(openFile).mockClear();
    mockApiGet.mockResolvedValue({
      items: [{ name: "env", path: "/workspace/.kiro/steering/env.md", type: "steering" }],
    });

    const { loadKiroConfig } = await import("./kiro-config.js");
    loadKiroConfig();
    await vi.advanceTimersByTimeAsync(0);

    const row = $.kiroConfigList.querySelector<HTMLElement>(".kiro-config-row");
    expect(row).not.toBeNull();
    expect(row?.getAttribute("role")).toBe("button");
    expect(row?.getAttribute("tabindex")).toBe("0");
    expect(row?.getAttribute("aria-label")).toBe("Open env");

    row?.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(vi.mocked(openFile)).toHaveBeenCalledWith("/workspace/.kiro/steering/env.md");
  });

  it("live-refetches on settings_updated when the list is visible", async () => {
    // offsetParent is null in happy-dom by default; force "visible".
    Object.defineProperty($.kiroConfigList, "offsetParent", {
      configurable: true,
      get: () => document.body,
    });
    mockApiGet.mockResolvedValue({ items: [] });

    const { initKiroConfig } = await import("./kiro-config.js");
    initKiroConfig();
    expect(settingsUpdatedHandler).toBeTypeOf("function");

    mockApiGet.mockClear();
    settingsUpdatedHandler?.();
    await vi.advanceTimersByTimeAsync(0);
    expect(mockApiGet).toHaveBeenCalledTimes(1);
  });
});
