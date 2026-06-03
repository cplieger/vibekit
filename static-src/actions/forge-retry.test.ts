// @vitest-environment happy-dom
// Tests for retry behavior of forge actions post round-2 changes:
// - signOut: NOT retryable (destructive DELETE may succeed on timeout)
// - startDeviceFlow: retryable, no auto-retry (no toast — error: false)
// - cloneRepo: retryable + retry config, auto-retries, idempotency key reused
// - connectPAT: retryable + retry config, auto-retries, idempotency key reused
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

import * as toast from "../toast.js";
const IDEMPOTENCY_HEADER = "idempotency-key";
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { signOut, startDeviceFlow, cloneRepo, connectPAT } from "./forge.js";

beforeEach(() => {
  resetActionFramework();
  vi.clearAllMocks();
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

function networkError(): never {
  throw new TypeError("Failed to fetch");
}

// ===========================================================================
// signOut — retryable: false (destructive DELETE that may succeed on timeout),
// error toast WITHOUT Retry button
// ===========================================================================

describe("forge.signOut retry", () => {
  it("does NOT show Retry button on network error (destructive DELETE)", async () => {
    const fetchSpy = vi.fn<typeof fetch>(networkError);
    vi.stubGlobal("fetch", fetchSpy);

    await signOut.dispatch({ forgeId: "gh:user" });

    // Only 1 attempt — no auto-retry for destructive DELETE
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    expect(toast.error).toHaveBeenCalledTimes(1);
    const retryArg = vi.mocked(toast.error).mock.calls[0]![1];
    // No retry button — a timed-out DELETE may have succeeded server-side
    expect(retryArg).toBeUndefined();
  });

  it("does NOT show Retry button on HTTP 403", async () => {
    const fetchSpy = vi.fn<typeof fetch>(() =>
      Promise.resolve(new Response('{"error":"forbidden"}', { status: 403 })),
    );
    vi.stubGlobal("fetch", fetchSpy);

    await signOut.dispatch({ forgeId: "gh:user" });

    expect(toast.error).toHaveBeenCalledTimes(1);
    const retryArg = vi.mocked(toast.error).mock.calls[0]![1];
    expect(retryArg).toBeUndefined();
  });
});

// ===========================================================================
// cloneRepo — retryable + retry config, auto-retries fire
// ===========================================================================

describe("forge.cloneRepo retry", () => {
  it("auto-retries on network error (2 retries, 3 total attempts)", async () => {
    let attempt = 0;
    const fetchSpy = vi.fn<typeof fetch>(() => {
      attempt++;
      if (attempt < 3) {
        return Promise.reject(new TypeError("Failed to fetch"));
      }
      return Promise.resolve(new Response('{"output":"ok"}', { status: 200 }));
    });
    vi.stubGlobal("fetch", fetchSpy);

    const p = cloneRepo.dispatch({ url: "https://github.com/org/repo" });
    // First retry at 300ms
    await vi.advanceTimersByTimeAsync(300);
    // Second retry at 300*2=600ms
    await vi.advanceTimersByTimeAsync(600);
    const result = await p;

    expect(fetchSpy).toHaveBeenCalledTimes(3);
    expect(result).toEqual({ output: "ok" });
    // No error toast since error: false
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("idempotency key is REUSED across retries", async () => {
    let attempt = 0;
    const fetchSpy = vi.fn<typeof fetch>(() => {
      attempt++;
      if (attempt < 3) {
        return Promise.reject(new TypeError("Failed to fetch"));
      }
      return Promise.resolve(new Response("{}", { status: 200 }));
    });
    vi.stubGlobal("fetch", fetchSpy);

    const p = cloneRepo.dispatch({ url: "https://github.com/org/repo" });
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    await p;

    expect(fetchSpy).toHaveBeenCalledTimes(3);
    const key1 = fetchSpy.mock.calls[0]![1]!.headers as Record<string, string>;
    const key2 = fetchSpy.mock.calls[1]![1]!.headers as Record<string, string>;
    const key3 = fetchSpy.mock.calls[2]![1]!.headers as Record<string, string>;
    expect(key1[IDEMPOTENCY_HEADER]).toBeDefined();
    expect(key1[IDEMPOTENCY_HEADER]).toBe(key2[IDEMPOTENCY_HEADER]);
    expect(key2[IDEMPOTENCY_HEADER]).toBe(key3[IDEMPOTENCY_HEADER]);
  });

  it("no retry toast button (error: false suppresses toast entirely)", async () => {
    const fetchSpy = vi.fn<typeof fetch>(networkError);
    vi.stubGlobal("fetch", fetchSpy);

    const p = cloneRepo.dispatch({ url: "https://github.com/org/repo" });
    // Exhaust all retries: 300ms + 600ms
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    await p;

    // 3 fetch calls (1 original + 2 retries)
    expect(fetchSpy).toHaveBeenCalledTimes(3);
    // No toast emitted
    expect(toast.error).not.toHaveBeenCalled();
  });
});

// ===========================================================================
// connectPAT — retryable + retry config, auto-retries fire
// ===========================================================================

describe("forge.connectPAT retry", () => {
  it("auto-retries on network error with idempotency key reused", async () => {
    let attempt = 0;
    const fetchSpy = vi.fn<typeof fetch>(() => {
      attempt++;
      if (attempt < 3) {
        return Promise.reject(new TypeError("Failed to fetch"));
      }
      return Promise.resolve(new Response('{"status":"ok"}', { status: 200 }));
    });
    vi.stubGlobal("fetch", fetchSpy);

    const p = connectPAT.dispatch({ kind: "github", host: "github.com", token: "ghp_xxx" });
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    const result = await p;

    expect(fetchSpy).toHaveBeenCalledTimes(3);
    expect(result).toEqual({ status: "ok" });

    // Idempotency key reused
    const key1 = fetchSpy.mock.calls[0]![1]!.headers as Record<string, string>;
    const key2 = fetchSpy.mock.calls[1]![1]!.headers as Record<string, string>;
    const key3 = fetchSpy.mock.calls[2]![1]!.headers as Record<string, string>;
    expect(key1[IDEMPOTENCY_HEADER]).toBe(key2[IDEMPOTENCY_HEADER]);
    expect(key2[IDEMPOTENCY_HEADER]).toBe(key3[IDEMPOTENCY_HEADER]);
  });

  it("no toast emitted (error: false)", async () => {
    const fetchSpy = vi.fn<typeof fetch>(networkError);
    vi.stubGlobal("fetch", fetchSpy);

    const p = connectPAT.dispatch({ kind: "github", host: "github.com", token: "ghp_xxx" });
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    await p;

    expect(toast.error).not.toHaveBeenCalled();
  });
});

// ===========================================================================
// startDeviceFlow — retryable (no retry config), no toast
// ===========================================================================

describe("forge.startDeviceFlow retry", () => {
  it("no auto-retry (no retry config), returns null on network error", async () => {
    const fetchSpy = vi.fn<typeof fetch>(networkError);
    vi.stubGlobal("fetch", fetchSpy);

    const result = await startDeviceFlow.dispatch(undefined);

    // Only 1 attempt — no retry config
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    expect(result).toBeNull();
    // No toast (error: false)
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("retryable flag enables manual retry via onError callback", async () => {
    // Even though error: false suppresses the toast retry button,
    // the retryable classification is still available for callers
    // using onError to implement their own retry UI.
    let attempt = 0;
    const fetchSpy = vi.fn<typeof fetch>(() => {
      attempt++;
      if (attempt === 1) {
        return Promise.reject(new TypeError("Failed to fetch"));
      }
      return Promise.resolve(new Response('{"device_code":"abc"}', { status: 200 }));
    });
    vi.stubGlobal("fetch", fetchSpy);

    const onError = vi.fn();
    await startDeviceFlow.dispatch(undefined, { onError });

    expect(onError).toHaveBeenCalledTimes(1);
    expect(onError.mock.calls[0]![0]).toMatchObject({ code: "network" });
  });
});
