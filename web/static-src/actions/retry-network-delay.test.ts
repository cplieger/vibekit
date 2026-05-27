// @vitest-environment happy-dom
// Tests for the retry primitives added in this iteration:
//   - networkMode: "online" (default) pauses retry while navigator.onLine === false
//   - networkMode: "always" retries regardless of network state
//   - retry.delay as a function: full control over backoff per attempt
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
}));

import { defineAction, _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import { ActionError, retryNetwork } from "./error.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.clearAllMocks();
  // happy-dom defaults navigator.onLine to true
});

afterEach(() => {
  // Restore navigator.onLine if any test changed it
  if ("onLine" in navigator) {
    Object.defineProperty(navigator, "onLine", { value: true, configurable: true });
  }
});

// ===========================================================================
// networkMode: "online" pauses retry while offline, resumes on online event
// ===========================================================================

describe("networkMode: 'online' (default)", () => {
  it("waits for online event before retrying when navigator.onLine is false", async () => {
    let attempts = 0;
    Object.defineProperty(navigator, "onLine", { value: false, configurable: true });

    const action = defineAction<void, string>({
      name: "test.online.pause",
      retryable: retryNetwork,
      retry: { count: 1, delay: 5 },
      error: false,
      run: () => {
        attempts++;
        throw new ActionError("offline", { code: "network" });
      },
    });

    const p = action.dispatch();
    // Give the loop a microtick to enter waitForOnline.
    await new Promise((r) => setTimeout(r, 20));
    expect(attempts).toBe(1); // first attempt fired, retry is paused

    // Come back online: dispatch the 'online' window event.
    Object.defineProperty(navigator, "onLine", { value: true, configurable: true });
    window.dispatchEvent(new Event("online"));

    await p;
    expect(attempts).toBe(2); // retried once after online event
  });

  it("doesn't pause when online to begin with", async () => {
    let attempts = 0;
    const action = defineAction<void, string>({
      name: "test.online.normal",
      retryable: retryNetwork,
      retry: { count: 2, delay: 1 },
      error: false,
      run: () => {
        attempts++;
        throw new ActionError("blip", { code: "network" });
      },
    });

    await action.dispatch();
    expect(attempts).toBe(3); // 1 initial + 2 retries, no pause needed
  });

  it("cancel during offline pause unwinds cleanly without retry", async () => {
    let attempts = 0;
    Object.defineProperty(navigator, "onLine", { value: false, configurable: true });

    const action = defineAction<void, string>({
      name: "test.online.cancel",
      retryable: retryNetwork,
      retry: { count: 5, delay: 1 },
      error: false,
      run: () => {
        attempts++;
        throw new ActionError("offline", { code: "network" });
      },
    });

    const p = action.dispatch();
    await new Promise((r) => setTimeout(r, 20));
    expect(attempts).toBe(1);

    action.cancel();
    await p;
    expect(attempts).toBe(1); // never retried
  });
});

// ===========================================================================
// networkMode: "always" — retry regardless of network state
// ===========================================================================

describe("networkMode: 'always'", () => {
  it("retries even when navigator.onLine is false", async () => {
    let attempts = 0;
    Object.defineProperty(navigator, "onLine", { value: false, configurable: true });

    const action = defineAction<void, string>({
      name: "test.always.retry",
      retryable: retryNetwork,
      retry: { count: 2, delay: 1 },
      networkMode: "always",
      error: false,
      run: () => {
        attempts++;
        throw new ActionError("ignored", { code: "network" });
      },
    });

    await action.dispatch();
    expect(attempts).toBe(3); // 1 + 2 retries, no offline pause
  });
});

// ===========================================================================
// retry.delay as a function — per-attempt backoff control
// ===========================================================================

describe("retry.delay as a function", () => {
  it("invokes delay function with attempt number and error", async () => {
    const seen: { attempt: number; code: string | undefined }[] = [];
    let runs = 0;

    const action = defineAction<void, string>({
      name: "test.delay.fn",
      retryable: retryNetwork,
      retry: {
        count: 2,
        delay: (attempt, err) => {
          seen.push({ attempt, code: err.code });
          return 0; // fast retry
        },
      },
      error: false,
      run: () => {
        runs++;
        throw new ActionError("blip", { code: "network" });
      },
    });

    await action.dispatch();
    expect(runs).toBe(3);
    expect(seen).toHaveLength(2); // delay computed before each retry, not before initial
    expect(seen[0]?.attempt).toBe(1);
    expect(seen[1]?.attempt).toBe(2);
    expect(seen[0]?.code).toBe("network");
  });

  it("respects custom delay values", async () => {
    const start = Date.now();

    const action = defineAction<void, string>({
      name: "test.delay.values",
      retryable: retryNetwork,
      retry: {
        count: 2,
        delay: (attempt) => attempt * 10, // 10ms, 20ms
      },
      error: false,
      run: () => {
        throw new ActionError("blip", { code: "network" });
      },
    });

    await action.dispatch();
    const elapsed = Date.now() - start;
    expect(elapsed).toBeGreaterThanOrEqual(28); // 10 + 20 = 30ms (fudge for timer skew)
  });

  it("can implement Retry-After header pattern", async () => {
    let attempts = 0;
    const action = defineAction<void, string>({
      name: "test.delay.retry_after",
      retryable: retryNetwork,
      retry: {
        count: 2,
        // Simulate respecting a Retry-After header value via err.cause.
        delay: (_attempt, err) => {
          const cause = err.cause as { headers?: Map<string, string> } | undefined;
          const ra = cause?.headers?.get("retry-after");
          return ra !== undefined ? parseInt(ra) * 1000 : 100;
        },
      },
      error: false,
      run: () => {
        attempts++;
        throw new ActionError("rate-limited", {
          code: "network",
          status: 429,
          cause: { headers: new Map([["retry-after", "0"]]) }, // 0 seconds for test speed
        });
      },
    });

    await action.dispatch();
    expect(attempts).toBe(3);
  });

  it("falls back to 0ms if delay function throws", async () => {
    let attempts = 0;
    const action = defineAction<void, string>({
      name: "test.delay.fn_throws",
      retryable: retryNetwork,
      retry: {
        count: 1,
        delay: () => {
          throw new Error("bad delay fn");
        },
      },
      error: false,
      run: () => {
        attempts++;
        throw new ActionError("blip", { code: "network" });
      },
    });

    await action.dispatch();
    expect(attempts).toBe(2); // retry still proceeds with 0ms delay
  });
});
