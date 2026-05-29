// ---------------------------------------------------------------------------
// Shell panel: server-side VT terminal connected via WebSocket.
//
// Replaces the previous xterm.js-based implementation. The server now
// maintains a VT500 screen buffer (internal/vt) and sends only changed
// rows as compact binary frames. The browser renders a simple DOM-based
// cell grid (term-render.ts) — no xterm.js dependency, ~8 MB lighter.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { getScrollEl } from "./messages.js";
import { setShellRunCallback } from "./code-blocks.js";
import { ShellWS, encoder } from "./shell-ws.js";
import { decodeWireBinary } from "./term-wire-binary.js";
import {
  handleScreen,
  handleScroll,
  init as initRender,
  computeSize,
} from "./term-render.js";
import { init as initScroll, scrollToBottom } from "./term-scroll.js";
import { mapKeyboardEvent, bracketTextForPaste } from "./term-keyboard.js";
import { setModes } from "./term-modes.js";
import type { ScreenMessage, ScrollMessage, ModesMessage } from "./term-types.js";

const RESIZE_DEBOUNCE_MS = 100;

const shellWS = new ShellWS();
let resizeTimer: ReturnType<typeof setTimeout> | null = null;
let shellContainer: HTMLElement | null = null;
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
  if (!shellContainer) {
    return;
  }

  // Initialize the DOM renderer + scroll tracking.
  initRender({ output: shellContainer, termWrap: shellContainer });
  initScroll({ scrollEl: shellContainer });

  // Keyboard input → WebSocket.
  shellContainer.setAttribute("tabindex", "0");
  shellContainer.addEventListener("keydown", onKeyDown);
  shellContainer.addEventListener("paste", onPaste);

  // Resize observer → send resize control message to server.
  const ro = new ResizeObserver(() => {
    if (resizeTimer !== null) {
      clearTimeout(resizeTimer);
    }
    resizeTimer = setTimeout(sendResize, RESIZE_DEBOUNCE_MS);
  });
  ro.observe(shellContainer);

  // Wire the "run in shell" callback for code blocks.
  setShellRunCallback((cmd: string) => {
    shellWS.sendRaw(encoder.encode(`${cmd}\n`));
  });

  // Set up WebSocket callbacks.
  shellWS.setCallbacks({
    onMessage(data: ArrayBuffer | string) {
      if (typeof data === "string") {
        return; // We only expect binary frames.
      }
      handleBinaryFrame(data);
    },
    onOpen() {
      sendResize();
    },
    onClose() {
      // ShellWS handles reconnect internally.
    },
    onReconnecting() {
      // Could show a "reconnecting..." indicator.
    },
  });

  // Connect on first show (the shell tab activation will call
  // restoreShell which sets active + connects).
}

/**
 * Restore the shell panel when the shell tab becomes visible.
 * Activates the WS connection and focuses the container.
 */
export function restoreShell(): void {
  shellWS.setActive(true);
  void shellWS.connect();
  if (shellContainer !== null) {
    shellContainer.focus();
  }
}

function handleBinaryFrame(data: ArrayBuffer): void {
  const msg = decodeWireBinary(data);
  if (msg === null) {
    return;
  }

  switch (msg.type) {
    case "screen":
      handleScreen(msg as ScreenMessage);
      scrollToBottom();
      break;
    case "scroll":
      handleScroll(msg as ScrollMessage);
      break;
    case "modes": {
      const m = msg as ModesMessage;
      setModes(m.bracketedPaste, m.applicationCursor);
      break;
    }
    case "resumeAck":
      // Resume protocol acknowledgement. Input replay not implemented
      // in vibekit (vibecli does it via predict.ts); just acknowledge.
      break;
  }
}

function onKeyDown(e: KeyboardEvent): void {
  if (e.target !== shellContainer) {
    return;
  }
  const result = mapKeyboardEvent(e);
  if (result.kind === "send") {
    e.preventDefault();
    shellWS.sendRaw(encoder.encode(result.bytes));
  }
  // "scroll-up"/"scroll-down"/"ignore" — let the browser handle or ignore.
}

function onPaste(e: ClipboardEvent): void {
  e.preventDefault();
  const text = e.clipboardData?.getData("text");
  if (text) {
    const prepared = bracketTextForPaste(text);
    shellWS.sendRaw(encoder.encode(prepared));
  }
}

function sendResize(): void {
  if (shellContainer === null) {
    return;
  }
  const { cols, rows } = computeSize();
  if (cols < 1 || rows < 1) {
    return;
  }
  shellWS.sendControl({ type: "resize", cols, rows });
}

// Suppress unused-import lint for getScrollEl (used by other modules
// that import from shell.ts for side effects).
void getScrollEl;
