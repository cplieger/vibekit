// ---------------------------------------------------------------------------
// Context bar update + the single "context is nearly full" signal.
//
// Every store emit refreshes the context bar from the active session. When the
// ACTIVE chat's context is (nearly) full, `contextFull` flips true — that signal
// is the ONE source of truth for the condition; prompt-input.ts reads it and
// renders it as a placeholder + tooltip (see status.ts note). It is ADVISORY: it
// no longer disables the composer, because kiro-cli compacts on the next turn,
// so refusing the send told the user about a problem they could do nothing about.
// There is no module-global previous-thinking flag anymore: it used to detect the
// active chat's thinking→idle transition to (a) emit a `turn:idle` bus event that
// drained the model-switch queue and (b) toggle the input disable. Both were
// active-chat-only and cross-contaminated across chats. (a) now drains from the
// per-chat `turn_ended` SSE (handlers/turn.ts →
// model-switcher.drainModelSwitchQueue); (b) is this continuous per-active-chat
// signal. So no transition state — per-chat or global — is needed here.
// ---------------------------------------------------------------------------

import type { Session, MeteringItem } from "./types.js";
import { updateContextBar } from "./status.js";
import { getActiveId } from "./store.js";
import { contextFull } from "./prompt-input.js";

const CONTEXT_RESERVE_TOKENS = 16_000;
const DEFAULT_CUTOFF_PCT = 95;

// `contextFull` is declared in prompt-input.ts — the module that owns the send
// button/textarea and is the sole renderer of the state. This module COMPUTES
// its value for the active chat below. Importing it from prompt-input (rather
// than declaring it here) keeps the light send-state → prompt-input import chain
// free of this module + status.ts.

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

  // Only the active chat drives the shared prompt bar's advisory.
  if (s.id !== getActiveId()) {
    return;
  }
  const cutoff =
    u.context_size > 0
      ? ((u.context_size - CONTEXT_RESERVE_TOKENS) / u.context_size) * 100
      : DEFAULT_CUTOFF_PCT;
  contextFull.value = u.context_pct >= cutoff;
}
