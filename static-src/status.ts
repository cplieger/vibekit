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
import { el } from "@cplieger/reactive";
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
    $.ctxModelPill.textContent = model === "" ? "auto" : humanName(model);
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
  $.stWs.textContent = s;

  // Debounced screen-reader announcement (2s stable).
  clearTimeout(statusAnnounceTimer);
  statusAnnounceTimer = setTimeout(() => {
    announce(`Connection ${s}`);
  }, 2000);
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
 *  Called on popup expand. */
export function refreshRuntimeLine(): void {
  $.stKiro.textContent = runtimeStatusLine();
  void checkRuntimeHealth().then(() => {
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
