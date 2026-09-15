// ---------------------------------------------------------------------------
// The git badge's vocabulary: TWO states plus hidden, painted in the two hues a
// developer already reads without a legend — red is broken, amber is modified.
// That is what `.git-st-*` uses per file and what VS Code uses per file
// (gitDecoration.deletedResourceForeground / .modifiedResourceForeground).
//
// Most of what this pins is the two states that are GONE. `remote` (behind
// origin) is not a state here because nothing in this app runs `git fetch`, so
// `behind` is stale by construction and a hue asserting it would be a claim the
// data cannot support — VS Code reports it as a colourless `↓N` count beside a
// sync icon, and the git panel does the same. `both` was the blend that state
// forced into existence, and a 50/50 amber-violet mix has no interpretation for
// a reader at all.
//
// Both halves live in one file because they are ONE decision: what a reader sees
// is the derivation's answer painted by the stylesheet, so pinning either alone
// leaves the other free to disagree. That already happened once in the other
// direction — the badge called behind-origin violet while the panel's own
// `.git-repo-behind` called it amber.
// ---------------------------------------------------------------------------

import { describe, expect, it } from "vitest";
import { deriveState, deriveTooltip } from "./git-badge.js";
import { allRules, loadCSS, manifestSheets, ruleContaining } from "./__test-helpers__/css-rules.js";
import type { GitRepoStatusBadge } from "./git-types.js";
import type { ConfiguredForge } from "./wire/types.gen.js";

function repo(over: Partial<GitRepoStatusBadge> = {}): GitRepoStatusBadge {
  return {
    repo: "app",
    is_repo: true,
    branch: "main",
    ahead: 0,
    behind: 0,
    has_dirty: false,
    ...over,
  };
}

function forge(over: Partial<ConfiguredForge> = {}): ConfiguredForge {
  return {
    id: "github:github.com",
    kind: "github",
    host: "github.com",
    connected: true,
    ...over,
  };
}

describe("git badge state", () => {
  it("hides when every repo is clean and no forge is broken", () => {
    expect(deriveState({ repos: [repo(), repo({ repo: "lib" })] }, [forge()])).toEqual({
      kind: "none",
    });
  });

  it("is dirty when a repo has an uncommitted change", () => {
    expect(deriveState({ repos: [repo({ has_dirty: true })] }, [])).toEqual({
      kind: "dirty",
      dirtyCount: 1,
    });
  });

  it("is dirty when a repo's only change is an unpushed commit", () => {
    // `ahead` is local work the reader still owns, so it counts as dirty even
    // though the working tree is clean.
    expect(deriveState({ repos: [repo({ ahead: 2 })] }, [])).toEqual({
      kind: "dirty",
      dirtyCount: 1,
    });
  });

  it("stays HIDDEN for a repo that is only behind origin", () => {
    // The retired `remote` state. `behind` never reaches the badge, because
    // nothing here fetches, so the number is stale by construction.
    expect(deriveState({ repos: [repo({ behind: 7 })] }, [])).toEqual({ kind: "none" });
  });

  it("reports dirty-AND-behind as plain dirty, with no blended state", () => {
    // The retired `both` state. One repo dirty, another behind: exactly the
    // input that used to produce the amber/violet mix.
    expect(
      deriveState({ repos: [repo({ has_dirty: true }), repo({ repo: "lib", behind: 3 })] }, []),
    ).toEqual({ kind: "dirty", dirtyCount: 1 });
  });

  it("counts each dirty repo once, however many ways it is dirty", () => {
    expect(deriveState({ repos: [repo({ has_dirty: true, ahead: 4, behind: 9 })] }, [])).toEqual({
      kind: "dirty",
      dirtyCount: 1,
    });
  });

  it("skips a path that is not a repo", () => {
    expect(deriveState({ repos: [repo({ is_repo: false, has_dirty: true })] }, [])).toEqual({
      kind: "none",
    });
  });

  it("lets a broken forge outrank dirty repos", () => {
    expect(
      deriveState({ repos: [repo({ has_dirty: true })] }, [forge({ last_error: "bad token" })]),
    ).toEqual({ kind: "error", forgeIds: ["github:github.com"] });
  });

  it("is not an error for a forge that is merely disconnected", () => {
    expect(
      deriveState({ repos: [] }, [forge({ connected: false, last_error: "bad token" })]),
    ).toEqual({ kind: "none" });
  });

  it("is not an error for an empty last_error", () => {
    expect(deriveState({ repos: [] }, [forge({ last_error: "" })])).toEqual({ kind: "none" });
  });

  it("tolerates an absent repo list", () => {
    expect(deriveState({}, [])).toEqual({ kind: "none" });
  });
});

describe("git badge tooltip", () => {
  it("names the one broken forge", () => {
    expect(deriveTooltip({ kind: "error", forgeIds: ["gitlab:gitlab.com"] })).toBe(
      "Forge auth issue: gitlab:gitlab.com",
    );
  });

  it("counts several broken forges rather than listing them", () => {
    expect(deriveTooltip({ kind: "error", forgeIds: ["a", "b", "c"] })).toBe(
      "3 forges with auth issues",
    );
  });

  it("says local changes rather than uncommitted, because ahead is committed", () => {
    expect(deriveTooltip({ kind: "dirty", dirtyCount: 1 })).toBe("1 repo with local changes");
    expect(deriveTooltip({ kind: "dirty", dirtyCount: 4 })).toBe("4 repos with local changes");
  });

  it("says nothing when the badge is hidden", () => {
    expect(deriveTooltip({ kind: "none" })).toBe("");
  });
});

describe("git badge paint", () => {
  /** Every rule in the bundle whose selector list mentions the badge. */
  function badgeRules(): { selector: string; body: string }[] {
    return manifestSheets().flatMap((s) =>
      allRules(s.css).filter((r) => r.selector.includes(".git-badge")),
    );
  }

  it("declares exactly one state arm, and it is the error one", () => {
    const arms = badgeRules()
      .map((r) => r.selector)
      .filter((s) => s.includes("[data-state="));
    expect(arms).toEqual(['.git-badge[data-state="error"]']);
  });

  it("paints red for error and amber for the dirty default", () => {
    const sheet = loadCSS("14-tools.css");
    const rules = allRules(sheet);
    const base = rules.find((r) => r.selector === ".git-badge");
    const error = rules.find((r) => r.selector === '.git-badge[data-state="error"]');
    expect(base?.body).toContain("background: var(--c-yellow)");
    expect(error?.body).toContain("background: var(--c-red)");
  });

  it("reaches for the state inks, never the destructive-action palette", () => {
    // --c-danger / --c-warning are the delete-confirm palette (vibekit-ui.md
    // "Color system"); a status mark takes --c-red / --c-yellow.
    const offenders = badgeRules().filter(
      (r) => r.body.includes("--c-danger") || r.body.includes("--c-warning"),
    );
    expect(offenders.map((r) => r.selector)).toEqual([]);
  });

  it("agrees with the per-repo dirty mark it aggregates", () => {
    const dot = allRules(loadCSS("22-git-multirepo.css")).find(
      (r) => r.selector === ".git-repo-dirty-dot",
    );
    expect(dot?.body).toContain("background: var(--c-yellow)");
  });

  it("leaves the ahead and behind counts unhued", () => {
    // They are numbers, and VS Code renders the same pair colourless. A tint
    // here would give amber a second meaning beside "modified". `ruleContaining`
    // rather than an exact selector match, so the two may be listed in either
    // order and on either line.
    const sheet = loadCSS("22-git-multirepo.css");
    for (const member of [".git-repo-ahead", ".git-repo-behind"]) {
      expect(ruleContaining(sheet, member).body).toContain("color: var(--c-text-secondary)");
    }
  });

  it("carries no arm for a retired state in any stylesheet", () => {
    const retired = ['data-state="both"', 'data-state="remote"', 'data-state="local"'];
    const offenders: string[] = [];
    for (const s of manifestSheets()) {
      for (const r of allRules(s.css)) {
        if (!r.selector.includes(".git-badge")) {
          continue;
        }
        for (const name of retired) {
          if (r.selector.includes(name)) {
            offenders.push(`${s.name}: ${r.selector}`);
          }
        }
      }
    }
    expect(offenders).toEqual([]);
  });
});
