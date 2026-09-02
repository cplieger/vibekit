// Tests for retry behavior of forge actions post round-2 changes:
// - signOut: NOT retryable (destructive DELETE may succeed on timeout)
// - startDeviceFlow: retryable, no auto-retry (no toast — error: false)
// - cloneRepo: NOT retryable (an interrupted clone can leave a partial
//   destination, so a retry reports a false "already exists")
// - connectPAT: retryable + retry config, auto-retries, idempotency key reused
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

import * as toast from "../toast.js";
const IDEMPOTENCY_HEADER = "idempotency-key";
import { resetActionFramework, headerValue } from "./__test-helpers__/action-test-setup.js";
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

// Capture the timeout budgets composed through @cplieger/fetch's withTimeout.
// Only cloneRepo's own direct call is intercepted (apiAction's internal
// timeout composition goes through the package's private module edge).
const timeoutSpy = vi.hoisted(() => ({ budgets: [] as number[] }));
vi.mock("@cplieger/fetch", async (importOriginal) => {
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  const orig = await importOriginal<typeof import("@cplieger/fetch")>();
  return {
    ...orig,
    withTimeout: (signal: AbortSignal | undefined, ms: number) => {
      timeoutSpy.budgets.push(ms);
      return orig.withTimeout(signal, ms);
    },
  };
});

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
// cloneRepo — NOT retryable: an interrupted clone may have left a partial
// destination server-side, so a retry would hit the "already exists" refusal
// and report a misleading failure instead of the real one.
// ===========================================================================

describe("forge.cloneRepo retry", () => {
  it("does NOT auto-retry on network error", async () => {
    const fetchSpy = vi.fn<typeof fetch>(networkError);
    vi.stubGlobal("fetch", fetchSpy);

    const p = cloneRepo.dispatch({ url: "https://github.com/org/repo" });
    // Give any (wrong) retry schedule room to fire.
    await vi.advanceTimersByTimeAsync(1000);
    const result = await p;

    expect(fetchSpy).toHaveBeenCalledTimes(1);
    expect(result).toBeNull();
    // No toast emitted — error: false, the caller aggregates.
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("runs on a clone-sized timeout, not the standard 30s", async () => {
    timeoutSpy.budgets.length = 0;
    const fetchSpy = vi.fn<typeof fetch>(() =>
      Promise.resolve(new Response("{}", { status: 200 })),
    );
    vi.stubGlobal("fetch", fetchSpy);

    await cloneRepo.dispatch({ url: "https://github.com/org/repo" });

    // The original defect: the standard 30s API timeout aborted the request,
    // which killed the server's git subprocess mid-transfer. The budget must
    // exceed the server's own 10-minute clone bound so the verdict the user
    // sees is the server's.
    expect(timeoutSpy.budgets).toEqual([11 * 60_000]);
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
    const key1 = headerValue(fetchSpy.mock.calls[0]![1], IDEMPOTENCY_HEADER);
    const key2 = headerValue(fetchSpy.mock.calls[1]![1], IDEMPOTENCY_HEADER);
    const key3 = headerValue(fetchSpy.mock.calls[2]![1], IDEMPOTENCY_HEADER);
    expect(key1).toBeDefined();
    expect(key1).toBe(key2);
    expect(key2).toBe(key3);
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
