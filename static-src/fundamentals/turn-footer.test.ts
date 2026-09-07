// The turn card's outcome ledger: the aggregate row, the per-file breakdown it
// expands into, and the click target on every file row.
import { describe, it, expect, vi } from "vitest";

const openFileGitDiff = vi.fn();
vi.mock("../editor-openers.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  openFile: undefined,
  openFileGitDiff: (path: string) => {
    openFileGitDiff(path);
  },
}));

const { buildTurnFooter, updateTurnFooter, hasTurnSummary } = await import("./turn-footer.js");

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

function rows(el: HTMLElement): HTMLButtonElement[] {
  return [...el.querySelectorAll<HTMLButtonElement>(".turn-file-row")];
}

const TWO_FILES = {
  "b.ts": { lines_added: 1, lines_removed: 0 },
  "a.ts": { lines_added: 5, lines_removed: 2 },
};

describe("the aggregate row", () => {
  it("leaves the duration out of the ledger string entirely", () => {
    // It used to be the sixth of up to seven `·`-separated parts, buried in a line
    // that otherwise reports cost. Its own slot is asserted in "the turn's own
    // time" below; here the point is that the string no longer carries it.
    const el = buildTurnFooter({ elapsedMs: 45500 });
    expect(line(el)).toBe("");
    expect(elapsed(el)).toBe("45.5s");
  });

  it("renders credits without an elapsed segment beside them", () => {
    expect(line(buildTurnFooter({ credits: 1.5, elapsedMs: 2000 }))).toBe("1.50 cr");
    expect(line(buildTurnFooter({ credits: 0.5 }))).toBe("0.50 cr");
  });

  it("summarises touched files with net line counts", () => {
    expect(line(buildTurnFooter({ changedFiles: TWO_FILES }))).toBe("2 files +6 \u22122");
  });

  // Non-file work was countable from the turn's tool calls all along and shown
  // nowhere, so a turn that ran ten commands and wrote nothing reported no work.
  it("counts commands run and files read", () => {
    expect(line(buildTurnFooter({ commands: 3, reads: 12 }))).toBe("3 cmds \u00b7 12 reads");
    expect(line(buildTurnFooter({ commands: 1, reads: 1 }))).toBe("1 cmd \u00b7 1 read");
  });

  it("leads with the outcome for EVERY turn that did not finish cleanly", () => {
    // Total over the five glyph-bearing outcomes, and three of them used to say
    // their name only in a `data-tooltip` — which is no channel at all on a touch
    // device, so a cancelled, refused or unknown turn rendered a coloured 8px
    // circle leading a cost line with nothing anywhere explaining the circle. Same
    // class of defect as the hollow tab dot: a status reachable only by a gesture
    // the reader may never make.
    expect(line(buildTurnFooter({ outcome: "interrupted", elapsedMs: 1000 }))).toBe("Interrupted");
    expect(line(buildTurnFooter({ outcome: "failed" }))).toBe("Failed");
    expect(line(buildTurnFooter({ outcome: "refused" }))).toBe("Refused");
    expect(line(buildTurnFooter({ outcome: "cancelled" }))).toBe("Cancelled");
    expect(line(buildTurnFooter({ outcome: "unknown" }))).toBe("Outcome unknown");
  });

  it("leads with nothing for the two outcomes that carry no glyph", () => {
    // The absence of a mark IS the clean case, and the footer reports how a turn
    // ENDED rather than one still going, so neither has a circle to explain.
    expect(line(buildTurnFooter({ outcome: "completed", commands: 1 }))).toBe("1 cmd");
    expect(line(buildTurnFooter({ outcome: "running", commands: 1 }))).toBe("1 cmd");
  });

  it("never opens with a cost on a turn that ended badly", () => {
    // The property behind the two cases above, stated so a sixth outcome cannot
    // quietly land on a line that opens with credits. A reader scanning a
    // transcript must not have to reach the glyph to know a turn failed.
    for (const outcome of ["cancelled", "interrupted", "failed", "refused", "unknown"] as const) {
      const text = line(buildTurnFooter({ outcome, credits: 1.5, elapsedMs: 1000 }));
      expect(text, `${outcome} leads with a word`).not.toMatch(/^[\d.]/u);
    }
  });

  it("orders files before non-file work before cost", () => {
    expect(
      line(
        buildTurnFooter({
          changedFiles: { "a.ts": { lines_added: 1, lines_removed: 0 } },
          commands: 2,
          reads: 1,
          credits: 0.5,
          elapsedMs: 1000,
        }),
      ),
    ).toBe("1 file +1 \u00b7 2 cmds \u00b7 1 read \u00b7 0.50 cr");
  });

  it("recomputes in place", () => {
    const el = buildTurnFooter({ credits: 0.5 });
    updateTurnFooter(el, { credits: 1, elapsedMs: 3000 });
    expect(line(el)).toBe("1.00 cr");
    expect(elapsed(el)).toBe("3.0s");
  });

  // Which model, last on the line: it answers "who did this" rather than "what
  // did it cost", so it reads as attribution at the end.
  it("names the model that served the turn", () => {
    expect(line(buildTurnFooter({ credits: 0.5, models: ["sonnet-4"] }))).toBe(
      "0.50 cr \u00b7 sonnet-4",
    );
  });

  it("shows an arrow when a switch split the turn", () => {
    expect(line(buildTurnFooter({ models: ["sonnet-4", "opus-4"] }))).toBe(
      "sonnet-4 \u2192 opus-4",
    );
  });

  it("renders nothing rather than 'unknown' for a turn with no model", () => {
    expect(line(buildTurnFooter({ credits: 0.5 }))).toBe("0.50 cr");
    expect(line(buildTurnFooter({ credits: 0.5, models: [] }))).toBe("0.50 cr");
  });
});

// ---------------------------------------------------------------------------
// The turn's own time, in its own slot.
//
// It was one `·`-separated part of the ledger string, sixth of up to seven, in a
// line that otherwise reports cost — so "how long did that take", which a reader
// asks of a turn often, was the hardest thing on the row to find. It now sits
// right-aligned in a `<time>` of its own, revealed on hover of the card.
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

  it("still reaches a screen reader through the summary's accessible name", () => {
    // The channel the duration had, and it left `line` when it moved. The slot is
    // NOT a replacement for it: the slot is revealed on hover, and a label on the
    // button beside it says nothing about a sibling.
    const el = buildTurnFooter({ credits: 1.5, elapsedMs: 92000 });
    expect(summary(el).getAttribute("aria-label")).toBe("Turn summary: 1.50 cr \u00b7 1m 32s");
  });

  it("names the duration even when the ledger line is otherwise empty", () => {
    expect(summary(buildTurnFooter({ elapsedMs: 2000 })).getAttribute("aria-label")).toBe(
      "Turn summary: 2.0s",
    );
  });

  it("leaves the label alone for a turn with neither", () => {
    expect(summary(buildTurnFooter({})).getAttribute("aria-label")).toBe("Turn summary");
  });
});

describe("the per-file breakdown", () => {
  it("is closed until asked for", () => {
    const el = buildTurnFooter({ changedFiles: TWO_FILES });
    expect(el.dataset["files"]).toBeUndefined();
    expect(summary(el).getAttribute("aria-expanded")).toBe("false");
  });

  it("opens and closes on the aggregate row", () => {
    const el = buildTurnFooter({ changedFiles: TWO_FILES });
    summary(el).click();
    expect(el.dataset["files"]).toBe("open");
    expect(summary(el).getAttribute("aria-expanded")).toBe("true");
    summary(el).click();
    expect(el.dataset["files"]).toBeUndefined();
  });

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

  // A control that does nothing teaches the reader to distrust every other one.
  it("is not a disclosure when the turn changed no files", () => {
    const el = buildTurnFooter({ credits: 1, elapsedMs: 1000 });
    expect(summary(el).disabled).toBe(true);
    expect(summary(el).hasAttribute("aria-expanded")).toBe(false);
    expect(rows(el)).toHaveLength(0);
  });

  it("becomes a disclosure once files arrive, and collapses if they go", () => {
    const el = buildTurnFooter({ credits: 1 });
    updateTurnFooter(el, { credits: 1, changedFiles: TWO_FILES });
    expect(summary(el).disabled).toBe(false);
    summary(el).click();
    expect(el.dataset["files"]).toBe("open");
    updateTurnFooter(el, { credits: 1 });
    expect(summary(el).disabled).toBe(true);
    expect(el.dataset["files"]).toBeUndefined();
  });

  it("keeps an expanded list expanded across a repaint", () => {
    const el = buildTurnFooter({ changedFiles: TWO_FILES });
    summary(el).click();
    updateTurnFooter(el, { changedFiles: TWO_FILES, credits: 2 });
    expect(el.dataset["files"]).toBe("open");
    expect(rows(el)).toHaveLength(2);
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
    // `data-tooltip` rather than `title` since 2026-09: the footer's tooltips
    // moved onto the app's styled system, which is what every other hover uses.
    const el = buildTurnFooter({ changedFiles: TWO_FILES });
    expect(rows(el)[0]?.getAttribute("data-tooltip")).toBe("Open the diff for a.ts");
  });
});

describe("hasTurnSummary", () => {
  it("is false for an empty / zero summary", () => {
    expect(hasTurnSummary({})).toBe(false);
    expect(hasTurnSummary({ credits: 0, elapsedMs: 0 })).toBe(false);
    expect(hasTurnSummary({ changedFiles: {} })).toBe(false);
    expect(hasTurnSummary({ commands: 0, reads: 0 })).toBe(false);
  });

  it("is true when any dimension is present", () => {
    expect(hasTurnSummary({ credits: 0.1 })).toBe(true);
    expect(hasTurnSummary({ elapsedMs: 1 })).toBe(true);
    expect(hasTurnSummary({ commands: 1 })).toBe(true);
    expect(hasTurnSummary({ reads: 1 })).toBe(true);
    expect(hasTurnSummary({ changedFiles: { "a.ts": { lines_added: 1, lines_removed: 0 } } })).toBe(
      true,
    );
  });

  // The files are on disk regardless, and a cancel is exactly when a reader
  // needs to know what landed — so a non-clean outcome earns a footer even when
  // the cancel beat the usage stamp and there are no numbers to show.
  it("is true for a non-clean outcome with nothing else to show", () => {
    expect(hasTurnSummary({ outcome: "interrupted" })).toBe(true);
    expect(hasTurnSummary({ outcome: "failed" })).toBe(true);
  });

  it("is not made true by a clean or running outcome alone", () => {
    expect(hasTurnSummary({ outcome: "completed" })).toBe(false);
    expect(hasTurnSummary({ outcome: "running" })).toBe(false);
  });

  // DELIBERATELY not widened for the model. Every completed turn has one, so
  // admitting it here would put a footer on every turn in the transcript —
  // including the ones this rule exists to suppress. The model rides a footer
  // that already earned its place; it never earns one.
  it("is not made true by a model alone", () => {
    expect(hasTurnSummary({ models: ["sonnet-4"] })).toBe(false);
    expect(hasTurnSummary({ models: ["sonnet-4"], outcome: "completed" })).toBe(false);
  });

  it("still shows the model on a footer something else earned", () => {
    expect(hasTurnSummary({ models: ["sonnet-4"], credits: 0.1 })).toBe(true);
    expect(line(buildTurnFooter({ models: ["sonnet-4"], credits: 0.1 }))).toContain("sonnet-4");
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
    footer.querySelector<HTMLElement>(".turn-ledger-summary")?.click();
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
    footer.querySelector<HTMLElement>(".turn-ledger-summary")?.click();
    const all = footer.querySelectorAll(".turn-review-all");
    expect(all).toHaveLength(1);
    expect(all[0]?.textContent).toContain("2 files");
  });
});

// ---------------------------------------------------------------------------
// The hover text: what the row does NOT already say
//
// It used to be a native `title` carrying the summary line verbatim, so the one
// hover affordance on the footer restated the text under the pointer and wore
// the UA chrome instead of the app's styled tooltip. These pin both halves of
// the replacement: the styled attribute, and that it never repeats the line.
// ---------------------------------------------------------------------------

describe("the ledger's hover text", () => {
  function tip(footer: HTMLElement): string | null {
    return footer.querySelector(".turn-ledger-summary")?.getAttribute("data-tooltip") ?? null;
  }

  it("is never a native title, and never restates the summary line", () => {
    const footer = buildTurnFooter({
      outcome: "completed",
      commands: 2,
      changedFiles: { "a.ts": { lines_added: 3, lines_removed: 1 } },
    });
    const summary = footer.querySelector<HTMLElement>(".turn-ledger-summary");
    expect(summary?.hasAttribute("title")).toBe(false);
    expect(tip(footer)).not.toContain(line(footer));
  });

  it("carries NO outcome clause at all, because the line names every outcome now", () => {
    // REWRITTEN twice, and this is the shape that ends it. The clause existed for
    // the three outcomes `summaryLine` left unnamed, which made a hover the only
    // explanation of a coloured circle — the same defect as `running`'s "Still
    // running" tooltip that a previous pass removed for exactly this reason. With
    // OUTCOME_LEAD total there is nothing left for the tooltip to add, so it is
    // gone rather than made conditional.
    for (const outcome of ["cancelled", "refused", "unknown", "failed", "interrupted"] as const) {
      const footer = buildTurnFooter({ outcome, commands: 1 });
      expect(line(footer), `${outcome} names itself in the row`).not.toBe("1 cmd");
      expect(tip(footer), `${outcome} adds no hover clause`).toBeNull();
    }
  });

  it("says nothing about a running turn, which carries no glyph", () => {
    const footer = buildTurnFooter({ outcome: "running", commands: 1 });
    expect(tip(footer)).toBeNull();
  });

  it("names the disclosure action, and tracks the open state", () => {
    const footer = buildTurnFooter({
      outcome: "completed",
      changedFiles: { "a.ts": { lines_added: 1, lines_removed: 0 } },
    });
    document.body.replaceChildren(footer);
    expect(tip(footer)).toBe("Show the changed files");
    footer.querySelector<HTMLElement>(".turn-ledger-summary")?.click();
    expect(tip(footer)).toBe("Hide the changed files");
  });

  it("advertises no disclosure on a plain readout", () => {
    // `summary.disabled` is the honest condition: no files, nothing to open.
    const footer = buildTurnFooter({ outcome: "completed", commands: 2 });
    expect(tip(footer)).toBeNull();
  });

  it("carries the disclosure clause ALONE, even on an outcome that has a glyph", () => {
    // This case asserted two clauses joined by the separator, and it is down to one
    // because the outcome half moved into the row itself. The pin that matters is
    // that the outcome is NOT repeated here: the line already leads with it, and
    // restating it was the defect the two-clause split existed to avoid.
    const footer = buildTurnFooter({
      outcome: "cancelled",
      changedFiles: { "a.ts": { lines_added: 1, lines_removed: 0 } },
    });
    expect(tip(footer)).toBe("Show the changed files");
    expect(line(footer)).toContain("Cancelled");
  });

  it("puts the styled tooltip on the file row too, not a native title", () => {
    const footer = buildTurnFooter({
      outcome: "completed",
      changedFiles: { "src/a.ts": { lines_added: 1, lines_removed: 0 } },
    });
    document.body.replaceChildren(footer);
    footer.querySelector<HTMLElement>(".turn-ledger-summary")?.click();
    const row = footer.querySelector<HTMLElement>(".turn-file-row");
    expect(row?.hasAttribute("title")).toBe(false);
    expect(row?.getAttribute("data-tooltip")).toBe("Open the diff for src/a.ts");
  });
});
