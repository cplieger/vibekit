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
import { handleScreen, handleScroll, init as initRender, computeSize } from "./term-render.js";
import { init as initScroll, scrollToBottom } from "./term-scroll.js";
import { mapKeyboardEvent, bracketTextForPaste } from "./term-keyboard.js";
import { setModes } from "./term-modes.js";

const RESIZE_DEBOUNCE_MS = 100;

const shellWS = new ShellWS();
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
  initRender({ output: shellContainer, termWrap: shellContainer });
  initScroll({ scrollEl: shellContainer });

  // Keyboard input → WebSocket.
  shellContainer.setAttribute("tabindex", "0");
  shellContainer.addEventListener("keydown", onKeyDown);
  shellContainer.addEventListener("paste", onPaste);

  // Delegated click handler for terminal links. term-render.ts wraps
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

  // Resize observer → send resize control message to server.
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
      sendSeq(e.data.replace(/\u00A0/g, " "));
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
  const keyToolbar = $.keyToolbar;
  document.getElementById("kb-toggle")?.addEventListener("pointerdown", (e) => {
    e.preventDefault();
    keyToolbar.classList.toggle("collapsed");
  });

  const keyMap: Record<string, string> = {
    "kb-tab": "\t",
    "kb-esc": "\x1b",
    "kb-up": "\x1b[A",
    "kb-down": "\x1b[B",
    "kb-left": "\x1b[D",
    "kb-right": "\x1b[C",
    "kb-enter": "\r",
    "kb-ctrlc": "\x03",
  };

  for (const [id, seq] of Object.entries(keyMap)) {
    document.getElementById(id)?.addEventListener("pointerdown", (e) => {
      e.preventDefault();
      sendSeq(seq);
    });
  }

  document.getElementById("kb-scroll-bottom")?.addEventListener("pointerdown", (e) => {
    e.preventDefault();
    scrollToBottom();
  });

  // Remove no-transition class after two rAF calls (port vibecli's pattern).
  requestAnimationFrame(() =>
    requestAnimationFrame(() => {
      keyToolbar.classList.remove("no-transition");
    }),
  );

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
  shellContainer.focus();
}

function handleBinaryFrame(data: ArrayBuffer): void {
  const msg = decodeWireBinary(data);
  if (msg === null) {
    return;
  }

  switch (msg.type) {
    case "screen":
      handleScreen(msg);
      break;
    case "scroll":
      handleScroll(msg);
      break;
    case "modes": {
      const m = msg;
      setModes(m.bracketedPaste, m.applicationCursor);
      break;
    }
    case "resumeAck":
      // Resume protocol acknowledgement. Input replay not implemented
      // in vibekit (vibecli does it via predict.ts); just acknowledge.
      break;
  }
}

function sendSeq(seq: string): void {
  shellWS.sendRaw(encoder.encode(seq));
}

function onKeyDown(e: KeyboardEvent): void {
  const target = e.target as HTMLElement;
  if (target !== shellContainer && target !== $.termInput) {
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
  const { cols, rows } = computeSize();
  if (cols < 1 || rows < 1) {
    return;
  }
  shellWS.sendControl({ type: "resize", cols, rows });
}

// Suppress unused-import lint for getScrollEl (used by other modules
// that import from shell.ts for side effects).
void getScrollEl;
