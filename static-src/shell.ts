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
// The terminal handle carries the host controls (ui v4): send(bytes) routes
// through the kernel's sanitizing funnel for the code-block "run in shell"
// button, and reset() clears the local scrollback + screen (the same reset the
// engine performs on a server restart) for the header Reset button, which then
// redraws the prompt with a Ctrl+L. layout: "container" makes #shell-terminal
// the terminal's own styling/positioning boundary, so the panel needs no
// containment workarounds and the served CSS is the component-only
// MANIFEST.touch bundle (no page reset, no fonts, no @layer quarantine).
// ---------------------------------------------------------------------------

import { createTerminal, localScrollbackStorage } from "@cplieger/web-terminal-ui";
import { presetTouch } from "@cplieger/web-terminal-ui/presets/touch";
import type { TerminalHandle } from "@cplieger/web-terminal-ui";
import { $ } from "./dom.js";
import { getScrollEl } from "./messages.js";
import { setShellRunCallback } from "./code-blocks.js";
import * as uiState from "./ui-state.js";
import { confirm as confirmDialog } from "./confirm.js";
import { restartShell } from "./actions/shell.js";

const SHELL_WS_PATH = "/api/shell/ws";
// Awaited by the kernel before the first server resize so the PTY is sized
// against the real cell metrics. vibekit's mono stack is system-provided (no
// web font to fetch), unlike web-terminal-kiro's bundled Monaspace; 14px matches
// the `.term` font-size the UI CSS sets.
const SHELL_FONT_READY = "14px ui-monospace";

// The scrollback store is built at MODULE load, not inside ensureTerminal, even
// though the terminal itself stays lazy. Constructing it is what runs its orphan
// sweep (expired entries, then the byte budget), and a store built only on first
// panel open never sweeps for a user who does not open the shell — leaving old
// snapshots sitting in this origin's localStorage indefinitely, which is the exact
// accumulation the sweep exists to prevent. It opens no connection and touches no
// DOM, so it costs a synchronous pass over this prefix's keys at boot.
const shellScrollback = localScrollbackStorage({ prefix: "vibekit.shell-scrollback." });

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

const encoder = new TextEncoder();

/** Send bytes to the PTY through the kernel's sanitizing, scroll-snapping
 *  funnel (the v4 handle's supported host path). Used by the code-block
 *  "run in shell" action. No-op until the terminal is created (first panel
 *  open) and after teardown — `handle` is null then. */
function hostSend(bytes: Uint8Array): void {
  handle?.send(bytes);
}

/** Kill the PTY and get a fresh one.
 *
 *  This replaced a Reset button that dropped the local scrollback and sent
 *  Ctrl+L: a screen clear, which Ctrl+L already does from the keyboard and the
 *  context menu offers anyway. It could not help with the failure that actually
 *  strands this panel — a wedged foreground process, or a child that exited and
 *  left a handler the server can never start again. A restart covers both, and
 *  a fresh shell arrives with a clear screen, so nothing was lost by having one
 *  button instead of two.
 *
 *  Confirmed, because unlike the clear it destroys whatever is running.
 *
 *  The local reset runs after the server has replaced the PTY, so the client's
 *  scrollback is dropped in the same gesture; the engine's own reconnect then
 *  draws the new shell's first prompt. */
async function hostRestart(): Promise<void> {
  const ok = await confirmDialog(
    "Restart the shell? Anything running in it is killed.",
    "Restart",
    "normal",
  );
  if (!ok) {
    return;
  }
  if ((await restartShell.dispatch()) === null) {
    return; // the action framework toasts the failure
  }
  handle?.reset();
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
  // Restart button kills the PTY and gets a fresh one.
  $.shellRestartBtn.addEventListener("click", () => {
    void hostRestart();
  });

  // Code-block "run in shell" → type the command into the live terminal.
  setShellRunCallback((cmd: string) => {
    hostSend(encoder.encode(`${cmd}\n`));
  });

  initShellResize();
}

// --- Panel resize ---

/** Smallest useful panel height (6rem): a few terminal rows + the header. */
const SHELL_MIN_H = 96;
/** Keyboard resize step (2rem) per ArrowUp/ArrowDown on the handle. */
const SHELL_KEY_STEP = 32;

/** Upper clamp: leave at least 20% of the viewport for the chat column. */
function shellMaxH(): number {
  return Math.round(window.innerHeight * 0.8);
}

function clampShellH(h: number): number {
  return Math.min(Math.max(h, SHELL_MIN_H), shellMaxH());
}

/** Clamp (and round) a panel height, then apply it via the --shell-h custom
 *  property the panel's `height` consumes. Returns the applied value. */
function applyShellH(h: number): number {
  const clamped = Math.round(clampShellH(h));
  $.shellPanel.style.setProperty("--shell-h", `${String(clamped)}px`);
  return clamped;
}

/**
 * Drag-to-resize on the panel's top edge handle: one pointer-capture path for
 * mouse + touch (bottom-docked panel, so dragging up = smaller clientY =
 * taller panel). The height lands in --shell-h and persists per-device in
 * ui-state (shell_h; 0 = CSS default). The handle is also a keyboard
 * separator: ArrowUp grows, ArrowDown shrinks (WCAG 2.1.1).
 */
function initShellResize(): void {
  const resizeEl = $.shellResize;

  // The markup ships a bare div; the a11y contract is set here so index.html
  // stays untouched.
  resizeEl.setAttribute("role", "separator");
  resizeEl.setAttribute("aria-orientation", "horizontal");
  resizeEl.setAttribute("aria-label", "Resize shell");
  resizeEl.tabIndex = 0;

  let startY = 0;
  let startH = 0;
  let lastH = 0;

  resizeEl.addEventListener("pointerdown", (e: PointerEvent) => {
    if (!e.isPrimary) {
      return;
    }
    startY = e.clientY;
    startH = $.shellPanel.getBoundingClientRect().height;
    lastH = Math.round(startH);
    resizeEl.setPointerCapture(e.pointerId);
    resizeEl.classList.add("dragging");
    // Suspend the panel's height transition so it tracks the pointer 1:1
    // instead of easing 200ms behind it (see .shell-panel.resizing).
    $.shellPanel.classList.add("resizing");
    e.preventDefault();
  });

  resizeEl.addEventListener("pointermove", (e: PointerEvent) => {
    if (!resizeEl.hasPointerCapture(e.pointerId)) {
      return;
    }
    lastH = applyShellH(startH + (startY - e.clientY));
  });

  const end = (e: PointerEvent): void => {
    if (!resizeEl.hasPointerCapture(e.pointerId)) {
      return;
    }
    resizeEl.releasePointerCapture(e.pointerId);
    resizeEl.classList.remove("dragging");
    $.shellPanel.classList.remove("resizing");
    uiState.save({ shell_h: lastH });
  };
  resizeEl.addEventListener("pointerup", end);
  resizeEl.addEventListener("pointercancel", end);

  resizeEl.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key !== "ArrowUp" && e.key !== "ArrowDown") {
      return;
    }
    e.preventDefault();
    const cur = $.shellPanel.getBoundingClientRect().height;
    const next = applyShellH(cur + (e.key === "ArrowUp" ? SHELL_KEY_STEP : -SHELL_KEY_STEP));
    uiState.save({ shell_h: next });
  });

  // Restore the persisted height (re-clamped: the viewport may have changed
  // since it was saved). Harmless while closed — the closed state pins
  // height 0; the value takes effect on open.
  const saved = uiState.load().shell_h;
  if (saved > 0) {
    applyShellH(saved);
  }
}

/** Build the terminal exactly once, into the (empty) #shell-terminal root. The
 *  server PTY persists across WS reconnects, so this is safe to call on the
 *  first open and never again; reopening the panel reuses the same terminal and
 *  its live connection.
 *
 *  `features` is the preset FUNCTION, not its result (ui v5): passing
 *  `presetTouch()` evaluated the preset as an argument, so a throw inside it
 *  escaped before createTerminal's failure boundary existed and the panel was
 *  left empty with only a console error. Handing over the uncalled function
 *  moves that failure inside, where the kernel reports it. */
function ensureTerminal(): void {
  if (handle !== null) {
    return;
  }
  handle = createTerminal($.shellTerminal, {
    features: presetTouch,
    layout: "container",
    wsPath: SHELL_WS_PATH,
    fontReady: SHELL_FONT_READY,
    theme: SHELL_THEME,
    // Restore the shell's scrollback from this device rather than pulling it back
    // over the wire. The server holds the PTY across a reload, but the client
    // comes back holding nothing, so a reopened panel refills its whole buffer
    // frame by frame — visible as the history filling in, and worst on the phone,
    // where iOS discards the tab and re-runs the page routinely.
    //
    // Keyed by the engine's own per-tab session id, so it matches across a reload
    // and an iOS restore and NOT across a genuinely new tab (which should get a
    // fresh terminal). A snapshot from a previous server process is DETECTED on the
    // first resume and cleared behind the loading overlay (and discarded outright
    // if that session is already gone), so a container restart cannot leave a
    // previous shell's output on screen. The store is built at module load — see
    // shellScrollback — and namespaced beside `vibekit.ui-state`.
    persistScrollback: shellScrollback,
  });
}

// --- Panel open / close ---

let shellOpen = false;

/** Open or close the shell slider.
 *
 *  Open: build the terminal on first use (opening its WebSocket), remove
 *  `shell-closed` (CSS slides the panel up), mark the toolbar button active,
 *  then focus the terminal via its handle (skipped with focus:false — the
 *  boot-time restore must not steal focus from the prompt input). Close:
 *  collapse the panel (the CSS animates height/opacity, then flips
 *  visibility); the server PTY keeps running so reopening resumes the same
 *  session. State is persisted per-device in ui-state so a reload restores
 *  it (see restoreShell). */
function setShellOpen(open: boolean, opts: { focus?: boolean } = {}): void {
  shellOpen = open;
  if (open) {
    // Capture the chat scroll-area height before the panel steals vertical
    // space, so the same content stays in view once it opens.
    const prevHeight = getScrollEl().clientHeight;
    ensureTerminal();
    $.shellPanel.classList.remove("shell-closed", "collapsed");
    $.shellBtn.classList.add("active");
    requestAnimationFrame(() => {
      if (opts.focus !== false) {
        handle?.focus();
      }
      const shrunk = prevHeight - getScrollEl().clientHeight;
      if (shrunk > 0) {
        getScrollEl().scrollTop += shrunk;
      }
    });
  } else {
    // Leave fullscreen before closing: the fullscreen rule pins height with
    // !important (un-animatable collapse), and a persisted class would make
    // the next open start fullscreen.
    $.shellPanel.classList.remove("shell-fullscreen");
    $.shellFullscreenBtn.setAttribute("aria-pressed", "false");
    // The panel usually holds focus (the terminal's hidden textarea); hiding
    // it would drop focus to <body> and restart Tab order from the document
    // top (WCAG 2.4.3). Hand focus back to the toolbar button instead.
    if ($.shellPanel.contains(document.activeElement)) {
      $.shellBtn.focus({ preventScroll: true });
    }
    $.shellPanel.classList.add("shell-closed");
    $.shellBtn.classList.remove("active");
  }
  uiState.save({ shell_open: open });
}

/**
 * Restore the shell panel on page load when ui-state had it open. Runs the full
 * open path (build terminal + CSS slide), so the WebSocket only opens when the
 * shell was actually left open — but without focusing the terminal, so boot
 * doesn't steal focus from the prompt input.
 */
export function restoreShell(): void {
  setShellOpen(true, { focus: false });
}
