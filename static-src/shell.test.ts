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

/** Cache-buster for the re-imports below.
 *
 * `vi.resetModules()` does not re-evaluate a module in Browser Mode: the module
 * map is URL-keyed, so a following `await import()` hands back the CACHED
 * instance and every test after the first observes stale module state. Busting
 * the specifier per evaluation is what actually mints a fresh instance. The `.ts`
 * extension is load-bearing — written `.js` the suite still passes while coverage
 * silently attributes every evaluation to a file that does not exist.
 *
 * Only the module under test is busted. Its own dependencies keep their plain
 * specifiers, so `vi.mock` still intercepts them and a shared module the test
 * also imports is the same instance the fresh module got.
 */
let bootSeq = 0;

/** The per-test spies and elements the mock factories below read.
 *
 * The factories are registered ONCE, at module scope, and they resolve through
 * this holder rather than closing over a per-test value. That indirection is
 * required rather than stylistic: `vi.resetModules()` does not re-evaluate a
 * module in Browser Mode, so a mocked module is evaluated the FIRST time it is
 * imported and cached forever. A per-test `vi.doMock` therefore re-registers a
 * factory that never runs again, and the fresh shell instance kept binding its
 * listeners to the first test's buttons while the test clicked its own.
 */
interface Live {
  createTerminal: (root: HTMLElement, opts: CreateTerminalOptions) => TerminalHandle;
  localScrollbackStorage: (opts: unknown) => unknown;
  presetTouch: (...args: unknown[]) => unknown;
  getScrollEl: () => HTMLElement;
  setShellRunCallback: (cb: (cmd: string) => void) => void;
  save: (patch: unknown) => void;
  load: () => { shell_h: number };
  restartDispatch: (...args: unknown[]) => Promise<unknown>;
  confirmMock: (...args: unknown[]) => Promise<boolean>;
  toastError: (...args: unknown[]) => void;
  els: Record<string, HTMLElement>;
}
let live: Live;

vi.mock("@cplieger/web-terminal-ui", () => ({
  createTerminal: (root: HTMLElement, opts: CreateTerminalOptions): TerminalHandle =>
    live.createTerminal(root, opts),
  localScrollbackStorage: (opts: unknown): unknown => live.localScrollbackStorage(opts),
}));
vi.mock("@cplieger/web-terminal-ui/presets/touch", () => ({
  presetTouch: (...args: unknown[]): unknown => live.presetTouch(...args),
}));
vi.mock("./messages.js", () => ({ getScrollEl: (): HTMLElement => live.getScrollEl() }));
vi.mock("./code-blocks.js", () => ({
  setShellRunCallback: (cb: (cmd: string) => void): void => live.setShellRunCallback(cb),
}));
vi.mock("./ui-state.js", () => ({
  save: (patch: unknown): void => live.save(patch),
  load: (): { shell_h: number } => live.load(),
}));
vi.mock("./actions/shell.js", () => ({
  restartShell: {
    dispatch: (...args: unknown[]): Promise<unknown> => live.restartDispatch(...args),
  },
}));
vi.mock("./confirm.js", () => ({
  confirm: (...args: unknown[]): Promise<boolean> => live.confirmMock(...args),
}));
vi.mock("./toast.js", () => ({
  error: (...args: unknown[]): void => live.toastError(...args),
  info: vi.fn(),
  success: vi.fn(),
}));
vi.mock("./dom.js", () => ({
  $: new Proxy(
    {},
    {
      get: (_t, prop: string) => live.els[prop],
    },
  ),
}));

interface Harness {
  mod: typeof Shell;
  createTerminal: ReturnType<typeof vi.fn>;
  localScrollbackStorage: ReturnType<typeof vi.fn>;
  resetSpy: ReturnType<typeof vi.fn>;
  sendSpy: ReturnType<typeof vi.fn>;
  reattachSpy: ReturnType<typeof vi.fn>;
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
  /** Report the definitive process-exited close, as the kernel does. */
  endSession: () => void;
}

/** Back the three pointer-capture methods with a Set so the
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

/** Synthetic pointer event: the handlers do not need a real PointerEvent, and
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
  bootSeq++;

  const shellBtn = document.createElement("button");
  const shellToggleBtn = document.createElement("button");
  const shellRestartBtn = document.createElement("button");
  const shellFullscreenBtn = document.createElement("button");
  // The real button ships a glyph in index.html and the toggle swaps its `d`,
  // so the harness carries one too — without it the icon assertions below pass
  // vacuously against a button that has no path to write.
  shellFullscreenBtn.innerHTML = '<svg viewBox="0 0 24 24"><path d="M8 3H5"/></svg>';
  const shellPanel = document.createElement("div");
  shellPanel.classList.add("shell-closed");
  const shellTerminal = document.createElement("div");
  const shellResize = document.createElement("div");
  stubPointerCapture(shellResize);
  shellPanel.append(shellResize, shellTerminal);
  // Panel + toolbar button live in the document so focus()/activeElement and
  // contains() checks behave; replaceChildren drops the previous test's DOM.
  document.body.replaceChildren(shellPanel, shellBtn);

  // The v6 handle's host controls: send routes the sanitizing funnel, reset
  // drops the local scrollback + screen, reattach picks up the PTY the server
  // serves now.
  const sendSpy = vi.fn();
  const resetSpy = vi.fn();
  const termFocus = vi.fn();
  const reattachSpy = vi.fn();
  // The panel's half of the ended contract is the callback it passes, so the mock
  // captures it. Without that the reattach path would only be reachable through
  // the Restart button, and the `exit` half of the same defect would go untested.
  let endedCb: (() => void) | null = null;
  const createTerminal = vi.fn(
    (_root: HTMLElement, opts: CreateTerminalOptions): TerminalHandle => {
      endedCb = opts.onSessionEnded ?? null;
      return {
        focus: termFocus,
        send: sendSpy,
        reset: resetSpy,
        reattach: reattachSpy,
        destroy: vi.fn(),
      };
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

  live = {
    createTerminal,
    localScrollbackStorage,
    presetTouch,
    getScrollEl,
    setShellRunCallback,
    save,
    load,
    restartDispatch,
    confirmMock,
    toastError,
    els: {
      shellBtn,
      shellToggleBtn,
      shellRestartBtn,
      shellFullscreenBtn,
      shellPanel,
      shellTerminal,
      shellResize,
    },
  };

  const mod = (await import(/* @vite-ignore */ `./shell.ts?boot=${bootSeq}`)) as typeof Shell;
  return {
    mod,
    createTerminal,
    localScrollbackStorage,
    resetSpy,
    sendSpy,
    reattachSpy,
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
    endSession: () => {
      if (endedCb === null) {
        throw new Error("no onSessionEnded handler; open the panel first");
      }
      endedCb();
    },
  };
}

afterEach(() => {
  // No doUnmock: the mocks above are registered once at module scope and stay,
  // because a mocked module is evaluated once and cached. Each test gets its
  // isolation from a fresh `live` holder plus a busted shell specifier.
  vi.useRealTimers();
  vi.resetModules();
  bootSeq++;
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
  it("the Restart button confirms, calls the server, then reattaches to the new PTY", async () => {
    const h = await setup();
    h.mod.initShellPanel();
    h.shellBtn.click(); // open so the terminal (and its handle) exists

    h.shellRestartBtn.click();
    await new Promise((r) => setTimeout(r, 0));

    expect(h.confirmMock).toHaveBeenCalledTimes(1);
    expect(h.restartDispatch).toHaveBeenCalledTimes(1);
    // Reattaching is the half that was missing: the server kills the PTY and
    // installs a fresh one lazily, INSIDE the next connect, so a client that
    // only cleared its screen left the panel reading "Session ended" forever.
    // The clear rides along inside reattach(), which is why this asserts nothing
    // about reset(): ordering it against the connect is the library's job.
    expect(h.reattachSpy).toHaveBeenCalledTimes(1);
    expect(h.resetSpy).not.toHaveBeenCalled();
  });

  it("a declined confirm neither calls the server nor touches the terminal", async () => {
    const h = await setup();
    h.confirmMock.mockResolvedValueOnce(false);
    h.mod.initShellPanel();
    h.shellBtn.click();

    h.shellRestartBtn.click();
    await new Promise((r) => setTimeout(r, 0));

    expect(h.restartDispatch).not.toHaveBeenCalled();
    expect(h.reattachSpy).not.toHaveBeenCalled();
  });

  // A failed restart must not reattach: the old PTY is still live, so dropping
  // the buffer and taking a full replay would blank a working shell for nothing.
  it("a failed restart leaves the terminal alone", async () => {
    const h = await setup();
    h.restartDispatch.mockResolvedValueOnce(null);
    h.mod.initShellPanel();
    h.shellBtn.click();

    h.shellRestartBtn.click();
    await new Promise((r) => setTimeout(r, 0));

    expect(h.reattachSpy).not.toHaveBeenCalled();
  });

  // The restart's own kill closes this client's socket, so the ended state
  // normally reattaches while the POST is still in flight. The response must not
  // then reconnect a second time over the prompt that reattach just drew.
  it("does not reattach twice when the ended close won the race", async () => {
    const h = await setup();
    h.mod.initShellPanel();
    h.shellBtn.click();

    let releaseDispatch: (value: { ok: boolean }) => void = () => undefined;
    h.restartDispatch.mockImplementationOnce(
      () =>
        new Promise<{ ok: boolean }>((resolve) => {
          releaseDispatch = resolve;
        }),
    );

    h.shellRestartBtn.click();
    await new Promise((r) => setTimeout(r, 0)); // confirm resolves, POST in flight
    expect(h.restartDispatch).toHaveBeenCalledTimes(1);

    h.endSession(); // the server's kill closed the socket
    releaseDispatch({ ok: true });
    await new Promise((r) => setTimeout(r, 0));

    expect(h.reattachSpy).toHaveBeenCalledTimes(1);
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

describe("shell.ts: reattaching after the session ends", () => {
  // The other half of the same defect, and the more common one: typing `exit`
  // ends the child, the engine's process-exited close suppresses its own backoff
  // reconnect (definitive, not transient), and the server only swaps in a fresh
  // PTY on the next connect. Nothing was making that connect, so the panel sat
  // on "Session ended" until a page reload.
  it("reattaches with no user gesture when the child exits", async () => {
    const h = await setup();
    h.mod.initShellPanel();
    h.shellBtn.click();

    h.endSession();

    expect(h.reattachSpy).toHaveBeenCalledTimes(1);
  });

  it("passes a handler at all, which is what makes the exit case reachable", async () => {
    const h = await setup();
    h.mod.initShellPanel();
    h.shellBtn.click();

    const [, opts] = h.createTerminal.mock.calls.at(0) as [HTMLElement, CreateTerminalOptions];
    expect(typeof opts.onSessionEnded).toBe("function");
  });

  // A shell that dies as fast as it is spawned (a login file that exits, a
  // missing interpreter) must not be respawned in a loop: the ladder backs off
  // and then stops, leaving the honest "Session ended" banner standing.
  it("gives up after four consecutive respawns", async () => {
    vi.useFakeTimers();
    const h = await setup();
    h.mod.initShellPanel();
    h.shellBtn.click();

    // Six deaths, each one landing while the previous spawn is still fresh, so
    // the ladder never resets: immediate, +250ms, +500ms, +1000ms, then nothing.
    for (let i = 0; i < 6; i++) {
      h.endSession();
      await vi.advanceTimersByTimeAsync(1000);
    }

    expect(h.reattachSpy).toHaveBeenCalledTimes(4);
  });

  // A session that ran for a while ended on its own terms, so its end is a new
  // incident rather than another failure of the spawn that replaced the last one.
  it("starts a fresh ladder once a session has run for a while", async () => {
    vi.useFakeTimers();
    const h = await setup();
    h.mod.initShellPanel();
    h.shellBtn.click();

    for (let i = 0; i < 6; i++) {
      h.endSession();
      await vi.advanceTimersByTimeAsync(1000);
    }
    expect(h.reattachSpy).toHaveBeenCalledTimes(4);

    await vi.advanceTimersByTimeAsync(5000); // the replacement stayed up
    h.endSession();

    expect(h.reattachSpy).toHaveBeenCalledTimes(5);
  });

  it("does nothing before the terminal exists", async () => {
    const h = await setup();
    h.mod.initShellPanel();
    // No open, so no terminal, no feature and no socket. Nothing can emit the
    // state here — which is the assertion: the panel's first open is what
    // connects, so there is no reattach path to take.
    expect(h.createTerminal).not.toHaveBeenCalled();
    expect(h.reattachSpy).not.toHaveBeenCalled();
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
    // The harness panel is unstyled, so its rect is 0-height and startH = 0; dragging up
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
    // The unstyled panel's rect height is 0 → 0 + 32 clamps to the 96px floor.
    h.shellResize.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowUp" }));
    expect(h.shellPanel.style.getPropertyValue("--shell-h")).toBe("96px");
    expect(h.save).toHaveBeenCalledWith({ shell_h: 96 });
  });
});

describe("shell.ts: fullscreen toggle", () => {
  // The toggle moved here from agent-terminal.ts when that module was deleted.
  // Nothing else wired this button, so without the move it would have gone dead
  // silently — the panel's CLOSE path already cleared the class, so the only
  // visible symptom was a header button that did nothing.
  const face = (btn: HTMLButtonElement): string =>
    btn.querySelector("path")?.getAttribute("d") ?? "";
  /** How many of the glyph's four corner arcs sweep the given way. Sweep is the
   *  ONLY difference between the two faces — the corner set is rotationally
   *  symmetric, so this is the assertion that "the corners point the other
   *  way", and a rotated copy of one face would fail it. */
  const sweeps = (d: string, flag: "0" | "1"): number => d.split(`a2 2 0 0 ${flag}`).length - 1;

  it("enters fullscreen and mirrors the state on aria-pressed", async () => {
    const h = await setup();
    h.mod.initShellPanel();
    expect(h.shellFullscreenBtn.getAttribute("aria-pressed")).toBe("false");

    h.shellFullscreenBtn.click();
    expect(h.shellPanel.classList.contains("shell-fullscreen")).toBe(true);
    expect(h.shellFullscreenBtn.getAttribute("aria-pressed")).toBe("true");
  });

  it("turns the glyph's corners inward while fullscreen, and back on exit", async () => {
    const h = await setup();
    h.mod.initShellPanel();
    // Boot: corners point outward (enter), all four arcs sweeping one way.
    expect(face(h.shellFullscreenBtn)).toMatch(/^M8 3H5/);
    expect(sweeps(face(h.shellFullscreenBtn), "0")).toBe(4);

    h.shellFullscreenBtn.click();
    expect(face(h.shellFullscreenBtn)).toMatch(/^M8 3v3/);
    expect(sweeps(face(h.shellFullscreenBtn), "1")).toBe(4);

    // Restored with aria-pressed, on the CLICK rather than at animationend: the
    // button already reads as off, so its face has to agree immediately.
    h.shellFullscreenBtn.click();
    expect(face(h.shellFullscreenBtn)).toMatch(/^M8 3H5/);
    expect(h.shellPanel.classList.contains("shell-fullscreen")).toBe(true);
  });

  it("labels what the click will do, in both directions", async () => {
    const h = await setup();
    h.mod.initShellPanel();
    expect(h.shellFullscreenBtn.getAttribute("data-tooltip")).toBe("Full screen");
    expect(h.shellFullscreenBtn.getAttribute("aria-label")).toBe("Full screen");

    h.shellFullscreenBtn.click();
    expect(h.shellFullscreenBtn.getAttribute("data-tooltip")).toBe("Exit full screen");
    expect(h.shellFullscreenBtn.getAttribute("aria-label")).toBe("Exit full screen");
  });

  it("leaves through the transient class so the exit has something to animate", async () => {
    const h = await setup();
    h.mod.initShellPanel();
    h.shellFullscreenBtn.click();

    h.shellFullscreenBtn.click();
    // aria-pressed flips immediately; the class is held for one animation.
    expect(h.shellFullscreenBtn.getAttribute("aria-pressed")).toBe("false");
    expect(h.shellPanel.classList.contains("shell-fullscreen-leaving")).toBe(true);

    h.shellPanel.dispatchEvent(new Event("animationend"));
    expect(h.shellPanel.classList.contains("shell-fullscreen")).toBe(false);
    expect(h.shellPanel.classList.contains("shell-fullscreen-leaving")).toBe(false);
  });
});

describe("shell.ts: close behavior", () => {
  it("clears fullscreen (class + aria-pressed) when closing", async () => {
    const h = await setup();
    h.mod.initShellPanel();
    h.shellBtn.click(); // open
    h.shellFullscreenBtn.click();
    expect(h.shellPanel.classList.contains("shell-fullscreen")).toBe(true);

    h.shellToggleBtn.click(); // close
    expect(h.shellPanel.classList.contains("shell-fullscreen")).toBe(false);
    expect(h.shellFullscreenBtn.getAttribute("aria-pressed")).toBe("false");
    // The close path is the third writer of this state; it must reset the face
    // too, or the next open shows a docked panel offering to shrink itself.
    expect(h.shellFullscreenBtn.querySelector("path")?.getAttribute("d")).toMatch(/^M8 3H5/);
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
