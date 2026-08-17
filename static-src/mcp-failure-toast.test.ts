// @vitest-environment happy-dom
// D77: a toast when a broken MCP server is actually used.
//
// Two properties, and both are the item's own implementation notes rather than
// nice-to-haves. Dedupe keys on the state TRANSITION, because each bridge emits
// its own `_kiro/mcp/status` on connect and a reconnect storm would otherwise be
// a toast storm. And the notice carries kiro-cli's own captured text, because
// that is what distinguishes a missing command from a handshake timeout.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

const toastCalls: { message: string; level: string }[] = [];

vi.mock("./toast.js", () => ({
  showToast: (message: string, level: string) => {
    toastCalls.push({ message, level });
    return () => {
      /* dismiss */
    };
  },
}));

// mcp-ui.ts reaches the DOM registry and the action framework at module scope
// through its imports; the two helpers under test are pure, so the heavy
// siblings are stubbed rather than staged.
vi.mock("./dom.js", () => ({ $: {}, byId: () => document.createElement("div") }));

const { announceMCPFailure, mcpFailureText } = await import("./mcp-ui.js");

describe("mcpFailureText", () => {
  it("carries the captured reason", () => {
    expect(mcpFailureText("github", "spawn ENOENT")).toContain("spawn ENOENT");
    expect(mcpFailureText("github", "spawn ENOENT")).toContain("github");
  });

  it("names the server when the reason is empty", () => {
    // adaptStatus defaults an absent error to "", so this is a real arrival
    // rather than a defensive branch. A notice saying only "an integration
    // failed" sends the reader looking for which one.
    const text = mcpFailureText("linear", "");
    expect(text).toContain("linear");
    expect(text.endsWith("failed to start.")).toBe(true);
  });

  it("ignores whitespace-only reasons", () => {
    expect(mcpFailureText("sentry", "   \n ")).toBe(mcpFailureText("sentry", ""));
  });
});

describe("announceMCPFailure", () => {
  beforeEach(() => {
    toastCalls.length = 0;
  });
  afterEach(() => {
    toastCalls.length = 0;
  });

  it("fires once per transition into failed, not once per frame", () => {
    // The reconnect storm, replayed: three bridges each emit their own status
    // frame for the same wedged server. The first crosses idle -> failed; the
    // rest arrive with the state already failed.
    announceMCPFailure("github", "spawn ENOENT", "idle");
    announceMCPFailure("github", "spawn ENOENT", "failed");
    announceMCPFailure("github", "spawn ENOENT", "failed");
    expect(toastCalls).toHaveLength(1);
    expect(toastCalls[0]?.level).toBe("error");
    expect(toastCalls[0]?.message).toContain("spawn ENOENT");
  });

  it("re-arms after the server leaves failed", () => {
    announceMCPFailure("github", "spawn ENOENT", "idle");
    // A reconnect succeeded, so the next genuine failure must be audible again.
    announceMCPFailure("github", "handshake timeout", "connected");
    expect(toastCalls).toHaveLength(2);
    expect(toastCalls[1]?.message).toContain("handshake timeout");
  });

  it("does not suppress a second server's first failure", () => {
    // Dedupe is per server per transition: one wedged server must not silence
    // another one breaking.
    announceMCPFailure("github", "spawn ENOENT", "idle");
    announceMCPFailure("linear", "handshake timeout", "idle");
    expect(toastCalls).toHaveLength(2);
    expect(toastCalls[1]?.message).toContain("linear");
  });

  it("announces a failure arriving from every non-failed state", () => {
    for (const prev of ["idle", "connected", "needs_auth", "disabled"] as const) {
      toastCalls.length = 0;
      announceMCPFailure("github", "boom", prev);
      expect(toastCalls, `prev=${prev}`).toHaveLength(1);
    }
  });
});
