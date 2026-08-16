// Tests for the PR row's pure read-outs: the check chip, the merge-block
// reason, and the per-forge capability rules. No DOM needed — which is
// the reason these live outside git-prs-tab.ts.

import { describe, it, expect } from "vitest";

import { checkChip, mergeBlockReason, supportsRerun, canArmAutoMerge } from "./git-pr-status.js";
import type { GitPR } from "./git-types.js";

function pr(over: Partial<GitPR> = {}): GitPR {
  return {
    number: 1,
    title: "T",
    state: "open",
    source_branch: "feat",
    target_branch: "main",
    ...over,
  };
}

describe("mergeBlockReason", () => {
  // Every branch the function now has. The catch-all this replaced said
  // the PR "isn't mergeable" and told the reader to open it on the forge;
  // no branch may reintroduce that.
  const table: [string | undefined, string][] = [
    ["draft", "draft"],
    ["conflicts", "conflicts"],
    ["checks_failing", "check is failing"],
    ["checks_running", "still running"],
    ["behind", "behind its target"],
    ["blocked", "merge policy"],
    ["unknown", "does not say why"],
  ];

  for (const [cause, fragment] of table) {
    it(`names the cause for ${String(cause)}`, () => {
      const reason = mergeBlockReason(pr({ merge_blocked: cause }));
      expect(reason).not.toBe("");
      expect(reason).toContain(fragment);
    });
  }

  it("enables the merge when nothing blocks it", () => {
    expect(mergeBlockReason(pr({ merge_blocked: "" }))).toBe("");
    expect(mergeBlockReason(pr())).toBe("");
  });

  // Fail CLOSED on a cause this build does not know. merge_blocked is a plain
  // string so the server vocabulary can grow, and the "" fallback this
  // replaced recreated the exact defect the function exists to fix: the forge
  // refuses the merge while the row enables the button.
  it("blocks on an unrecognised cause and quotes it", () => {
    const reason = mergeBlockReason(pr({ merge_blocked: "requires_two_approvals" }));
    expect(reason).not.toBe("");
    expect(reason).toContain("requires_two_approvals");
  });

  it("reserves empty and absent for mergeable, and nothing else", () => {
    expect(mergeBlockReason(pr({ merge_blocked: "" }))).toBe("");
    expect(mergeBlockReason(pr({ merge_blocked: undefined }))).toBe("");
    // Whitespace is not emptiness: a value the server sent is a cause.
    expect(mergeBlockReason(pr({ merge_blocked: " " }))).not.toBe("");
  });

  // The unknown cause reaches a tooltip, so it is normalised to one bounded
  // line rather than trusted for its provenance.
  it("keeps an unrecognised cause to one short line", () => {
    const reason = mergeBlockReason(pr({ merge_blocked: "a\nb".padEnd(120, "x") }));
    expect(reason).not.toContain("\n");
    expect(reason.length).toBeLessThan(160);
  });

  it("never tells the reader to go use the forge instead", () => {
    for (const [cause] of table) {
      const reason = mergeBlockReason(pr({ merge_blocked: cause }));
      expect(reason.toLowerCase()).not.toContain("on the forge");
    }
  });

  it("no reason carries an em dash", () => {
    for (const [cause] of table) {
      expect(mergeBlockReason(pr({ merge_blocked: cause }))).not.toContain("\u2014");
    }
  });

  // A draft PR whose merge_blocked the server did not set must not be
  // reported as mergeable by accident: `draft` is the server's job now,
  // and this pins that the client does not second-guess it.
  it("reads the server's cause, not the legacy mergeable bool", () => {
    expect(mergeBlockReason(pr({ mergeable: false }))).toBe("");
    expect(mergeBlockReason(pr({ merge_blocked: "unknown", mergeable: false }))).not.toBe("");
  });
});

describe("checkChip", () => {
  it("is absent when the forge reported no checks (the gitea case)", () => {
    expect(checkChip(pr())).toBeNull();
    expect(checkChip(pr({ check_status: "" }))).toBeNull();
    expect(checkChip(pr({ check_status: "some_future_value" }))).toBeNull();
  });

  it("reports a pass with its count", () => {
    const chip = checkChip(pr({ check_status: "passing", checks_total: 7 }));
    expect(chip?.className).toBe("git-pr-check-pass");
    expect(chip?.text).toBe("checks passed");
    expect(chip?.tooltip).toBe("7 checks passed");
  });

  it("singularises a lone check", () => {
    const chip = checkChip(pr({ check_status: "passing", checks_total: 1 }));
    expect(chip?.tooltip).toBe("1 check passed");
  });

  it("reports failures as a count out of the total", () => {
    const chip = checkChip(pr({ check_status: "failing", checks_total: 7, checks_failing: 2 }));
    expect(chip?.className).toBe("git-pr-check-fail");
    expect(chip?.text).toBe("2 failing");
    expect(chip?.tooltip).toBe("2 of 7 checks failing");
  });

  it("still says something when a failing PR carries no counts", () => {
    const chip = checkChip(pr({ check_status: "failing" }));
    expect(chip?.text).toBe("checks failing");
    expect(chip?.tooltip).not.toBe("");
  });

  it("reports running checks", () => {
    const chip = checkChip(pr({ check_status: "pending", checks_total: 3 }));
    expect(chip?.className).toBe("git-pr-check-pending");
    expect(chip?.text).toBe("checks running");
    expect(chip?.tooltip).toBe("3 checks running");
  });

  it("carries words as well as colour on every state", () => {
    for (const status of ["passing", "failing", "pending"]) {
      const chip = checkChip(pr({ check_status: status, checks_total: 2, checks_failing: 1 }));
      expect(chip?.text).not.toBe("");
    }
  });
});

describe("supportsRerun", () => {
  it("is true where a re-run mechanism exists", () => {
    expect(supportsRerun("github")).toBe(true);
    expect(supportsRerun("gitlab")).toBe(true);
  });

  // The degradation: tea has no CI verb and Gitea Actions' re-run
  // endpoints are outside the stable API, so the row hides the control
  // instead of offering one the server answers 501 to.
  it("is false on gitea and codeberg", () => {
    expect(supportsRerun("gitea")).toBe(false);
    expect(supportsRerun("codeberg")).toBe(false);
  });
});

describe("canArmAutoMerge", () => {
  it("offers arming while checks are unsettled", () => {
    expect(canArmAutoMerge(pr({ merge_blocked: "checks_running" }))).toBe(true);
    expect(canArmAutoMerge(pr({ check_status: "pending" }))).toBe(true);
  });

  // The pending arm has to be gated on nothing ELSE blocking the merge. A
  // draft or conflicting PR with a check still running would not merge when
  // that check went green, so "Merge when green" is a promise the forge cannot
  // keep — the shown-and-failing control this row exists to avoid.
  it("does not offer arming when another cause blocks the merge", () => {
    for (const cause of [
      "draft",
      "conflicts",
      "behind",
      "blocked",
      "unknown",
      "checks_failing",
      "a_future_cause",
    ]) {
      expect(canArmAutoMerge(pr({ check_status: "pending", merge_blocked: cause }))).toBe(false);
    }
  });

  // checks_running is the one non-empty cause that DOES earn the offer: it
  // says the checks are the only thing in the way.
  it("still offers arming when the checks are the stated cause", () => {
    expect(canArmAutoMerge(pr({ check_status: "pending", merge_blocked: "checks_running" }))).toBe(
      true,
    );
    expect(canArmAutoMerge(pr({ check_status: "failing", merge_blocked: "checks_running" }))).toBe(
      true,
    );
  });

  it("does not offer arming twice", () => {
    expect(canArmAutoMerge(pr({ check_status: "pending", auto_merge_armed: true }))).toBe(false);
  });

  it("does not offer arming when the merge could happen now", () => {
    expect(canArmAutoMerge(pr({ check_status: "passing" }))).toBe(false);
    expect(canArmAutoMerge(pr())).toBe(false);
  });

  it("does not offer arming on a closed PR", () => {
    expect(canArmAutoMerge(pr({ state: "closed", check_status: "pending" }))).toBe(false);
  });
});
