// @vitest-environment happy-dom
// Unit tests for git.ts pure helpers — no DOM dependency.
import { describe, it, expect } from "vitest";
import { remoteToWebUrl, friendlyGitError, GIT_ERROR_RULES } from "./git.js";

describe("remoteToWebUrl", () => {
  const cases: Array<[string, string]> = [
    ["git@github.com:user/repo.git", "https://github.com/user/repo"],
    ["git@github.com:user/repo", "https://github.com/user/repo"],
    ["https://github.com/user/repo.git", "https://github.com/user/repo"],
    ["https://github.com/user/repo", "https://github.com/user/repo"],
    ["git@gitlab.com:group/sub/repo.git", "https://gitlab.com/group/sub/repo"],
    ["https://gitlab.com/group/sub/repo.git", "https://gitlab.com/group/sub/repo"],
    ["https://gitlab.com/group/sub/repo", "https://gitlab.com/group/sub/repo"],
  ];

  it.each(cases)("converts %s → %s", (input, expected) => {
    expect(remoteToWebUrl(input)).toBe(expected);
  });
});

describe("friendlyGitError", () => {
  const matchCases: Array<[string, string]> = [
    ["error: Not possible to fast-forward, aborting.", "Branches have diverged. Push your local commits first, or use Re-clone to reset from remote."],
    ["CONFLICT (content): Merge conflict in file.txt", "Merge conflict detected. Use Re-clone to reset, or resolve conflicts manually."],
    ["Permission denied (publickey).", "Permission denied. Check your credentials or SSH key."],
    ["fatal: Could not resolve host: github.com", "Network error: could not reach the remote. Check your connection."],
    ["fatal: could not read Username for 'https://github.com'", "Auth required — connect a forge in Settings → Git to enable push/pull."],
    ["fatal: Authentication failed for 'https://github.com/user/repo.git'", "Auth expired — reconnect your forge in Settings → Git."],
  ];

  it.each(matchCases)("maps %j to friendly message", (input, expected) => {
    expect(friendlyGitError(input)).toBe(expected);
  });

  it("returns null for unrecognized errors", () => {
    expect(friendlyGitError("some unknown git error")).toBeNull();
  });

  it("returns null for empty string", () => {
    expect(friendlyGitError("")).toBeNull();
  });

  it("matches the first applicable rule", () => {
    // "Permission denied" appears before auth patterns
    const result = friendlyGitError("Permission denied (publickey).");
    expect(result).toBe("Permission denied. Check your credentials or SSH key.");
  });
});

describe("GIT_ERROR_RULES", () => {
  it("has triggerAuth set on auth-related rules only", () => {
    const authRules = GIT_ERROR_RULES.filter(r => r.triggerAuth === true);
    expect(authRules.length).toBe(6);
    for (const r of authRules) {
      expect(r.match).toMatch(/Username|device|Authentication|401|403/i);
    }
  });
});
