// @vitest-environment happy-dom
// Tests for notify.ts action configuration and error paths.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
}));

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
  apiGet: vi.fn(),
  apiPost: vi.fn(),
}));

vi.mock("../push-util.js", () => ({
  urlBase64ToUint8Array: (s: string) => new Uint8Array(s.length),
}));

import { unsubscribePush, registerPush } from "./notify.js";
import { apiGet } from "../api-client.js";
import { _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry, recentLog } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import * as toast from "../toast.js";

const mockFetch = vi.fn();

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.stubGlobal("fetch", mockFetch);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("unsubscribePush", () => {
  it("has correct action name", () => {
    expect(unsubscribePush.name).toBe("notify.unsubscribe_push");
  });

  it("POSTs to /api/push/unsubscribe", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    await unsubscribePush.dispatch({ endpoint: "https://push.example.com/sub1" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/push/unsubscribe");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body as string)).toEqual({ endpoint: "https://push.example.com/sub1" });
  });

  it("suppresses error toast on failure", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "gone" }), { status: 410 }));
    await unsubscribePush.dispatch({ endpoint: "x" });
    expect(toast.error).not.toHaveBeenCalled();
  });
});

describe("registerPush", () => {
  it("has correct action name", () => {
    expect(registerPush.name).toBe("notify.register_push");
  });

  it("throws when serviceWorker not supported", async () => {
    // Delete the property so `"serviceWorker" in navigator` is false
    const orig = Object.getOwnPropertyDescriptor(navigator, "serviceWorker");
     
    delete (navigator as unknown as Record<string, unknown>)["serviceWorker"];
    try {
      const result = await registerPush.dispatch(undefined);
      expect(result).toBeNull();
      const log = recentLog();
      expect(log[0]?.status).toBe("error");
      expect(log[0]?.error?.code).toBe("unsupported");
    } finally {
      if (orig) {Object.defineProperty(navigator, "serviceWorker", orig);}
    }
  });

  it("fails when VAPID key fetch returns null", async () => {
    const mockReg = { pushManager: { subscribe: vi.fn() } };
    Object.defineProperty(navigator, "serviceWorker", {
      value: { register: () => Promise.resolve(mockReg) },
      configurable: true,
    });
    vi.mocked(apiGet).mockResolvedValue(null);

    const result = await registerPush.dispatch(undefined);
    expect(result).toBeNull();
    const log = recentLog();
    expect(log[0]?.status).toBe("error");
    expect(log[0]?.error?.code).toBe("network");
  });

  it("rolls back toggle on failure", async () => {
    // Create a toggle element
    const toggle = document.createElement("input");
    toggle.type = "checkbox";
    toggle.id = "notify-toggle";
    toggle.checked = true;
    document.body.appendChild(toggle);

    Object.defineProperty(navigator, "serviceWorker", {
      value: undefined,
      configurable: true,
    });

    await registerPush.dispatch(undefined);
    expect(toggle.checked).toBe(false);
    document.body.removeChild(toggle);
  });
});
