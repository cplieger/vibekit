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
// The turn's SEVERITY rides the footer's tint and leading glyph, scannable
// without reading a word — nothing for a clean turn, yellow for a stop, RED for
// a broken one, and `interrupted` is broken. That last clause is a correction:
// this comment used to promise "amber interrupted", and the stylesheet delivered
// it, while the inline notice beside it read the shared severity table and
// painted red. One outcome, two hues, three surfaces. See turn-severity.ts.
//
// An interrupted turn still gets a ledger: the files are on disk regardless.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { chevronEl } from "../chevron.js";
import { openChange, openChangeSet } from "../navigate.js";
import { formatElapsed, isoDuration } from "../strings.js";
import { severityOf } from "../turn-severity.js";
import type { FileChange } from "../types.js";
import type { TurnOutcome } from "../turns.js";

/** The word the ledger line LEADS with, per outcome. TOTAL over `TurnOutcome`, so
 *  every value the wire can send has a treatment and none can fall through to a
 *  line that opens with a cost.
 *
 *  Two outcomes carry no word because they carry no glyph either: `completed`,
 *  where the absence of a mark IS the clean case, and `running`, which
 *  29-turns.css hides on the same rule — the footer reports how a turn ENDED and
 *  does not claim to report one still going.
 *
 *  THE OTHER FIVE ALL SAY THEIR NAME IN THE ROW, and three of them used to say it
 *  only in a `data-tooltip`. A hover is not a durable indication and does not exist
 *  on a touch device, so `cancelled`, `refused` and `unknown` rendered as a coloured
 *  8px circle leading a dense cost line with nothing anywhere saying what the circle
 *  meant. That is the same class of defect as the hollow tab dot: a status whose
 *  only channel is one a reader may never reach. With every glyph-bearing outcome
 *  named here, the outcome clause of `syncLedgerTooltip` had nothing left to add and
 *  is gone.
 *
 *  Short by design — the row is dense and the turn's own `.turn-notice` carries the
 *  sentence. `unknown` names the absence rather than guessing at a cause, which is
 *  `ConcludeStopReason`'s own ruling in three words. */
const OUTCOME_LEAD: Record<TurnOutcome, string> = {
  running: "",
  completed: "",
  cancelled: "Cancelled",
  interrupted: "Interrupted",
  failed: "Failed",
  refused: "Refused",
  unknown: "Outcome unknown",
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

  // The turn's own time, out of the ledger string and into a slot of its own —
  // right-aligned, revealed on hover of the card (29-turns.css), and a real
  // `<time>` so the value is machine-readable as well as legible. Built
  // unconditionally; `syncElapsed` decides whether it says anything.
  footer.appendChild(el("time", { className: "turn-elapsed" }));

  footer.appendChild(el("ul", { className: "turn-ledger-files" }));

  updateTurnFooter(footer, d);
  return footer;
}

/** Recompute the footer from turn metadata. Idempotent, and preserves an
 *  expanded file list across repaints. */
export function updateTurnFooter(footer: HTMLElement, d: TurnSummaryData): void {
  const outcome = d.outcome ?? "completed";
  footer.dataset["outcome"] = outcome;
  // TWO attributes, two questions, one writer each. `data-outcome` still carries
  // the WORDS (OUTCOME_LEAD above) and the one stated hue exception (`unknown`);
  // `data-severity` carries hue, from the shared table rather than from a
  // per-outcome colour rule the stylesheet had to keep in step by hand.
  footer.dataset["severity"] = severityOf(outcome);

  const files = Object.entries(d.changedFiles ?? {});
  const summary = footer.querySelector<HTMLButtonElement>(":scope > .turn-ledger-summary");
  const text = footer.querySelector<HTMLElement>(
    ":scope > .turn-ledger-summary > .turn-ledger-text",
  );
  const line = summaryLine(d, files);
  if (text !== null) {
    text.textContent = line;
  }

  syncElapsed(footer, d.elapsedMs ?? 0);

  // NO native `title`. It carried this same `line` verbatim, so the one hover
  // affordance on the footer restated the text the reader was already looking at
  // — and as a UA tooltip it also missed the styled `data-tooltip` treatment every
  // other hover in the app uses. The accessible name keeps the full statement,
  // which is what a truncated line on a narrow row needs; the tooltip below says
  // the things the row does NOT.
  //
  // THE DURATION IS APPENDED BACK ON, because it left `line` when it moved into its
  // own slot and this label was the one channel it reached a screen reader by. The
  // slot itself is not that channel: it is revealed on hover, and an `aria-label`
  // on the button beside it wins over the button's own text but says nothing about
  // a sibling.
  if (summary !== null) {
    summary.removeAttribute("title");
    const spoken = [line, elapsedText(d.elapsedMs ?? 0)].filter((p) => p !== "").join(" \u00b7 ");
    summary.setAttribute("aria-label", spoken === "" ? "Turn summary" : `Turn summary: ${spoken}`);
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
  // One table lookup rather than a two-arm if/else over two of the seven outcomes:
  // the three it did not name were left to a hover, which is not a channel every
  // reader has.
  //
  // No `?? ""` on the lookup, and the linter is what insists: OUTCOME_LEAD is a
  // total `Record<TurnOutcome, string>`, so indexing it with a `TurnOutcome` yields
  // a string and the fallback would be dead code. An absent outcome defaults its
  // KEY instead, and a value the wire adds later cannot arrive here at all — the
  // generated decoder rejects it at the boundary, which is the same guarantee every
  // other consumer of this union relies on.
  const lead = OUTCOME_LEAD[d.outcome ?? "completed"];
  if (lead !== "") {
    parts.push(lead);
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
  // The DURATION IS NOT HERE any more. It was the sixth of up to seven
  // `·`-separated parts, buried mid-string in a line that otherwise reports cost —
  // and "how long did that take" is the one thing a reader asks of a turn often
  // enough to earn its own place. It now sits right-aligned in `.turn-elapsed`,
  // revealed on hover of the card; `syncElapsed` is the writer.
  //
  // Model last: attribution rather than cost. Arrow when a switch split
  // the turn.
  const models = d.models ?? [];
  if (models.length > 0) {
    parts.push(models.join(" \u2192 "));
  }
  return parts.join(" \u00b7 ");
}

/** The turn's elapsed time, or "" when there is none to show. One reader for the
 *  zero test, so the slot and the accessible name cannot disagree about whether a
 *  turn HAS a duration. */
function elapsedText(ms: number): string {
  return ms > 0 ? formatElapsed(ms) : "";
}

/** Fill (or empty) the right-aligned time slot.
 *
 *  `datetime` travels with the text, which is what a `<time>` is for and costs
 *  nothing; both spellings come from one value so they cannot drift.
 *
 *  A turn with NO duration gets an element that makes no claim: the attribute is
 *  removed rather than set to a zero span, because a duration nobody stamped is not
 *  a duration of zero — and a `<time>` with neither an attribute nor valid content
 *  is not a conforming `<time>` either, so it is hidden too. `hidden`, not a class,
 *  for the reason the header's copy button uses it: `display: none` alone leaves the
 *  element in the accessibility tree. Nothing is lost by not reserving that box —
 *  the reveal exists to avoid shifting the row, and an empty slot has nothing to
 *  reveal and nothing to shift. */
function syncElapsed(footer: HTMLElement, ms: number): void {
  const slot = footer.querySelector<HTMLTimeElement>(":scope > .turn-elapsed");
  if (slot === null) {
    return;
  }
  const text = elapsedText(ms);
  slot.textContent = text;
  slot.hidden = text === "";
  if (text === "") {
    slot.removeAttribute("datetime");
    return;
  }
  slot.dateTime = isoDuration(ms);
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
 *  ONE clause now — what the click does, and only when there is something to
 *  disclose, so a plain readout never advertises a disclosure it does not have. The
 *  attribute is removed outright rather than left empty, because an empty
 *  `data-tooltip` still opens a tip.
 *
 *  It used to carry a second clause naming the outcome for the three states
 *  `summaryLine` left unexplained. That clause is gone because those states are
 *  explained IN THE ROW now (OUTCOME_LEAD is total): a status whose only channel is
 *  a hover has no channel at all on a phone, and repeating a word the line already
 *  leads with was the defect the split existed to avoid. */
function syncLedgerTooltip(footer: HTMLElement): void {
  const summary = footer.querySelector<HTMLButtonElement>(":scope > .turn-ledger-summary");
  if (summary === null) {
    return;
  }
  if (summary.disabled) {
    summary.removeAttribute("data-tooltip");
    return;
  }
  summary.setAttribute(
    "data-tooltip",
    filesOpen(footer) ? "Hide the changed files" : "Show the changed files",
  );
}
