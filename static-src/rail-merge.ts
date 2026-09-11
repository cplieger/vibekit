// The rail's SET of turns: the session-wide index, extended forwards by the resident
// window. Neither side alone answers "which turns exist" — the index cannot see the
// turn running now, the window cannot see the turns paged out. Pure and DOM-free,
// like `turns.ts` beside it.

import { OUTCOME_LABEL } from "./turn-severity.js";
import type { Turn, TurnOutcome, TurnWindowBase } from "./turns.js";

/** One row of the session-wide turn index. Mirrors vibekit.TurnSummary. */
export interface TurnSummary {
  id: string;
  first_line?: string;
  outcome: TurnOutcome;
  n: number;
  ts: number;
  agent_initiated?: boolean;
}

/** The merged set plus the session's turn count. `total` is the highest `n` seen, so
 *  the index's count once one has landed and the highest RESIDENT `n` before that:
 *  positions are correct for the window and re-scale when the index arrives. */
export interface MergedTurns {
  turns: TurnSummary[];
  total: number;
}

/** Rows the index answered with, and how many it carried that could not be read. */
export interface ValidatedIndex {
  turns: TurnSummary[];
  dropped: number;
}

/** The hover label's cap, matching `internal/chat/turns.go`'s `turnFirstLineMax`. */
const FIRST_LINE_MAX = 120;

/** Validate the index, which arrives through an unchecked cast rather than a
 *  generated decoder. A non-finite `n` reaches `calc(NaN * …)`, an invalid
 *  declaration the browser drops, so a bad row would pin a marker to the top of the
 *  track rather than misplace it. Identity and position are DROPPED, description is
 *  COERCED. One `console.warn` per call, which is one per fetch. */
export function validateTurnIndex(raw: unknown): ValidatedIndex {
  if (!Array.isArray(raw)) {
    return { turns: [], dropped: 0 };
  }
  const rows: TurnSummary[] = [];
  const badTs: number[] = [];
  let dropped = 0;
  for (const row of raw as readonly unknown[]) {
    if (typeof row !== "object" || row === null) {
      dropped++;
      continue;
    }
    const r = row as Record<string, unknown>;
    const id = r["id"];
    const n = r["n"];
    if (
      typeof id !== "string" ||
      id === "" ||
      typeof n !== "number" ||
      !Number.isInteger(n) ||
      n < 1
    ) {
      dropped++;
      continue;
    }
    const ts = r["ts"];
    const tsOK = typeof ts === "number" && Number.isFinite(ts) && ts >= 0;
    if (!tsOK) {
      badTs.push(rows.length);
    }
    const outcome = r["outcome"];
    const firstLine = r["first_line"];
    const out: TurnSummary = {
      id,
      n,
      ts: tsOK ? ts : 0,
      // `OUTCOME_LABEL` is total by type, so this is not a second spelling of the union.
      outcome:
        typeof outcome === "string" && Object.hasOwn(OUTCOME_LABEL, outcome)
          ? (outcome as TurnOutcome)
          : "unknown",
      agent_initiated: r["agent_initiated"] === true,
    };
    if (typeof firstLine === "string" && firstLine !== "") {
      out.first_line = firstLine;
    }
    rows.push(out);
  }
  fillBadTimestamps(rows, badTs);
  if (dropped > 0) {
    console.warn("turn rail: dropped unreadable index rows", dropped);
  }
  return { turns: rows, dropped };
}

/** Sit an unreadable `ts` on a neighbour, so the row opens no seam of its own and
 *  the real pause between the rows around it survives. */
function fillBadTimestamps(rows: TurnSummary[], bad: readonly number[]): void {
  if (bad.length === 0) {
    return;
  }
  const isBad = new Set(bad);
  for (const i of bad) {
    let fill = 0;
    for (let j = i - 1; j >= 0; j--) {
      if (!isBad.has(j)) {
        fill = rows[j]?.ts ?? 0;
        break;
      }
    }
    if (fill === 0) {
      for (let j = i + 1; j < rows.length; j++) {
        if (!isBad.has(j)) {
          fill = rows[j]?.ts ?? 0;
          break;
        }
      }
    }
    const row = rows[i];
    if (row !== undefined) {
      row.ts = fill;
    }
  }
}

/** Merge the resident window into the fetched index, BY `n` and never by id: the ids
 *  diverge for one turn — the window's first, when it is a fragment whose opening
 *  message was paged out — so keying on id counts that turn twice.
 *
 *  Resident wins per field, because it sees the running turn. ONE exemption, that
 *  same fragment: with no trigger it has no label, derives `agent_initiated` wrongly,
 *  times itself mid-turn and reads its outcome off a partial body, so the index wins
 *  on those four and resident wins on `outcome` only while it is `running`. */
export function mergeTurnSets(
  resident: readonly Turn[],
  indexed: readonly TurnSummary[],
  base: TurnWindowBase,
): MergedTurns {
  const byN = new Map<number, TurnSummary>();
  for (const row of indexed) {
    byN.set(row.n, row);
  }
  const firstResident = resident[0];
  for (const t of resident) {
    const indexRow = byN.get(t.n);
    const fragment = t === firstResident && base.offset > 0 && t.trigger === undefined;
    if (fragment && indexRow !== undefined) {
      byN.set(t.n, t.outcome === "running" ? { ...indexRow, outcome: "running" } : indexRow);
      continue;
    }
    byN.set(t.n, residentRow(t));
  }
  const turns = [...byN.values()].sort((a, b) => a.n - b.n);
  let total = 0;
  for (const row of turns) {
    total = Math.max(total, row.n);
  }
  return { turns, total };
}

function residentRow(t: Turn): TurnSummary {
  const out: TurnSummary = {
    id: t.id,
    n: t.n,
    ts: t.ts,
    outcome: t.outcome,
    agent_initiated: t.trigger === undefined,
  };
  const line = firstLine(t.trigger?.content ?? "");
  if (line !== "") {
    out.first_line = line;
  }
  return out;
}

/** A request as ONE readable line, rune-safe. A TWIN of `internal/chat/turns.go`
 *  `firstLine`, because the index cannot answer for the turn that is running and the
 *  two spellings must agree for every turn it can. No shared fixture yet. */
function firstLine(s: string): string {
  const collapsed = s.replace(/\s+/gu, " ").trim();
  // Code points, not graphemes: Go's `for range` yields runes and the cap must match.
  const runes = Array.from(collapsed);
  return runes.length > FIRST_LINE_MAX
    ? runes.slice(0, FIRST_LINE_MAX).join("") + "\u2026"
    : collapsed;
}
