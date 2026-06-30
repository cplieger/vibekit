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
import { render, keyboard, scroll, connection } from "@cplieger/web-terminal-engine";
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

/**
 * Restore the shell panel when the shell tab becomes visible.
 * Activates the WS connection and focuses the container.
 */
export function restoreShell(): void {
  connection.connect();
  shellContainer.focus();
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
  const result = mapKeyboardEvent(e);
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

// Suppress unused-import lint for getScrollEl (used by other modules
// that import from shell.ts for side effects).
void getScrollEl;
