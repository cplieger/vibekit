// Tests for actions/settings.ts: saveSteering, logout, setKiroSetting, patchAppSettings.

import { describe, it, expect, vi, beforeEach } from "vitest";
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

vi.mock("../api-client.js", () => ({
  apiGetOrError: vi.fn(),
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,

  apiGet: vi.fn(),
  apiPost: vi.fn(),
  // Reached through tabs.ts -> tabs-sync.ts, whose `GET /api/tabs` is the only
  // read in the projection. Nothing here lists tabs; the name has to exist for
  // real-ESM linking.
  apiGetTyped: vi.fn(),
}));
import * as toast from "../toast.js";

const mockFetch = vi.fn();

beforeEach(() => {
  resetActionFramework();
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
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: "disk full" }), { status: 500 }),
    );
    const { saveSteering } = await import("./settings.js");
    const r = await saveSteering.dispatch({ content: "x" });
    expect(r).toBeNull();
    expect(toast.error).toHaveBeenCalledWith(
      expect.stringContaining("Couldn't save steering"),
      undefined,
    );
  });
});

// The action's argument became `{ render, prev }` — an INJECTED render callback plus
// the whole VERDICT it replaces — where it used to be two DOM elements it wrote
// directly. Two reasons, and both are asserted below. `renderIdentity` is the one
// writer of the auth row AND its separator now (two elements, one fact), so an
// action writing `stAuth.textContent` itself would put "not signed in" into a row
// that stays hidden; and carrying the VERDICT rather than the address is what makes
// all THREE arms restorable, since the address cannot tell `signed_out` from
// `unavailable` — both render empty.
//
// Asserted through a `vi.fn()` render callback rather than two elements: the
// callback IS the contract, and reading the DOM would be testing `settings.ts`'s
// writer from inside this module's tests.
describe("logout", () => {
  it("POSTs to /api/logout and renders the signed-out verdict optimistically", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    const render = vi.fn();

    const { logout } = await import("./settings.js");
    await logout.dispatch({ render, prev: { state: "signed_in", email: "user@test.com" } });

    expect(render).toHaveBeenCalledWith({ state: "signed_out" });
    expect(mockFetch).toHaveBeenCalledWith(
      "/api/logout",
      expect.objectContaining({ method: "POST" }),
    );
  });

  it("rolls back to the verdict it replaced on failure", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "nope" }), { status: 500 }));
    const render = vi.fn();
    const prev = { state: "signed_in", email: "user@test.com" } as const;

    const { logout } = await import("./settings.js");
    await logout.dispatch({ render, prev });

    expect(render).toHaveBeenNthCalledWith(1, { state: "signed_out" });
    expect(render).toHaveBeenLastCalledWith(prev);
  });

  it("restores an UNAVAILABLE verdict rather than signed_out", async () => {
    // THE ARM THE ADDRESS-CARRYING OP COULD NOT EXPRESS, and the reason the whole
    // verdict travels. `unavailable` and `signed_out` both render an EMPTY address,
    // so an op holding `emailEl.textContent` restored `""` for either and the old
    // rollback then guessed `"not signed in"` from it — writing that where "unknown"
    // had been. A refused logout from an unavailable verdict must restore the
    // unavailable verdict.
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "nope" }), { status: 500 }));
    const render = vi.fn();
    const prev = { state: "unavailable", reason: "whoami unreachable" } as const;

    const { logout } = await import("./settings.js");
    await logout.dispatch({ render, prev });

    expect(render).toHaveBeenLastCalledWith(prev);
    expect(render).not.toHaveBeenLastCalledWith({ state: "signed_out" });
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
    input.type = "checkbox";
    input.checked = true;

    const { setKiroSetting } = await import("./settings.js");
    await setKiroSetting.dispatch({
      key: "telemetry.enabled",
      value: "true",
      input,
    });

    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/kiro-settings");
    expect(JSON.parse(opts.body as string)).toEqual({ key: "telemetry.enabled", value: "true" });
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
