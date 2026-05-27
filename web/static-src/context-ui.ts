// ---------------------------------------------------------------------------
// Context bar update + "input disabled when context full" rule.
//
// Every store emit refreshes the context bar from the active session.
// When `thinking` settles to false we emit a `turn:idle` bus event —
// model-switcher.ts subscribes to drain any queued switch and clear
// the button's transient spinner class. That pattern keeps this
// module free of switcher-specific state.
// ---------------------------------------------------------------------------

import type { Session, MeteringItem } from "./types.js";
import { updateContextBar, setInputDisabled } from "./status.js";
import { emitBus, BUS_TURN_IDLE } from "./bus.js";

const CONTEXT_RESERVE_TOKENS = 16_000;
const DEFAULT_CUTOFF_PCT = 95;
let _prevThinking = false;

export function refreshContextUI(s: Session): void {
  const u = s.usage;
  const metering: MeteringItem[] = u.metering_items ?? [];
  // Count messages and tool calls for the context breakdown.
  let msgCount = 0;
  let toolCount = 0;
  let summarizedCount = 0;
  const watermark = s.compaction_watermark ?? "";
  let pastWatermark = watermark === "";
  for (const m of s.messages) {
    msgCount++;
    if (!pastWatermark) {
      summarizedCount++;
      if (m.id === watermark) {
        pastWatermark = true;
      }
    }
    if (m.tool_calls !== undefined) {
      toolCount += m.tool_calls.length;
    }
  }
  updateContextBar({
    pct: u.context_pct,
    contextSize: u.context_size,
    credits: u.credits,
    turnCount: u.turn_count,
    lastTurnMs: u.last_turn_ms,
    model: s.model,
    metering,
    msgCount,
    toolCount,
    summarizedCount,
  });
  const cutoff =
    u.context_size > 0
      ? ((u.context_size - CONTEXT_RESERVE_TOKENS) / u.context_size) * 100
      : DEFAULT_CUTOFF_PCT;
  const full = u.context_pct >= cutoff;

  const isThinking = s.thinking ?? false;
  if (_prevThinking && !isThinking) {
    emitBus(BUS_TURN_IDLE, s.id);
    setInputDisabled(
      full,
      full
        ? "Context nearly full. kiro-cli will compact automatically on the next turn."
        : undefined,
    );
  }
  _prevThinking = isThinking;
}
