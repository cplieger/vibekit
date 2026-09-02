// ---------------------------------------------------------------------------
// Status indicators, context bar, input disable state.
//
// Send-button state lives in prompt-input.ts since it's part of the input
// affordance (busy=cancel, idle=send). Agent lifecycle is expressed via
// the send button and the per-message streaming cursor — no separate
// "thinking" indicator.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { formatTokens, formatMetering } from "./status-format.js";
import { humanName } from "./strings.js";
import { checkRuntimeHealth, runtimeStatusLine } from "./runtime-health.js";
import { versionsSignal } from "./versions.js";
import { el, effect } from "@cplieger/reactive";
import { announce } from "@cplieger/ui-primitives/announce";
import type { MeteringItem, ConnectionStatus } from "./types.js";

// --- Context bar controller ---

/** Options for updateContextBar — named fields prevent argument-order bugs. */
interface ContextBarUpdate {
  pct: number;
  contextSize: number;
  credits: number;
  turnCount: number;
  lastTurnMs: number;
  model: string;
  /** The reasoning tier to name beside the model, or "" for the ordinary case.
   *  Already resolved by context-ui.ts: empty means either the chat runs at the
   *  model's own default or nothing knows what the default is. This module
   *  renders it and decides nothing about it. */
  effort?: string;
  metering?: MeteringItem[];
  msgCount?: number;
  toolCount?: number;
  summarizedCount?: number;
}

class ContextBarController {
  private contextBarQueued = false;
  private contextBarArgs: ContextBarUpdate | null = null;

  update(opts: ContextBarUpdate): void {
    this.contextBarArgs = opts;
    if (!this.contextBarQueued) {
      this.contextBarQueued = true;
      requestAnimationFrame(() => {
        this.contextBarQueued = false;
        if (this.contextBarArgs !== null) {
          this.updateImpl(this.contextBarArgs);
        }
      });
    }
  }

  private updateImpl(opts: ContextBarUpdate): void {
    const { pct, contextSize, credits, turnCount, lastTurnMs, model } = opts;
    const metering = opts.metering ?? [];
    const msgCount = opts.msgCount ?? 0;
    const toolCount = opts.toolCount ?? 0;
    const summarizedCount = opts.summarizedCount ?? 0;
    const clamped = Math.min(100, Math.max(0, pct));

    // pathLength="100" on the element makes the dash pattern speak in percent,
    // so the offset IS the unused remainder. The hardcoded 50.27 circumference
    // this replaced was the ring's one magic constant.
    $.contextRingFill.style.strokeDashoffset = String(100 - clamped);
    const stroke =
      clamped >= 90 ? "var(--c-red)" : clamped >= 70 ? "var(--c-yellow)" : "var(--c-green)";
    $.contextRingFill.style.stroke = stroke;
    $.contextLabel.textContent = `${pct.toFixed(0)}%`;

    $.switchModelBtn.setAttribute("data-tooltip", "Switch model");

    // Empty model = server-side default; label it "auto" rather than blank.
    const modelLabel = model === "" ? "auto" : humanName(model);
    $.ctxModelPill.textContent = modelLabel;

    // The tier rides its OWN element, not the model label, for two reasons. The
    // label is capped at 10rem with an ellipsis, so a concatenated tier is the
    // half that gets clipped — and the tier is the exceptional information here,
    // present only when the chat departs from the model's default. Hidden with
    // the `.hidden` utility rather than emptied: `.pill` is a flex row with a
    // gap, so an empty span would still pad the pill.
    const effort = opts.effort ?? "";
    $.ctxEffortPill.textContent = effort === "" ? "" : `· ${effort}`;
    $.ctxEffortPill.classList.toggle("hidden", effort === "");
    // The button's aria-label wins over its own text, so the current selection
    // reaches assistive tech only from here. Spelled in words rather than with
    // the separator, which a screen reader reads out.
    $.switchModelBtn.setAttribute(
      "aria-label",
      effort === ""
        ? `Switch model, currently ${modelLabel}`
        : `Switch model, currently ${modelLabel} at ${effort} reasoning effort`,
    );
    $.ctxTokens.textContent =
      contextSize > 0
        ? `${formatTokens(Math.round((contextSize * pct) / 100))} / ${formatTokens(contextSize)}`
        : `${pct.toFixed(1)}%`;
    $.ctxCredits.textContent = credits > 0 ? `${credits.toFixed(2)} cr` : "0.00 cr";
    $.ctxTurns.textContent = String(turnCount);
    $.ctxLastTurn.textContent = lastTurnMs > 0 ? `${(lastTurnMs / 1000).toFixed(1)}s` : "-";
    $.ctxMsgs.textContent =
      summarizedCount > 0
        ? `${String(msgCount)} (${String(summarizedCount)} summarized)`
        : String(msgCount);
    $.ctxTools.textContent = String(toolCount);

    renderMetering(metering);
  }
}

const contextBar = new ContextBarController();

function renderMetering(items: MeteringItem[]): void {
  const box = $.ctxMetering;
  if (items.length <= 1) {
    box.classList.add("hidden");
    box.replaceChildren();
    return;
  }
  box.classList.remove("hidden");
  const rows: HTMLElement[] = items.map((item) =>
    el(
      "span",
      { className: "pill-metering-row" },
      el(
        "span",
        { className: "pill-metering-label" },
        item.value === 1 ? item.unit_singular : item.unit_plural,
      ),
      el("span", { className: "pill-metering-value" }, formatMetering(item.value)),
    ),
  );
  box.replaceChildren(...rows);
}

// --- Public API (delegate functions) ---

// --- Connection status announcement debounce ---
// Prevents rapid connecting→disconnected→connecting cycles from spamming
// screen readers. Only announces after status is stable for 2s; the actual
// announcement rides the shared @cplieger/ui-primitives announce() live
// region (no more per-module sr-only element). announce() re-announces an
// identical repeated message — after a flappy reconnect that resettles on the
// same status, the repeat is the honest signal.
let statusAnnounceTimer: ReturnType<typeof setTimeout> | undefined;

/** Maps each ConnectionStatus to its CSS class and custom property color. */
const STATUS_STYLES: Readonly<Record<ConnectionStatus, { cls: string | null; color: string }>> = {
  connected: { cls: "connected", color: "var(--c-green)" },
  disconnected: { cls: "error", color: "var(--c-red)" },
  connecting: { cls: null, color: "var(--c-yellow)" },
};

export function setStatus(s: ConnectionStatus): void {
  const dot = $.statusDot;
  dot.classList.remove("connected", "error");
  const style = STATUS_STYLES[s];
  if (style.cls) {
    dot.classList.add(style.cls);
  }
  // The expanded card tints its border and background from --status-color. It
  // is the dot's sibling, not its child (15-input.css .pill-slot), so the
  // value lands on the card: on the dot it would reach nothing.
  $.statusCard.style.setProperty("--status-color", style.color);
  dot.setAttribute("aria-label", `Connection: ${s}`);
  lastStatus = s;
  paintConnectionLine();

  // Debounced screen-reader announcement (2s stable).
  clearTimeout(statusAnnounceTimer);
  statusAnnounceTimer = setTimeout(() => {
    announce(`Connection ${s}`);
  }, 2000);
}

/** The status the transport last reported, held so the version arriving later can
 *  repaint the line without a second status change. */
let lastStatus: ConnectionStatus = "connecting";

/** The connection line: the status, plus WHICH server, once its version is known.
 *
 *  The version rides the CONNECTED line only. `connecting` and `disconnected`
 *  describe this page's socket rather than the server behind it, and naming a
 *  build beside "disconnected" would claim knowledge of something the page has
 *  just lost contact with — while the value is a fact about the container it
 *  reached, so it belongs exactly where the line says it reached one. */
function paintConnectionLine(): void {
  const build = versionsSignal().value.vibekit;
  $.stWs.textContent =
    lastStatus === "connected" && build !== "" ? `connected to vibekit ${build}` : lastStatus;
}

/** Repaint both card lines when the version pair lands.
 *
 *  An effect rather than a callback from the loader: the pair arrives once, well
 *  after the card is built and after the transport's first `connected`, and both
 *  lines are derived from it plus state this module already holds.
 *
 *  Registered from the composition root, NOT at module scope: an effect runs its
 *  body once on registration, and this one reads `$.stWs`, which throws when the
 *  element is absent. At import time that is every test of an unrelated export in
 *  this module, and in production it would make a markup change a module-load
 *  failure rather than a missing line. */
export function initStatusVersions(): void {
  effect(() => {
    // Read INSIDE the effect so it subscribes; paintConnectionLine reads it again
    // for its own value. `void` rather than a bare expression, matching
    // forge-auth.ts: the read is the whole point and the value is not wanted.
    void versionsSignal().value;
    paintConnectionLine();
    $.stKiro.textContent = runtimeStatusLine();
  });
}

export function updateContextBar(opts: ContextBarUpdate): void {
  contextBar.update(opts);
}

/** Paint the status card's agent-runtime line, then re-probe so an open card is
 *  never as stale as the last transport gap (the boot probe and the gap probe
 *  are the only other times /api/health is read). Painted twice on purpose: the
 *  cached line lands in the same frame the card opens in, and the fresh one
 *  replaces it when the probe answers. The probe also reconciles the global
 *  degraded banner, which is wanted — opening the status surface is exactly
 *  when a stale banner should clear and a real one should appear.
 *  `runtime-health.ts` owns the reason vocabulary; this only renders its line.
 *  Called on popup expand.
 *
 *  Returns the probe's promise so a caller can wait for the SECOND paint. The
 *  app's caller does not (it opens a popup and has nothing to sequence), but a
 *  discarded promise is what makes the two-paint behaviour untestable. */
export function refreshRuntimeLine(): Promise<void> {
  $.stKiro.textContent = runtimeStatusLine();
  return checkRuntimeHealth().then(() => {
    $.stKiro.textContent = runtimeStatusLine();
  });
}

// The send button's face (and the "context nearly full" placeholder) has a
// single owner: prompt-input.ts, which reads the `contextFull` signal written by
// context-ui.ts. status.ts used to write the `disabled` DOM props too
// (setInputDisabled), which fought prompt-input's send-state machine on every
// turn boundary — last-writer-wins left the state unreliable. That second writer
// is gone, and so is the disable itself: nothing may lock the composer (see
// prompt-input.ts's header).
