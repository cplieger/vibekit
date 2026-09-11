// The turn card's outcome ledger and the INFO PANEL it discloses.
//
// The row says ONE thing — how the turn ended — and the panel under it says
// everything else in labelled rows: timings, the work, the delegates, the cost, the
// model, and diagnostics on a turn that did not end clean. Every section withholds
// on absence, which is most of what this file pins: a delegate can fill three of the
// six and a mid-flight turn fewer, so a section that painted itself on absence would
// grow empty headings on most cards in a transcript.
//
// IT USED TO PIN A COMPOSED STRING. The whole first describe asserted the ledger
// line as one `·`-separated join (`2 files +6 −2 · 3 cmds · 12 reads · 1.50 cr ·
// sonnet-4`) and six more cases asserted the trigger's `aria-label` and its
// disabled/expandable branches. Those are gone with the string and the readout
// state; what replaced them is the lead word asserted EXACTLY for all seven
// outcomes, the panel's own rows, and the button's name read through the role query.
import { describe, it, expect, vi, beforeAll, afterAll } from "vitest";
import { page } from "vitest/browser";

import { loadCSS } from "../__test-helpers__/css-rules.js";

const openFileGitDiff = vi.fn();
vi.mock("../editor-openers.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  openFile: undefined,
  openFileDiff: undefined,
  openFileGitDiff: (path: string) => {
    openFileGitDiff(path);
  },
}));

const { buildTurnFooter, updateTurnFooter, earnsTurnFooter } = await import("./turn-footer.js");

function line(el: HTMLElement): string {
  return el.querySelector(".turn-ledger-text")?.textContent ?? "";
}

/** The turn's own time, which left the ledger string for a right-aligned slot of
 *  its own. Returns "" for a turn with no duration, matching the element's own
 *  empty state. */
function elapsed(el: HTMLElement): string {
  return el.querySelector(".turn-elapsed")?.textContent ?? "";
}

function elapsedEl(el: HTMLElement): HTMLTimeElement {
  const t = el.querySelector<HTMLTimeElement>(".turn-elapsed");
  if (t === null) {
    throw new Error("no .turn-elapsed");
  }
  return t;
}

function summary(el: HTMLElement): HTMLButtonElement {
  const b = el.querySelector<HTMLButtonElement>(".turn-ledger-summary");
  if (b === null) {
    throw new Error("no .turn-ledger-summary");
  }
  return b;
}

function panel(el: HTMLElement): HTMLElement {
  const p = el.querySelector<HTMLElement>(":scope > .turn-info-panel");
  if (p === null) {
    throw new Error("no .turn-info-panel");
  }
  return p;
}

/** The panel's section headings in DOM order — which is the order a reader meets
 *  them AND the withholding rule's own observable, since a withheld section is an
 *  absent heading rather than an empty one. */
function sections(el: HTMLElement): string[] {
  return [...panel(el).querySelectorAll(".turn-info-title")].map((h) => h.textContent ?? "");
}

/** One section's rows as `[label, value]` pairs, in DOM order. Answers `[]` both for
 *  a section that is absent and for one carrying no labelled rows (Work, when the
 *  turn changed files and ran no other tool) — `sections()` above is what tells the
 *  two apart, so a withholding case asserts on that instead. */
function sectionRows(el: HTMLElement, title: string): [string, string][] {
  for (const s of panel(el).querySelectorAll(".turn-info-section")) {
    if ((s.querySelector(".turn-info-title")?.textContent ?? "") !== title) {
      continue;
    }
    return [...s.querySelectorAll(".turn-info-row")].map((r) => [
      r.querySelector(".turn-info-label")?.textContent ?? "",
      r.querySelector(".turn-info-value")?.textContent ?? "",
    ]);
  }
  return [];
}

function rows(el: HTMLElement): HTMLButtonElement[] {
  return [...el.querySelectorAll<HTMLButtonElement>(".turn-file-row")];
}

/** The trigger's COMPUTED accessible name, through the role query rather than by
 *  reading an attribute: the name is built from the button's own contents now, so
 *  the only honest assertion is the one the accessibility tree answers.
 *
 *  THE `.sr-only` RULE HAS TO BE MOUNTED FOR THIS, and that is a measurement rather
 *  than a convenience. Name-from-content concatenates a child's text with no
 *  separator when the child is INLINE, so with no stylesheet the name computes as
 *  `CancelledTurn details` — measured in this container's Chromium, both spellings
 *  probed. `40-a11y.css` makes `.sr-only` `position: absolute`, which is a block
 *  box, and Chromium then inserts the space: `Cancelled Turn details`. So the shipped
 *  name depends on the shipped stylesheet, and a CSS-less assertion here would pin a
 *  string no reader ever hears. */
async function expectName(footer: HTMLElement, name: string): Promise<void> {
  document.body.replaceChildren(footer);
  await expect.element(page.getByRole("button", { name, exact: true })).toBeInTheDocument();
}

const TWO_FILES = {
  "b.ts": { lines_added: 1, lines_removed: 0 },
  "a.ts": { lines_added: 5, lines_removed: 2 },
};

describe("the ledger row", () => {
  it("says EXACTLY the outcome's lead word, for all seven outcomes", () => {
    // TOTAL over `TurnOutcome`, and the point of asserting it as an equality rather
    // than a `toContain` is that nothing else may join it: the numbers a reader used
    // to have to parse out of this row are rows in the panel now. `completed` and
    // `running` say NOTHING — the absence of a mark IS the clean case, and the footer
    // reports how a turn ENDED rather than one still going — and both are asserted
    // against a turn that spent credits and ran commands, so a clause creeping back
    // in fails here.
    for (const [outcome, word] of [
      ["running", ""],
      ["completed", ""],
      ["cancelled", "Cancelled"],
      ["interrupted", "Interrupted"],
      ["failed", "Failed"],
      ["refused", "Refused"],
      ["unknown", "Outcome unknown"],
    ] as const) {
      const el = buildTurnFooter({
        outcome,
        credits: 1.5,
        elapsedMs: 92000,
        commands: 3,
        reads: 12,
        models: ["sonnet-4"],
        changedFiles: TWO_FILES,
      });
      expect(line(el), `${outcome} says only its lead word`).toBe(word);
    }
  });

  it("defaults the KEY rather than the value when the turn carries no outcome", () => {
    expect(line(buildTurnFooter({ credits: 1 }))).toBe("");
  });

  it("recomputes in place", () => {
    const el = buildTurnFooter({ outcome: "failed", credits: 0.5 });
    expect(line(el)).toBe("Failed");
    updateTurnFooter(el, { outcome: "cancelled", credits: 1, elapsedMs: 3000 });
    expect(line(el)).toBe("Cancelled");
    expect(elapsed(el)).toBe("3.0s");
    expect(sectionRows(el, "Cost")).toEqual([["Credits", "1.00"]]);
  });
});

// ---------------------------------------------------------------------------
// The turn's own time, in its own slot.
//
// It was one `·`-separated part of the ledger string, sixth of up to seven, in a
// line that otherwise reports cost — so "how long did that take", which a reader
// asks of a turn often, was the hardest thing on the row to find. It now sits
// right-aligned in a `<time>` of its own, painted unconditionally.
// ---------------------------------------------------------------------------

describe("the turn's own time", () => {
  it("renders the same span the ledger used to spell", () => {
    // The thresholds are `formatElapsed`'s and did not move: a tenth of a second
    // below a minute, whole seconds above it, floored rather than rounded.
    expect(elapsed(buildTurnFooter({ elapsedMs: 45500 }))).toBe("45.5s");
    expect(elapsed(buildTurnFooter({ elapsedMs: 90000 }))).toBe("1m 30s");
    expect(elapsed(buildTurnFooter({ elapsedMs: 119999 }))).toBe("1m 59s");
    expect(elapsed(buildTurnFooter({ elapsedMs: 7_200_000 }))).toBe("2h 0m");
  });

  it("carries a machine-readable duration beside the text", () => {
    // What a `<time>` is FOR, and it costs nothing: both spellings come from one
    // value, so they cannot disagree.
    expect(elapsedEl(buildTurnFooter({ elapsedMs: 92000 })).dateTime).toBe("PT1M32S");
    expect(elapsedEl(buildTurnFooter({ elapsedMs: 400 })).dateTime).toBe("PT0.4S");
    expect(elapsedEl(buildTurnFooter({ elapsedMs: 7_200_000 })).dateTime).toBe("PT2H");
  });

  it("makes no claim at all for a turn that has no duration", () => {
    // Not a zero span: a duration nobody stamped is not a duration of zero, and a
    // `<time>` with neither a `datetime` nor valid content is not a conforming
    // `<time>` — so the element is emptied AND hidden rather than asserting `PT0S`.
    const el = buildTurnFooter({ credits: 1 });
    expect(elapsed(el)).toBe("");
    expect(elapsedEl(el).hasAttribute("datetime")).toBe(false);
    expect(elapsedEl(el).hidden).toBe(true);
  });

  it("clears itself when a repaint drops the duration", () => {
    const el = buildTurnFooter({ elapsedMs: 3000 });
    expect(elapsedEl(el).hasAttribute("datetime")).toBe(true);
    expect(elapsedEl(el).hidden).toBe(false);
    updateTurnFooter(el, { credits: 1 });
    expect(elapsed(el)).toBe("");
    expect(elapsedEl(el).hasAttribute("datetime")).toBe(false);
    expect(elapsedEl(el).hidden).toBe(true);
  });

  it("comes back when a later repaint has one", () => {
    // The reachable direction: `updateTurnFooter` runs on every paint and a turn's
    // duration is stamped at turn end, so the footer exists before the value does.
    const el = buildTurnFooter({ commands: 2 });
    expect(elapsedEl(el).hidden).toBe(true);
    updateTurnFooter(el, { commands: 2, elapsedMs: 3000 });
    expect(elapsed(el)).toBe("3.0s");
    expect(elapsedEl(el).hidden).toBe(false);
  });

  it("is the footer's own child, so the grid can place it", () => {
    // `:scope >` is how every one of the footer's own readers addresses its parts,
    // and the stylesheet places this one by `.turn-footer > .turn-elapsed`.
    const el = buildTurnFooter({ elapsedMs: 1000 });
    expect(el.querySelector(":scope > .turn-elapsed")).not.toBeNull();
  });
});

// ---------------------------------------------------------------------------
// The trigger. Always a disclosure, named by its CONTENT.
// ---------------------------------------------------------------------------

describe("the trigger", () => {
  // Only `.sr-only` matters here, so this is the one slice rather than the whole
  // bundle — see `expectName` for why the rule is load-bearing on the NAME.
  let a11y: HTMLStyleElement;

  beforeAll(() => {
    a11y = document.createElement("style");
    a11y.textContent = loadCSS("40-a11y.css");
    document.head.appendChild(a11y);
  });

  afterAll(() => {
    a11y.remove();
  });

  it("is a disclosure on every footer, however little the turn did", () => {
    // It used to be `disabled` unless the turn changed files, on the rule that an
    // inert button is worse than a plain readout. Every footer has a panel now, so
    // there is nothing left to be inert about — and `aria-expanded` is written
    // unconditionally rather than removed on the readout branch.
    const el = buildTurnFooter({ credits: 1, elapsedMs: 1000 });
    expect(rows(el)).toHaveLength(0);
    expect(summary(el).disabled).toBe(false);
    expect(summary(el).getAttribute("aria-expanded")).toBe("false");
  });

  it("opens and closes the panel", () => {
    const el = buildTurnFooter({ credits: 1 });
    expect(el.dataset["info"]).toBeUndefined();
    summary(el).click();
    expect(el.dataset["info"]).toBe("open");
    expect(summary(el).getAttribute("aria-expanded")).toBe("true");
    summary(el).click();
    expect(el.dataset["info"]).toBeUndefined();
    expect(summary(el).getAttribute("aria-expanded")).toBe("false");
  });

  it("keeps an open panel open across a repaint that empties the ledger", () => {
    // The reset that closed it is deleted: a repaint dropping the turn's last file
    // used to disable the button and force the disclosure shut under whoever had
    // opened it. The panel still has five other sections to state.
    const el = buildTurnFooter({ changedFiles: TWO_FILES, elapsedMs: 1000 });
    summary(el).click();
    updateTurnFooter(el, { elapsedMs: 1000 });
    expect(el.dataset["info"]).toBe("open");
    expect(summary(el).getAttribute("aria-expanded")).toBe("true");
  });

  it("names the ACTION on a clean turn, where the row itself says nothing", async () => {
    // D3's failure mode, and the reason this case exists beside the cancelled one
    // below: with the `aria-label` removed and no `.sr-only` span, a clean turn's
    // trigger has NO accessible name at all — the text is empty, the caret and the
    // `i` are decorative, and the glyph carries no text.
    const el = buildTurnFooter({ outcome: "completed", credits: 1 });
    expect(summary(el).hasAttribute("aria-label")).toBe(false);
    await expectName(el, "Turn details");
  });

  it("still carries the outcome word in the name of a turn that ended badly", async () => {
    // Why there is no `aria-label`: one would WIN over the button's own text and
    // hide this word, which is the exact defect `OUTCOME_LEAD` exists to fix.
    const el = buildTurnFooter({ outcome: "cancelled", credits: 1 });
    await expectName(el, "Cancelled Turn details");
  });

  it("carries the `i` glyph that says the row is a door", () => {
    const el = buildTurnFooter({ outcome: "completed", credits: 1 });
    expect(summary(el).querySelector(".turn-ledger-info svg")).not.toBeNull();
  });
});

// ---------------------------------------------------------------------------
// The info panel: six sections, each withheld when it has nothing to state.
// ---------------------------------------------------------------------------

describe("the info panel", () => {
  it("withholds every section on a turn with nothing to state", () => {
    // A cancel that beat the usage stamp: the footer is still earned (the reader
    // needs to know the turn was cancelled) and the panel has no fact to show, so it
    // renders no headings rather than six empty ones.
    const el = buildTurnFooter({ outcome: "cancelled" });
    expect(sections(el)).toEqual([]);
  });

  it("orders its sections timings, work, delegates, cost, model, diagnostics", () => {
    const el = buildTurnFooter({
      outcome: "failed",
      elapsedMs: 92000,
      toolMs: 30000,
      changedFiles: TWO_FILES,
      kindCounts: { read: 2 },
      delegateCount: 1,
      delegateMs: 9000,
      credits: 1.5,
      models: ["sonnet-4"],
      stopReasonRaw: "max_tokens",
      truncated: true,
    });
    expect(sections(el)).toEqual(["Timings", "Work", "Delegates", "Cost", "Model", "Diagnostics"]);
  });

  it("states the wall clock, the tool time, and the model time between them", () => {
    const el = buildTurnFooter({ elapsedMs: 92000, toolMs: 30000 });
    expect(sectionRows(el, "Timings")).toEqual([
      ["Wall clock", "1m 32s"],
      ["Tool time", "30.0s"],
      ["Model time", "1m 2s"],
    ]);
  });

  it("withholds model time when the tool calls overran the wall clock", () => {
    // Tool calls can overlap, so Σ `duration_ms` legitimately exceeds the turn's wall
    // clock and the difference goes negative. The panel states what it can prove
    // rather than clamping to a zero nobody measured — asserted at both the strictly
    // greater and the exactly equal boundary.
    expect(sectionRows(buildTurnFooter({ elapsedMs: 30000, toolMs: 45000 }), "Timings")).toEqual([
      ["Wall clock", "30.0s"],
      ["Tool time", "45.0s"],
    ]);
    expect(sectionRows(buildTurnFooter({ elapsedMs: 30000, toolMs: 30000 }), "Timings")).toEqual([
      ["Wall clock", "30.0s"],
      ["Tool time", "30.0s"],
    ]);
  });

  it("withholds model time when nothing measured any tool time", () => {
    // With no tool time there is nothing to subtract, so the row would restate the
    // wall clock verbatim under a second name.
    expect(sectionRows(buildTurnFooter({ elapsedMs: 30000, toolMs: 0 }), "Timings")).toEqual([
      ["Wall clock", "30.0s"],
    ]);
    expect(sectionRows(buildTurnFooter({ elapsedMs: 30000 }), "Timings")).toEqual([
      ["Wall clock", "30.0s"],
    ]);
  });

  it("renders the start and the end as machine-readable stamps", () => {
    const started = Date.UTC(2026, 0, 2, 9, 5, 0);
    const ended = started + 92000;
    const el = buildTurnFooter({ startedAt: started, endedAt: ended });
    expect(sectionRows(el, "Timings").map(([label]) => label)).toEqual(["Started", "Ended"]);
    const stamps = [...panel(el).querySelectorAll<HTMLTimeElement>("time")];
    expect(stamps).toHaveLength(2);
    expect(stamps[0]?.dateTime).toBe("2026-01-02T09:05:00.000Z");
    expect(stamps[1]?.dateTime).toBe("2026-01-02T09:06:32.000Z");
    // The TEXT is the reader's own locale and 12h-or-24h preference, so it is pinned
    // as a shape rather than as a string — hardcoding "09:05" would assert the
    // runner's locale, and recomputing it here would be a copy of the production
    // formatter asserting against itself.
    expect(stamps[0]?.textContent).toMatch(/\d{1,2}:\d{2}/u);
  });

  it("withholds a stamp nobody made", () => {
    // A running delegate has a start and no end, which is the reachable half.
    const el = buildTurnFooter({ startedAt: 1000 });
    expect(sectionRows(el, "Timings")).toHaveLength(1);
    expect(
      sectionRows(buildTurnFooter({ endedAt: 0, startedAt: 0, elapsedMs: 1000 }), "Timings"),
    ).toEqual([["Wall clock", "1.0s"]]);
  });

  it("puts the file rows and one row per tool kind under Work", () => {
    // The kind rows are sorted by count and then by kind, so two repaints of one turn
    // cannot reshuffle them: `kindCounts` is built in the ledger's walk order, which
    // is arrival order, and arrival order is not a fact about the turn worth showing.
    // `edit` before `execute` at the same count is that tie-break.
    const el = buildTurnFooter({
      changedFiles: TWO_FILES,
      kindCounts: { read: 12, execute: 3, edit: 3 },
    });
    expect(sections(el)).toEqual(["Work"]);
    expect(sectionRows(el, "Work")).toEqual([
      ["reads", "12"],
      ["edits", "3"],
      ["commands", "3"],
    ]);
    // The file `<ul>` is nested in this section, so the rows are still built and the
    // Review-changes row still lands under them.
    expect(panel(el).querySelector(".turn-info-section .turn-ledger-files")).not.toBeNull();
    expect(rows(el)).toHaveLength(2);
  });

  it("names a single call in the singular, through the shared kind vocabulary", () => {
    // `tool-kind-noun.ts` is the one owner of these words, read here and by a tool
    // group's mixed summary, so the two surfaces cannot call one kind two things.
    expect(sectionRows(buildTurnFooter({ kindCounts: { read: 1, mcp: 1 } }), "Work")).toEqual([
      ["integration call", "1"],
      ["read", "1"],
    ]);
  });

  it("withholds Work on a turn that touched no file and ran no tool", () => {
    expect(sections(buildTurnFooter({ elapsedMs: 1000, kindCounts: {} }))).toEqual(["Timings"]);
  });

  it("counts the delegates the turn dispatched, and their time", () => {
    expect(
      sectionRows(buildTurnFooter({ delegateCount: 2, delegateMs: 9000 }), "Delegates"),
    ).toEqual([
      ["Dispatched", "2"],
      ["Time", "9.0s"],
    ]);
  });

  it("withholds Delegates on a turn that dispatched none", () => {
    const el = buildTurnFooter({ elapsedMs: 1000, delegateCount: 0, delegateMs: 0 });
    expect(sections(el)).toEqual(["Timings"]);
  });

  it("states the credits to two places", () => {
    expect(sectionRows(buildTurnFooter({ credits: 1.5 }), "Cost")).toEqual([["Credits", "1.50"]]);
    expect(sectionRows(buildTurnFooter({ credits: 0.5 }), "Cost")).toEqual([["Credits", "0.50"]]);
  });

  it("withholds Cost on a turn nothing metered", () => {
    expect(sections(buildTurnFooter({ elapsedMs: 1000, credits: 0 }))).toEqual(["Timings"]);
  });

  it("names the model, and both models with an arrow on a mid-turn switch", () => {
    expect(sectionRows(buildTurnFooter({ models: ["sonnet-4"] }), "Model")).toEqual([
      ["Answered by", "sonnet-4"],
    ]);
    expect(sectionRows(buildTurnFooter({ models: ["sonnet-4", "opus-4"] }), "Model")).toEqual([
      ["Answered by", "sonnet-4 \u2192 opus-4"],
    ]);
  });

  it("withholds Model rather than inventing one", () => {
    // Absent on every turn persisted before the field existed, so this is the common
    // case rather than an edge.
    expect(sections(buildTurnFooter({ elapsedMs: 1000 }))).toEqual(["Timings"]);
    expect(sections(buildTurnFooter({ elapsedMs: 1000, models: [] }))).toEqual(["Timings"]);
  });

  it("states the raw stop reason and the truncation on a turn that ended badly", () => {
    // VERBATIM, and nothing branches on it: the wire declares that enum OPEN, so
    // `outcome` is what any decision reads.
    const el = buildTurnFooter({
      outcome: "failed",
      stopReasonRaw: "max_tokens",
      truncated: true,
    });
    expect(sectionRows(el, "Diagnostics")).toEqual([
      ["Stop reason", "max_tokens"],
      ["Truncated", "yes"],
    ]);
  });

  it("withholds Diagnostics on a clean turn and on one still running", () => {
    // On a clean turn the stop reason is the ordinary one, so the section would be a
    // heading over the absence of news; a running turn has not ended at all, so it
    // has no verdict to diagnose.
    for (const outcome of ["completed", "running"] as const) {
      const el = buildTurnFooter({
        outcome,
        elapsedMs: 1000,
        stopReasonRaw: "max_tokens",
        truncated: true,
      });
      expect(sections(el), `${outcome} has no diagnostics`).toEqual(["Timings"]);
    }
  });

  it("rebuilds its sections in place rather than stacking them", () => {
    const el = buildTurnFooter({ credits: 1 });
    updateTurnFooter(el, { credits: 1, elapsedMs: 1000 });
    updateTurnFooter(el, { credits: 1, elapsedMs: 1000 });
    expect(sections(el)).toEqual(["Timings", "Cost"]);
    expect(panel(el).querySelectorAll(".turn-info-section")).toHaveLength(2);
  });
});

describe("the per-file rows", () => {
  it("renders one row per file with its own line counts", () => {
    const el = buildTurnFooter({ changedFiles: TWO_FILES });
    const r = rows(el);
    expect(r).toHaveLength(2);
    // Sorted by path, so a repaint cannot reshuffle rows under the cursor.
    expect(r[0]?.querySelector(".turn-file-path")?.textContent).toBe("a.ts");
    expect(r[0]?.querySelector(".turn-file-add")?.textContent).toBe("+5");
    expect(r[0]?.querySelector(".turn-file-del")?.textContent).toBe("\u22122");
    expect(r[1]?.querySelector(".turn-file-path")?.textContent).toBe("b.ts");
    // No deletions on b.ts, so no deletion span at all rather than a "−0".
    expect(r[1]?.querySelector(".turn-file-del")).toBeNull();
  });

  it("badges a file the turn created", () => {
    const el = buildTurnFooter({
      changedFiles: {
        "new.ts": { lines_added: 9, lines_removed: 0, is_new_file: true },
        "old.ts": { lines_added: 1, lines_removed: 1 },
      },
    });
    const r = rows(el);
    expect(r[0]?.querySelector(".turn-file-badge")?.textContent).toBe("new");
    expect(r[1]?.querySelector(".turn-file-badge")).toBeNull();
  });

  // THE FOUR CONDITIONAL-DISCLOSURE CASES THAT USED TO SIT HERE ARE DELETED with the
  // mechanism they pinned: the summary was a disclosure only when the turn changed a
  // file, so a credits-only turn got a `disabled` button and the tests asserted that
  // plus the become/collapse transitions either side of it. Every turn that earns a
  // footer now has a panel to open — the reasoning is at `updateTurnFooter`'s
  // `aria-expanded` write — so `summary.disabled` is never set and an assertion on it
  // would pass whatever the code did. The one fact those cases carried that is still
  // live is the GATE admitting `sinceMs`, and it is asserted where the gate is, in the
  // `earnsTurnFooter` block below.
  it("keeps its rows across a repaint that leaves the files alone", () => {
    const el = buildTurnFooter({ changedFiles: TWO_FILES });
    summary(el).click();
    updateTurnFooter(el, { changedFiles: TWO_FILES, credits: 2 });
    expect(el.dataset["info"]).toBe("open");
    expect(rows(el)).toHaveLength(2);
  });

  it("drops its rows when a repaint drops the files", () => {
    const el = buildTurnFooter({ changedFiles: TWO_FILES, elapsedMs: 1000 });
    updateTurnFooter(el, { elapsedMs: 1000 });
    expect(rows(el)).toHaveLength(0);
    expect(sections(el)).toEqual(["Timings"]);
  });
});

describe("the click target", () => {
  it("opens the clicked file's diff", () => {
    openFileGitDiff.mockClear();
    const el = buildTurnFooter({ changedFiles: TWO_FILES });
    rows(el)[1]?.click();
    expect(openFileGitDiff).toHaveBeenCalledWith("b.ts");
  });

  it("names the file in each row's hover text so the target is never a mystery", () => {
    // `data-tooltip`, not `title`: the styled tooltip system is what every other
    // hover in the app uses, and a UA tooltip beside it reads as foreign chrome.
    const el = buildTurnFooter({ changedFiles: TWO_FILES });
    expect(rows(el)[0]?.getAttribute("data-tooltip")).toBe("Open the diff for a.ts");
  });
});

describe("earnsTurnFooter", () => {
  it("is false for an empty / zero summary", () => {
    expect(earnsTurnFooter({})).toBe(false);
    expect(earnsTurnFooter({ credits: 0, elapsedMs: 0 })).toBe(false);
    expect(earnsTurnFooter({ changedFiles: {} })).toBe(false);
    expect(earnsTurnFooter({ commands: 0, reads: 0 })).toBe(false);
    // A gap of zero earns nothing. Not in tension with `timingRows`, which renders
    // the row for `sinceMs: 0`: this gate asks whether the turn has anything worth a
    // footer and two turns starting together does not, while the row asks whether a
    // gap was MEASURED, which is a different question with `undefined` as its no.
    expect(earnsTurnFooter({ sinceMs: 0 })).toBe(false);
  });

  it("is true when any ledger dimension is present", () => {
    expect(earnsTurnFooter({ credits: 0.1 })).toBe(true);
    expect(earnsTurnFooter({ elapsedMs: 1 })).toBe(true);
    // The gap before the turn, and it is the ONE panel-only field the gate admits
    // (the reasoning is at `earnsTurnFooter`). This gate decides the footer EXISTS,
    // and with no footer there is no panel to state the gap in words — leaving the
    // rail's seam tooltip as the only channel, which needs a pointer.
    expect(earnsTurnFooter({ sinceMs: 14_400_000 })).toBe(true);
    expect(earnsTurnFooter({ commands: 1 })).toBe(true);
    expect(earnsTurnFooter({ reads: 1 })).toBe(true);
    expect(
      earnsTurnFooter({ changedFiles: { "a.ts": { lines_added: 1, lines_removed: 0 } } }),
    ).toBe(true);
  });

  // The files are on disk regardless, and a cancel is exactly when a reader
  // needs to know what landed — so a non-clean outcome earns a footer even when
  // the cancel beat the usage stamp and there are no numbers to show.
  it("is true for a non-clean outcome with nothing else to show", () => {
    expect(earnsTurnFooter({ outcome: "interrupted" })).toBe(true);
    expect(earnsTurnFooter({ outcome: "failed" })).toBe(true);
  });

  it("is not made true by a clean or running outcome alone", () => {
    expect(earnsTurnFooter({ outcome: "completed" })).toBe(false);
    expect(earnsTurnFooter({ outcome: "running" })).toBe(false);
  });

  // DELIBERATELY not widened for the model. Every completed turn has one, so
  // admitting it here would put a footer on every turn in the transcript —
  // including the ones this rule exists to suppress. The model rides a footer
  // that already earned its place; it never earns one.
  it("is not made true by a model alone", () => {
    expect(earnsTurnFooter({ models: ["sonnet-4"] })).toBe(false);
    expect(earnsTurnFooter({ models: ["sonnet-4"], outcome: "completed" })).toBe(false);
  });

  // Nor by the panel's OWN fields, for the same reason and one more: the panel is
  // reached THROUGH this row, and a row with no lead word and no numbers is not a
  // door worth painting on every turn in the transcript.
  it("is not made true by the info panel's own fields", () => {
    expect(
      earnsTurnFooter({
        toolMs: 9000,
        kindCounts: { read: 3 },
        delegateCount: 1,
        delegateMs: 9000,
        startedAt: 1000,
        endedAt: 9000,
        stopReasonRaw: "max_tokens",
        truncated: true,
      }),
    ).toBe(false);
  });

  it("still shows the model on a footer something else earned", () => {
    expect(earnsTurnFooter({ models: ["sonnet-4"], credits: 0.1 })).toBe(true);
    expect(sectionRows(buildTurnFooter({ models: ["sonnet-4"], credits: 0.1 }), "Model")).toEqual([
      ["Answered by", "sonnet-4"],
    ]);
  });

  // THE EXTRAS, which used to be an inline expression in `messages.ts` beside this
  // predicate: the footer also carries the turn ACTIONS and Rewind, so an unstamped
  // ledger must not cost the reader the buttons. A delegate card passes neither,
  // which is why they are parameters rather than fields on the summary.
  it("is true for a rewind target with no ledger at all", () => {
    expect(earnsTurnFooter({}, { rewindable: true })).toBe(true);
  });

  it("is true when there is settled prose for the turn actions to act on", () => {
    expect(earnsTurnFooter({}, { settledProse: true })).toBe(true);
  });

  it("is false when both extra reasons are explicitly absent", () => {
    expect(earnsTurnFooter({}, { rewindable: false, settledProse: false })).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// `Review changes` — the multi-file seam
// ---------------------------------------------------------------------------

describe("Review changes", () => {
  it("is absent on a single-file turn, where the row above already opens it", () => {
    const footer = buildTurnFooter({
      outcome: "completed",
      changedFiles: { "a.ts": { lines_added: 1, lines_removed: 0 } },
    });
    document.body.replaceChildren(footer);
    summary(footer).click();
    expect(footer.querySelector(".turn-review-all")).toBeNull();
  });

  it("appears once for a multi-file turn and names the count", () => {
    const footer = buildTurnFooter({
      outcome: "completed",
      changedFiles: {
        "a.ts": { lines_added: 1, lines_removed: 0 },
        "b.ts": { lines_added: 2, lines_removed: 1 },
      },
    });
    document.body.replaceChildren(footer);
    summary(footer).click();
    const all = footer.querySelectorAll(".turn-review-all");
    expect(all).toHaveLength(1);
    expect(all[0]?.textContent).toContain("2 files");
  });
});

// ---------------------------------------------------------------------------
// The hover text: what the row does NOT already say.
//
// It used to be a native `title` carrying the summary line verbatim, so the one
// hover affordance on the footer restated the text under the pointer and wore the
// UA chrome instead of the app's styled tooltip. These pin both halves of the
// replacement: the styled attribute, and that it says only what the click does.
// ---------------------------------------------------------------------------

describe("the trigger's hover text", () => {
  function tip(footer: HTMLElement): string | null {
    return summary(footer).getAttribute("data-tooltip");
  }

  it("is never a native title", () => {
    const footer = buildTurnFooter({
      outcome: "completed",
      commands: 2,
      changedFiles: { "a.ts": { lines_added: 3, lines_removed: 1 } },
    });
    expect(summary(footer).hasAttribute("title")).toBe(false);
  });

  it("names the disclosure action, and tracks the open state", () => {
    const footer = buildTurnFooter({ outcome: "completed", elapsedMs: 1000 });
    document.body.replaceChildren(footer);
    expect(tip(footer)).toBe("Show turn details");
    summary(footer).click();
    expect(tip(footer)).toBe("Hide turn details");
  });

  it("advertises the disclosure on a turn that changed no files too", () => {
    // The clause that withheld the tooltip is gone with the readout state it tested
    // for: every footer discloses a panel, so a footer with no tooltip would be a
    // door with nothing saying so.
    expect(tip(buildTurnFooter({ outcome: "completed", commands: 2 }))).toBe("Show turn details");
  });

  it("carries NO outcome clause at all, because the row names every outcome now", () => {
    // REWRITTEN twice, and this is the shape that ends it. The clause existed for the
    // three outcomes the ledger line left unnamed, which made a hover the only
    // explanation of a coloured circle — the same defect as `running`'s "Still
    // running" tooltip that a previous pass removed for exactly this reason. With
    // OUTCOME_LEAD total there is nothing left for the tooltip to add.
    for (const outcome of ["cancelled", "refused", "unknown", "failed", "interrupted"] as const) {
      const footer = buildTurnFooter({ outcome, commands: 1 });
      expect(line(footer), `${outcome} names itself in the row`).not.toBe("");
      expect(tip(footer), `${outcome} adds no hover clause`).toBe("Show turn details");
    }
  });

  it("puts the styled tooltip on the file row too, not a native title", () => {
    const footer = buildTurnFooter({
      outcome: "completed",
      changedFiles: { "src/a.ts": { lines_added: 1, lines_removed: 0 } },
    });
    document.body.replaceChildren(footer);
    summary(footer).click();
    const row = footer.querySelector<HTMLElement>(".turn-file-row");
    expect(row?.hasAttribute("title")).toBe(false);
    expect(row?.getAttribute("data-tooltip")).toBe("Open the diff for src/a.ts");
  });
});
