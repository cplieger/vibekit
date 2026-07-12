// ---------------------------------------------------------------------------
// Shell panel: a single global server-side PTY, rendered by the standard
// @cplieger/web-terminal-ui terminal (createTerminal) over the EXISTING
// WebSocket at /api/shell/ws.
//
// The UI package owns everything above the raw terminal — the display surface,
// the hidden-textarea input model, IME/composition, predictive local echo, the
// mobile key toolbar, the right-click/long-press context menu, scroll-to-bottom,
// and the connection banner (presetTouch: presetSingle + mobileToolbar, NO
// tabs). The engine (@cplieger/web-terminal-engine) underneath owns the wire
// protocol, the reconnect/resume reliability layer, and the render/scroll
// modules. vibekit keeps only its panel chrome (the slide-up panel, the header
// open/close/reset buttons) and its lifecycle (ui-state persistence).
//
// The terminal's own handle is sealed ({ focus, destroy }) — it exposes no way
// to send bytes or clear the screen. A small host-bridge feature captures the
// kernel's sanitizing send funnel (ctx.send) into module scope so vibekit's two
// host-driven actions can reach the terminal: the code-block "run in shell"
// button and the header Reset button. Reset also clears the engine renderer's
// local scrollback + screen (the same reset the engine performs on a server
// restart) before redrawing the prompt.
// ---------------------------------------------------------------------------

import { createTerminal } from "@cplieger/web-terminal-ui";
import { presetTouch } from "@cplieger/web-terminal-ui/presets";
import type { TerminalFeature, TerminalHandle } from "@cplieger/web-terminal-ui";
import { render } from "@cplieger/web-terminal-engine";
import { $ } from "./dom.js";
import { getScrollEl } from "./messages.js";
import { setShellRunCallback } from "./code-blocks.js";
import * as uiState from "./ui-state.js";

const SHELL_WS_PATH = "/api/shell/ws";
// Awaited by the kernel before the first server resize so the PTY is sized
// against the real cell metrics. vibekit's mono stack is system-provided (no
// web font to fetch), unlike web-terminal-kiro's bundled Monaspace; 14px matches
// the `.term` font-size the UI CSS sets.
const SHELL_FONT_READY = "14px ui-monospace";

// The shell terminal recolored to vibekit's palette. The engine renderer reads
// default fg/bg (and inverse) from --bg / --text, and the UI chrome reads
// --accent / --surface / --border; pointing them at vibekit's theme-reactive
// terminal tokens makes the shell follow the app's light/dark theme with no
// per-theme code. Set on the #shell-terminal root by createTerminal, so only
// the terminal subtree is affected.
const SHELL_THEME: Readonly<Record<string, string>> = {
  "--bg": "var(--c-term-bg)",
  "--text": "var(--c-term-fg)",
  "--accent": "var(--c-accent)",
  "--surface": "var(--c-bg-tertiary)",
  "--border": "var(--c-border)",
};

// Ctrl+L (form feed): the terminal-native "clear screen and redraw the prompt".
// The shell's line editor intercepts it and repaints, which — after we reset the
// local buffer below — refills the window with a fresh prompt.
const CTRL_L = new Uint8Array([0x0c]);

const encoder = new TextEncoder();

// The kernel's sanitizing send funnel, captured by the host-bridge feature at
// setup. Null until the terminal is created (first panel open) and again after
// teardown; every caller guards with `?.`.
let bridgeSend: ((bytes: Uint8Array) => void) | null = null;

// A custom feature whose only job is to hand vibekit the kernel's send funnel.
// The sealed TerminalHandle exposes neither send nor a screen reset, so this is
// the supported way for host code (the Reset button, run-in-shell) to drive the
// live terminal. It mounts no chrome.
const hostBridge: TerminalFeature = {
  name: "vibekit-host-bridge",
  setup(ctx) {
    // Wrap rather than alias ctx.send so the funnel is always invoked with the
    // kernel as its receiver (and to keep eslint's unbound-method happy).
    bridgeSend = (bytes) => {
      ctx.send(bytes);
    };
    return {
      teardown() {
        bridgeSend = null;
      },
    };
  },
};

/** Send bytes to the PTY through the kernel's sanitizing, scroll-snapping
 *  funnel. Used by the code-block "run in shell" action. */
function hostSend(bytes: Uint8Array): void {
  bridgeSend?.(bytes);
}

/** Reset the terminal to its default (clean) state: drop the client's local
 *  scrollback + screen (the engine's own server-restart reset — a full store
 *  wipe + repaint on the shared renderer singleton), then send Ctrl+L so the
 *  shell redraws a fresh prompt into the cleared window. */
function hostReset(): void {
  render.resetScrollback();
  render.resetScreen();
  hostSend(CTRL_L);
}

let handle: TerminalHandle | null = null;
let initialized = false;

/**
 * Wire the shell panel's host controls. Called once from app.ts on boot. The
 * terminal itself is NOT created here — it is built lazily on first open (see
 * ensureTerminal), so a session that never opens the shell opens no WebSocket.
 */
export function initShellPanel(): void {
  if (initialized) {
    return;
  }
  initialized = true;

  // Toolbar button opens/toggles the panel; the header X closes it.
  $.shellBtn.addEventListener("click", () => {
    setShellOpen(!shellOpen);
  });
  $.shellToggleBtn.addEventListener("click", () => {
    setShellOpen(false);
  });
  // Reset button reverts the terminal to a clean prompt.
  $.shellClearBtn.addEventListener("click", () => {
    hostReset();
  });

  // Code-block "run in shell" → type the command into the live terminal.
  setShellRunCallback((cmd: string) => {
    hostSend(encoder.encode(`${cmd}\n`));
  });
}

/** Build the terminal exactly once, into the (empty) #shell-terminal root. The
 *  server PTY persists across WS reconnects, so this is safe to call on the
 *  first open and never again; reopening the panel reuses the same terminal and
 *  its live connection. */
function ensureTerminal(): void {
  if (handle !== null) {
    return;
  }
  handle = createTerminal($.shellTerminal, {
    features: [...presetTouch(), hostBridge],
    wsPath: SHELL_WS_PATH,
    fontReady: SHELL_FONT_READY,
    theme: SHELL_THEME,
  });
}

// --- Panel open / close ---

let shellOpen = false;

/** Open or close the shell slider.
 *
 *  Open: build the terminal on first use (opening its WebSocket), remove
 *  `shell-closed` (CSS slides the panel up), mark the toolbar button active,
 *  then focus the terminal via its handle. Close: hide the panel; the server
 *  PTY keeps running so reopening resumes the same session. State is persisted
 *  per-device in ui-state so a reload restores it (see restoreShell). */
function setShellOpen(open: boolean): void {
  shellOpen = open;
  if (open) {
    // Capture the chat scroll-area height before the panel steals vertical
    // space, so the same content stays in view once it opens.
    const prevHeight = getScrollEl().clientHeight;
    ensureTerminal();
    $.shellPanel.classList.remove("shell-closed", "collapsed");
    $.shellBtn.classList.add("active");
    requestAnimationFrame(() => {
      handle?.focus();
      const shrunk = prevHeight - getScrollEl().clientHeight;
      if (shrunk > 0) {
        getScrollEl().scrollTop += shrunk;
      }
    });
  } else {
    $.shellPanel.classList.add("shell-closed");
    $.shellBtn.classList.remove("active");
  }
  uiState.save({ shell_open: open });
}

/**
 * Restore the shell panel on page load when ui-state had it open. Runs the full
 * open path (build terminal + CSS slide + focus), so the WebSocket only opens
 * when the shell was actually left open.
 */
export function restoreShell(): void {
  setShellOpen(true);
}
