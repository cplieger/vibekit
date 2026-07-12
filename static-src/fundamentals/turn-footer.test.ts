// @vitest-environment happy-dom
// Formatting + presence logic for the turn footer (moved here from
// handlers/turn.ts when the summary became a keyed view over message metadata).
import { describe, it, expect } from "vitest";
import { buildTurnFooter, updateTurnFooter, hasTurnSummary } from "./turn-footer.js";

describe("turn-footer formatting", () => {
  it("uses one decimal second for sub-minute elapsed", () => {
    expect(buildTurnFooter({ elapsedMs: 45500 }).textContent).toBe("45.5s");
  });

  it("splits minute+ elapsed into m and s", () => {
    expect(buildTurnFooter({ elapsedMs: 90000 }).textContent).toBe("1m 30s");
  });

  it("floors the seconds in the minute branch (no round-up to 60)", () => {
    expect(buildTurnFooter({ elapsedMs: 119999 }).textContent).toBe("1m 59s");
  });

  it("joins credits and elapsed", () => {
    expect(buildTurnFooter({ credits: 1.5, elapsedMs: 2000 }).textContent).toBe(
      "Est. 1.50 credits · 2.0s",
    );
  });

  it("renders credits alone without an elapsed segment", () => {
    expect(buildTurnFooter({ credits: 0.5 }).textContent).toBe("Est. 0.50 credits");
  });

  it("summarises touched files with net line counts", () => {
    const el = buildTurnFooter({
      changedFiles: {
        "a.ts": { lines_added: 5, lines_removed: 2 },
        "b.ts": { lines_added: 1, lines_removed: 0 },
      },
    });
    expect(el.textContent).toBe("2 files changed +6 -2");
  });

  it("updateTurnFooter recomputes in place", () => {
    const el = buildTurnFooter({ credits: 0.5 });
    updateTurnFooter(el, { credits: 1, elapsedMs: 3000 });
    expect(el.textContent).toBe("Est. 1.00 credits · 3.0s");
  });
});

describe("hasTurnSummary", () => {
  it("is false for an empty / zero summary", () => {
    expect(hasTurnSummary({})).toBe(false);
    expect(hasTurnSummary({ credits: 0, elapsedMs: 0 })).toBe(false);
    expect(hasTurnSummary({ changedFiles: {} })).toBe(false);
  });

  it("is true when any dimension is present", () => {
    expect(hasTurnSummary({ credits: 0.1 })).toBe(true);
    expect(hasTurnSummary({ elapsedMs: 1 })).toBe(true);
    expect(hasTurnSummary({ changedFiles: { "a.ts": { lines_added: 1, lines_removed: 0 } } })).toBe(
      true,
    );
  });
});
