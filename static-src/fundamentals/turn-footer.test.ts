// @vitest-environment happy-dom
// The turn card's outcome ledger: the aggregate row, the per-file breakdown it
// expands into, and the click target on every file row.
import { describe, it, expect, vi } from "vitest";

const openFileGitDiff = vi.fn();
vi.mock("../editor-openers.js", () => ({
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

  it("names the file in each row's title so the target is never a mystery", () => {
    const el = buildTurnFooter({ changedFiles: TWO_FILES });
    expect(rows(el)[0]?.title).toBe("Open the diff for a.ts");
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
