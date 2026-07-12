// ---------------------------------------------------------------------------
// Fundamental: TurnFooter — the per-turn summary (credits · elapsed · files
// changed) shown under an assistant turn.
//
// This replaces handlers/turn.ts's direct un-keyed DOM writes (the root cause
// of the double-render on SSE replay AND the disappear-on-refresh: those nodes
// were never in the store and never persisted). Here it is a pure keyed view
// rendered from turn metadata carried on the assistant Message, so it
// reconciles like everything else and survives reload.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import type { FileChange } from "../types.js";

/** The per-turn summary inputs, sourced from turn metadata on the message. */
export interface TurnSummaryData {
  credits?: number;
  elapsedMs?: number;
  changedFiles?: Record<string, FileChange>;
}

/** Whether there is anything worth showing (avoid an empty footer). */
export function hasTurnSummary(d: TurnSummaryData): boolean {
  return (
    (d.credits !== undefined && d.credits > 0) ||
    (d.elapsedMs !== undefined && d.elapsedMs > 0) ||
    (d.changedFiles !== undefined && Object.keys(d.changedFiles).length > 0)
  );
}

/** Build the footer element (empty until updateTurnFooter fills it). */
export function buildTurnFooter(d: TurnSummaryData): HTMLDivElement {
  const footer = el("div", {
    className: "turn-footer",
    role: "note",
    "aria-label": "Turn summary",
  }) as HTMLDivElement;
  updateTurnFooter(footer, d);
  return footer;
}

/** Recompute the footer text from turn metadata. Idempotent. */
export function updateTurnFooter(footer: HTMLElement, d: TurnSummaryData): void {
  const parts: string[] = [];
  if (d.credits !== undefined && d.credits > 0) {
    parts.push(`Est. ${d.credits.toFixed(2)} credits`);
  }
  if (d.elapsedMs !== undefined && d.elapsedMs > 0) {
    parts.push(formatElapsed(d.elapsedMs));
  }
  const files = Object.values(d.changedFiles ?? {});
  if (files.length > 0) {
    let added = 0;
    let removed = 0;
    for (const f of files) {
      added += f.lines_added;
      removed += f.lines_removed;
    }
    let fp = `${String(files.length)} file${files.length > 1 ? "s" : ""} changed`;
    if (added > 0) {
      fp += ` +${String(added)}`;
    }
    if (removed > 0) {
      fp += ` -${String(removed)}`;
    }
    parts.push(fp);
  }
  footer.textContent = parts.join(" · ");
}

function formatElapsed(ms: number): string {
  if (ms >= 60_000) {
    const m = Math.floor(ms / 60_000);
    const s = Math.floor((ms % 60_000) / 1000);
    return `${String(m)}m ${String(s)}s`;
  }
  return `${(ms / 1000).toFixed(1)}s`;
}
