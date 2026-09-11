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
import { iconEl } from "../icon-el.js";
import { ICON_INFO } from "../icons.js";
import { openChange, openChangeSet } from "../navigate.js";
import { formatElapsed, isoDuration } from "../strings.js";
import { kindNoun } from "../tool-kind-noun.js";
import { severityOf } from "../turn-severity.js";
import type { FileChange, ToolKind } from "../types.js";
import type { TurnOutcome } from "../turns.js";

// ---------------------------------------------------------------------------
// THE EXPORT BOUNDARY, and it has TWO consumers.
//
// `messages.ts` mounts this on a turn card; `fundamentals/subagent-block.ts`
// mounts the same three exports on a DELEGATE card through its `setSummary`,
// which is why `TurnSummaryData`'s panel fields are optional rather than
// required. A change here reaches both surfaces, and the delegate one has no
// test of its own inside this file — `fundamentals/subagent-block.test.ts` is
// where that half is pinned.
//
// THREE PANEL SECTIONS A DELEGATE CAN NEVER FILL: Cost, Model and Diagnostics.
// Nothing on the ACP wire carries credits, a model id or a stop reason PER
// delegate — `messages-blocks.ts`'s two producers say so at their own literals —
// so those three withhold on every delegate card, permanently, and that is the
// designed outcome rather than a gap to fill later. Timings, Work and Delegates
// are the three a delegate does fill.
// ---------------------------------------------------------------------------

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
  /** The gap before this turn: how long after the previous turn's start this one
   *  began. ABSENT means no predecessor in the window, which is a different fact
   *  from a gap of zero, so callers must not fold one into the other. */
  sinceMs?: number;
  /** The model(s) that answered, distinct and in order. Rendered as one name
   *  normally and `a -> b` when a switch split the turn. Absent on every turn
   *  persisted before the field existed. */
  models?: string[];
  /** The turn's result, carried as the footer's tint so outcome is scannable
   *  down the transcript without reading a word. */
  outcome?: TurnOutcome;
  /** The info panel's facts, mirroring `TurnLedger`, which owns each field's
   *  ABSENCE RULE — read it there before rendering any of them. In short: a
   *  duration nobody stamped is not a duration of zero, `kindCounts` omits a kind
   *  rather than reporting zero of it, `toolMs` is not bounded by `elapsedMs`, and
   *  `endedAt` is a stamp rather than `startedAt + elapsedMs`.
   *
   *  OPTIONAL, and that is load-bearing rather than incidental: a DELEGATE can fill
   *  some of them and none of the rest, so `messages-blocks.ts`'s two producers
   *  construct this type with whatever the wire actually carries per delegate. A
   *  panel section withholds on absence; it never invents a zero. */
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

/** Whether the footer is earned — ONE predicate, and it used to be two.
 *  `hasTurnSummary` answered only "is there a ledger", and `messages.ts` kept a
 *  second inline expression beside it for the two reasons a footer survives an
 *  unstamped ledger. Two statements of one question is how the turn card and the
 *  delegate card come to disagree about when a footer exists.
 *
 *  The LEDGER half: a non-clean outcome always qualifies even with no numbers,
 *  since an interrupted or failed turn is exactly when a reader needs to know
 *  what landed. `models` is deliberately NOT admitted — every completed turn has
 *  one, so counting it would put a footer on every turn in the transcript,
 *  including the ones this rule exists to suppress. Nor are the info panel's own
 *  fields (tool time, kind counts, timestamps): a turn whose only content is a
 *  measured duration of tool work still earns no footer, because the panel is
 *  reached THROUGH the ledger row and a row with nothing to lead with is not a
 *  door worth painting.
 *
 *  `sinceMs` IS admitted, and it is the one panel-only field that is, because it
 *  is the only one no other surface states: the turn's own duration is painted in
 *  the row beside this gate, while the GAP before it is drawn as the rail's seam
 *  and nowhere else — so a clean turn with no credits, no commands, no reads and
 *  no changed files would otherwise carry no door to reach it through.
 *
 *  The EXTRAS half: the footer also carries the turn ACTIONS and Rewind, so
 *  settled prose to act on or a rewind target keeps it — an unstamped ledger must
 *  not cost the reader the buttons. A delegate card has neither, which is why
 *  they are parameters rather than fields on the summary. */
export function earnsTurnFooter(d: TurnSummaryData, extra: FooterExtras = {}): boolean {
  return (
    (d.outcome !== undefined && d.outcome !== "completed" && d.outcome !== "running") ||
    (d.credits ?? 0) > 0 ||
    (d.elapsedMs ?? 0) > 0 ||
    (d.sinceMs ?? 0) > 0 ||
    (d.commands ?? 0) > 0 ||
    (d.reads ?? 0) > 0 ||
    Object.keys(d.changedFiles ?? {}).length > 0 ||
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
  // AN `i`, NOT A CHEVRON, and it LEADS. The row is a door onto turn INFORMATION on
  // every turn rather than onto the rest of its own line, so the glyph names the
  // content instead of the mechanism; a chevron would be the app's disclosure
  // vocabulary (chevron.ts) claiming a panel that is not more of this row.
  //
  // IT LEADS BECAUSE ITS POSITION MUST NOT DEPEND ON CONTENT. Built after the
  // outcome text it sat at x=319 on a clean turn and x=366 on a failed one, so the
  // one affordance in the row moved by 47px depending on whether the turn ended
  // badly, and beside the word it read as belonging to `Failed` rather than to the
  // row. Measured at 900px; leading holds it at 289px in both.
  //
  // Both glyphs shipped here for one commit and that was the defect: two adjacent
  // signs for one door, with nothing between them on the ~48% of turns that end
  // clean and carry no outcome word.
  summary.appendChild(el("span", { className: "turn-ledger-info" }, iconEl(ICON_INFO)));
  summary.appendChild(el("span", { className: "turn-ledger-glyph" }));
  summary.appendChild(el("span", { className: "turn-ledger-text" }));
  // THE BUTTON'S NAME COMES FROM ITS CONTENT, and this span is the whole of it on
  // a clean turn. There is deliberately no `aria-label`: a label WINS over the
  // element's own text, so one here would hide the outcome word — which is the
  // exact defect `OUTCOME_LEAD` exists to fix, reintroduced one attribute later.
  // The computed name is `Cancelled Turn details`, or `Turn details` alone when the
  // turn ended clean and the text is empty. Without this span that clean case is a
  // button with NO accessible name at all, since the caret and the `i` are
  // decorative and the glyph is empty.
  summary.appendChild(el("span", { className: "sr-only" }, "Turn details"));
  summary.addEventListener("click", () => {
    setInfoOpen(footer, !infoOpen(footer));
  });
  footer.appendChild(summary);

  // The turn's own time, out of the ledger string and into a slot of its own —
  // right-aligned, always painted (29-turns.css), and a real `<time>` so the value
  // is machine-readable as well as legible. Built unconditionally; `syncElapsed`
  // decides whether it says anything.
  footer.appendChild(el("time", { className: "turn-elapsed" }));

  footer.appendChild(el("div", { className: "turn-info-panel" }));

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
  if (text !== null) {
    text.textContent = summaryLine(d);
  }

  syncElapsed(footer, d.elapsedMs ?? 0);

  // ALWAYS a disclosure, so `aria-expanded` is written unconditionally. It used to
  // be one only when the turn changed files, on the rule that an inert button is
  // worse than a plain readout — true then, and moot now: every turn has a panel
  // to open, because the facts the panel states (how long, what ran, what it cost)
  // exist on every turn that earned a footer at all. `summary.disabled` and the
  // `removeAttribute("aria-expanded")` arm are gone with it, and so is the reset
  // that closed the panel underneath a reader whose turn lost its last file.
  if (summary !== null) {
    summary.setAttribute("aria-expanded", infoOpen(footer) ? "true" : "false");
    syncLedgerTooltip(footer);
  }

  const panel = footer.querySelector<HTMLElement>(":scope > .turn-info-panel");
  if (panel !== null) {
    renderInfoPanel(panel, d, files);
  }
}

/** The ledger LINE: the outcome's lead word, and nothing else.
 *
 *  It used to compose up to six `·`-separated clauses — files, commands, reads,
 *  credits, the model — in a fixed order this comment used to argue for. Every one
 *  of them moved into the info panel below, where each is a labelled row instead of
 *  a token in a dense string a reader has to parse. What is left is the ONE thing
 *  the row has to say before it is opened: how the turn ended.
 *
 *  No `?? ""` on the lookup, and the linter is what insists: OUTCOME_LEAD is a
 *  total `Record<TurnOutcome, string>`, so indexing it with a `TurnOutcome` yields
 *  a string and the fallback would be dead code. An absent outcome defaults its
 *  KEY instead, and a value the wire adds later cannot arrive here at all — the
 *  generated decoder rejects it at the boundary, which is the same guarantee every
 *  other consumer of this union relies on. */
function summaryLine(d: TurnSummaryData): string {
  return OUTCOME_LEAD[d.outcome ?? "completed"];
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

/** One section, or null when it has nothing to state. Withholding rather than
 *  rendering an empty heading is the panel's whole discipline: a delegate can fill
 *  three of the six and a mid-flight turn fewer, so a section that painted itself
 *  on absence would grow empty rows on most cards. */
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

/** A wall-clock STAMP as a `<time>`: the machine-readable instant in `datetime`,
 *  the reader's own locale and 24h-or-not preference in the text. Same pair, from
 *  the same value, as `fundamentals/turn-header.ts` writes for the turn's start —
 *  so the panel's "Started" and the header's timestamp cannot disagree. */
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

/** The timings section. Three durations and two stamps, each withheld on absence.
 *
 *  MODEL TIME IS WITHHELD RATHER THAN CLAMPED. It is wall minus tool, and tool
 *  calls can overlap, so Σ`duration_ms` legitimately exceeds the turn's wall clock
 *  and the difference goes negative — reporting a clamped zero would assert a
 *  measurement nobody made. It is also withheld when there is NO measured tool
 *  time, because then the subtraction adds nothing: the row would restate the wall
 *  clock verbatim under a second name.
 *
 *  `endedAt` is a STAMP, not `startedAt + elapsedMs`. A turn's `turn_elapsed_ms` is
 *  the agent's own measured duration and excludes admission wait, so the two are
 *  read independently and neither is derived from the other. */
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
  if ((d.endedAt ?? 0) > 0) {
    rows.push(infoRow("Ended", stampEl(d.endedAt ?? 0)));
  }
  // THE GAP BEFORE THIS TURN, and this panel is the only place it is stated in
  // words. The rail draws it as a seam between two sittings and its marker names it
  // in a tooltip, so a reader without a pointer had no path to it at all — which is
  // why `earnsTurnFooter` admits `sinceMs` even though every other panel-only field
  // is refused. ABSENT is not zero: no predecessor in the window is a different fact
  // from two turns starting together, so the test is `undefined` rather than a
  // truthiness check that would fold the two together.
  if (d.sinceMs !== undefined) {
    rows.push(infoRow("Gap before", formatElapsed(d.sinceMs)));
  }
  return rows;
}

/** One row per non-zero tool kind, named through the shared noun vocabulary
 *  (`tool-kind-noun.ts`) so the panel and a tool group's mixed summary call the
 *  same kind the same thing.
 *
 *  Sorted by count and then by kind, so two repaints of one turn cannot reshuffle
 *  the rows: `kindCounts` is built in the ledger's own walk order, which is arrival
 *  order, and arrival order is not a fact about the turn worth showing. A kind with
 *  no calls is ABSENT from the map rather than zero, which is the ledger's stated
 *  absence rule and is why nothing here has to filter zeroes it invented. */
function kindRows(counts: Partial<Record<ToolKind, number>>): HTMLElement[] {
  const entries = Object.entries(counts) as [ToolKind, number][];
  return entries
    .filter(([, n]) => n > 0)
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .map(([kind, n]) => infoRow(kindNoun(kind, n), String(n)));
}

/** The six sections, in order, each withheld when it has nothing to state.
 *
 *  Rebuilt wholesale on every repaint rather than patched row by row: the sections
 *  ARE a function of the data, and the reader's own state — whether the panel is
 *  open — lives on the footer's `data-info` attribute rather than in here, so
 *  replacing the contents cannot close a panel under them. */
function renderInfoPanel(
  panel: HTMLElement,
  d: TurnSummaryData,
  files: [string, FileChange][],
): void {
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
    (d.credits ?? 0) > 0 ? [infoRow("Credits", (d.credits ?? 0).toFixed(2))] : [],
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

/** The turn's elapsed time, or "" when there is none to show. The ZERO TEST kept
 *  apart from the formatting, because `syncElapsed` makes three writes off one
 *  answer — the text, the `hidden` flag and the `datetime` attribute — and a turn
 *  that HAS a duration must not be able to get two of them and not the third. */
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
 *  element in the accessibility tree. The reserved box the stylesheet protects is
 *  the box of a slot that HAS a value; an empty one holds nothing worth reserving,
 *  so hiding it moves nothing a reader was reading. */
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

/** `data-info`, and it was `data-files` until the disclosure stopped being a file
 *  list. The attribute NAME is a three-surface fact — this writer, three selectors
 *  in `29-turns.css`, and two hand-built test fixtures — and a half-finished rename
 *  fails SILENTLY in the worst direction: CSS keyed on an attribute nothing writes
 *  paints nothing, so the panel is permanently closed with no error anywhere. */
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

/** The trigger's hover text: what the row does NOT already say.
 *
 *  ONE clause — what the click does. It is written unconditionally now, because the
 *  panel exists on every footer; the clause that withheld it on a readout with
 *  nothing to disclose is unreachable, since there is no readout state left.
 *
 *  It used to carry a second clause naming the outcome for the three states the
 *  ledger line left unexplained. That clause is gone because those states are
 *  explained IN THE ROW now (OUTCOME_LEAD is total): a status whose only channel is
 *  a hover has no channel at all on a phone, and repeating a word the line already
 *  leads with was the defect the split existed to avoid. */
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
