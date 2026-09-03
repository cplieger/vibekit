// ---------------------------------------------------------------------------
// Shell panel: a single global server-side PTY, rendered by the standard
// @cplieger/web-terminal-ui terminal (createTerminal) over the EXISTING
// WebSocket at /api/shell/ws.
//
// The UI package owns everything above the raw terminal — the display surface,
// the hidden-textarea input model, IME/composition, predictive local echo, the
// mobile key toolbar, the right-click/long-press context menu, scroll-to-bottom,
// and the connection banner (presetSingle plus an externally-driven
// mobileToolbar — what presetTouch composes, with the key grid's trigger moved
// into this panel's header; see `keys` below). The engine
// (@cplieger/web-terminal-engine) underneath owns the wire
// protocol, the reconnect/resume reliability layer, and the render/scroll
// modules. vibekit keeps only its panel chrome (the slide-up panel, the header
// open/close/restart/full-screen buttons) and its lifecycle (device-view
// persistence, and the bounded reattach policy below).
//
// The terminal handle carries the host controls (ui v6): send(bytes) routes
// through the kernel's sanitizing funnel for the code-block "run in shell"
// button, and reattach() attaches to the PTY the server put in place of one that
// ended, which is what the header Restart button and the onSessionEnded handler
// below both drive. layout: "container" makes #shell-terminal the terminal's own
// styling/positioning boundary, so the panel needs no containment workarounds
// and the served CSS is the component-only MANIFEST.touch bundle (no page
// reset, no fonts, no @layer quarantine).
// ---------------------------------------------------------------------------

import { createTerminal, localScrollbackStorage } from "@cplieger/web-terminal-ui";
import { presetSingle } from "@cplieger/web-terminal-ui/presets/single";
import { mobileToolbar } from "@cplieger/web-terminal-ui/features/mobile-toolbar";
import type { MobileToolbarApi } from "@cplieger/web-terminal-ui/features/mobile-toolbar";
import type { TerminalFeature, TerminalHandle } from "@cplieger/web-terminal-ui";
import { $ } from "./dom.js";
import { getScrollEl } from "./messages.js";
import { setShellRunCallback } from "./code-blocks.js";
import { shellHeight, setShellHeight, setShellOpen as recordShellOpen } from "./device-view.js";
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
 *  "type in shell" action. No-op until the terminal is created (first panel
 *  open) and after teardown — `handle` is null then. */
function hostSend(bytes: Uint8Array): void {
  handle?.send(bytes);
}

// --- Reattaching after the PTY ends ---
//
// A session that ends leaves this panel dead until something reattaches, and the
// panel is the only thing that can decide to. The engine's process-exited close
// is DEFINITIVE, so it does not reconnect, and it is right not to on a
// per-session server, where reconnecting would earn the same close again.
//
// vibekit's server is the other shape. It holds ONE handler for a global PTY and
// replaces a spent one lazily, inside the next CONNECT (ShellManager.current), so
// the connection the engine declines is exactly what produces a new shell. That
// mismatch stranded the panel in both directions: the Restart button killed the
// PTY server-side and then only cleared the local screen, and typing `exit` left
// the same corpse. handle.reattach() (ui v6.1.0) fixes both, and the library owns
// the ORDER inside it — drop the local buffer, leave the ended state, then
// reconnect — so a host cannot resume against the old PTY's line-index space or
// leave the banner contradicting a blanked screen.
//
// What stays here is the POLICY, which the library deliberately does not own:
// whether a reattach can succeed at all is a fact about this server, and so is
// how many are worth making.

/** How long a session must have run for its end to count as a fresh incident
 *  rather than another failure of the spawn that replaced the last one. Above
 *  the ladder's longest step, or a storm would reset its own backoff. */
const REATTACH_STABLE_MS = 3000;
/** Consecutive reattaches before the panel gives up and leaves "Session ended"
 *  standing. A shell that dies as fast as it is spawned (a login file that
 *  exits, a missing interpreter) must not be respawned forever; four attempts
 *  across ~2s ride out a transient and stop well short of a hot loop. */
const REATTACH_MAX = 4;
/** First backoff step; each later attempt doubles it (0, 250, 500, 1000ms). */
const REATTACH_BASE_MS = 250;

let reattachAttempts = 0;
/** When the last reattach actually ran (0 = never), for the stability window. */
let reattachedAt = 0;
/** Counts reattaches so an awaiting caller can tell whether one landed while it
 *  was waiting. The restart POST and the ended close race each other, and this
 *  is what keeps the winner from being reconnected over. */
let reattachSeq = 0;
let reattachTimer: ReturnType<typeof setTimeout> | null = null;

/** Queue ONE reattach, backing off across consecutive failures. Idempotent: a
 *  second call while a reattach is already pending is the two triggers (the
 *  ended close and the restart response) naming the same incident, not two
 *  incidents.
 *
 *  The FIRST attempt runs synchronously, because by the time either trigger
 *  fires the server has already installed the replacement handler — the swap
 *  precedes the kill that closes the socket, and the restart response comes
 *  after both — so there is nothing to wait for. The delay is the backoff
 *  between repeated failures, not a settling period.
 *
 *  A user gesture always earns a fresh ladder: clicking Restart is a new
 *  decision, not the continuation of a failing respawn. */
function scheduleReattach(reason: "ended" | "restart"): void {
  // No terminal means no connection to reattach; the first panel open connects.
  if (handle === null || reattachTimer !== null) {
    return;
  }
  if (reason === "restart" || Date.now() - reattachedAt > REATTACH_STABLE_MS) {
    reattachAttempts = 0;
  }
  if (reattachAttempts >= REATTACH_MAX) {
    return;
  }
  const delay = reattachAttempts === 0 ? 0 : REATTACH_BASE_MS * 2 ** (reattachAttempts - 1);
  reattachAttempts++;
  if (delay === 0) {
    reattachShell();
    return;
  }
  reattachTimer = setTimeout(() => {
    reattachTimer = null;
    reattachShell();
  }, delay);
}

/** Attach to the PTY the server now serves. */
function reattachShell(): void {
  reattachedAt = Date.now();
  reattachSeq++;
  handle?.reattach();
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
 *  The server's kill closes this client's socket, so onSessionEnded usually
 *  reattaches before this call returns; the seq check is what stops a second,
 *  redundant reattach from landing on the prompt the first one drew. The
 *  schedule is still made when nothing moved, so the panel does not depend on a
 *  close frame arriving to come back. */
async function hostRestart(): Promise<void> {
  const ok = await confirmDialog(
    "Restart the shell? Anything running in it is killed.",
    "Restart",
    "normal",
  );
  if (!ok) {
    return;
  }
  const seq = reattachSeq;
  if ((await restartShell.dispatch()) === null) {
    return; // the action framework toasts the failure
  }
  if (reattachSeq === seq) {
    scheduleReattach("restart");
  }
}

let handle: TerminalHandle | null = null;
let initialized = false;

// The on-screen key grid (Tab/Esc/arrows/Enter/sticky-Ctrl). `externalToggle`
// hides the grid's own toggle so a peer can open it; presetTouch leaves it off,
// which parked a 54px pill over the terminal's top-right corner on every
// coarse-pointer device, iPad desktop mode included, while this panel's own
// control row sat 8px above it. Placement is 21-shell-panel.css's, since the
// library's copy ships in a CSS file the touch bundle does not carry. Held as
// the FEATURE rather than its api: the kernel writes `feature.api` after setup,
// and minting it in the thunk keeps a throw inside createTerminal's boundary.
let keys: TerminalFeature<MobileToolbarApi> | null = null;

/** Show or hide the key grid, and write the trigger's pressed state. ONE writer,
 *  because a face left behind is the whole failure mode of a toggle whose panel
 *  lives somewhere else. */
function toggleKeys(): void {
  const api = keys?.api;
  if (api === undefined) {
    return; // no terminal yet: the panel's first open builds it
  }
  api.toggle();
  $.shellKeysBtn.setAttribute("aria-pressed", api.isOpen() ? "true" : "false");
}

/** The toggle's two faces, both Lucide (`maximize` / `minimize`) flattened into
 *  one path: four corners pointing OUT to enter full screen, the same four
 *  pointing IN to leave. Rotating the glyph is not the same icon — the corner
 *  set is rotationally symmetric, so only the arc sweep distinguishes them.
 *  index.html paints the enter face so the button is not blank before boot;
 *  from then on setFullscreenBtnState owns it. */
const FS_ICON_ENTER =
  "M8 3H5a2 2 0 0 0-2 2v3M21 8V5a2 2 0 0 0-2-2h-3M3 16v3a2 2 0 0 0 2 2h3M16 21h3a2 2 0 0 0 2-2v-3";
const FS_ICON_LEAVE =
  "M8 3v3a2 2 0 0 1-2 2H3M21 8h-3a2 2 0 0 1-2-2V3M3 16h3a2 2 0 0 1 2 2v3M16 21v-3a2 2 0 0 1 2-2h3";

/** Write every part of the toggle that depends on whether the panel is full
 *  screen: the pressed flag, the label, and the glyph. ONE writer, because
 *  three sites change that state (boot, the click, the close path) and a face
 *  left behind by one of them is the bug this exists to prevent — the button
 *  offered "expand" while the panel was already expanded, so the only cue that
 *  clicking again would shrink it was aria-pressed, which nothing renders.
 *
 *  The label names what the click DOES rather than the state it is in, matching
 *  the editor's diff toggle and the turn's raw-source toggle. */
function setFullscreenBtnState(on: boolean): void {
  const fsBtn = $.shellFullscreenBtn;
  fsBtn.setAttribute("aria-pressed", on ? "true" : "false");
  const label = on ? "Exit full screen" : "Full screen";
  fsBtn.setAttribute("data-tooltip", label);
  fsBtn.setAttribute("aria-label", label);
  // Optional chained: the panel's glyph is markup this module does not build,
  // and a missing one is no reason to drop the rest of the state.
  fsBtn.querySelector("path")?.setAttribute("d", on ? FS_ICON_LEAVE : FS_ICON_ENTER);
}

/** Wire the panel's full-screen toggle. aria-pressed mirrors the panel class so
 *  the button reads as a toggle; setShellOpen resets both when the panel closes.
 *
 *  This lived in agent-terminal.ts until that module was deleted, which was
 *  always the wrong home: it is the shell panel's own control and has nothing to
 *  do with agent output. */
function wireFullscreenToggle(): void {
  const fsBtn = $.shellFullscreenBtn;
  setFullscreenBtnState(false);
  fsBtn.addEventListener("click", () => {
    const panel = $.shellPanel;
    if (!panel.classList.contains("shell-fullscreen")) {
      panel.classList.add("shell-fullscreen");
      setFullscreenBtnState(true);
      return;
    }
    // Leaving: the panel's geometry has to SNAP back (its fullscreen height is
    // an `!important` 100vh that cannot be tweened to a docked height without
    // replaying the old grow-downwards motion), so the exit is a fade held by
    // a transient class. Without it the class drop is instantaneous and there
    // is nothing to animate — CSS cannot transition an element out of a state
    // it has already left.
    panel.classList.add("shell-fullscreen-leaving");
    setFullscreenBtnState(false);
    const done = (): void => {
      panel.classList.remove("shell-fullscreen", "shell-fullscreen-leaving");
    };
    panel.addEventListener("animationend", done, { once: true });
    // Belt and braces: reduced-motion zeroes the animation, and a suppressed
    // renderer may never fire animationend, which would strand the panel
    // fullscreen with the button already reading "off".
    setTimeout(done, 250);
  });
}

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
  // Key-toolbar button drives the grid the library no longer draws a toggle for.
  $.shellKeysBtn.addEventListener("click", () => {
    toggleKeys();
  });

  wireFullscreenToggle();

  // Code-block "type in shell" → the command lands at the prompt and WAITS.
  // No trailing newline: pressing Enter is the confirmation, and until then the
  // line is editable in place like anything else the user typed. handle.send
  // snaps the view to the bottom, so the typed command is visible.
  setShellRunCallback((cmd: string) => {
    hostSend(encoder.encode(cmd));
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
 * device-view (shell_h; 0 = CSS default). The handle is also a keyboard
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
    setShellHeight(lastH);
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
    setShellHeight(next);
  });

  // Restore the persisted height (re-clamped: the viewport may have changed
  // since it was saved). Harmless while closed — the closed state pins
  // height 0; the value takes effect on open.
  const saved = shellHeight();
  if (saved > 0) {
    applyShellH(saved);
  }
}

/** Build the terminal exactly once, into the (empty) #shell-terminal root. The
 *  server PTY persists across WS reconnects, so this is safe to call on the
 *  first open and never again; reopening the panel reuses the same terminal and
 *  its live connection.
 *
 *  `features` is a THUNK, not a built array (ui v5): an argument expression is
 *  evaluated before createTerminal is entered, so a throw while composing the
 *  list escaped its failure boundary and left the panel empty with only a
 *  console error. Building the list inside moves that failure where the kernel
 *  reports it — which is also what lets the key-grid feature be minted here. */
function ensureTerminal(): void {
  if (handle !== null) {
    return;
  }
  handle = createTerminal($.shellTerminal, {
    features: () => {
      keys = mobileToolbar({ externalToggle: true });
      return [...presetSingle(), keys];
    },
    layout: "container",
    wsPath: SHELL_WS_PATH,
    fontReady: SHELL_FONT_READY,
    theme: SHELL_THEME,
    // The shell died and nothing is retrying. On this server that is recoverable
    // (the next connect gets a fresh PTY), so typing `exit` or wedging a child
    // ends with a new prompt rather than a dead panel. The ladder is ours; the
    // library reports the end and refuses to guess a policy from it.
    onSessionEnded: () => {
      scheduleReattach("ended");
    },
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
 *  session. State is persisted per-device in device-view so a reload restores
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
    setFullscreenBtnState(false);
    // The panel usually holds focus (the terminal's hidden textarea); hiding
    // it would drop focus to <body> and restart Tab order from the document
    // top (WCAG 2.4.3). Hand focus back to the toolbar button instead.
    if ($.shellPanel.contains(document.activeElement)) {
      $.shellBtn.focus({ preventScroll: true });
    }
    $.shellPanel.classList.add("shell-closed");
    $.shellBtn.classList.remove("active");
  }
  recordShellOpen(open);
}

/**
 * Restore the shell panel on page load when this device left it open. Runs the full
 * open path (build terminal + CSS slide), so the WebSocket only opens when the
 * shell was actually left open — but without focusing the terminal, so boot
 * doesn't steal focus from the prompt input.
 */
export function restoreShell(): void {
  setShellOpen(true, { focus: false });
}
