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

    $.ctxModelPill.textContent = humanName(model);
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
  const rows: HTMLElement[] = items.map((item) => {
    const row = document.createElement("span");
    row.className = "pill-metering-row";
    const label = document.createElement("span");
    label.className = "pill-metering-label";
    label.textContent = item.value === 1 ? item.unit_singular : item.unit_plural;
    const value = document.createElement("span");
    value.className = "pill-metering-value";
    value.textContent = formatMetering(item.value);
    row.append(label, value);
    return row;
  });
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
      statusLiveEl = document.createElement("span");
      statusLiveEl.id = "status-live";
      statusLiveEl.setAttribute("role", "status");
      statusLiveEl.setAttribute("aria-live", "polite");
      statusLiveEl.setAttribute("aria-atomic", "true");
      statusLiveEl.className = "sr-only";
      document.body.appendChild(statusLiveEl);
    }
  }
  return statusLiveEl;
}

export function setStatus(s: ConnectionStatus): void {
  const dot = $.statusDot;
  dot.classList.remove("connected", "error");
  if (s === "connected") {
    dot.classList.add("connected");
    dot.style.setProperty("--status-color", "var(--c-green)");
  } else if (s === "disconnected") {
    dot.classList.add("error");
    dot.style.setProperty("--status-color", "var(--c-red)");
  } else {
    dot.style.setProperty("--status-color", "var(--c-yellow)");
  }
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

export function setInputDisabled(disabled: boolean, reason?: string): void {
  $.promptInput.disabled = disabled;
  $.sendBtn.disabled = disabled;
  if (disabled && reason !== undefined) {
    $.promptInput.placeholder = reason;
  } else {
    $.promptInput.placeholder = "Message Kiro...";
  }
}
