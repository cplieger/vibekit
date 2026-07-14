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
import { fetchKiroSetting, CancellableSlot } from "./api-client.js";
import { registerCleanup } from "./actions/index.js";
import { el } from "@cplieger/reactive";
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
  private compactionBufferPct = 10;
  private compactionSlot = new CancellableSlot();
  private contextTickInitialised = false;
  private contextBarQueued = false;
  private contextBarArgs: ContextBarUpdate | null = null;

  private async fetchCompactionBuffer(): Promise<void> {
    const signal = this.compactionSlot.start();
    this.compactionBufferPct = await fetchKiroSetting(
      "compaction.excludeContextWindowPercent",
      (v) => {
        const n = Number.parseInt(v, 10);
        return !Number.isNaN(n) && n >= 0 && n <= 100 ? n : null;
      },
      10,
      signal,
    );
    if (signal.aborted) {
      return;
    }
    this.positionContextTick();
  }

  private positionContextTick(): void {
    const tick = document.getElementById("context-ring-tick");
    if (tick === null) {
      return;
    }
    const thresholdPct = Math.max(0, Math.min(100, 100 - this.compactionBufferPct));
    const deg = (thresholdPct / 100) * 360;
    tick.setAttribute("transform", `rotate(${String(deg)} 10 10)`);
  }

  refreshCompactionThreshold(): void {
    void this.fetchCompactionBuffer();
  }

  cancelCompactionLoad(): void {
    this.compactionSlot.abort();
  }

  update(opts: ContextBarUpdate): void {
    if (!this.contextTickInitialised) {
      this.contextTickInitialised = true;
      void this.fetchCompactionBuffer();
    }
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

    const circ = 50.27;
    const offset = circ * (1 - clamped / 100);
    $.contextRingFill.style.strokeDashoffset = String(offset);
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
registerCleanup(() => {
  contextBar.cancelCompactionLoad();
});

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

/** Re-read the compaction threshold from kiro-cli. Called by the
 *  settings_updated SSE handler so changes to context-buffer in
 *  Settings → General update the tick mark without a page reload. */
export function refreshCompactionThreshold(): void {
  contextBar.refreshCompactionThreshold();
}

// --- Connection status live-region debounce ---
// Prevents rapid connecting→disconnected→connecting cycles from spamming
// screen readers. Only announces after status is stable for 2s.
let statusAnnounceTimer: ReturnType<typeof setTimeout> | undefined;
let statusLiveEl: HTMLElement | null = null;

function getStatusLiveEl(): HTMLElement {
  if (statusLiveEl === null) {
    statusLiveEl = document.getElementById("status-live");
    if (statusLiveEl === null) {
      statusLiveEl = el("span", {
        id: "status-live",
        role: "status",
        "aria-live": "polite",
        "aria-atomic": "true",
        className: "sr-only",
      });
      document.body.appendChild(statusLiveEl);
    }
  }
  return statusLiveEl;
}

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
  dot.style.setProperty("--status-color", style.color);
  dot.setAttribute("aria-label", `Connection: ${s}`);
  $.stWs.textContent = s;

  // Debounced live-region announcement (2s stable).
  clearTimeout(statusAnnounceTimer);
  statusAnnounceTimer = setTimeout(() => {
    getStatusLiveEl().textContent = `Connection ${s}`;
  }, 2000);
}

export function updateContextBar(opts: ContextBarUpdate): void {
  contextBar.update(opts);
}

// The send button / textarea `disabled` state (and the "context nearly full"
// placeholder) has a single owner: prompt-input.ts, which reads the
// `sendDisabled` signal from context-ui.ts. status.ts used to write those DOM
// props too (setInputDisabled), which fought prompt-input's send-state machine
// on every turn boundary — last-writer-wins left the disable unreliable. That
// second writer is gone.
