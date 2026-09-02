// ---------------------------------------------------------------------------
// Fundamental: TurnFooter — the turn card's outcome ledger.
//
// One small tinted line mirroring the header band, so the turn is visually
// bracketed. Three depths: the aggregate row answers "did it work", the
// per-file rows answer "what changed", clicking a row answers "let me look".
//
// It used to be one flat `textContent` string with no per-file detail; it
// expands to one row per file with its own `+N −M`, a new-file badge, and a
// click into the diff.
//
// The turn's OUTCOME rides the footer's tint and leading glyph, scannable
// without reading a word — green clean, amber interrupted, red failed. An
// interrupted turn still gets a ledger: the files are on disk regardless.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { chevronEl } from "../chevron.js";
import { openChange, openChangeSet } from "../navigate.js";
import { formatElapsed } from "../strings.js";
import type { FileChange } from "../types.js";
import type { TurnOutcome } from "../turns.js";

/** The outcome the footer's GLYPH stands for, in words, for the tooltip.
 *
 *  Only the outcomes the summary LINE does not already name. `summaryLine` leads
 *  with "Interrupted" / "Failed" for those two, so repeating them is the defect
 *  being fixed rather than a fix; `completed` hides the glyph entirely, and the
 *  absence of a mark IS the clean case. What is left is the set where a reader
 *  sees a coloured circle and the row says nothing about it — chief among them
 *  `running`, whose accent ring is the only thing on the footer that says the
 *  turn has not finished. */
const OUTCOME_TOOLTIP: Partial<Record<TurnOutcome, string>> = {
  running: "Still running",
  cancelled: "Cancelled",
  refused: "Refused by the model",
  unknown: "Ended without a recorded outcome",
};

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

/** Whether there is anything worth showing (avoid an empty footer). A
 *  non-clean outcome always qualifies even with no numbers, since an
 *  interrupted or failed turn is exactly when a reader needs to know what
 *  landed.
 *
 *  `models` is deliberately not admitted: every completed turn has one, so
 *  counting it would put a footer on every turn — including the ones this
 *  rule exists to suppress. */
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

  // NO native `title`. It carried this same `line` verbatim, so the one hover
  // affordance on the footer restated the text the reader was already looking at
  // — and as a UA tooltip it also missed the styled `data-tooltip` treatment every
  // other hover in the app uses. The accessible name keeps the full statement,
  // which is what a truncated line on a narrow row needs; the tooltip below says
  // the things the row does NOT.
  if (summary !== null) {
    summary.removeAttribute("title");
    summary.setAttribute("aria-label", line === "" ? "Turn summary" : `Turn summary: ${line}`);
  }

  // Disclosure only when there is something to disclose — an inert button
  // is worse than a plain readout.
  if (summary !== null) {
    const expandable = files.length > 0;
    summary.disabled = !expandable;
    if (expandable) {
      summary.setAttribute("aria-expanded", filesOpen(footer) ? "true" : "false");
    } else {
      summary.removeAttribute("aria-expanded");
      setFilesOpen(footer, false);
    }
    syncLedgerTooltip(footer);
  }

  const list = footer.querySelector<HTMLElement>(":scope > .turn-ledger-files");
  if (list !== null) {
    renderFileRows(list, files);
  }
}

/** The aggregate: files first (what a reader came for), then work with no
 *  file to show, then cost. */
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
  // Model last: attribution rather than cost. Arrow when a switch split
  // the turn.
  const models = d.models ?? [];
  if (models.length > 0) {
    parts.push(models.join(" \u2192 "));
  }
  return parts.join(" \u00b7 ");
}

/** One row per changed file: `path +N −M`, with a new-file badge. Every row
 *  opens that file's diff — the aggregate answers whether it worked, rows
 *  answer what changed, the click answers let me look. */
function renderFileRows(list: HTMLElement, files: [string, FileChange][]): void {
  // Sorted by path so a repaint cannot reshuffle rows under the cursor.
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
  // `data-tooltip`, not `title`: the styled tooltip system is what every other
  // hover in the app uses, and a UA tooltip beside it reads as foreign chrome.
  const btn = el("button", {
    className: "turn-file-row",
    type: "button",
    "data-tooltip": `Open the diff for ${path}`,
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
 *  per-file rows. Appears only when the turn touched more than one file — a
 *  single-file turn's row above already opens that diff. */
function reviewRow(count: number): HTMLElement | null {
  if (count < 2) {
    return null;
  }
  const btn = el(
    "button",
    {
      className: "turn-review-all",
      type: "button",
      "data-tooltip": "Review every changed file in the git view",
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
  syncLedgerTooltip(footer);
}

/** The ledger's hover text: what the row does NOT already say.
 *
 *  Two clauses, either of which may be absent. The OUTCOME names the coloured
 *  glyph in words for the states `summaryLine` leaves unexplained (see
 *  OUTCOME_TOOLTIP) — the answer to "what is that hollow circle". The ACTION names
 *  what the click does, and only when there is something to disclose, so a plain
 *  readout never advertises a disclosure it does not have. Joined with the same
 *  separator the summary line uses, and the attribute is removed outright when
 *  neither clause applies rather than left empty, because an empty `data-tooltip`
 *  still opens a tip. */
function syncLedgerTooltip(footer: HTMLElement): void {
  const summary = footer.querySelector<HTMLButtonElement>(":scope > .turn-ledger-summary");
  if (summary === null) {
    return;
  }
  const parts: string[] = [];
  const outcome = OUTCOME_TOOLTIP[(footer.dataset["outcome"] ?? "") as TurnOutcome];
  if (outcome !== undefined) {
    parts.push(outcome);
  }
  if (!summary.disabled) {
    parts.push(filesOpen(footer) ? "Hide the changed files" : "Show the changed files");
  }
  if (parts.length === 0) {
    summary.removeAttribute("data-tooltip");
    return;
  }
  summary.setAttribute("data-tooltip", parts.join(" \u00b7 "));
}
