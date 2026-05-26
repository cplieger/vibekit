// @vitest-environment happy-dom
// Tests for actions/settings.ts: saveSteering, logout, setKiroSetting, patchAppSettings.

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
}));

import { _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import * as toast from "../toast.js";

const mockFetch = vi.fn();

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
});

describe("saveSteering", () => {
  it("PUTs to /api/steering with content body", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    const { saveSteering } = await import("./settings.js");
    await saveSteering.dispatch({ content: "# My steering" });
    expect(mockFetch).toHaveBeenCalledTimes(1);
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/steering");
    expect(opts.method).toBe("PUT");
    expect(JSON.parse(opts.body as string)).toEqual({ content: "# My steering" });
  });

  it("toasts error on failure", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "disk full" }), { status: 500 }));
    const { saveSteering } = await import("./settings.js");
    const r = await saveSteering.dispatch({ content: "x" });
    expect(r).toBeNull();
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("Couldn't save steering"), undefined);
  });
});

describe("logout", () => {
  it("POSTs to /api/logout and applies optimistic UI", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    const emailEl = document.createElement("span");
    emailEl.textContent = "user@test.com";
    const stAuthEl = document.createElement("span");
    stAuthEl.textContent = "signed in";

    const { logout } = await import("./settings.js");
    await logout.dispatch({ emailEl, stAuthEl });

    expect(emailEl.textContent).toBe("");
    expect(stAuthEl.textContent).toBe("not signed in");
    expect(mockFetch).toHaveBeenCalledWith("/api/logout", expect.objectContaining({ method: "POST" }));
  });

  it("rolls back optimistic UI on failure", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "nope" }), { status: 500 }));
    const emailEl = document.createElement("span");
    emailEl.textContent = "user@test.com";
    const stAuthEl = document.createElement("span");
    stAuthEl.textContent = "signed in";

    const { logout } = await import("./settings.js");
    await logout.dispatch({ emailEl, stAuthEl });

    expect(emailEl.textContent).toBe("user@test.com");
    expect(stAuthEl.textContent).toBe("signed in");
  });
});

describe("setKiroSetting", () => {
  it("PUTs to /api/kiro-settings and rolls back checkbox on failure", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "bad" }), { status: 400 }));
    const input = document.createElement("input");
    input.type = "checkbox";
    input.checked = true; // user just toggled ON

    const { setKiroSetting } = await import("./settings.js");
    await setKiroSetting.dispatch({ key: "debug", value: "true", input });

    // Rollback should restore previous state (opposite of current)
    expect(input.checked).toBe(false);
  });

  it("PUTs to /api/kiro-settings with key/value body", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    const input = document.createElement("input");
    input.type = "text";
    input.value = "new-val";

    const { setKiroSetting } = await import("./settings.js");
    await setKiroSetting.dispatch({ key: "compaction", value: "new-val", input, previousValue: "old-val" });

    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/kiro-settings");
    expect(JSON.parse(opts.body as string)).toEqual({ key: "compaction", value: "new-val" });
  });
});

describe("patchAppSettings", () => {
  it("PATCHes to /api/settings and rolls back inputs on failure", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    const input = document.createElement("input");
    input.type = "checkbox";
    input.checked = true; // user just toggled ON

    const { patchAppSettings } = await import("./settings.js");
    await patchAppSettings.dispatch({ body: { debug_logs: true }, inputs: [input] });

    // Rollback: prevChecked is !current at optimistic time = false
    expect(input.checked).toBe(false);
  });
});
