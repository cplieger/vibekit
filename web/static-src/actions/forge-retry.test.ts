// @vitest-environment happy-dom
// Tests for retry behavior of forge actions post round-2 changes:
// - signOut: retryable, error toast with Retry button on network error
// - startDeviceFlow: retryable, auto-retry (no toast — error: false)
// - cloneRepo: retryable + retry config, auto-retries, idempotency key reused
// - connectPAT: retryable + retry config, auto-retries, idempotency key reused
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

import * as toast from "../toast.js";
import { _resetForTest as resetDefine, IDEMPOTENCY_HEADER } from "./define.js";
import { _resetForTest as resetRegistry } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import { signOut, startDeviceFlow, cloneRepo, connectPAT } from "./forge.js";

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
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
// signOut — retryable: "network", error toast with Retry button
// ===========================================================================

describe("forge.signOut retry", () => {
  it("shows Retry button on network error", async () => {
    const fetchSpy = vi.fn<typeof fetch>(networkError);
    vi.stubGlobal("fetch", fetchSpy);

    const p = signOut.dispatch({ forgeId: "gh:user" });
    // Advance past auto-retry delays (300ms + 600ms)
    await vi.advanceTimersByTimeAsync(1000);
    await p;

    expect(toast.error).toHaveBeenCalledTimes(1);
    const retryArg = vi.mocked(toast.error).mock.calls[0]![1];
    expect(retryArg).toBeDefined();
    expect(typeof retryArg!.onClick).toBe("function");
  });

  it("does NOT show Retry button on HTTP 403", async () => {
    const fetchSpy = vi.fn<typeof fetch>(() => Promise.resolve(new Response('{"error":"forbidden"}', { status: 403 })));
    vi.stubGlobal("fetch", fetchSpy);

    await signOut.dispatch({ forgeId: "gh:user" });

    expect(toast.error).toHaveBeenCalledTimes(1);
    const retryArg = vi.mocked(toast.error).mock.calls[0]![1];
    expect(retryArg).toBeUndefined();
  });

  it("Retry re-dispatches with same frozen args", async () => {
    let attempt = 0;
    const fetchSpy = vi.fn<typeof fetch>(() => {
      attempt++;
      if (attempt <= 3) return Promise.reject(new TypeError("Failed to fetch"));
      return Promise.resolve(new Response(null, { status: 204 }));
    });
    vi.stubGlobal("fetch", fetchSpy);

    const p = signOut.dispatch({ forgeId: "gh:user" });
    // Advance past auto-retry delays (300ms + 600ms) for initial dispatch
    await vi.advanceTimersByTimeAsync(1000);
    await p;
    expect(attempt).toBe(3); // 1 initial + 2 retries, all fail

    // Click the retry button (manual retry triggers a new dispatch)
    const retryFn = vi.mocked(toast.error).mock.calls[0]![1]!.onClick;
    const p2 = retryFn();
    // Advance past auto-retry delays for the manual retry dispatch
    await vi.advanceTimersByTimeAsync(1000);
    if (p2 instanceof Promise) await p2;

    // 4th attempt succeeds
    expect(attempt).toBe(4);
    // Last call used the same path (same args)
    const lastCall = fetchSpy.mock.calls[fetchSpy.mock.calls.length - 1]!;
    expect(lastCall[0]).toBe("/api/forges/gh%3Auser");
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
      if (attempt < 3) return Promise.reject(new TypeError("Failed to fetch"));
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
      if (attempt < 3) return Promise.reject(new TypeError("Failed to fetch"));
      return Promise.resolve(new Response('{}', { status: 200 }));
    });
    vi.stubGlobal("fetch", fetchSpy);

    const p = cloneRepo.dispatch({ url: "https://github.com/org/repo" });
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    await p;

    expect(fetchSpy).toHaveBeenCalledTimes(3);
    const key1 = (fetchSpy.mock.calls[0]![1] as RequestInit).headers as Record<string, string>;
    const key2 = (fetchSpy.mock.calls[1]![1] as RequestInit).headers as Record<string, string>;
    const key3 = (fetchSpy.mock.calls[2]![1] as RequestInit).headers as Record<string, string>;
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
      if (attempt < 3) return Promise.reject(new TypeError("Failed to fetch"));
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
    const key1 = (fetchSpy.mock.calls[0]![1] as RequestInit).headers as Record<string, string>;
    const key2 = (fetchSpy.mock.calls[1]![1] as RequestInit).headers as Record<string, string>;
    const key3 = (fetchSpy.mock.calls[2]![1] as RequestInit).headers as Record<string, string>;
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

    const result = await startDeviceFlow.dispatch({});

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
      if (attempt === 1) return Promise.reject(new TypeError("Failed to fetch"));
      return Promise.resolve(new Response('{"device_code":"abc"}', { status: 200 }));
    });
    vi.stubGlobal("fetch", fetchSpy);

    const onError = vi.fn();
    await startDeviceFlow.dispatch({}, { onError });

    expect(onError).toHaveBeenCalledTimes(1);
    expect(onError.mock.calls[0]![0]).toMatchObject({ code: "network" });
  });
});
