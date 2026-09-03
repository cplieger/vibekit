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
  it("uses one decimal second for sub-minute elapsed", () => {
    expect(line(buildTurnFooter({ elapsedMs: 45500 }))).toBe("45.5s");
  });

  it("splits minute+ elapsed into m and s", () => {
    expect(line(buildTurnFooter({ elapsedMs: 90000 }))).toBe("1m 30s");
  });

  it("floors the seconds in the minute branch (no round-up to 60)", () => {
    expect(line(buildTurnFooter({ elapsedMs: 119999 }))).toBe("1m 59s");
  });

  it("joins credits and elapsed", () => {
    expect(line(buildTurnFooter({ credits: 1.5, elapsedMs: 2000 }))).toBe("1.50 cr \u00b7 2.0s");
  });

  it("renders credits alone without an elapsed segment", () => {
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

  it("leads with the outcome when the turn did not finish cleanly", () => {
    expect(line(buildTurnFooter({ outcome: "interrupted", elapsedMs: 1000 }))).toBe(
      "Interrupted \u00b7 1.0s",
    );
    expect(line(buildTurnFooter({ outcome: "failed" }))).toBe("Failed");
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
    ).toBe("1 file +1 \u00b7 2 cmds \u00b7 1 read \u00b7 0.50 cr \u00b7 1.0s");
  });

  it("recomputes in place", () => {
    const el = buildTurnFooter({ credits: 0.5 });
    updateTurnFooter(el, { credits: 1, elapsedMs: 3000 });
    expect(line(el)).toBe("1.00 cr \u00b7 3.0s");
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

  it("names the outcome the glyph stands for when the line does not", () => {
    // `summaryLine` leads with a word only for `interrupted` and `failed`, so a
    // cancelled turn shows a coloured circle the row says nothing about.
    const footer = buildTurnFooter({ outcome: "cancelled", commands: 1 });
    expect(tip(footer)).toBe("Cancelled");
  });

  it("says nothing about a running turn, which no longer carries a glyph", () => {
    // REWRITTEN, not weakened: this case asserted "Still running", which was the
    // one state in the app whose whole explanation was a hover. The footer reports
    // how a turn ENDED — 29-turns.css folds `running` into `completed`'s
    // `display: none` — so there is no circle left to name, and a tooltip naming
    // an invisible mark is worse than none.
    const footer = buildTurnFooter({ outcome: "running", commands: 1 });
    expect(tip(footer)).toBeNull();
  });

  it("does not repeat an outcome the line already leads with", () => {
    const footer = buildTurnFooter({ outcome: "failed", commands: 1 });
    expect(line(footer)).toContain("Failed");
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

  it("carries both clauses when both apply", () => {
    // `running` used to be this case's outcome; it names nothing now, so the pair
    // is demonstrated with one that still does.
    const footer = buildTurnFooter({
      outcome: "cancelled",
      changedFiles: { "a.ts": { lines_added: 1, lines_removed: 0 } },
    });
    expect(tip(footer)).toBe("Cancelled \u00b7 Show the changed files");
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
