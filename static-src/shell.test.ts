// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// shell.ts wiring tests.
//
// shell.ts is a thin panel controller over @cplieger/web-terminal-ui's
// createTerminal: it builds the terminal lazily on first open, wires the
// header Reset button to the engine's local-buffer reset + a Ctrl+L redraw,
// and routes the code-block "run in shell" action through the kernel's send
// funnel (captured by a host-bridge feature). These tests lock that contract
// in with the UI package, the engine, and the sibling modules mocked — the
// terminal internals (canvas measurement, the WebSocket) are the UI package's
// concern and are not exercised here.
//
// Each test re-imports shell.ts fresh (vi.resetModules) so its module-level
// singletons (initialized flag, terminal handle, captured send funnel) reset.
// createTerminal is mocked to invoke the host-bridge feature's setup with a
// send spy, so we can assert the Reset button and run-in-shell drive it.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, afterEach } from "vitest";
import type {
  CreateTerminalOptions,
  TerminalContext,
  TerminalHandle,
} from "@cplieger/web-terminal-ui";
import type * as Shell from "./shell.js";

interface Harness {
  mod: typeof Shell;
  createTerminal: ReturnType<typeof vi.fn>;
  render: { resetScrollback: ReturnType<typeof vi.fn>; resetScreen: ReturnType<typeof vi.fn> };
  sendSpy: ReturnType<typeof vi.fn>;
  shellBtn: HTMLButtonElement;
  shellToggleBtn: HTMLButtonElement;
  shellClearBtn: HTMLButtonElement;
  shellPanel: HTMLDivElement;
  shellTerminal: HTMLDivElement;
  save: ReturnType<typeof vi.fn>;
  getRunCb: () => ((cmd: string) => void) | null;
}

async function setup(): Promise<Harness> {
  vi.resetModules();

  const shellBtn = document.createElement("button");
  const shellToggleBtn = document.createElement("button");
  const shellClearBtn = document.createElement("button");
  const shellPanel = document.createElement("div");
  shellPanel.classList.add("shell-closed");
  const shellTerminal = document.createElement("div");

  // The kernel's sanitizing send funnel, captured by the host-bridge feature
  // when createTerminal runs its setup.
  const sendSpy = vi.fn();
  const createTerminal = vi.fn(
    (_root: HTMLElement, opts: CreateTerminalOptions): TerminalHandle => {
      const hb = (opts.features ?? []).find((f) => f.name === "vibekit-host-bridge");
      // The host-bridge feature only reads ctx.send; a partial context suffices.
      hb?.setup({ send: sendSpy } as unknown as TerminalContext);
      return { focus: vi.fn(), destroy: vi.fn() };
    },
  );
  const presetTouch = vi.fn(() => ["preset-feature"]);
  const render = { resetScrollback: vi.fn(), resetScreen: vi.fn() };
  const scrollEl = document.createElement("div");
  const getScrollEl = vi.fn(() => scrollEl);
  let runCb: ((cmd: string) => void) | null = null;
  const setShellRunCallback = vi.fn((cb: (cmd: string) => void) => {
    runCb = cb;
  });
  const save = vi.fn();

  vi.doMock("@cplieger/web-terminal-ui", () => ({ createTerminal }));
  vi.doMock("@cplieger/web-terminal-ui/presets", () => ({ presetTouch }));
  vi.doMock("@cplieger/web-terminal-engine", () => ({ render }));
  vi.doMock("./messages.js", () => ({ getScrollEl }));
  vi.doMock("./code-blocks.js", () => ({ setShellRunCallback }));
  vi.doMock("./ui-state.js", () => ({ save, load: vi.fn() }));
  vi.doMock("./dom.js", () => ({
    $: { shellBtn, shellToggleBtn, shellClearBtn, shellPanel, shellTerminal },
  }));

  const mod = await import("./shell.js");
  return {
    mod,
    createTerminal,
    render,
    sendSpy,
    shellBtn,
    shellToggleBtn,
    shellClearBtn,
    shellPanel,
    shellTerminal,
    save,
    getRunCb: () => runCb,
  };
}

afterEach(() => {
  vi.doUnmock("@cplieger/web-terminal-ui");
  vi.doUnmock("@cplieger/web-terminal-ui/presets");
  vi.doUnmock("@cplieger/web-terminal-engine");
  vi.doUnmock("./messages.js");
  vi.doUnmock("./code-blocks.js");
  vi.doUnmock("./ui-state.js");
  vi.doUnmock("./dom.js");
  vi.resetModules();
});

describe("shell.ts: lazy terminal creation", () => {
  it("does not create the terminal until the panel is first opened", async () => {
    const h = await setup();
    h.mod.initShellPanel();
    expect(h.createTerminal).not.toHaveBeenCalled();
  });

  it("builds the terminal on first open with the shell WS path, theme, and host-bridge feature", async () => {
    const h = await setup();
    h.mod.initShellPanel();
    h.shellBtn.click();

    expect(h.createTerminal).toHaveBeenCalledTimes(1);
    const call = h.createTerminal.mock.calls.at(0);
    if (!call) {
      throw new Error("createTerminal was not called");
    }
    const [root, opts] = call as [HTMLElement, CreateTerminalOptions];
    expect(root).toBe(h.shellTerminal);
    expect(opts.wsPath).toBe("/api/shell/ws");
    expect(opts.fontReady).toBeDefined();
    expect(opts.theme).toMatchObject({ "--bg": "var(--c-term-bg)", "--accent": "var(--c-accent)" });
    // The preset features plus the vibekit host-bridge feature.
    expect(opts.features).toContain("preset-feature");
    expect(opts.features?.some((f) => f.name === "vibekit-host-bridge")).toBe(true);
  });

  it("reuses the same terminal across close/reopen (no second WebSocket)", async () => {
    const h = await setup();
    h.mod.initShellPanel();
    h.shellBtn.click(); // open (creates terminal)
    h.shellToggleBtn.click(); // close
    h.shellBtn.click(); // reopen
    expect(h.createTerminal).toHaveBeenCalledTimes(1);
    expect(h.shellPanel.classList.contains("shell-closed")).toBe(false);
  });
});

describe("shell.ts: host-driven actions", () => {
  it("the Reset button clears the engine's scrollback + screen and redraws with Ctrl+L", async () => {
    const h = await setup();
    h.mod.initShellPanel();
    h.shellBtn.click(); // open so the host-bridge captures the send funnel

    h.shellClearBtn.click();

    expect(h.render.resetScrollback).toHaveBeenCalledTimes(1);
    expect(h.render.resetScreen).toHaveBeenCalledTimes(1);
    expect(h.sendSpy).toHaveBeenCalledWith(new Uint8Array([0x0c]));
  });

  it("run-in-shell types the command into the terminal through the send funnel", async () => {
    const h = await setup();
    h.mod.initShellPanel(); // registers the run callback
    h.shellBtn.click(); // open so the send funnel is captured

    const runCb = h.getRunCb();
    expect(runCb).not.toBeNull();
    runCb?.("echo hi");

    expect(h.sendSpy).toHaveBeenCalledWith(new TextEncoder().encode("echo hi\n"));
  });
});

describe("shell.ts: restore", () => {
  it("restoreShell opens the panel and builds the terminal", async () => {
    const h = await setup();
    h.mod.initShellPanel();
    h.mod.restoreShell();
    expect(h.createTerminal).toHaveBeenCalledTimes(1);
    expect(h.shellPanel.classList.contains("shell-closed")).toBe(false);
    expect(h.save).toHaveBeenCalledWith({ shell_open: true });
  });
});
