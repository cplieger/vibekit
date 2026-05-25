// ---------------------------------------------------------------------------
// Shell panel: xterm.js terminal connected to a server-side PTY via
// WebSocket at /api/shell/ws. The server holds the PTY; the client is
// a pure viewport. On reconnect (iOS sleep, network blip) the server
// replays its scrollback buffer so xterm.js can reconstruct state.
//
// Wire protocol (binary WS frames):
//   server → client: raw PTY output bytes
//   client → server: raw terminal input bytes
//   client → server: JSON control prefixed with 0x00:
//     {"type":"resize","cols":N,"rows":N}
//     {"type":"signal","name":"SIGINT"}
//     {"type":"kill"}
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { getScrollEl, setShellRunCallback } from "./messages.js";
import { ShellWS, encoder } from "./shell-ws.js";

const shellWS = new ShellWS();
import * as uiState from "./ui-state.js";
import type {
  ITheme, Terminal as XTerm,
} from "xterm";
import type { FitAddon as XFit } from "xterm/addon-fit";

const MIN_HEIGHT_PX = 80;
const MAX_HEIGHT_PCT = 0.8;

// Debounce window for ResizeObserver → fitAddon.fit() calls. During a
// drag we get a resize event every animation frame; 100ms covers the
// gap without feeling laggy.
const RESIZE_DEBOUNCE_MS = 100;

// --- Vendor module loading ---
//
// xterm.js ESM files live at /vendor/xterm/*.mjs, placed there at Docker
// build time. During local tsgo type-checking they don't exist on disk,
// so imports are dynamic and typed via xterm-vendor.d.ts. The loaded
// classes are cached on `vendor` after the first open.

interface Vendor {
  Terminal: typeof XTerm;
  FitAddon: typeof XFit;
  WebglAddon: new () => import("xterm/addon-webgl").WebglAddon;
  WebLinksAddon: new () => import("xterm/addon-web-links").WebLinksAddon;
}

// --- Theme: read CSS custom properties so the terminal tracks the app
//     theme automatically on light/dark/system switches. ---

function cssVar(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

function getTheme(): ITheme {
  return {
    background: cssVar("--c-term-bg"),
    foreground: cssVar("--c-term-fg"),
    cursor: cssVar("--c-term-cursor"),
    cursorAccent: cssVar("--c-term-cursor-accent"),
    selectionBackground: cssVar("--c-term-selection"),
    selectionInactiveBackground: cssVar("--c-term-selection-inactive"),
    black: cssVar("--c-term-black"),
    red: cssVar("--c-term-red"),
    green: cssVar("--c-term-green"),
    yellow: cssVar("--c-term-yellow"),
    blue: cssVar("--c-term-blue"),
    magenta: cssVar("--c-term-magenta"),
    cyan: cssVar("--c-term-cyan"),
    white: cssVar("--c-term-white"),
    brightBlack: cssVar("--c-term-bright-black"),
    brightRed: cssVar("--c-term-bright-red"),
    brightGreen: cssVar("--c-term-bright-green"),
    brightYellow: cssVar("--c-term-bright-yellow"),
    brightBlue: cssVar("--c-term-bright-blue"),
    brightMagenta: cssVar("--c-term-bright-magenta"),
    brightCyan: cssVar("--c-term-bright-cyan"),
    brightWhite: cssVar("--c-term-bright-white"),
  };
}

// ---------------------------------------------------------------------------
// ShellController: owns all shell panel state as instance fields.
// ---------------------------------------------------------------------------

class ShellController {
  private terminal: XTerm | null = null;
  private fitAddon: XFit | null = null;
  private shellOpen = false;
  private vendor: Vendor | null = null;

  // --- Vendor module loading ---

  private async loadVendor(): Promise<Vendor> {
    if (this.vendor !== null) return this.vendor;
    const [xterm, fit, webgl, links] = await Promise.all([
      import("xterm"),
      import("xterm/addon-fit"),
      import("xterm/addon-webgl"),
      import("xterm/addon-web-links"),
    ]);
    this.vendor = {
      Terminal: xterm.Terminal,
      FitAddon: fit.FitAddon,
      WebglAddon: webgl.WebglAddon,
      WebLinksAddon: links.WebLinksAddon,
    };
    return this.vendor;
  }

  // --- Control + input plumbing ---

  private sendResize(): void {
    if (this.terminal === null) return;
    shellWS.sendControl({ type: "resize", cols: this.terminal.cols, rows: this.terminal.rows });
  }

  // --- Terminal lifecycle ---

  private async ensureTerminal(): Promise<void> {
    if (this.terminal !== null) return;
    const v = await this.loadVendor();

    this.terminal = new v.Terminal({
      cursorBlink: true,
      cursorStyle: "bar",
      cursorInactiveStyle: "outline",
      fontSize: 13,
      fontFamily: "ui-monospace, 'Cascadia Code', 'Source Code Pro', Menlo, Consolas, monospace",
      theme: getTheme(),
      allowProposedApi: true,
      scrollback: 10000,
      smoothScrollDuration: 100,
      minimumContrastRatio: 4.5,
      macOptionIsMeta: true,
      rightClickSelectsWord: true,
      scrollOnUserInput: true,
    });

    this.fitAddon = new v.FitAddon();
    this.terminal.loadAddon(this.fitAddon);
    this.terminal.loadAddon(new v.WebLinksAddon());

    this.terminal.open($.shellTerminal);

    // WebGL renderer is an optimisation; fall back silently if the GPU
    // context can't be created (older iOS, Linux without hw accel).
    try {
      this.terminal.loadAddon(new v.WebglAddon());
    } catch {
      // DOM renderer is the default fallback; nothing to do.
    }

    this.fitAddon.fit();

    this.terminal.onData((data: string) => { shellWS.sendRaw(encoder.encode(data)); });

    // Binary input covers non-UTF-8 paste/mouse-report payloads.
    this.terminal.onBinary((data: string) => {
      const buf = new Uint8Array(data.length);
      for (let i = 0; i < data.length; i++) buf[i] = data.charCodeAt(i) & 0xff;
      shellWS.sendRaw(buf);
    });

    this.terminal.onResize(() => { this.sendResize(); });

    // Surface the shell title (cwd in bash, program name in vim/htop etc).
    this.terminal.onTitleChange((title: string) => {
      $.shellTitle.textContent = title === "" ? "Shell" : title;
    });
  }

  // --- Visibility / bfcache recovery (iOS sleep, tab switch, back/forward) ---

  private onVisibilityChange(): void {
    if (document.visibilityState === "visible") shellWS.reconnectIfStale();
  }

  // Safari fires pageshow with persisted=true on bfcache restore.
  private onPageShow(e: PageTransitionEvent): void {
    if (e.persisted) shellWS.reconnectIfStale();
  }

  // --- Open / close ---

  restoreShell(): void {
    // Called on page load when ui-state has shell_open=true. The full
    // open path (load modules + connect) runs, not just CSS toggles.
    void this.setShellOpen(true);
  }

  private toggleShell(): void { void this.setShellOpen(!this.shellOpen); }

  private async setShellOpen(open: boolean): Promise<void> {
    if (open) {
      try {
        await this.ensureTerminal();
      } catch (e) {
        console.error("Failed to load terminal modules", e);
        // Surface the error visibly so "toggle shell did nothing" isn't
        // a silent failure. The panel opens in a degraded state with the
        // error message where the terminal would be.
        $.shellPanel.classList.remove("shell-closed", "collapsed");
        $.shellBtn.classList.add("active");
        $.shellStatus.textContent = "load failed — check console";
        this.shellOpen = true;
        shellWS.setActive(true);
        uiState.save({ shell_open: true });
        return;
      }

      const prevHeight = getScrollEl().clientHeight;
      $.shellPanel.classList.remove("shell-closed", "collapsed");
      $.shellBtn.classList.add("active");
      this.shellOpen = true;
      shellWS.setActive(true);

      // Fit once the panel is visible so dimensions are correct.
      requestAnimationFrame(() => {
        this.fitAddon?.fit();
        this.terminal?.focus();
        const shrunk = prevHeight - getScrollEl().clientHeight;
        if (shrunk > 0) getScrollEl().scrollTop += shrunk;
      });

      void shellWS.connect();
    } else {
      $.shellPanel.classList.add("shell-closed");
      $.shellBtn.classList.remove("active");
      this.shellOpen = false;
      shellWS.setActive(false);
      shellWS.disconnect();
      $.shellStatus.textContent = "";
      $.shellPanel.classList.remove("shell-disconnected");
    }
    uiState.save({ shell_open: open });
  }

  // --- Imperative "run this command" from tool cards ---

  private async openWithCommand(cmd: string): Promise<void> {
    if (!this.shellOpen) await this.setShellOpen(true);
    try {
      await shellWS.whenSocketReady(5000);
    } catch {
      return;
    }
    shellWS.sendRaw(encoder.encode(`${cmd}\n`));
  }

  // --- Theme sync ---

  private syncShellTheme(): void {
    if (this.terminal === null) return;
    this.terminal.options.theme = getTheme();
  }

  // --- Init ---

  init(): void {
    // Wire up WebSocket callbacks to the terminal/UI.
    shellWS.setCallbacks({
      onMessage: (data: ArrayBuffer | string) => {
        if (this.terminal === null) return;
        if (data instanceof ArrayBuffer) {
          this.terminal.write(new Uint8Array(data));
        } else {
          this.terminal.write(data);
        }
      },
      onOpen: () => {
        $.shellStatus.textContent = "";
        $.shellPanel.classList.remove("shell-disconnected");
        this.sendResize();
      },
      onClose: (code: number) => {
        $.shellPanel.classList.add("shell-disconnected");
        if (code === 1000) {
          $.shellStatus.textContent = "exited";
        } else {
          $.shellStatus.textContent = "";
        }
      },
      onReconnecting: () => {
        $.shellStatus.textContent = "reconnecting...";
      },
    });

    setShellRunCallback((cmd: string) => { void this.openWithCommand(cmd); });
    document.addEventListener("shell-run", (e: Event) => {
      void this.openWithCommand((e as CustomEvent<string>).detail);
    });

    $.shellBtn.addEventListener("click", () => { this.toggleShell(); });
    $.shellToggleBtn.addEventListener("click", () => { void this.setShellOpen(false); });
    $.shellClearBtn.addEventListener("click", () => { this.terminal?.clear(); });
    $.shellKillBtn.addEventListener("click", () => { shellWS.sendControl({ type: "kill" }); });

    // iOS sleep / tab-switch / bfcache recovery.
    document.addEventListener("visibilitychange", () => { this.onVisibilityChange(); });
    window.addEventListener("pageshow", (e) => { this.onPageShow(e); });

    this.initResize();

    // Re-theme when data-theme flips or the OS color scheme changes.
    new MutationObserver(() => { this.syncShellTheme(); }).observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["data-theme"],
    });
    window.matchMedia("(prefers-color-scheme: light)")
      .addEventListener("change", () => { this.syncShellTheme(); });
  }

  // --- Resize drag (pointer events) ---

  private initResize(): void {
    let startY = 0;
    let startH = 0;
    let pointerId = -1;

    $.shellResize.addEventListener("pointerdown", (e: PointerEvent) => {
      if (!e.isPrimary) return;
      startY = e.clientY;
      startH = $.shellPanel.offsetHeight;
      pointerId = e.pointerId;
      $.shellResize.setPointerCapture(pointerId);
      $.shellResize.classList.add("dragging");
      document.body.style.cursor = "ns-resize";
      document.body.style.userSelect = "none";
      e.preventDefault();
    });

    $.shellResize.addEventListener("pointermove", (e: PointerEvent) => {
      if (pointerId !== e.pointerId) return;
      const maxH = window.innerHeight * MAX_HEIGHT_PCT;
      const newH = Math.max(MIN_HEIGHT_PX, Math.min(maxH, startH + (startY - e.clientY)));
      $.shellPanel.style.setProperty("--shell-h", `${String(newH)}px`);
    });

    const end = (e: PointerEvent): void => {
      if (pointerId !== e.pointerId) return;
      $.shellResize.releasePointerCapture(pointerId);
      pointerId = -1;
      $.shellResize.classList.remove("dragging");
      document.body.style.cursor = "";
      document.body.style.userSelect = "";
      // Fit once at the end of the drag, not on every pixel.
      this.fitAddon?.fit();
    };
    $.shellResize.addEventListener("pointerup", end);
    $.shellResize.addEventListener("pointercancel", end);

    // Debounced re-fit on any container resize (window, orientation, drag).
    let resizeTimer: ReturnType<typeof setTimeout> | null = null;
    const ro = new ResizeObserver(() => {
      if (this.fitAddon === null || !this.shellOpen) return;
      if (resizeTimer !== null) clearTimeout(resizeTimer);
      resizeTimer = setTimeout(() => {
        resizeTimer = null;
        if (this.shellOpen) this.fitAddon?.fit();
      }, RESIZE_DEBOUNCE_MS);
    });
    ro.observe($.shellPanel);
  }
}

// ---------------------------------------------------------------------------
// Singleton + delegate exports (backward-compatible public API).
// ---------------------------------------------------------------------------

const shell = new ShellController();

export function initShellPanel(): void { shell.init(); }
export function restoreShell(): void { shell.restoreShell(); }
