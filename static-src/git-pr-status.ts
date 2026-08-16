// ---------------------------------------------------------------------------
// Pure read-outs for a PR row: the CI check chip, the merge-block reason,
// and which per-forge controls a row may offer.
//
// DOM-free on purpose. The row renderer in git-prs-tab.ts turns these
// descriptors into elements; keeping the prose and the rules here is what
// makes every branch testable without a document, which the previous
// module-private mergeReason function was not.
// ---------------------------------------------------------------------------

import type { ForgeKind } from "./forge-types.js";
import type { GitPR } from "./git-types.js";

/** A rendered CI chip: text, a CSS state class, and its hover detail. */
export interface CheckChip {
  text: string;
  className: string;
  tooltip: string;
}

/** Describe the CI chip for a PR, or null when the forge reported no
 *  checks. Gitea/Forgejo always lands here: its PR payload carries no CI
 *  state, and an absent chip is the honest rendering of that. */
export function checkChip(pr: GitPR): CheckChip | null {
  const total = pr.checks_total ?? 0;
  const failing = pr.checks_failing ?? 0;
  switch (pr.check_status) {
    case "passing":
      return {
        text: "checks passed",
        className: "git-pr-check-pass",
        tooltip: countText(total, "check passed", "checks passed"),
      };
    case "failing":
      return {
        text: failing > 0 ? `${String(failing)} failing` : "checks failing",
        className: "git-pr-check-fail",
        tooltip:
          total > 0 && failing > 0
            ? `${String(failing)} of ${String(total)} checks failing`
            : "A required check is failing.",
      };
    case "pending":
      return {
        text: "checks running",
        className: "git-pr-check-pending",
        tooltip: total > 0 ? countText(total, "check running", "checks running") : "CI is running.",
      };
    default:
      return null;
  }
}

function countText(n: number, one: string, many: string): string {
  return `${String(n)} ${n === 1 ? one : many}`;
}

/** Explain why the Merge button is disabled, or "" when it should be
 *  enabled.
 *
 *  Every branch names the cause. The catch-all this replaced said the PR
 *  "isn't mergeable" and told the reader to open it on the forge for
 *  details, which is the panel admitting it cannot answer its own
 *  question; the row's number and title are already links there, so the
 *  advice bought nothing even when it was true.
 *
 *  ABSENT OR EMPTY IS THE ONLY MERGEABLE ANSWER. `merge_blocked` is a plain
 *  string rather than a union so the server's vocabulary can grow, and an
 *  unrecognised value used to fall through to "" — which the row reads as
 *  enabled. That recreates exactly the defect this function was written to
 *  fix: the forge says the PR is blocked while the client offers Merge. So a
 *  cause this build does not know is still a cause, and it is reported
 *  generically with the server's own word in it, which is more useful to
 *  whoever is looking at an unfamiliar block than silence. */
export function mergeBlockReason(pr: GitPR): string {
  const cause = pr.merge_blocked ?? "";
  switch (cause) {
    case "":
      return "";
    case "draft":
      return "this PR is a draft. Mark it ready for review first.";
    case "conflicts":
      return "this PR conflicts with its target branch. Rebase or merge the target in.";
    case "checks_failing":
      return "a required check is failing.";
    case "checks_running":
      return "required checks are still running.";
    case "behind":
      return "the source branch is behind its target and must be updated first.";
    case "blocked":
      return "the forge's merge policy refuses this merge (review, approvals or a protected branch).";
    case "unknown":
      return "the forge reports this PR is not mergeable and does not say why.";
    default:
      return `the forge refuses this merge and reports a cause this build does not recognise (${causeLabel(cause)}).`;
  }
}

/** Render an unrecognised block cause for a tooltip: one line, bounded.
 *  The value is server-produced, but it reaches the reader as text and a
 *  future cause could be long or carry newlines, so it is normalised here
 *  rather than trusted for its provenance. */
function causeLabel(cause: string): string {
  const CAUSE_MAX = 40;
  const oneLine = cause.replace(/\s+/g, " ").trim();
  return oneLine.length > CAUSE_MAX ? oneLine.slice(0, CAUSE_MAX - 1) + "\u2026" : oneLine;
}

/** Forges whose CI can be re-run from a row.
 *
 *  gitea and codeberg are absent because they genuinely cannot: tea has no
 *  CI verb and Gitea Actions' re-run endpoints are outside the stable API,
 *  so the server answers 501 and the row hides the control rather than
 *  offering one that always fails. */
const RERUN_CAPABLE: ReadonlySet<string> = new Set(["github", "gitlab"]);

export function supportsRerun(kind: ForgeKind): boolean {
  return RERUN_CAPABLE.has(kind);
}

/** Whether a row should offer to arm the forge's auto-merge: the checks
 *  are not settled yet, nothing else blocks the merge, and the forge is
 *  not already holding it. Arming a merge that could happen now would be
 *  a slower way to press Merge.
 *
 *  "Nothing else blocks it" has to gate BOTH arms, and that is the fix here.
 *  `checks_running` already says the checks are the only cause, but a bare
 *  `check_status === "pending"` says nothing about the rest — so a draft,
 *  conflicting, behind or policy-blocked PR with a check still running was
 *  offered "Merge when green", which the forge would not honour when the check
 *  went green. That is the shown-and-failing control this row exists to avoid. */
export function canArmAutoMerge(pr: GitPR): boolean {
  if (pr.auto_merge_armed === true || pr.state !== "open") {
    return false;
  }
  const blocked = pr.merge_blocked ?? "";
  if (blocked === "checks_running") {
    return true;
  }
  return blocked === "" && pr.check_status === "pending";
}
