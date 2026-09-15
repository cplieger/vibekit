// THE DOT CLUSTER'S CONTRAST GATE, RUN.
//
// `scripts/css-contrast.py dot` measures the tab strip's two marks and the run
// bar's glyph against WCAG 1.4.11's 3:1 floor for a graphical object, and — since
// the mark landed — judges each ROW as its own 1.4.1 population: every pair of
// states on one row has to differ on a channel that is not colour. It prints a
// verdict per population and per state, and it ALWAYS EXITS 0. So the verdicts were
// prose nobody read: a token retune that put two states on one look, or a fill
// change that dropped a state under the floor, printed FAIL and shipped green.
//
// This is that gate, run. It asserts nothing about colour itself and reimplements
// none of the maths — the script is the one implementation, and the numbers in
// 12-tabs.css's comments were measured with it (the reason `send-btn-contrast` and
// `rail-mark-contrast` shell out rather than computing).
//
// WHY THE WHOLE SUBCOMMAND rather than a `pair` call per state: the 1.4.1 half is
// not a pair of colours at all, it is a PAIRWISE SWEEP over a population's channel
// matrix under two motion modes, and reproducing that here would be a second copy
// of the rule the script owns. The floors ride along because they are printed by
// the same subcommand.
//
// Node environment: this runs a process.

import { describe, it, expect } from "vitest";
import { execFileSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const script = join(here, "..", "scripts", "css-contrast.py");

/** The four cluster verdicts the 1.4.1 sweep prints: two ROWS (the marks share a
 *  chat row, a run's own row is its own population) across two motion modes. Named
 *  here rather than discovered from the output, so a script that stops printing one
 *  fails instead of passing over a population it no longer checks. */
const CLUSTERS = [
  "chat row, motion available",
  "run row, motion available",
  "chat row, prefers-reduced-motion",
  "run row, prefers-reduced-motion",
] as const;

// The script lives outside static-src, and Stryker's sandbox copies static-src
// alone — so its absence is a skip, the rule every sibling floor uses.
describe.skipIf(!existsSync(script))("the dot cluster's contrast gate passes", () => {
  const out = execFileSync("python3", [script, "dot"], { encoding: "utf8" });

  it("separates every pair on each row without relying on hue", () => {
    // WCAG 1.4.1. The sweep answers per population, so each verdict is asserted by
    // NAME: a bare "no FAIL anywhere" would pass just as well if the sweep silently
    // stopped running, and that is the failure this whole file exists to close.
    for (const cluster of CLUSTERS) {
      const line = out.split("\n").find((l) => l.trim().startsWith(cluster));
      expect(line, `${cluster} has no verdict`).toBeDefined();
      expect(line, cluster).toContain("PASS");
    }
  });

  it("holds every state to the 3:1 graphical-object floor on every fill", () => {
    // WCAG 1.4.11, per state per row-fill. The script prints one verdict column at
    // the end of each state's row of ratios, so counting them is what says the table
    // was populated: two themes over the chat states plus the mark's, which is the
    // shape a state added to either table changes deliberately.
    const verdicts = out.split("\n").filter((l) => /^ {4}\w+\s+(var\(--|--)/.test(l));
    expect(verdicts.length, "the floor table printed no state rows").toBeGreaterThan(8);
    for (const line of verdicts) {
      expect(line.trim(), "state row under the floor").toContain("PASS");
    }
  });

  it("reports no failure of any kind", () => {
    // The catch-all, and it is LAST rather than first: it covers the verdicts the two
    // cases above do not name — the gamut check on each ink, the hover-fill floors,
    // the favicon badge's own reading — while neither of them can be satisfied by an
    // empty run.
    const fails = out.split("\n").filter((l) => l.includes("FAIL"));
    expect(fails, fails.join("\n")).toEqual([]);
  });
});
