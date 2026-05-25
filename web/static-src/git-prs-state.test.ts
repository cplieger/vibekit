// @vitest-environment happy-dom
import { describe, it, expect, beforeEach } from "vitest";
import { bindPRState, updateGroupsRef, removePRFromGroups, reinsertPRInGroups } from "./git-prs-state.js";

function makePR(number: number) {
  return { number, title: `PR #${number}`, state: "open", source_branch: "feat", target_branch: "main" };
}

function makeGroup(overrides: Partial<{ forge_id: string; owner: string; name: string; prs: any[] }> = {}) {
  return {
    forge_id: overrides.forge_id ?? "f1",
    forge_kind: "github" as const,
    forge_host: "github.com",
    owner: overrides.owner ?? "org",
    name: overrides.name ?? "repo",
    full_name: `${overrides.owner ?? "org"}/${overrides.name ?? "repo"}`,
    prs: overrides.prs ?? [makePR(10), makePR(5), makePR(2)],
  };
}

describe("git-prs-state", () => {
  let groups: any[];
  let paintCount: number;

  beforeEach(() => {
    groups = [makeGroup()];
    paintCount = 0;
    bindPRState({ groups, paint: () => { paintCount++; } });
    updateGroupsRef(groups);
  });

  describe("reinsertPRInGroups", () => {
    it("reinserts PR at correct position by number", () => {
      const result = removePRFromGroups("f1", "org", "repo", 5);
      expect(result).toBeDefined();
      expect(groups[0]!.prs).toHaveLength(2);
      reinsertPRInGroups(result!);
      expect(groups[0]!.prs).toHaveLength(3);
      expect(groups[0]!.prs[1]!.number).toBe(5);
    });

    it("finds group by forge_id+owner+name after groups array is replaced", () => {
      // Simulate a refresh replacing the groups array with new objects
      const result = removePRFromGroups("f1", "org", "repo", 10);
      expect(result).toBeDefined();

      // Replace groups with a fresh array (simulates refreshPRs assigning new lastGroups)
      const newGroups = [makeGroup({ prs: [makePR(5), makePR(2)] })];
      updateGroupsRef(newGroups);

      // Rollback should find the new group by identity fields, not reference
      reinsertPRInGroups(result!);
      expect(newGroups[0]!.prs).toHaveLength(3);
      expect(newGroups[0]!.prs[0]!.number).toBe(10);
    });

    it("dedup guard prevents double-rollback", () => {
      const result = removePRFromGroups("f1", "org", "repo", 5);
      expect(result).toBeDefined();
      reinsertPRInGroups(result!);
      expect(groups[0]!.prs).toHaveLength(3);
      // Second rollback should be a no-op
      const paintBefore = paintCount;
      reinsertPRInGroups(result!);
      expect(groups[0]!.prs).toHaveLength(3);
      expect(paintCount).toBe(paintBefore); // no extra paint
    });

    it("no-ops when group no longer exists", () => {
      const result = removePRFromGroups("f1", "org", "repo", 5);
      expect(result).toBeDefined();
      // Replace with empty groups
      updateGroupsRef([]);
      const paintBefore = paintCount;
      reinsertPRInGroups(result!);
      expect(paintCount).toBe(paintBefore);
    });
  });
});
