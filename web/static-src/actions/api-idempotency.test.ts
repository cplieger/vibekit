// @vitest-environment happy-dom
// Tests for apiAction idempotency key and edge-case response handling.
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
import { IDEMPOTENCY_HEADER, _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry, recentLog } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";

const mockFetch = vi.fn();

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("apiAction — idempotency key", () => {
  it("sends Idempotency-Key header when idempotencyKey: true", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    const action = apiAction<{ id: string }, unknown>({
      name: "test.idem",
      request: ({ id }) => ({ method: "POST", path: `/api/${id}`, body: { x: 1 } }),
      idempotencyKey: true,
      error: "Failed",
    });
    await action.dispatch({ id: "abc" });
    const [, opts] = mockFetch.mock.calls[0]!;
    expect(opts.headers[IDEMPOTENCY_HEADER]).toEqual(expect.any(String));
    expect(opts.headers[IDEMPOTENCY_HEADER].length).toBeGreaterThan(5);
  });

  it("does NOT send Idempotency-Key when not configured", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    const action = apiAction<void, unknown>({
      name: "test.no_idem",
      request: () => ({ method: "POST", path: "/api/x", body: {} }),
      error: "Failed",
    });
    await action.dispatch(undefined);
    const [, opts] = mockFetch.mock.calls[0]!;
    expect(opts.headers?.[IDEMPOTENCY_HEADER]).toBeUndefined();
  });

  it("idempotencyKey function receives args", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    const action = apiAction<{ id: string }, unknown>({
      name: "test.idem_fn",
      request: ({ id }) => ({ method: "POST", path: `/api/${id}`, body: {} }),
      idempotencyKey: (args) => `custom-${args.id}`,
      error: "Failed",
    });
    await action.dispatch({ id: "xyz" });
    const [, opts] = mockFetch.mock.calls[0]!;
    expect(opts.headers[IDEMPOTENCY_HEADER]).toBe("custom-xyz");
  });
});

describe("apiAction — response edge cases", () => {
  it("handles empty body on DELETE gracefully", async () => {
    mockFetch.mockResolvedValue(new Response("", { status: 200 }));
    const action = apiAction<void, unknown>({
      name: "test.empty_delete",
      request: () => ({ method: "DELETE", path: "/api/item/1" }),
      error: "Failed",
    });
    const result = await action.dispatch(undefined);
    expect(result).toBeUndefined();
    expect(recentLog()[0]?.status).toBe("success");
  });

  it("throws ActionError on non-JSON response body", async () => {
    mockFetch.mockResolvedValue(
      new Response("<html>error</html>", {
        status: 200,
        headers: { "Content-Type": "text/html" },
      }),
    );
    const action = apiAction<void, unknown>({
      name: "test.bad_json",
      request: () => ({ method: "GET", path: "/api/broken" }),
      error: "Parse failed",
    });
    const result = await action.dispatch(undefined);
    expect(result).toBeNull();
    expect(recentLog()[0]?.status).toBe("error");
    expect(recentLog()[0]?.error?.message).toContain("response not JSON");
  });

  it("falls back to HTTP status string when error body is not JSON", async () => {
    mockFetch.mockResolvedValue(new Response("plain text error", { status: 502 }));
    const action = apiAction<void, unknown>({
      name: "test.non_json_err",
      request: () => ({ method: "GET", path: "/api/down" }),
      error: "Server error",
    });
    const result = await action.dispatch(undefined);
    expect(result).toBeNull();
    expect(recentLog()[0]?.error?.message).toBe("HTTP 502");
    expect(recentLog()[0]?.error?.status).toBe(502);
  });

  it("GET request does not send Content-Type or body", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ v: 1 }), { status: 200 }));
    const action = apiAction<void, unknown>({
      name: "test.get_clean",
      request: () => ({ method: "GET", path: "/api/data" }),
      error: "Failed",
    });
    await action.dispatch(undefined);
    const [, opts] = mockFetch.mock.calls[0]!;
    expect(opts.body).toBeUndefined();
    expect(opts.headers?.["Content-Type"]).toBeUndefined();
  });
});

describe("apiAction — error code propagation", () => {
  it("propagates code from JSON error body to ActionError", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: "rate limited", code: "rate_limit" }), { status: 429 }),
    );
    const action = apiAction<void, unknown>({
      name: "test.code_prop",
      request: () => ({ method: "POST", path: "/api/limited", body: {} }),
      error: false,
    });
    await action.dispatch(undefined);
    const entry = recentLog()[0]!;
    expect(entry.status).toBe("error");
    expect(entry.error?.message).toBe("rate limited");
    expect(entry.error?.status).toBe(429);
    expect(entry.error?.code).toBe("rate_limit");
  });

  it("omits code when error body has no code field", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: "not found" }), { status: 404 }),
    );
    const action = apiAction<void, unknown>({
      name: "test.no_code",
      request: () => ({ method: "GET", path: "/api/missing" }),
      error: false,
    });
    await action.dispatch(undefined);
    const entry = recentLog()[0]!;
    expect(entry.error?.message).toBe("not found");
    expect(entry.error?.code).toBeUndefined();
  });
});
