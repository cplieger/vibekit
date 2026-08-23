// Tests for the live MCP control actions: reconnect_server, get_prompt,
// get_resource — request shaping (method/path/body) + result passthrough.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import {
  reconnectServer,
  getPromptContent,
  getResourceContent,
  relayOAuthCallback,
} from "./mcp.js";

const mockFetch = vi.fn();

beforeEach(() => {
  vi.useFakeTimers();
  resetActionFramework();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
});

afterEach(() => {
  vi.useRealTimers();
});

function lastCall(): { url: string; init: RequestInit } {
  const call = mockFetch.mock.calls[0] as [string, RequestInit];
  return { url: call[0], init: call[1] };
}

describe("reconnectServer", () => {
  it("POSTs the server name to /api/mcp/reconnect and returns the count", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ reconnected: 2 }), { status: 200 }));
    const res = await reconnectServer.dispatch({ server: "everything" });
    const { url, init } = lastCall();
    expect(url).toContain("/api/mcp/reconnect");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual({ server: "everything" });
    expect(res?.reconnected).toBe(2);
  });
});

describe("getPromptContent", () => {
  it("POSTs server/prompt/arguments to /api/mcp/prompt", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ messages: [{ content: { type: "text", text: "hi" } }] }), {
        status: 200,
      }),
    );
    const res = await getPromptContent.dispatch({
      server: "everything",
      prompt: "args-prompt",
      arguments: { city: "Paris" },
    });
    const { url, init } = lastCall();
    expect(url).toContain("/api/mcp/prompt");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual({
      server: "everything",
      prompt: "args-prompt",
      arguments: { city: "Paris" },
    });
    expect(res?.messages?.[0]?.content).toBeDefined();
  });

  it("defaults arguments to an empty object", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ messages: [] }), { status: 200 }));
    await getPromptContent.dispatch({ server: "everything", prompt: "simple-prompt" });
    const { init } = lastCall();
    expect(JSON.parse(init.body as string).arguments).toEqual({});
  });
});

describe("getResourceContent", () => {
  it("POSTs server/uri to /api/mcp/resource", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ contents: [{ uri: "demo://x", text: "body" }] }), {
        status: 200,
      }),
    );
    const res = await getResourceContent.dispatch({ server: "everything", uri: "demo://x" });
    const { url, init } = lastCall();
    expect(url).toContain("/api/mcp/resource");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual({ server: "everything", uri: "demo://x" });
    expect(res?.contents?.[0]?.text).toBe("body");
  });
});

describe("relayOAuthCallback", () => {
  it("POSTs the server and the pasted address to /api/mcp/oauth-relay", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ status: 200 }), { status: 200 }));
    const pasted = "http://localhost:41234/oauth/callback?code=abc&state=st";

    const res = await relayOAuthCallback.dispatch({ server: "linear", redirect_url: pasted });

    const { url, init } = lastCall();
    expect(url).toContain("/api/mcp/oauth-relay");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual({ server: "linear", redirect_url: pasted });
    expect(res?.status).toBe(200);
  });

  // An authorization code is single-use. A retried relay can only replay a
  // request that may already have spent it, so the definition carries no retry
  // and this pins that: one dispatch, one request, whatever the failure.
  it("never retries — a replayed callback would spend the code twice", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: "the local sign-in listener did not answer" }), {
        status: 502,
      }),
    );

    await relayOAuthCallback
      .dispatch({ server: "linear", redirect_url: "http://localhost:1/x?code=a&state=s" })
      .catch(() => undefined);
    await vi.runAllTimersAsync();

    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  // The refusal is shown inline beside the field, so the action must not also
  // raise a toast: the server's reason names which part of the pasted address
  // was wrong and belongs next to the box it was pasted into.
  it("raises no error toast, so the panel can show the reason inline", async () => {
    const toast = await import("../toast.js");
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: "that address belongs to a different sign-in" }), {
        status: 400,
      }),
    );

    await relayOAuthCallback
      .dispatch({ server: "linear", redirect_url: "http://localhost:1/x?code=a&state=s" })
      .catch(() => undefined);
    await vi.runAllTimersAsync();

    expect(toast.error).not.toHaveBeenCalled();
    expect(toast.showToast).not.toHaveBeenCalled();
  });
});
