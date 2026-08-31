// ---------------------------------------------------------------------------
// Fundamental: TurnFooter — the turn card's outcome ledger.
//
// One small tinted line mirroring the header band, so the turn is visually
// bracketed. There is no separate status rail: the footer IS the rail.
//
// Three questions, three depths, and the depth ladder is the whole point:
//
//   the aggregate row  answers  "did it work"
//   the per-file rows  answer   "what changed"
//   clicking a row     answers  "let me look"
//
// It used to be one flat `textContent` string — not clickable, not expandable,
// and it never named a single file, so the one thing a reader wants after a
// forty-file refactor ("which files?") was the one thing it could not say. It
// is a surface now: the aggregate stays as the summary, and it expands to one
// row per file with its own `+N −M`, a new-file badge, and a click into the
// diff.
//
// The turn's OUTCOME rides the footer's tint and its leading glyph, so a
// transcript is scannable by result without reading a word — green clean,
// amber interrupted, red failed. An interrupted turn still gets a ledger: the
// files are on disk regardless, and a cancel is exactly when a reader needs to
// know what landed.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { chevronEl } from "../chevron.js";
import { openChange, openChangeSet } from "../navigate.js";
import { formatElapsed } from "../strings.js";
import type { FileChange } from "../types.js";
import type { TurnOutcome } from "../turns.js";

/** The per-turn summary inputs, sourced from turn metadata on the message. */
export interface TurnSummaryData {
  credits?: number;
  elapsedMs?: number;
  changedFiles?: Record<string, FileChange>;
  /** Commands run and files read — work a file list cannot show. */
  commands?: number;
  reads?: number;
  /** The model(s) that answered, distinct and in order. Rendered as one name
   *  normally and `a -> b` when a switch split the turn. Absent on every turn
   *  persisted before the field existed. */
  models?: string[];
  /** The turn's result, carried as the footer's tint so outcome is scannable
   *  down the transcript without reading a word. */
  outcome?: TurnOutcome;
}

/** Whether there is anything worth showing (avoid an empty footer).
 *
 *  A non-clean outcome always qualifies, even with no numbers behind it: an
 *  interrupted or failed turn is exactly when a reader needs to know what
 *  landed, and suppressing the row because a cancel arrived before any usage
 *  was stamped would hide the one fact that matters.
 *
 *  `models` is DELIBERATELY not admitted here. Every completed turn has a model,
 *  so counting it as "something worth showing" would put a footer on every turn
 *  in the transcript — including the ones this rule exists to suppress. The
 *  model rides a footer that already earned its place; it never earns one. */
export function hasTurnSummary(d: TurnSummaryData): boolean {
  return (
    (d.outcome !== undefined && d.outcome !== "completed" && d.outcome !== "running") ||
    (d.credits ?? 0) > 0 ||
    (d.elapsedMs ?? 0) > 0 ||
    (d.commands ?? 0) > 0 ||
    (d.reads ?? 0) > 0 ||
    Object.keys(d.changedFiles ?? {}).length > 0
  );
}

/** Build the footer element (empty until updateTurnFooter fills it). */
export function buildTurnFooter(d: TurnSummaryData): HTMLDivElement {
  const footer = el("div", {
    className: "turn-footer",
    role: "note",
    "aria-label": "Turn summary",
  }) as HTMLDivElement;

  const summary = el("button", {
    className: "turn-ledger-summary",
    type: "button",
  }) as HTMLButtonElement;
  summary.appendChild(el("span", { className: "turn-ledger-glyph" }));
  summary.appendChild(el("span", { className: "turn-ledger-text" }));
  const caret = chevronEl();
  caret.classList.add("turn-ledger-caret");
  summary.appendChild(caret);
  summary.addEventListener("click", () => {
    setFilesOpen(footer, !filesOpen(footer));
  });
  footer.appendChild(summary);

  footer.appendChild(el("ul", { className: "turn-ledger-files" }));

  updateTurnFooter(footer, d);
  return footer;
}

/** Recompute the footer from turn metadata. Idempotent, and preserves an
 *  expanded file list across repaints. */
export function updateTurnFooter(footer: HTMLElement, d: TurnSummaryData): void {
  footer.dataset["outcome"] = d.outcome ?? "completed";

  const files = Object.entries(d.changedFiles ?? {});
  const summary = footer.querySelector<HTMLButtonElement>(":scope > .turn-ledger-summary");
  const text = footer.querySelector<HTMLElement>(
    ":scope > .turn-ledger-summary > .turn-ledger-text",
  );
  const line = summaryLine(d, files);
  if (text !== null) {
    text.textContent = line;
  }

  // Mobile may ellipsize the tail of a long cost/model line to keep the footer
  // on one row. The full statement remains the control's accessible name and
  // hover text rather than disappearing with the clipped pixels.
  if (summary !== null) {
    summary.title = line;
    summary.setAttribute("aria-label", line === "" ? "Turn summary" : `Turn summary: ${line}`);
  }

  // The summary is only a disclosure when there is something to disclose. With
  // no files it stays a plain readout rather than a button that does nothing.
  if (summary !== null) {
    const expandable = files.length > 0;
    summary.disabled = !expandable;
    if (expandable) {
      summary.setAttribute("aria-expanded", filesOpen(footer) ? "true" : "false");
    } else {
      summary.removeAttribute("aria-expanded");
      setFilesOpen(footer, false);
    }
  }

  const list = footer.querySelector<HTMLElement>(":scope > .turn-ledger-files");
  if (list !== null) {
    renderFileRows(list, files);
  }
}

/** The aggregate: what happened, in the order a reader asks it. Files first
 *  (the thing they came for), then work with no file to show, then cost. */
function summaryLine(d: TurnSummaryData, files: [string, FileChange][]): string {
  const parts: string[] = [];
  if (d.outcome === "interrupted") {
    parts.push("Interrupted");
  } else if (d.outcome === "failed") {
    parts.push("Failed");
  }
  if (files.length > 0) {
    let added = 0;
    let removed = 0;
    for (const [, f] of files) {
      added += f.lines_added;
      removed += f.lines_removed;
    }
    let fp = `${String(files.length)} file${files.length > 1 ? "s" : ""}`;
    if (added > 0) {
      fp += ` +${String(added)}`;
    }
    if (removed > 0) {
      fp += ` \u2212${String(removed)}`;
    }
    parts.push(fp);
  }
  const cmds = d.commands ?? 0;
  if (cmds > 0) {
    parts.push(`${String(cmds)} cmd${cmds > 1 ? "s" : ""}`);
  }
  const reads = d.reads ?? 0;
  if (reads > 0) {
    parts.push(`${String(reads)} read${reads > 1 ? "s" : ""}`);
  }
  if ((d.credits ?? 0) > 0) {
    parts.push(`${(d.credits ?? 0).toFixed(2)} cr`);
  }
  if ((d.elapsedMs ?? 0) > 0) {
    parts.push(formatElapsed(d.elapsedMs ?? 0));
  }
  // Which model, last: it answers "who did this" rather than "what did it
  // cost", so it reads as attribution at the end of the line. An arrow when a
  // switch split the turn, because "sonnet-4" alone would name one half.
  const models = d.models ?? [];
  if (models.length > 0) {
    parts.push(models.join(" \u2192 "));
  }
  return parts.join(" \u00b7 ");
}

/** One row per changed file: `path +N −M`, with a badge when the turn created
 *  it. Every row is a click target into that file's diff — the aggregate
 *  answers whether it worked, the rows answer what changed, the click answers
 *  let me look. */
function renderFileRows(list: HTMLElement, files: [string, FileChange][]): void {
  // Sorted by path so a repaint cannot reshuffle the rows under the cursor
  // (Object.entries order follows insertion, and the map is rebuilt per turn).
  const sorted = [...files].sort((a, b) => a[0].localeCompare(b[0]));
  const rows: HTMLElement[] = sorted.map(([path, fc]) => fileRow(path, fc));
  const review = reviewRow(sorted.length);
  if (review !== null) {
    rows.push(review);
  }
  list.replaceChildren(...rows);
}

function fileRow(path: string, fc: FileChange): HTMLElement {
  const item = el("li", { className: "turn-ledger-file" });
  const btn = el("button", {
    className: "turn-file-row",
    type: "button",
    title: `Open the diff for ${path}`,
  }) as HTMLButtonElement;

  btn.appendChild(el("span", { className: "turn-file-path" }, path));
  if (fc.is_new_file === true) {
    btn.appendChild(el("span", { className: "turn-file-badge" }, "new"));
  }
  const delta = el("span", { className: "turn-file-delta" });
  if (fc.lines_added > 0) {
    delta.appendChild(el("span", { className: "turn-file-add" }, `+${String(fc.lines_added)}`));
  }
  if (fc.lines_removed > 0) {
    delta.appendChild(
      el("span", { className: "turn-file-del" }, `\u2212${String(fc.lines_removed)}`),
    );
  }
  btn.appendChild(delta);

  btn.addEventListener("click", () => {
    openChange(path);
  });
  item.appendChild(btn);
  return item;
}

/** `Review changes`: the multi-file seam, offered once per turn beneath the
 *  per-file rows rather than on each row.
 *
 *  It appears ONLY when the turn touched more than one file. On a single-file
 *  turn the row above it already opens that file's diff, so a second control
 *  going to a broader view of the same one change is noise. */
function reviewRow(count: number): HTMLElement | null {
  if (count < 2) {
    return null;
  }
  const btn = el(
    "button",
    {
      className: "turn-review-all",
      type: "button",
      title: "Review every changed file in the git view",
    },
    `Review changes (${String(count)} files)`,
  ) as HTMLButtonElement;
  btn.addEventListener("click", () => {
    openChangeSet();
  });
  return el("li", { className: "turn-ledger-file turn-ledger-review" }, btn);
}

function filesOpen(footer: HTMLElement): boolean {
  return footer.dataset["files"] === "open";
}

function setFilesOpen(footer: HTMLElement, on: boolean): void {
  if (on) {
    footer.dataset["files"] = "open";
  } else {
    delete footer.dataset["files"];
  }
  const summary = footer.querySelector<HTMLButtonElement>(":scope > .turn-ledger-summary");
  if (summary !== null && !summary.disabled) {
    summary.setAttribute("aria-expanded", on ? "true" : "false");
  }
}
