import { describe, it, expect, beforeEach } from "vitest";
import {
  bindPRPaint,
  getPRGroups,
  setPRGroups,
  removePRFromGroups,
  reinsertPRInGroups,
} from "./git-prs-state.js";
import type { GitPR, GitRepoGroup } from "./git-types.js";

function makePR(number: number): GitPR {
  return {
    number,
    title: `PR #${number}`,
    state: "open",
    source_branch: "feat",
    target_branch: "main",
  };
}

function makeGroup(
  overrides: Partial<Pick<GitRepoGroup, "forge_id" | "owner" | "name" | "prs">> = {},
): GitRepoGroup {
  const owner = overrides.owner ?? "org";
  const name = overrides.name ?? "repo";
  return {
    forge_id: overrides.forge_id ?? "f1",
    forge_kind: "github",
    forge_host: "github.com",
    owner,
    name,
    full_name: `${owner}/${name}`,
    prs: overrides.prs ?? [makePR(10), makePR(5), makePR(2)],
  };
}

describe("git-prs-state", () => {
  let groups: GitRepoGroup[];
  let paintCount: number;

  beforeEach(() => {
    groups = [makeGroup()];
    paintCount = 0;
    bindPRPaint(() => {
      paintCount++;
    });
    setPRGroups(groups);
  });

  it("getPRGroups returns the installed canonical array (single source)", () => {
    expect(getPRGroups()).toBe(groups);
  });

  describe("removePRFromGroups", () => {
    it("removes the PR in place and repaints", () => {
      const result = removePRFromGroups("f1", "org", "repo", 5);
      expect(result).toBeDefined();
      expect(result!.pr.number).toBe(5);
      expect(groups[0]!.prs.map((p) => p.number)).toEqual([10, 2]);
      expect(paintCount).toBe(1);
    });

    it("returns undefined when the PR number is not present", () => {
      expect(removePRFromGroups("f1", "org", "repo", 999)).toBeUndefined();
      expect(paintCount).toBe(0);
    });

    it("returns undefined when no group matches the identity", () => {
      expect(removePRFromGroups("other", "org", "repo", 5)).toBeUndefined();
      expect(paintCount).toBe(0);
    });
  });

  describe("reinsertPRInGroups", () => {
    it("reinserts PR at correct position by number", () => {
      const result = removePRFromGroups("f1", "org", "repo", 5);
      expect(result).toBeDefined();
      expect(groups[0]!.prs).toHaveLength(2);
      reinsertPRInGroups(result!);
      expect(groups[0]!.prs).toHaveLength(3);
      expect(groups[0]!.prs.map((p) => p.number)).toEqual([10, 5, 2]);
    });

    it("appends when the number is lower than every remaining PR", () => {
      const result = removePRFromGroups("f1", "org", "repo", 2);
      expect(result).toBeDefined();
      reinsertPRInGroups(result!);
      expect(groups[0]!.prs.map((p) => p.number)).toEqual([10, 5, 2]);
    });

    it("finds group by forge_id+owner+name after the array is replaced", () => {
      // Simulate a refresh replacing the canonical array with new objects.
      const result = removePRFromGroups("f1", "org", "repo", 10);
      expect(result).toBeDefined();

      const newGroups = [makeGroup({ prs: [makePR(5), makePR(2)] })];
      setPRGroups(newGroups);

      // Rollback should find the new group by identity fields, not reference.
      reinsertPRInGroups(result!);
      expect(newGroups[0]!.prs).toHaveLength(3);
      expect(newGroups[0]!.prs[0]!.number).toBe(10);
    });

    it("dedup guard prevents double-rollback", () => {
      const result = removePRFromGroups("f1", "org", "repo", 5);
      expect(result).toBeDefined();
      reinsertPRInGroups(result!);
      expect(groups[0]!.prs).toHaveLength(3);

      // Second rollback should be a no-op (no length change, no extra paint).
      const paintBefore = paintCount;
      reinsertPRInGroups(result!);
      expect(groups[0]!.prs).toHaveLength(3);
      expect(paintCount).toBe(paintBefore);
    });

    it("no-ops when the group no longer exists", () => {
      const result = removePRFromGroups("f1", "org", "repo", 5);
      expect(result).toBeDefined();

      setPRGroups([]);
      const paintBefore = paintCount;
      reinsertPRInGroups(result!);
      expect(paintCount).toBe(paintBefore);
    });
  });
});
