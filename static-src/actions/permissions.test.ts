// Tests for the native policy actions (editNativeRule / explainPolicy):
// request wire shape — including the guard_resource pre-flight field the
// permission dialog's "Always allow" sends — and failure propagation (a
// guard refusal must surface as a failed dispatch so the caller does NOT
// approve the pending permission).

import { describe, it, expect, vi, beforeEach } from "vitest";

import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
}));

import { editNativeRule, explainPolicy } from "./permissions.js";

const mockFetch = vi.fn();

beforeEach(() => {
  resetActionFramework();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
});

function requestBody(): Record<string, unknown> {
  const init = mockFetch.mock.calls[0]?.[1] as RequestInit | undefined;
  return JSON.parse(String(init?.body ?? "{}")) as Record<string, unknown>;
}

describe("editNativeRule wire shape", () => {
  it("POSTs the rule to /api/permissions/rules", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    const res = await editNativeRule.dispatch({
      op: "add",
      scope: "workspace",
      capability: "fs_write",
      effect: "ask",
      match: ["src/**"],
    });
    expect(res).not.toBeNull();
    expect(String(mockFetch.mock.calls[0]?.[0])).toContain("/api/permissions/rules");
    expect(requestBody()).toMatchObject({
      op: "add",
      scope: "workspace",
      capability: "fs_write",
      effect: "ask",
      match: ["src/**"],
    });
  });

  it("carries guard_resource for the always-allow pre-flight", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await editNativeRule.dispatch({
      op: "add",
      scope: "workspace",
      capability: "shell",
      effect: "allow",
      match: ["npm *"],
      guard_resource: "npm install",
    });
    expect(requestBody()).toMatchObject({
      effect: "allow",
      match: ["npm *"],
      guard_resource: "npm install",
    });
  });

  it("fails the dispatch when the server refuses the guarded write (409)", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: "an explicit ask rule covers this command" }), {
        status: 409,
      }),
    );
    const res = await editNativeRule.dispatch({
      op: "add",
      scope: "workspace",
      capability: "shell",
      effect: "allow",
      match: ["rm *"],
      guard_resource: "rm -rf x",
    });
    // Callers treat null as "not written": the permission stays pending.
    expect(res).toBeNull();
  });
});

describe("explainPolicy wire shape", () => {
  it("POSTs the simulation request to /api/permissions/explain", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ capability: "shell", effect: "ask", is_explicit_ask: true }), {
        status: 200,
      }),
    );
    const res = await explainPolicy.dispatch({ capability: "shell", resource: "rm -rf /" });
    expect(String(mockFetch.mock.calls[0]?.[0])).toContain("/api/permissions/explain");
    expect(requestBody()).toMatchObject({ capability: "shell", resource: "rm -rf /" });
    expect(res).toMatchObject({ effect: "ask" });
  });
});
