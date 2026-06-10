// ---------------------------------------------------------------------------
// Shell panel: server-side VT terminal connected via WebSocket.
//
// The engine is the shared @cplieger/vterm library. The Go server
// (internal/hub/shell.go → vterm/terminal) maintains a VT500 screen
// buffer and sends only changed rows as compact binary frames; the
// browser renders a DOM cell grid via vterm's render module. The
// client → server socket lifecycle (reconnect backoff + resume/inputAck
// reliability) lives in vterm's connection module — this file only wires
// vibekit-specific UI (sticky-Ctrl, mobile key toolbar, tap-to-focus).
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { getScrollEl } from "./messages.js";
import { setShellRunCallback } from "./code-blocks.js";
import { render, keyboard, scroll, connection } from "@cplieger/vterm";
import type { ServerMessage } from "@cplieger/vterm";

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
      sendSeq(applyStickyCtrl(e.data.replace(/\u00A0/g, " ")));
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
  };

  for (const [id, seq] of Object.entries(keyMap)) {
    document.getElementById(id)?.addEventListener("pointerdown", (e) => {
      e.preventDefault();
      setCtrlArmed(false);
      sendSeq(seq);
    });
  }

  // Sticky Ctrl: tap to arm/disarm. preventDefault keeps focus on the
  // terminal so the iOS virtual keyboard stays up for the next tap.
  ctrlBtn = document.getElementById("kb-ctrl");
  ctrlBtn?.addEventListener("pointerdown", (e) => {
    e.preventDefault();
    setCtrlArmed(!ctrlArmed);
  });

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
// on touch. The toolbar's Ctrl button arms a one-shot modifier: tap it,
// then tap a letter on the virtual keyboard and that keystroke is sent
// as its C0 control byte. Auto-disarms after one printable character.
let ctrlArmed = false;
let ctrlBtn: HTMLElement | null = null;

function setCtrlArmed(on: boolean): void {
  ctrlArmed = on;
  ctrlBtn?.classList.toggle("armed", on);
  ctrlBtn?.setAttribute("aria-pressed", on ? "true" : "false");
}

// Map one printable character to its Ctrl+<char> C0 control byte.
function ctrlByteFor(ch: string): string | null {
  const code = ch.toLowerCase().charCodeAt(0);
  if (code >= 97 && code <= 122) {
    return String.fromCharCode(code - 96); // a–z → 0x01–0x1a
  }
  switch (ch) {
    case " ":
    case "@":
      return "\x00";
    case "[":
      return "\x1b";
    case "\\":
      return "\x1c";
    case "]":
      return "\x1d";
    case "^":
      return "\x1e";
    case "_":
      return "\x1f";
    case "?":
      return "\x7f";
    default:
      return null;
  }
}

// One-shot armed Ctrl: a single printable char becomes its control byte;
// longer input (paste) just disarms and passes through unchanged.
function applyStickyCtrl(data: string): string {
  if (!ctrlArmed) {
    return data;
  }
  setCtrlArmed(false);
  if (data.length === 1) {
    return ctrlByteFor(data) ?? data;
  }
  return data;
}

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
