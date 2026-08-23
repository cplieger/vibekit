// Tests for notify.ts action configuration and error paths.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

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
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { getActionLog as recentLog } from "./index.js";
import * as toast from "../toast.js";

const mockFetch = vi.fn();

beforeEach(() => {
  resetActionFramework();
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
    // Production asks `"serviceWorker" in navigator`, so the absence has to be
    // real: an own `undefined` shadow still answers TRUE to `in`, and a `delete`
    // on the instance removes nothing because the property is an accessor on
    // Navigator.prototype. Taking it off the PROTOTYPE for the duration is the
    // only shape that expresses "this browser does not have it", and it is put
    // back in the finally.
    const proto = Object.getPrototypeOf(navigator) as object;
    const orig = Object.getOwnPropertyDescriptor(proto, "serviceWorker");
    Reflect.deleteProperty(proto, "serviceWorker");
    try {
      const result = await registerPush.dispatch(undefined);
      expect(result).toBeNull();
      const log = recentLog();
      expect(log[0]?.status).toBe("error");
      expect(log[0]?.error?.code).toBe("unsupported");
    } finally {
      if (orig) {
        Object.defineProperty(proto, "serviceWorker", orig);
      }
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

    // The action's rollback dispatches a custom event; the UI layer
    // listens and unchecks the toggle. Wire that listener here so the
    // test exercises the public contract instead of poking internals.
    const onFailed = (): void => {
      toggle.checked = false;
    };
    document.addEventListener("notify:registration-failed", onFailed);

    Object.defineProperty(navigator, "serviceWorker", {
      value: undefined,
      configurable: true,
    });

    try {
      await registerPush.dispatch(undefined);
      expect(toggle.checked).toBe(false);
    } finally {
      document.removeEventListener("notify:registration-failed", onFailed);
      document.body.removeChild(toggle);
    }
  });
});
