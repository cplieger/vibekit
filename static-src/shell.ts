// ---------------------------------------------------------------------------
// Shell panel: server-side VT terminal connected via WebSocket.
//
// The engine is the shared @cplieger/web-terminal-engine library. The Go server
// (internal/hub/shell.go → web-terminal-engine/terminal) maintains a VT500 screen
// buffer and sends only changed rows as compact binary frames; the
// browser renders a DOM cell grid via the engine's render module. The
// client → server socket lifecycle (reconnect backoff + resume/inputAck
// reliability) lives in the engine's connection module — this file only wires
// vibekit-specific UI (sticky-Ctrl, mobile key toolbar, tap-to-focus).
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { getScrollEl } from "./messages.js";
import { setShellRunCallback } from "./code-blocks.js";
import * as uiState from "./ui-state.js";
import { render, keyboard, scroll, connection, modes } from "@cplieger/web-terminal-engine";
import type { ServerMessage } from "@cplieger/web-terminal-engine";

const { mapKeyboardEvent, bracketTextForPaste } = keyboard;

const RESIZE_DEBOUNCE_MS = 100;
const SHELL_WS_PATH = "/api/shell/ws";

const encoder = new TextEncoder();
let resizeTimer: ReturnType<typeof setTimeout> | null = null;
let shellContainer: HTMLElement;
let initialized = false;

/**
 * Initialize the shell panel. Called once from app.ts on boot.
 */
export function initShellPanel(): void {
  if (initialized) {
    return;
  }
  initialized = true;

  shellContainer = $.shellTerminal;

  // Initialize the DOM renderer + scroll tracking.
  render.init({ output: shellContainer, termWrap: shellContainer });
  render.updateFontMetrics();
  scroll.init({ scrollEl: shellContainer });

  // Keyboard input → WebSocket.
  shellContainer.setAttribute("tabindex", "0");
  shellContainer.addEventListener("keydown", onKeyDown);
  shellContainer.addEventListener("paste", onPaste);

  // Delegated click handler for terminal links. vterm's renderer wraps
  // detected URLs in <a class="term-link" target="_blank"> but in some
  // contexts (contenteditable, focus-capturing containers) the browser
  // suppresses default link navigation. This handler ensures clicks on
  // .term-link always open in a new tab.
  shellContainer.addEventListener("click", (e) => {
    const link = (e.target as HTMLElement).closest<HTMLAnchorElement>(".term-link");
    if (link) {
      e.preventDefault();
      window.open(link.href, "_blank", "noopener,noreferrer");
    }
  });

  // Resize observer → re-measure font metrics and send a resize control
  // message to the server.
  const ro = new ResizeObserver(() => {
    if (resizeTimer !== null) {
      clearTimeout(resizeTimer);
    }
    resizeTimer = setTimeout(sendResize, RESIZE_DEBOUNCE_MS);
  });
  ro.observe(shellContainer);

  // --- Mobile soft-keyboard textarea ---
  const termInput = $.termInput;
  termInput.addEventListener("keydown", onKeyDown);
  termInput.addEventListener("paste", onPaste);
  termInput.addEventListener("beforeinput", (e: InputEvent) => {
    if (e.inputType === "insertText" && e.data) {
      e.preventDefault();
      sendSeq(toolbarCtrl.applyStickyCtrl(e.data.replace(/\u00A0/g, " ")));
    }
  });

  // Tap-to-focus: open soft keyboard on touch tap.
  let pointerDownX = 0;
  let pointerDownY = 0;
  const TAP_MOVEMENT_PX = 10;
  shellContainer.addEventListener(
    "pointerdown",
    (e: PointerEvent) => {
      pointerDownX = e.clientX;
      pointerDownY = e.clientY;
    },
    { passive: true },
  );
  shellContainer.addEventListener(
    "pointerup",
    (e: PointerEvent) => {
      if (e.pointerType !== "touch") {
        return;
      }
      const dx = Math.abs(e.clientX - pointerDownX);
      const dy = Math.abs(e.clientY - pointerDownY);
      if (dx > TAP_MOVEMENT_PX || dy > TAP_MOVEMENT_PX) {
        return;
      }
      termInput.focus({ preventScroll: true });
    },
    { passive: true },
  );

  // --- Mobile key toolbar ---
  // The engine's keyboard.bindMobileToolbar wires the collapse toggle, the
  // arrows (DECCKM-aware — SS3 under application-cursor mode), Tab/Enter/Esc,
  // and the sticky-Ctrl button on the shared #key-toolbar; toolbarCtrl owns
  // the one-shot sticky-Ctrl state the beforeinput handler reads through
  // applyStickyCtrl. The scroll-to-bottom button and no-transition priming
  // are vibekit-specific and stay local.
  const keyToolbar = $.keyToolbar;
  toolbarCtrl = keyboard.bindMobileToolbar({ toolbar: keyToolbar, send: sendSeq });

  document.getElementById("kb-scroll-bottom")?.addEventListener("pointerdown", (e) => {
    e.preventDefault();
    scroll.scrollToBottom();
  });

  // Remove no-transition class after two rAF calls (port vibecli's pattern).
  requestAnimationFrame(() =>
    requestAnimationFrame(() => {
      keyToolbar.classList.remove("no-transition");
    }),
  );

  // Wire the "run in shell" callback for code blocks.
  setShellRunCallback((cmd: string) => {
    connection.sendBinary(encoder.encode(`${cmd}\n`));
  });

  // Panel open/close + header controls. The toolbar shell button, the
  // header close (X), clear, and kill buttons were all wired in the
  // pre-engine shell.ts and dropped in the web-terminal-engine migration —
  // without these the shell button "did nothing" (never opened the slider).
  $.shellBtn.addEventListener("click", () => {
    setShellOpen(!shellOpen);
  });
  $.shellToggleBtn.addEventListener("click", () => {
    setShellOpen(false);
  });
  // Clear scrollback: reset the engine's local screen + scrollback store.
  $.shellClearBtn.addEventListener("click", () => {
    render.resetScrollback();
  });
  // The engine exposes no dedicated "kill" control message, so send Ctrl+C
  // (SIGINT) — the terminal-native way to interrupt the foreground process.
  $.shellKillBtn.addEventListener("click", () => {
    connection.sendBinary(encoder.encode("\x03"));
  });

  // Set up the vterm connection (client → server socket lifecycle).
  connection.init({
    wsPath: SHELL_WS_PATH,
    computeSize: render.computeSize,
    // Resume by absolute line index: on reconnect (iOS sleep/wake, network
    // blip) the server replays only the rows printed while the tab was away,
    // backfilled by absolute line index with no duplicates, instead of a full
    // retained replay (-1).
    getHaveThrough: render.getHighestIndex,
    onResumeBounds: render.noteResumeBounds,
    onMessage(msg: ServerMessage) {
      switch (msg.type) {
        case "screen":
          render.handleScreen(msg);
          break;
        case "scroll":
          render.handleScroll(msg);
          break;
        case "modes":
          // The connection module already applied the mode state via
          // modes.setModes; reflect reverse-video (DECSCNM) into the DOM.
          render.updateReverseVideo();
          break;
        // "title"/"resumeAck" are handled inside the connection module
        // (or intentionally ignored — vibekit owns its own document.title).
      }
    },
    onOpen() {
      sendResize();
    },
    onClose() {
      // The connection module handles reconnect internally.
    },
    onConnecting() {
      // Could show a "reconnecting..." indicator.
    },
  });

  // Reconnect promptly when the tab returns from background (iOS sleep,
  // bfcache restore). The server PTY persists across WS drops and
  // replays screen + scrollback on resume.
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible" && initialized) {
      connection.reconnectNow();
    }
  });
  window.addEventListener("pageshow", () => {
    connection.reconnectNow();
  });
}

// --- Panel open / close ---

let shellOpen = false;

/** Open or close the shell slider.
 *
 *  Open: remove `shell-closed` (CSS slides the panel up), mark the toolbar
 *  button active, connect the WebSocket (the engine's connection module is
 *  idempotent and the server PTY persists across reconnects), then re-measure
 *  and focus. Close: hide the panel; the PTY keeps running server-side so
 *  reopening resumes the same session. State is persisted per-device in
 *  ui-state so a reload restores it (see restoreShell). */
function setShellOpen(open: boolean): void {
  shellOpen = open;
  if (open) {
    // Capture the chat scroll-area height before the panel steals vertical
    // space, so the same content stays in view once it opens.
    const prevHeight = getScrollEl().clientHeight;
    $.shellPanel.classList.remove("shell-closed", "collapsed");
    $.shellBtn.classList.add("active");
    connection.connect();
    requestAnimationFrame(() => {
      sendResize();
      shellContainer.focus({ preventScroll: true });
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
 * Restore the shell panel on page load when ui-state had it open. Runs the
 * full open path (CSS + connect + focus), not just a bare connect — the
 * earlier version only connected the socket and left the panel hidden.
 */
export function restoreShell(): void {
  setShellOpen(true);
}

// --- Sticky Ctrl modifier ---
// The iOS virtual keyboard has no Ctrl key, so control sequences
// (Ctrl+C, Ctrl+L = clear screen, Ctrl+X, ...) are otherwise unreachable
// on touch. The engine's keyboard.bindMobileToolbar (wired in
// initShellPanel) owns the one-shot sticky-Ctrl state machine and the
// Ctrl+<char> → C0 mapping. #key-toolbar is part of the shared scaffold and
// $.keyToolbar throws if it is missing, so the controller is always bound
// before any input fires; the beforeinput handler reads applyStickyCtrl
// straight off it.
let toolbarCtrl!: keyboard.MobileToolbarController;

function sendSeq(seq: string): void {
  connection.sendBinary(encoder.encode(seq));
}

function onKeyDown(e: KeyboardEvent): void {
  const target = e.target as HTMLElement;
  if (target !== shellContainer && target !== $.termInput) {
    return;
  }
  // v2's mapKeyboardEvent takes the active session's DECCKM/DECKPAM state
  // explicitly rather than reading it off module-global mutation; vibekit's
  // shell panel is a single global PTY session, so the engine's own `modes`
  // namespace (which structurally satisfies KeyboardModes) is the right arg.
  const result = mapKeyboardEvent(e, modes);
  if (result.kind === "send") {
    e.preventDefault();
    connection.sendBinary(encoder.encode(result.bytes));
  }
  // "scroll-up"/"scroll-down"/"ignore" — let the browser handle or ignore.
}

function onPaste(e: ClipboardEvent): void {
  e.preventDefault();
  const text = e.clipboardData?.getData("text");
  if (text) {
    const prepared = bracketTextForPaste(text);
    connection.sendBinary(encoder.encode(prepared));
  }
}

function sendResize(): void {
  render.updateFontMetrics();
  const { cols, rows } = render.computeSize();
  if (cols < 1 || rows < 1) {
    return;
  }
  connection.sendResize();
}
