// Fundamental: TurnFooter — the turn card's outcome ledger. Three depths: the
// aggregate row answers "did it work", the per-file rows answer "what changed",
// a row's click answers "let me look". Tint and glyph come from the shared
// severity table (turn-severity.ts), never from a per-outcome rule here.

import { el } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import { iconEl } from "../icon-el.js";
import { ICON_INFO } from "../icons.js";
import { openChange, openChangeSet } from "../navigate.js";
import { sigChanged } from "../paint-sig.js";
import { formatElapsed } from "../strings.js";
import { kindNoun } from "../tool-kind-noun.js";
import { severityOf } from "../turn-severity.js";
import type { FileChange, ToolKind } from "../types.js";
import { COMMAND_KINDS, type TurnOutcome } from "../turns.js";

// TWO consumers: `messages.ts` mounts this on a turn card, `subagent-block.ts` on a
// DELEGATE card (pinned in its own test), which is why every panel field is optional.
// Nothing on the ACP wire carries credits, a model id or a stop reason PER delegate, so
// Cost, Model and Diagnostics withhold on every delegate card — designed, not a gap.

/** The word the ledger line LEADS with, per outcome. TOTAL over `TurnOutcome`, so no
 *  value the wire can send falls through to a line that opens with a cost.
 *
 *  Two outcomes carry no word because they carry no glyph either: `completed`, where
 *  the absence of a mark IS the clean case, and `running`, which 29-turns.css hides on
 *  the same rule. The other five say their name in the ROW rather than in a hover,
 *  which does not exist on a touch device. Short by design — the row is dense and the
 *  turn's own `.turn-notice` carries the sentence. */
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
  /** The model(s) that answered, distinct and in order. Rendered as one name
   *  normally and `a -> b` when a switch split the turn. Absent on every turn
   *  persisted before the field existed. */
  models?: string[];
  /** The turn's result, carried as the footer's tint so outcome is scannable
   *  down the transcript without reading a word. */
  outcome?: TurnOutcome;
  /** The info panel's facts, mirroring `TurnLedger`, which owns each field's ABSENCE
   *  RULE — read it there. A section withholds on absence and never invents a zero: a
   *  duration nobody stamped is not a duration of zero, `kindCounts` omits a kind
   *  rather than reporting zero of it, and `endedAt` is a stamp, not a sum. */
  toolMs?: number;
  kindCounts?: Partial<Record<ToolKind, number>>;
  delegateCount?: number;
  delegateMs?: number;
  startedAt?: number;
  endedAt?: number;
  stopReasonRaw?: string;
  truncated?: boolean;
}

/** Reasons a footer is earned that are NOT in the summary data, passed by the
 *  consumer that can see them. Both are turn-card facts, so the delegate card
 *  passes neither. */
export interface FooterExtras {
  /** This turn is a rewind target, so the footer carries a Rewind control. */
  rewindable?: boolean;
  /** This turn has settled prose, so the footer carries the turn ACTIONS
   *  (copy / source / export) that act on it. */
  settledProse?: boolean;
}

/** Whether the footer is earned, READING `turnFacts` rather than re-listing the fields:
 *  a fact for the row, the outcome WORD, or a CONTROL the caller mounts. Two statements
 *  of one question is how those diverged, so an earned footer with nothing on it is
 *  unrepresentable. WIDER than the disjunction it replaced: every kind count leads, so
 *  the twelve kinds outside `COMMAND_KINDS` and `read` earn one now. */
export function earnsTurnFooter(d: TurnSummaryData, extra: FooterExtras = {}): boolean {
  return (
    turnFacts(d).length > 0 ||
    OUTCOME_LEAD[d.outcome ?? "completed"] !== "" ||
    extra.rewindable === true ||
    extra.settledProse === true
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
  // An `i` rather than a chevron, LEADING. The button spans the band on the BLOCK
  // axis only — measured on a clean turn it is 28px wide of a 798px band, 3.5% — so
  // `data-tooltip-anchor` moves the tip's POSITION to the ink. Why: `vibekit-ui.md`
  // "A THIRD case: a door onto INFORMATION" and "A TOOLTIP POINTS AT INK".
  summary.appendChild(
    el("span", { className: "turn-ledger-info", "data-tooltip-anchor": "" }, iconEl(ICON_INFO)),
  );
  summary.appendChild(el("span", { className: "turn-ledger-glyph" }));
  summary.appendChild(el("span", { className: "turn-ledger-text" }));
  // The name comes from the button's CONTENT, so there is deliberately no
  // `aria-label`: one would WIN over the element's own text and hide the outcome word.
  // Without this span a clean turn's button has no accessible name at all.
  summary.appendChild(el("span", { className: "sr-only" }, "Turn details"));
  summary.addEventListener("click", () => {
    setInfoOpen(footer, !infoOpen(footer));
  });
  footer.appendChild(summary);

  // A SIBLING of the button, not a child: the button's name is computed from its
  // content, so a fact inside it would be read out as part of the trigger's name and
  // would change whenever the turn's numbers do.
  footer.appendChild(el("span", { className: "turn-fact" }));

  footer.appendChild(el("div", { className: "turn-info-panel" }));

  updateTurnFooter(footer, d);
  return footer;
}

/** Recompute the footer from turn metadata. Idempotent, and preserves an
 *  expanded file list across repaints. */
export function updateTurnFooter(footer: HTMLElement, d: TurnSummaryData): void {
  const outcome = d.outcome ?? "completed";
  footer.dataset["outcome"] = outcome;
  // TWO attributes, two questions, one writer each. `data-outcome` carries the WORDS
  // (OUTCOME_LEAD above); `data-severity` carries hue, from the shared table rather than
  // from a per-outcome colour rule the stylesheet had to keep in step by hand.
  footer.dataset["severity"] = severityOf(outcome);

  const files = Object.entries(d.changedFiles ?? {});
  const summary = footer.querySelector<HTMLButtonElement>(":scope > .turn-ledger-summary");
  const text = footer.querySelector<HTMLElement>(
    ":scope > .turn-ledger-summary > .turn-ledger-text",
  );
  if (text !== null) {
    text.textContent = summaryLine(d);
  }

  const slot = footer.querySelector<HTMLElement>(":scope > .turn-fact");
  if (slot !== null) {
    const facts = turnFacts(d);
    slot.hidden = facts.length === 0;
    slot.textContent = facts[0] ?? "";
  }

  // ALWAYS a disclosure, so `aria-expanded` is written unconditionally: every turn
  // that earned a footer at all has a panel to open.
  if (summary !== null) {
    summary.setAttribute("aria-expanded", infoOpen(footer) ? "true" : "false");
    syncLedgerTooltip(footer);
  }

  const panel = footer.querySelector<HTMLElement>(":scope > .turn-info-panel");
  if (panel !== null) {
    renderInfoPanel(panel, d, files);
  }
}

/** The ledger LINE: the outcome's lead word and nothing else — every other clause moved
 *  into the info panel as a labelled row. No `?? ""` on the lookup, because OUTCOME_LEAD
 *  is total over `TurnOutcome` and the fallback would be dead code the linter rejects;
 *  an absent outcome defaults its KEY instead. */
function summaryLine(d: TurnSummaryData): string {
  return OUTCOME_LEAD[d.outcome ?? "completed"];
}

/** The turn's facts, most important first, each from the same field the info panel
 *  renders it from. A zero or absent value contributes nothing, and the
 *  ROW PAINTS `[0]` — the panel one click away is the full statement. Do NOT rotate the
 *  slot through this list: a dwell replaces a gesture-gated value with a TIME-gated one,
 *  and it fails WCAG 2.2.2 with no pause control anywhere in the app (`vibekit-ui.md`
 *  "A ROTATING READOUT IS NOT A BEAT"). */
export function turnFacts(d: TurnSummaryData): readonly string[] {
  const facts: string[] = [];
  const files = Object.values(d.changedFiles ?? {});
  if (files.length > 0) {
    let added = 0;
    let removed = 0;
    for (const fc of files) {
      added += fc.lines_added;
      removed += fc.lines_removed;
    }
    let fact = `${String(files.length)} ${files.length === 1 ? "file" : "files"}`;
    if (added > 0) {
      fact += ` +${String(added)}`;
    }
    if (removed > 0) {
      fact += ` \u2212${String(removed)}`;
    }
    facts.push(fact);
  }
  // `kindCounts` is the ONLY count on the wire now: the `commands`/`reads` aggregates
  // beside it were deleted once this projection became the gate, because nothing
  // rendered them and the kind map carries the same walk at finer grain.
  const kinds = sortedKinds(d.kindCounts ?? {});
  for (const [kind, n] of kinds) {
    if (COMMAND_KINDS.has(kind)) {
      facts.push(`${String(n)} ${kindNoun(kind, n)}`);
    }
  }
  const delegates = d.delegateCount ?? 0;
  if (delegates > 0) {
    facts.push(`${String(delegates)} ${delegates === 1 ? "delegate" : "delegates"}`);
  }
  for (const [kind, n] of kinds) {
    if (!COMMAND_KINDS.has(kind)) {
      facts.push(`${String(n)} ${kindNoun(kind, n)}`);
    }
  }
  const wall = d.elapsedMs ?? 0;
  if (wall > 0) {
    facts.push(formatElapsed(wall));
  }
  const credits = d.credits ?? 0;
  if (credits > 0) {
    facts.push(`${creditFigure(credits)} credits`);
  }
  return facts;
}

/** A metered turn's credits, at the two places that state them. `toFixed(2)` alone
 *  renders a sub-cent charge as `0.00`, asserting the opposite of the `> 0` gate that let
 *  it through. Measured 0 of 941 metered turns, min 0.036, so this is correctness at the
 *  boundary rather than a shape anything has produced. */
function creditFigure(credits: number): string {
  return credits < 0.005 ? "<0.01" : credits.toFixed(2);
}

/** One `label · value` row. `<li>` because every section's rows are a list. */
function infoRow(label: string, value: string | Node): HTMLElement {
  return el(
    "li",
    { className: "turn-info-row" },
    el("span", { className: "turn-info-label" }, label),
    el("span", { className: "turn-info-value" }, value),
  );
}

/** One section, or null when it has nothing to state. Withholding is the panel's whole
 *  discipline: a delegate can fill three of the six and a mid-flight turn fewer, so a
 *  section that painted itself on absence would grow empty rows on most cards. */
function infoSection(title: string, rows: HTMLElement[], extra: Node[] = []): HTMLElement | null {
  if (rows.length === 0 && extra.length === 0) {
    return null;
  }
  const section = el("section", { className: "turn-info-section" });
  section.appendChild(el("h4", { className: "turn-info-title" }, title));
  section.append(...extra);
  if (rows.length > 0) {
    section.appendChild(el("ul", { className: "turn-info-rows" }, ...rows));
  }
  return section;
}

/** A wall-clock STAMP as a `<time>`: the machine-readable instant in `datetime`, the
 *  reader's own locale in the text. Same pair, from the same value, as `turn-header.ts`
 *  writes, so the panel's "Started" and the header's timestamp cannot disagree. */
function stampEl(ms: number): HTMLElement {
  const when = new Date(ms);
  const t = el(
    "time",
    {},
    when.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" }),
  );
  t.setAttribute("datetime", when.toISOString());
  return t;
}

/** Three durations and two stamps, each withheld on absence. MODEL TIME is withheld
 *  rather than clamped: tool calls overlap, so wall minus tool goes negative and a clamped
 *  zero would assert a measurement nobody made — and with no tool time the row would
 *  restate the wall clock under a second name. `endedAt` is read as a STAMP. */
function timingRows(d: TurnSummaryData): HTMLElement[] {
  const rows: HTMLElement[] = [];
  const wall = d.elapsedMs ?? 0;
  const tool = d.toolMs ?? 0;
  if (wall > 0) {
    rows.push(infoRow("Wall clock", formatElapsed(wall)));
  }
  if (tool > 0) {
    rows.push(infoRow("Tool time", formatElapsed(tool)));
  }
  if (tool > 0 && wall > tool) {
    rows.push(infoRow("Model time", formatElapsed(wall - tool)));
  }
  if ((d.startedAt ?? 0) > 0) {
    rows.push(infoRow("Started", stampEl(d.startedAt ?? 0)));
  }
  // WITHHELD while the turn is still running, because `endedAt` is non-zero long before
  // the turn ends: a turn's is the LAST BODY MESSAGE's `ts` (`turns.ts` `turnLedger`),
  // which on a live turn is whenever the reply's first chunk landed — and the stamp is
  // minute-precision, so the row painted the START time under the word "Ended". Keyed on
  // the OUTCOME rather than on the two stamps matching: a genuinely sub-minute turn would
  // lose a legitimate row, and a running turn whose end has drifted a millisecond would go
  // on lying. Same signal Diagnostics reads below, for the same reason — a running turn has
  // not ended at all.
  if ((d.endedAt ?? 0) > 0 && (d.outcome ?? "completed") !== "running") {
    rows.push(infoRow("Ended", stampEl(d.endedAt ?? 0)));
  }
  return rows;
}

/** One row per non-zero tool kind, named through the shared noun vocabulary. Sorted by
 *  count then kind, so two repaints cannot reshuffle the rows: `kindCounts` is built in
 *  arrival order, which is not a fact about the turn worth showing. */
function kindRows(counts: Partial<Record<ToolKind, number>>): HTMLElement[] {
  return sortedKinds(counts).map(([kind, n]) => infoRow(kindNoun(kind, n), String(n)));
}

/** The non-zero kinds in the panel's order, read by the panel's rows and by the row's
 *  facts alike so the two surfaces cannot disagree about it. */
function sortedKinds(counts: Partial<Record<ToolKind, number>>): [ToolKind, number][] {
  const entries = Object.entries(counts) as [ToolKind, number][];
  return entries.filter(([, n]) => n > 0).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
}

/** The six sections, in order, each withheld when it has nothing to state. Rebuilt
 *  wholesale but only when the data moved: `updateTurnFooter` runs at chunk cadence on a
 *  live turn, and every file row is a button with a tooltip that opens a diff. */
function renderInfoPanel(
  panel: HTMLElement,
  d: TurnSummaryData,
  files: [string, FileChange][],
): void {
  if (!sigChanged(panel, panelSignature(d))) {
    return;
  }
  const sections: HTMLElement[] = [];

  const timings = infoSection("Timings", timingRows(d));
  if (timings !== null) {
    sections.push(timings);
  }

  // The file rows keep their own `<ul class="turn-ledger-files">`, nested here
  // inside Work: `renderFileRows`, `fileRow` and `reviewRow` all still address it,
  // and the list is what the disclosure used to BE before the panel grew around it.
  const kinds = kindRows(d.kindCounts ?? {});
  const list = el("ul", { className: "turn-ledger-files" });
  renderFileRows(list, files);
  const work = infoSection("Work", kinds, files.length > 0 ? [list] : []);
  if (work !== null) {
    sections.push(work);
  }

  const delegateRows: HTMLElement[] = [];
  if ((d.delegateCount ?? 0) > 0) {
    delegateRows.push(infoRow("Dispatched", String(d.delegateCount ?? 0)));
  }
  if ((d.delegateMs ?? 0) > 0) {
    delegateRows.push(infoRow("Time", formatElapsed(d.delegateMs ?? 0)));
  }
  const delegates = infoSection("Delegates", delegateRows);
  if (delegates !== null) {
    sections.push(delegates);
  }

  const cost = infoSection(
    "Cost",
    (d.credits ?? 0) > 0 ? [infoRow("Credits", creditFigure(d.credits ?? 0))] : [],
  );
  if (cost !== null) {
    sections.push(cost);
  }

  // Arrow when a mid-turn switch split the turn, which is the one case where the
  // plural matters: two names in order say the turn changed hands.
  const models = d.models ?? [];
  const model = infoSection(
    "Model",
    models.length > 0 ? [infoRow("Answered by", models.join(" \u2192 "))] : [],
  );
  if (model !== null) {
    sections.push(model);
  }

  // ONLY on a turn that did not end clean. On a clean turn the stop reason is the
  // ordinary one and `truncated` is false, so the section would be a heading over
  // the absence of news — and `running` has not ended at all, so it has no verdict
  // to diagnose. The raw reason is rendered VERBATIM and nothing branches on it:
  // the wire declares that enum OPEN, so `outcome` is what any decision reads.
  const outcomeNow = d.outcome ?? "completed";
  const unclean = outcomeNow !== "completed" && outcomeNow !== "running";
  const diagRows: HTMLElement[] = [];
  if (unclean && (d.stopReasonRaw ?? "") !== "") {
    diagRows.push(infoRow("Stop reason", d.stopReasonRaw ?? ""));
  }
  if (unclean && d.truncated === true) {
    diagRows.push(infoRow("Truncated", "yes"));
  }
  const diagnostics = infoSection("Diagnostics", diagRows);
  if (diagnostics !== null) {
    sections.push(diagnostics);
  }

  panel.replaceChildren(...sections);
}

/** Everything the panel renders, as signature parts. Total over `TurnSummaryData` by
 *  TYPE, which is what a signature needs: a field added to that interface fails the type
 *  check here rather than leaving a panel that stops updating. */
function panelSignature(d: TurnSummaryData): string[] {
  const num = (n: number | undefined): string => (n === undefined ? "" : String(n));
  const parts: Record<keyof TurnSummaryData, string> = {
    credits: num(d.credits),
    elapsedMs: num(d.elapsedMs),
    changedFiles: filesSignature(d.changedFiles),
    models: join(...(d.models ?? [])),
    outcome: d.outcome ?? "",
    toolMs: num(d.toolMs),
    kindCounts: join(...sortedKinds(d.kindCounts ?? {}).flatMap(([k, n]) => [k, String(n)])),
    delegateCount: num(d.delegateCount),
    delegateMs: num(d.delegateMs),
    startedAt: num(d.startedAt),
    endedAt: num(d.endedAt),
    stopReasonRaw: d.stopReasonRaw ?? "",
    truncated: d.truncated === true ? "1" : "",
  };
  return Object.values(parts);
}

/** SORTED by path, matching `renderFileRows`' own sort: a `Record`'s insertion order
 *  is the order the paths happened to arrive in, so an unsorted signature would move
 *  for a set that did not change and repaint the panel for nothing. */
function filesSignature(files: Record<string, FileChange> | undefined): string {
  return join(
    ...Object.entries(files ?? {})
      .sort((a, b) => a[0].localeCompare(b[0]))
      .flatMap(([path, fc]) => [
        path,
        String(fc.lines_added),
        String(fc.lines_removed),
        fc.is_new_file === true ? "1" : "",
      ]),
  );
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

  // The path is the row's ink AND what the hover text is about, so it is where the
  // tooltip points: the button spans the panel's width while its content sits at
  // the leading edge, which put the tip 264px to the right of the name.
  btn.appendChild(el("span", { className: "turn-file-path", "data-tooltip-anchor": "" }, path));
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

/** The attribute NAME is a three-surface fact — this writer, three selectors in
 *  `29-turns.css`, two hand-built test fixtures — and a half-finished rename fails
 *  SILENTLY: CSS keyed on an attribute nothing writes leaves the panel shut forever. */
function infoOpen(footer: HTMLElement): boolean {
  return footer.dataset["info"] === "open";
}

function setInfoOpen(footer: HTMLElement, on: boolean): void {
  if (on) {
    footer.dataset["info"] = "open";
  } else {
    delete footer.dataset["info"];
  }
  const summary = footer.querySelector<HTMLButtonElement>(":scope > .turn-ledger-summary");
  if (summary !== null) {
    summary.setAttribute("aria-expanded", on ? "true" : "false");
  }
  syncLedgerTooltip(footer);
}

/** The trigger's hover text: ONE clause, what the click does. It names no outcome —
 *  OUTCOME_LEAD is total, so the row already says it, and a status whose only channel is
 *  a hover has no channel at all on a phone. */
function syncLedgerTooltip(footer: HTMLElement): void {
  const summary = footer.querySelector<HTMLButtonElement>(":scope > .turn-ledger-summary");
  if (summary === null) {
    return;
  }
  summary.setAttribute(
    "data-tooltip",
    infoOpen(footer) ? "Hide turn details" : "Show turn details",
  );
}
