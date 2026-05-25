// @vitest-environment happy-dom
// Tests for apiAction HTTP request handling and error classification.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
}));

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined, _ms: number) =>
    signal ?? new AbortController().signal,
}));

import { apiAction } from "./api.js";
import { _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry, recentLog } from "./registry.js";

const mockFetch = vi.fn();

beforeEach(() => {
  resetDefine();
  resetRegistry();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
});

afterEach(() => {
  vi.restoreAllMocks();
});

const testAction = () =>
  apiAction<{ id: string }, { name: string }>({
    name: "test.api",
    request: ({ id }) => ({ method: "GET", path: `/api/items/${id}` }),
    error: "Test failed",
  });

describe("apiAction", () => {
  it("returns parsed JSON on 200", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ name: "foo" }), { status: 200 }));
    const action = testAction();
    const result = await action.dispatch({ id: "1" });
    expect(result).toEqual({ name: "foo" });
    expect(recentLog()[0]?.status).toBe("success");
  });

  it("returns undefined on 204 (no JSON parse)", async () => {
    mockFetch.mockResolvedValue(new Response(null, { status: 204 }));
    const action = testAction();
    const result = await action.dispatch({ id: "1" });
    expect(result).toBeUndefined();
    expect(recentLog()[0]?.status).toBe("success");
  });

  it("throws ActionError with code 'timeout' on TimeoutError DOMException", async () => {
    const err = new DOMException("The operation timed out", "TimeoutError");
    mockFetch.mockRejectedValue(err);
    const action = testAction();
    const result = await action.dispatch({ id: "1" });
    expect(result).toBeNull();
    const log = recentLog()[0];
    expect(log?.status).toBe("error");
    expect(log?.error?.code).toBe("timeout");
  });

  it("throws ActionError with code 'cancelled' on AbortError when signal.aborted", async () => {
    mockFetch.mockRejectedValue(new DOMException("The operation was aborted", "AbortError"));
    const action = testAction();
    // Cancel immediately so signal.aborted is true when the catch runs.
    const promise = action.dispatch({ id: "1" });
    action.cancel();
    const result = await promise;
    expect(result).toBeNull();
    expect(recentLog()[0]?.status).toBe("cancelled");
  });

  it("throws ActionError with code 'network' on TypeError (Failed to fetch)", async () => {
    mockFetch.mockRejectedValue(new TypeError("Failed to fetch"));
    const action = testAction();
    const result = await action.dispatch({ id: "1" });
    expect(result).toBeNull();
    const log = recentLog()[0];
    expect(log?.status).toBe("error");
    expect(log?.error?.code).toBe("network");
  });

  it("throws ActionError with status + body.error message on non-OK response", async () => {
    mockFetch.mockResolvedValue(new Response(
      JSON.stringify({ error: "Not found" }),
      { status: 404 },
    ));
    const action = testAction();
    const result = await action.dispatch({ id: "1" });
    expect(result).toBeNull();
    const log = recentLog()[0];
    expect(log?.status).toBe("error");
    expect(log?.error?.status).toBe(404);
    expect(log?.error?.message).toBe("Not found");
  });
});
