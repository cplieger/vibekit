// Picking a mode with NO active chat, which is the one call site in this module
// that depends on a chat existing.
//
// `selectMode` used to read `createSession(); getActive();` — correct only while
// the chat id was minted in this tab's memory. The id is the server's now, so the
// read has to happen in the create's continuation. The failure mode a bare `void`
// produces is silent: `set_mode` would be addressed to whatever chat was active
// before, or dropped entirely on a first-ever visit, and the pill would still flip
// because the action's own optimistic update does not consult the server.
//
// Driven through the real click path (expand the pill, click an option) rather than
// by exporting `selectMode`, so the test exercises the wiring a user reaches.
import { describe, it, expect, vi, beforeEach } from "vitest";

const { setModeDispatch, createSessionMock, getActiveMock } = vi.hoisted(() => ({
  setModeDispatch: vi.fn(),
  createSessionMock: vi.fn(),
  getActiveMock: vi.fn(),
}));

vi.mock("./chat.js", () => ({ createSession: createSessionMock }));
vi.mock("./actions/chat.js", () => ({ setMode: { dispatch: setModeDispatch } }));
vi.mock("./api-client.js", () => ({ apiGet: vi.fn(async () => ({ items: [] })) }));
vi.mock("./store.js", () => ({
  getActive: getActiveMock,
  activeSession: { value: undefined },
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  get: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  tabStatusFor: vi.fn(() => ""),
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  apiGetTyped: vi.fn(),
}));
// The expandable pill is @cplieger/ui-primitives machinery; what this file needs is
// the list rendered, which is what onExpand does.
vi.mock("./pill-expand.js", () => ({
  makeExpandable: (_pill: HTMLElement, _list: HTMLElement, opts: { onExpand: () => void }) => {
    expandList = opts.onExpand;
  },
  collapseAll: vi.fn(),
}));
vi.mock("@cplieger/ui-primitives/roving-focus", () => ({
  rovingFocus: () => ({ refresh: vi.fn() }),
}));
vi.mock("./icon-el.js", () => ({ iconEl: () => document.createElement("span") }));

let expandList: (() => void) | undefined;

function mountPill(): HTMLElement {
  document.body.innerHTML = `
    <button id="role-pill"><span id="role-pill-icon"></span><span id="role-pill-label"></span></button>
    <div id="role-list"></div>
  `;
  return document.getElementById("role-list") as HTMLElement;
}

vi.mock("./dom.js", () => ({
  $: {
    get rolePill() {
      return document.getElementById("role-pill") as HTMLElement;
    },
    get roleList() {
      return document.getElementById("role-list") as HTMLElement;
    },
  },
  byId: (id: string) => document.getElementById(id) as HTMLElement,
}));

const { initRolePicker } = await import("./role-picker.js");

/** Expand the pill and click the first mode option, which is what a user does. */
function pickFirstMode(list: HTMLElement): string {
  expandList?.();
  const option = list.querySelector<HTMLButtonElement>(".pill-role-item");
  if (option === null) {
    throw new Error("the mode list rendered no options");
  }
  const id = option.dataset["modeId"] ?? option.textContent ?? "";
  option.click();
  return id;
}

beforeEach(() => {
  vi.clearAllMocks();
  getActiveMock.mockReturnValue(undefined);
  createSessionMock.mockResolvedValue("c-created");
});

describe("picking a mode with no active chat", () => {
  it("addresses set_mode to the chat the create returned", async () => {
    const list = mountPill();
    initRolePicker();

    pickFirstMode(list);
    // The dispatch is in the create's continuation, so it has not happened yet.
    expect(setModeDispatch).not.toHaveBeenCalled();
    await vi.waitFor(() => {
      expect(setModeDispatch).toHaveBeenCalledTimes(1);
    });

    const arg = setModeDispatch.mock.calls[0]?.[0] as { chatID: string };
    expect(arg.chatID).toBe("c-created");
  });

  // The silent failure a bare `void` plus a synchronous read would produce, made
  // observable: another chat becomes active while the create is in flight. A
  // `getActive()` read after the detached call would send the pick THERE; the
  // continuation sends it to the chat that was asked for.
  it("addresses the created chat even when another becomes active meanwhile", async () => {
    let resolveCreate: (id: string) => void = () => undefined;
    createSessionMock.mockReturnValue(
      new Promise<string>((res) => {
        resolveCreate = res;
      }),
    );
    const list = mountPill();
    initRolePicker();

    pickFirstMode(list);
    getActiveMock.mockReturnValue({ id: "c-somewhere-else" });
    resolveCreate("c-created");
    await vi.waitFor(() => {
      expect(setModeDispatch).toHaveBeenCalledTimes(1);
    });

    const arg = setModeDispatch.mock.calls[0]?.[0] as { chatID: string };
    expect(arg.chatID).toBe("c-created");
  });

  it("dispatches nothing when the create is refused", async () => {
    createSessionMock.mockResolvedValue("");
    const list = mountPill();
    initRolePicker();

    pickFirstMode(list);
    await Promise.resolve();
    await Promise.resolve();

    expect(setModeDispatch).not.toHaveBeenCalled();
  });
});

describe("picking a mode on a chat that already exists", () => {
  it("creates nothing and dispatches straight away", () => {
    getActiveMock.mockReturnValue({ id: "c-live", current_mode_id: "", available_modes: [] });
    const list = mountPill();
    initRolePicker();

    pickFirstMode(list);

    expect(createSessionMock).not.toHaveBeenCalled();
    expect(setModeDispatch).toHaveBeenCalledTimes(1);
    const arg = setModeDispatch.mock.calls[0]?.[0] as { chatID: string };
    expect(arg.chatID).toBe("c-live");
  });
});
