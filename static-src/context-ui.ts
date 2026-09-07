// ---------------------------------------------------------------------------
// Context bar update + the single "context is nearly full" signal.
//
// Runs from chat.ts's active-session effect: every change to the ACTIVE
// session (and every switch of which session is active) refreshes the context
// bar; background session churn never reaches it. When the active chat's
// context is (nearly) full, `contextFull` flips true — that signal
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
import { nonDefaultEffortLabel } from "./effort.js";
import { getCachedModels } from "./picker.js";
import { getLastEffortFor } from "./session-context.js";
import { KAS_SUMMARIZATION_PCT, KAS_TRUNCATION_PCT } from "./context-ring.js";

// The live cutoff, derived from the model's real window: a flat percentage leaves
// tens of thousands of tokens of slack on a large one. KAS_TRUNCATION_PCT is its
// fallback, for a window this chat does not know yet.
const CONTEXT_RESERVE_TOKENS = 16_000;

// `contextFull` is declared in prompt-input.ts — the module that owns the send
// button/textarea and is the sole renderer of the state. This module COMPUTES
// its value for the active chat below. Importing it from prompt-input (rather
// than declaring it here) keeps the light send-state → prompt-input import chain
// free of this module + status.ts.

/** How many of the loaded messages the compaction watermark covers, or
 *  undefined when this window cannot say.
 *
 *  The boundary is found FIRST and only then measured: counting while scanning
 *  makes an unmet condition indistinguishable from a satisfied one, so a
 *  watermark that is not resident — the ordinary state of a paged chat, and
 *  permanent after a rewind — counted every loaded message. 0 is not the answer
 *  for that case either; it keeps meaning the chat has no watermark. */
function summarizedCount(s: Session): number | undefined {
  const watermark = s.compaction_watermark ?? "";
  if (watermark === "") {
    return 0;
  }
  const idx = s.messages.findIndex((m) => m.id === watermark);
  return idx < 0 ? undefined : idx + 1;
}

export function refreshContextUI(s: Session): void {
  const u = s.usage;
  const metering: MeteringItem[] = u.metering_items ?? [];
  // Count messages and tool calls for the context breakdown.
  let msgCount = 0;
  let toolCount = 0;
  for (const m of s.messages) {
    msgCount++;
    if (m.tool_calls !== undefined) {
      toolCount += m.tool_calls.length;
    }
  }
  const summarized = summarizedCount(s);
  // The ONE fallback site: the streaming usage channel carries no threshold.
  const summarizationPct = u.summarization_threshold_pct ?? KAS_SUMMARIZATION_PCT;
  updateContextBar({
    pct: u.context_pct,
    contextSize: u.context_size,
    summarizationPct,
    credits: u.credits,
    turnCount: u.turn_count,
    lastTurnMs: u.last_turn_ms,
    model: s.model,
    // The reasoning tier, when the user decided it or a known default proves it
    // is a departure (effort.ts owns that test, and the default is the catalog's
    // per-model field rather than anything stored here). Resolved HERE rather
    // than in status.ts so the renderer keeps writing what it is handed: this
    // module already runs on every active-session change, so the pill repaints
    // when an optimistic set_effort write lands, when the session reports a new
    // currentValue, and when a model switch changes which default applies.
    effort: nonDefaultEffortLabel(s, getCachedModels(), getLastEffortFor(s.model)),
    metering,
    msgCount,
    toolCount,
    // Withheld rather than sent as a number when the window cannot say. The
    // renderer already prints nothing for an absent count, so "unknowable" and
    // "nothing was summarized" read the same to the user and differ here.
    ...(summarized === undefined ? {} : { summarizedCount: summarized }),
  });

  // Only the active chat drives the shared prompt bar's advisory.
  if (s.id !== getActiveId()) {
    return;
  }
  const cutoff =
    u.context_size > 0
      ? ((u.context_size - CONTEXT_RESERVE_TOKENS) / u.context_size) * 100
      : (u.truncation_threshold_pct ?? KAS_TRUNCATION_PCT);
  contextFull.value = u.context_pct >= cutoff;
}
