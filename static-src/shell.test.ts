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
import type { CreateTerminalOptions, TerminalHandle } from "@cplieger/web-terminal-ui";
import type * as Shell from "./shell.js";

interface Harness {
  mod: typeof Shell;
  createTerminal: ReturnType<typeof vi.fn>;
  localScrollbackStorage: ReturnType<typeof vi.fn>;
  resetSpy: ReturnType<typeof vi.fn>;
  sendSpy: ReturnType<typeof vi.fn>;
  termFocus: ReturnType<typeof vi.fn>;
  shellBtn: HTMLButtonElement;
  shellToggleBtn: HTMLButtonElement;
  shellRestartBtn: HTMLButtonElement;
  shellFullscreenBtn: HTMLButtonElement;
  shellPanel: HTMLDivElement;
  shellTerminal: HTMLDivElement;
  shellResize: HTMLDivElement;
  save: ReturnType<typeof vi.fn>;
  restartDispatch: ReturnType<typeof vi.fn>;
  confirmMock: ReturnType<typeof vi.fn>;
  getRunCb: () => ((cmd: string) => void) | null;
}

/** happy-dom lacks pointer capture; back the three methods with a Set so the
 *  resize handlers' capture-gated move/up paths run. */
function stubPointerCapture(el: HTMLElement): void {
  const captured = new Set<number>();
  el.setPointerCapture = (id: number): void => {
    captured.add(id);
  };
  el.releasePointerCapture = (id: number): void => {
    captured.delete(id);
  };
  el.hasPointerCapture = (id: number): boolean => captured.has(id);
}

/** Synthetic pointer event: happy-dom's PointerEvent support is spotty, and
 *  the handlers only read pointerId/isPrimary/clientY, so a plain Event with
 *  those fields assigned is sufficient. */
function ptr(
  type: string,
  clientY: number,
  opts: { pointerId?: number; isPrimary?: boolean } = {},
): Event {
  return Object.assign(new Event(type), {
    pointerId: opts.pointerId ?? 1,
    isPrimary: opts.isPrimary ?? true,
    clientY,
  });
}

async function setup(uiStateData: { shell_h?: number } = {}): Promise<Harness> {
  vi.resetModules();

  const shellBtn = document.createElement("button");
  const shellToggleBtn = document.createElement("button");
  const shellRestartBtn = document.createElement("button");
  const shellFullscreenBtn = document.createElement("button");
  const shellPanel = document.createElement("div");
  shellPanel.classList.add("shell-closed");
  const shellTerminal = document.createElement("div");
  const shellResize = document.createElement("div");
  stubPointerCapture(shellResize);
  shellPanel.append(shellResize, shellTerminal);
  // Panel + toolbar button live in the document so focus()/activeElement and
  // contains() checks behave; replaceChildren drops the previous test's DOM.
  document.body.replaceChildren(shellPanel, shellBtn);

  // The v4 handle's host controls: send routes the sanitizing funnel, reset
  // drops the local scrollback + screen.
  const sendSpy = vi.fn();
  const resetSpy = vi.fn();
  const termFocus = vi.fn();
  const createTerminal = vi.fn(
    (_root: HTMLElement, _opts: CreateTerminalOptions): TerminalHandle => {
      return { focus: termFocus, send: sendSpy, reset: resetSpy, destroy: vi.fn() };
    },
  );
  const presetTouch = vi.fn(() => ["preset-feature"]);
  // A sentinel, so the assertion below checks the panel hands the LIBRARY's
  // localStorage store through rather than inventing storage of its own.
  const localScrollbackStorage = vi.fn(() => ({ kind: "scrollback-store" }));
  const scrollEl = document.createElement("div");
  const getScrollEl = vi.fn(() => scrollEl);
  let runCb: ((cmd: string) => void) | null = null;
  const setShellRunCallback = vi.fn((cb: (cmd: string) => void) => {
    runCb = cb;
  });
  const save = vi.fn();
  const restartDispatch = vi.fn(() => Promise.resolve({ ok: true }));
  const confirmMock = vi.fn(() => Promise.resolve(true));
  const toastError = vi.fn();
  // initShellPanel reads load().shell_h to restore a persisted height.
  const load = vi.fn(() => ({ shell_h: uiStateData.shell_h ?? 0 }));

  vi.doMock("@cplieger/web-terminal-ui", () => ({
    createTerminal,
    localScrollbackStorage,
  }));
  vi.doMock("@cplieger/web-terminal-ui/presets/touch", () => ({ presetTouch }));
  vi.doMock("./messages.js", () => ({ getScrollEl }));
  vi.doMock("./code-blocks.js", () => ({ setShellRunCallback }));
  vi.doMock("./ui-state.js", () => ({ save, load }));
  vi.doMock("./actions/shell.js", () => ({ restartShell: { dispatch: restartDispatch } }));
  vi.doMock("./confirm.js", () => ({ confirm: confirmMock }));
  vi.doMock("./toast.js", () => ({ error: toastError, info: vi.fn(), success: vi.fn() }));
  vi.doMock("./dom.js", () => ({
    $: {
      shellBtn,
      shellToggleBtn,
      shellRestartBtn,
      shellFullscreenBtn,
      shellPanel,
      shellTerminal,
      shellResize,
    },
  }));

  const mod = await import("./shell.js");
  return {
    mod,
    createTerminal,
    localScrollbackStorage,
    resetSpy,
    sendSpy,
    termFocus,
    shellBtn,
    shellToggleBtn,
    shellRestartBtn,
    shellFullscreenBtn,
    shellPanel,
    shellTerminal,
    shellResize,
    save,
    restartDispatch,
    confirmMock,
    getRunCb: () => runCb,
  };
}

afterEach(() => {
  vi.doUnmock("@cplieger/web-terminal-ui");
  vi.doUnmock("@cplieger/web-terminal-ui/presets/touch");
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

  it("builds the terminal on first open with the shell WS path, theme, and container layout", async () => {
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
    // The touch preset, handed over UNCALLED (ui v5's lazy `features`), so a
    // throwing preset fails inside createTerminal rather than at this call
    // site. Embedded in container layout (the panel is the terminal's
    // boundary; no page-level styling, no bridge feature).
    expect(typeof opts.features).toBe("function");
    expect(opts.features?.()).toContain("preset-feature");
    expect(opts.layout).toBe("container");
  });

  it("persists the shell scrollback through the library's store, in vibekit's namespace", async () => {
    // The server keeps the PTY across a reload but the client comes back holding
    // nothing, so without this a reopened panel refills its whole buffer over the
    // wire — visible as the history filling in, and routine on a phone where iOS
    // discards the tab.
    //
    // It must be the LIBRARY's store: a hand-rolled one here would be another copy
    // of the same logic across the consumers, and the part a copy omits is the
    // orphan sweep, whose absence is invisible until the origin quota fills. The
    // prefix keeps it in vibekit's own localStorage namespace, beside
    // `vibekit.ui-state`.
    const h = await setup();
    h.mod.initShellPanel();
    h.shellBtn.click();

    // Built at MODULE load, not on first open: constructing the store is what runs
    // its orphan sweep, and a user who never opens the shell would otherwise leave
    // old snapshots in this origin's localStorage indefinitely.
    expect(h.localScrollbackStorage).toHaveBeenCalledWith({
      prefix: "vibekit.shell-scrollback.",
    });
    const [, opts] = h.createTerminal.mock.calls.at(0) as [HTMLElement, CreateTerminalOptions];
    expect(opts.persistScrollback).toEqual({ kind: "scrollback-store" });
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
  // The Restart button replaced a Reset that only cleared the screen. It exists
  // because terminal.Handler is single-use server-side: a child that exits (or a
  // wedged foreground process) leaves a panel that can never start again, and a
  // screen clear cannot help with either. Confirmed, because unlike the clear it
  // kills whatever is running.
  it("the Restart button confirms, calls the server, then drops the local buffer", async () => {
    const h = await setup();
    h.mod.initShellPanel();
    h.shellBtn.click(); // open so the terminal (and its handle) exists

    h.shellRestartBtn.click();
    await new Promise((r) => setTimeout(r, 0));

    expect(h.confirmMock).toHaveBeenCalledTimes(1);
    expect(h.restartDispatch).toHaveBeenCalledTimes(1);
    expect(h.resetSpy).toHaveBeenCalledTimes(1);
  });

  it("a declined confirm neither calls the server nor touches the terminal", async () => {
    const h = await setup();
    h.confirmMock.mockResolvedValueOnce(false);
    h.mod.initShellPanel();
    h.shellBtn.click();

    h.shellRestartBtn.click();
    await new Promise((r) => setTimeout(r, 0));

    expect(h.restartDispatch).not.toHaveBeenCalled();
    expect(h.resetSpy).not.toHaveBeenCalled();
  });

  // A failed restart must not drop the buffer: the old PTY is still live, so
  // clearing the screen would hide a working shell behind a blank pane.
  it("a failed restart leaves the terminal alone", async () => {
    const h = await setup();
    h.restartDispatch.mockResolvedValueOnce(null);
    h.mod.initShellPanel();
    h.shellBtn.click();

    h.shellRestartBtn.click();
    await new Promise((r) => setTimeout(r, 0));

    expect(h.resetSpy).not.toHaveBeenCalled();
  });

  // The command lands at the prompt and WAITS. No trailing newline anywhere on
  // this path: send() writes raw bytes, so one would be Enter and the click
  // would execute rather than type. Pressing Enter is the user's confirmation.
  it("types the command with NO trailing newline, so it waits at the prompt", async () => {
    const h = await setup();
    h.mod.initShellPanel(); // registers the run callback
    h.shellBtn.click(); // open so the terminal (and its handle) exists

    const runCb = h.getRunCb();
    expect(runCb).not.toBeNull();
    runCb?.("echo hi");

    expect(h.sendSpy).toHaveBeenCalledWith(new TextEncoder().encode("echo hi"));
    const sent = h.sendSpy.mock.calls[0]?.[0] as Uint8Array;
    expect(sent.at(-1)).not.toBe(0x0a);
    expect(sent.at(-1)).not.toBe(0x0d);
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

  it("restoreShell does NOT steal focus at boot; a user open does focus", async () => {
    const h = await setup();
    h.mod.initShellPanel();
    h.mod.restoreShell();
    await nextFrame();
    expect(h.termFocus).not.toHaveBeenCalled();

    h.shellToggleBtn.click(); // close
    h.shellBtn.click(); // user-initiated open
    await nextFrame();
    expect(h.termFocus).toHaveBeenCalled();
  });

  it("restores a persisted height (clamped) onto --shell-h at init", async () => {
    const h = await setup({ shell_h: 300 });
    h.mod.initShellPanel();
    expect(h.shellPanel.style.getPropertyValue("--shell-h")).toBe("300px");
  });

  it("leaves --shell-h unset when no height was persisted (CSS default)", async () => {
    const h = await setup();
    h.mod.initShellPanel();
    expect(h.shellPanel.style.getPropertyValue("--shell-h")).toBe("");
  });
});

function nextFrame(): Promise<void> {
  return new Promise((resolve) => {
    requestAnimationFrame(() => {
      resolve();
    });
  });
}

describe("shell.ts: resize handle", () => {
  it("gets its separator a11y contract from init (markup ships a bare div)", async () => {
    const h = await setup();
    h.mod.initShellPanel();
    expect(h.shellResize.getAttribute("role")).toBe("separator");
    expect(h.shellResize.getAttribute("aria-orientation")).toBe("horizontal");
    expect(h.shellResize.getAttribute("aria-label")).toBe("Resize shell");
    expect(h.shellResize.tabIndex).toBe(0);
  });

  it("drag up grows the panel (bottom-docked math) and persists on release", async () => {
    const h = await setup();
    h.mod.initShellPanel();
    // happy-dom rects are 0-height, so startH = 0; dragging the pointer up
    // by 200px (500 → 300) must yield --shell-h: 200px.
    h.shellResize.dispatchEvent(ptr("pointerdown", 500));
    expect(h.shellResize.classList.contains("dragging")).toBe(true);
    expect(h.shellPanel.classList.contains("resizing")).toBe(true);

    h.shellResize.dispatchEvent(ptr("pointermove", 300));
    expect(h.shellPanel.style.getPropertyValue("--shell-h")).toBe("200px");

    h.shellResize.dispatchEvent(ptr("pointerup", 300));
    expect(h.shellResize.classList.contains("dragging")).toBe(false);
    expect(h.shellPanel.classList.contains("resizing")).toBe(false);
    expect(h.save).toHaveBeenCalledWith({ shell_h: 200 });
  });

  it("clamps the drag to [96px, 80% of innerHeight]", async () => {
    const h = await setup();
    h.mod.initShellPanel();
    const max = Math.round(window.innerHeight * 0.8);

    h.shellResize.dispatchEvent(ptr("pointerdown", 500));
    // Tiny drag (below the 96px floor) clamps up to the minimum.
    h.shellResize.dispatchEvent(ptr("pointermove", 490));
    expect(h.shellPanel.style.getPropertyValue("--shell-h")).toBe("96px");
    // Huge drag clamps down to 80% of the viewport.
    h.shellResize.dispatchEvent(ptr("pointermove", 500 - max - 5000));
    expect(h.shellPanel.style.getPropertyValue("--shell-h")).toBe(`${String(max)}px`);

    h.shellResize.dispatchEvent(ptr("pointerup", 0));
    expect(h.save).toHaveBeenCalledWith({ shell_h: max });
  });

  it("ignores pointermove without capture and non-primary pointerdown", async () => {
    const h = await setup();
    h.mod.initShellPanel();
    h.shellResize.dispatchEvent(ptr("pointermove", 100));
    expect(h.shellPanel.style.getPropertyValue("--shell-h")).toBe("");
    h.shellResize.dispatchEvent(ptr("pointerdown", 500, { isPrimary: false }));
    expect(h.shellResize.classList.contains("dragging")).toBe(false);
  });

  it("ArrowUp/ArrowDown resize from the keyboard and persist each step", async () => {
    const h = await setup();
    h.mod.initShellPanel();
    // Panel rect height is 0 in happy-dom → 0 + 32 clamps to the 96px floor.
    h.shellResize.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowUp" }));
    expect(h.shellPanel.style.getPropertyValue("--shell-h")).toBe("96px");
    expect(h.save).toHaveBeenCalledWith({ shell_h: 96 });
  });
});

describe("shell.ts: close behavior", () => {
  it("clears fullscreen (class + aria-pressed) when closing", async () => {
    const h = await setup();
    h.mod.initShellPanel();
    h.shellBtn.click(); // open
    // Simulate the agent-terminal fullscreen toggle having been used.
    h.shellPanel.classList.add("shell-fullscreen");
    h.shellFullscreenBtn.setAttribute("aria-pressed", "true");

    h.shellToggleBtn.click(); // close
    expect(h.shellPanel.classList.contains("shell-fullscreen")).toBe(false);
    expect(h.shellFullscreenBtn.getAttribute("aria-pressed")).toBe("false");
    expect(h.shellPanel.classList.contains("shell-closed")).toBe(true);
  });

  it("returns focus to the toolbar button when the panel held it", async () => {
    const h = await setup();
    h.mod.initShellPanel();
    h.shellBtn.click(); // open

    const inner = document.createElement("input");
    h.shellPanel.appendChild(inner);
    inner.focus();
    expect(document.activeElement).toBe(inner);

    h.shellToggleBtn.click(); // close
    expect(document.activeElement).toBe(h.shellBtn);
  });

  it("leaves focus alone when it was outside the panel", async () => {
    const h = await setup();
    h.mod.initShellPanel();
    h.shellBtn.click(); // open

    const outside = document.createElement("input");
    document.body.appendChild(outside);
    outside.focus();

    h.shellToggleBtn.click(); // close
    expect(document.activeElement).toBe(outside);
  });
});
