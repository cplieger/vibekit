// @vitest-environment happy-dom
// The OAuth loopback relay's row affordance. KAS binds its redirect listener on
// the CONTAINER's localhost, so a browser reaching vibekit from another machine
// is sent to its own localhost and the sign-in dies with no recovery path. This
// box is that path: the user pastes the dead address and the server replays it
// inward.
//
// What is asserted here is the CLIENT's half of the contract — that the pasted
// address travels verbatim, that the server's reason is what the user is shown,
// and that a delivered relay is not reported as a completed sign-in. The
// validation itself is the server's and is tested in
// internal/hub/mcp_oauth_relay_test.go; duplicating any of it here would be a
// second copy of a rule that only the server can apply, since the authorization
// URL it checks against never reaches the browser.
import { describe, it, expect, vi, beforeEach } from "vitest";

import type * as ApiClient from "./api-client.js";
import type * as McpActions from "./actions/mcp.js";
import type * as McpStateMod from "./mcp-state.js";

vi.mock("./toast.js", () => import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock()));

// The real mcp-state module is loaded (see its partial mock below) and its
// controller reaches the network. Stubbing the api-client keeps that off
// happy-dom's fetch, which otherwise leaves an in-flight request for the
// environment to abort at teardown and prints an AbortError over a green run.
vi.mock("./api-client.js", async (importOriginal) => {
  const actual = await importOriginal<typeof ApiClient>();
  return {
    ...actual,
    apiGet: async (): Promise<null> => null,
    apiGetTyped: async (): Promise<null> => null,
    apiPost: async (): Promise<null> => null,
    apiDelete: async (): Promise<boolean> => false,
  };
});

// vi.hoisted, because a vi.mock factory is hoisted above ordinary top-level
// consts and would otherwise read these before initialization.
const { relayDispatch, refetchStatus } = vi.hoisted(() => ({
  relayDispatch: vi.fn(),
  refetchStatus: vi.fn(),
}));

vi.mock("./actions/mcp.js", async (importOriginal) => {
  const actual = await importOriginal<typeof McpActions>();
  return { ...actual, relayOAuthCallback: { dispatch: relayDispatch } };
});
// Partial, not wholesale: mcp-ui.ts pulls the whole actions graph in
// transitively and actions/mcp.ts needs the real apiAction to build its
// definitions. Only the process-wide cleanup registry is replaced.
vi.mock(import("./actions/index.js"), async (importOriginal) => {
  const actual = await importOriginal();
  return {
    ...actual,
    registerCleanup:
      (_fn: () => void): (() => void) =>
      () => {
        /* noop: the singleton cleanup registry is process-wide */
      },
  };
});
vi.mock("./mcp-state.js", async (importOriginal) => {
  const actual = await importOriginal<typeof McpStateMod>();
  return { ...actual, mcpState: { ...actual.mcpState, refetchStatus } };
});

import { renderOAuthRelay } from "./mcp-ui.js";

/** Resolve the dispatch by invoking the callback the caller handed it, the way
 *  the real action framework does. */
function resolveWith(hook: "onSuccess" | "onError", arg: unknown): void {
  relayDispatch.mockImplementation((_args: unknown, opts: Record<string, (v: unknown) => void>) => {
    opts[hook]?.(arg);
    return Promise.resolve(null);
  });
}

interface Parts {
  box: HTMLDetailsElement;
  input: HTMLInputElement;
  submit: HTMLButtonElement;
  note: () => string;
}

function mount(server = "linear"): Parts {
  const box = renderOAuthRelay(server);
  document.body.replaceChildren(box);
  return {
    box,
    input: box.querySelector<HTMLInputElement>(".mcp-relay-input") as HTMLInputElement,
    submit: box.querySelector<HTMLButtonElement>("button") as HTMLButtonElement,
    note: () => box.querySelector(".mcp-relay-note")?.textContent ?? "",
  };
}

const flush = (): Promise<void> => new Promise((r) => setTimeout(r, 0));

beforeEach(() => {
  relayDispatch.mockReset();
  refetchStatus.mockReset();
  document.body.replaceChildren();
});

describe("renderOAuthRelay", () => {
  // Collapsed by default: a sign-in from a browser ON the container works
  // normally and needs none of this, so the row must not grow a form for it.
  it("starts collapsed so a working sign-in is not asked to read a form", () => {
    const { box } = mount();
    expect(box.open).toBe(false);
    expect(box.querySelector("summary")?.textContent).toContain("did not load");
  });

  it("sends the pasted address verbatim under the server it belongs to", async () => {
    resolveWith("onSuccess", { status: 200 });
    const { input, submit } = mount("linear");
    // Deliberately awkward: percent-encoding and a long code are exactly what a
    // real callback carries, and re-encoding either would break the exchange.
    const pasted = "http://localhost:41234/oauth/callback?code=A%2Fb%2Bc-9&state=st-abc";
    input.value = pasted;

    submit.click();
    await flush();

    expect(relayDispatch).toHaveBeenCalledTimes(1);
    expect(relayDispatch.mock.calls[0]?.[0]).toEqual({
      server: "linear",
      redirect_url: pasted,
    });
  });

  it("trims the clipboard's whitespace rather than refusing the paste", async () => {
    resolveWith("onSuccess", { status: 200 });
    const { input, submit } = mount();
    input.value = "  http://localhost:41234/oauth/callback?code=abc&state=st  \n";

    submit.click();
    await flush();

    expect(relayDispatch.mock.calls[0]?.[0]).toMatchObject({
      redirect_url: "http://localhost:41234/oauth/callback?code=abc&state=st",
    });
  });

  it("asks for an address instead of posting an empty one", async () => {
    const { submit, note } = mount();

    submit.click();
    await flush();

    expect(relayDispatch).not.toHaveBeenCalled();
    expect(note()).toContain("Paste the address");
  });

  // DELIVERED, not connected. The code reaching KAS's listener only starts the
  // token exchange; the connected state is KAS's to report over _kiro/mcp/status,
  // and claiming it here would invent a transition the server never made.
  it("reports a delivered callback as delivered, not as a finished sign-in", async () => {
    resolveWith("onSuccess", { status: 200 });
    const { input, submit, note } = mount();
    input.value = "http://localhost:41234/oauth/callback?code=abc&state=st";

    submit.click();
    await flush();

    expect(note()).toContain("Delivered");
    expect(note()).not.toContain("Connected");
    expect(refetchStatus).toHaveBeenCalled();
    // The code is spent, so the field must not still hold it for a second click.
    expect(input.value).toBe("");
  });

  // The server's message is the only one that can name which part of the address
  // was wrong: it is checked against the authorization URL KAS stored, which the
  // client never sees. So the client must show it, not paraphrase it.
  it("shows the server's own refusal, verbatim", async () => {
    resolveWith("onError", new Error("that address belongs to a different sign-in"));
    const { input, submit, note, box } = mount();
    input.value = "http://localhost:41234/oauth/callback?code=abc&state=wrong";

    submit.click();
    await flush();

    expect(note()).toBe("that address belongs to a different sign-in");
    expect(box.querySelector(".mcp-relay-note")?.classList.contains("mcp-relay-err")).toBe(true);
    // Still holding the address, so a corrected paste is an edit rather than a
    // retype: a refused relay did not spend the code.
    expect(input.value).not.toBe("");
  });

  it("re-enables the button after a refusal so a corrected address can be sent", async () => {
    resolveWith("onError", new Error("nope"));
    const { input, submit } = mount();
    input.value = "http://localhost:41234/oauth/callback?code=abc&state=wrong";

    submit.click();
    await flush();

    expect(submit.disabled).toBe(false);
  });
});
