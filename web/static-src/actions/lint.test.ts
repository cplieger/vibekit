// @vitest-environment node
// Lint test: catches regressions to the action framework policy.
//
// Policy: user-initiated mutations must go through the actions
// framework (defineAction / apiAction / transportAction). Background
// reads, cleanup, and infrastructure stay silent.
//
// What this test asserts:
//   - No new `void apiPost(`, `void apiPut(`, `void apiPatch(`,
//     `void apiDelete(`, or `void transport.send(` calls appear
//     outside the explicit allowlist below.
//   - Also catches aliased transport imports: `await transportSend(`
//     and `void transportSend(` (common when destructuring or
//     renaming the import).
//   - The allowlist consists of:
//       * actions/*.ts (the framework + per-area action files)
//       * api-client.ts and transport.ts (the underlying transports)
//       * test files (*.test.ts)
//       * specific files with documented background-poll / cleanup
//         exceptions
//
// If you're adding a new user-initiated mutation, declare an action
// in actions/<area>.ts and dispatch it. See actions/README.md.
// If you're adding a legitimate background poll or cleanup that
// must remain silent, add the file to the BACKGROUND_ALLOWLIST
// below with a one-line comment explaining why.
// ---------------------------------------------------------------------------

import { describe, it, expect } from "vitest";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative } from "node:path";

const ROOT = join(import.meta.dirname, "..");

/** Files where `void apiX(` or `void transport.send(` is permitted
 *  because they're documented background paths or cleanup.
 *
 *  NOTE: Matching is by basename only (the last path segment). This
 *  means entries must be unique filenames across the source tree. If
 *  two files share a basename and only one should be allowlisted,
 *  switch to relative-path matching for that entry. */
const BACKGROUND_ALLOWLIST = new Set<string>([
  // Background reads on view-open. Errors render as inline empty/error
  // state in the panel; not user-initiated mutations.
  "settings.ts",        // void apiGet steering content
  "tools.ts",           // void apiGet tools list
  "kiro-config.ts",     // void apiGet workspace kiro-config
  "git-changes-tab.ts", // void apiGet for inline diff + recent commits expansion
  "git-branch-switcher.ts", // void apiGet branches list when popover opens
  "app.ts",             // void apiGet whoami at startup

  // Best-effort cleanup the user already moved past.
  "notify.ts",          // void apiPost /api/push/unsubscribe on disable

  // Background fan-out for revalidation; partial failure is expected.
  "forge-auth.ts",      // apiPost in revalidateInBackground (probe per forge)

  // Fire-and-forget cleanup after successful plan send.
  "plan-actions.ts",    // await apiDelete plan-draft
]);

/** Regex for forbidden patterns. Each match is a regression candidate.
 *  Matches both `void apiX(` (fire-and-forget mutation) and bare
 *  `await apiX(` outside of action files (caller bypassing the
 *  framework). The lint runs against non-test, non-action source
 *  files. */
const PATTERNS: { name: string; re: RegExp }[] = [
  { name: "void apiPost", re: /\bvoid\s+apiPost\s*\(/g },
  { name: "void apiPut", re: /\bvoid\s+apiPut\s*\(/g },
  { name: "void apiPatch", re: /\bvoid\s+apiPatch\s*\(/g },
  { name: "void apiDelete", re: /\bvoid\s+apiDelete\s*\(/g },
  { name: "void transport.send", re: /\bvoid\s+transport\.send\s*\(/g },
  { name: "await transport.send", re: /\bawait\s+transport\.send\s*\(/g },
  { name: "void transportSend", re: /\bvoid\s+transportSend\s*\(/g },
  { name: "await transportSend", re: /\bawait\s+transportSend\s*\(/g },
  { name: "await apiPost", re: /\bawait\s+apiPost\s*\(/g },
  { name: "await apiPut", re: /\bawait\s+apiPut\s*\(/g },
  { name: "await apiPatch", re: /\bawait\s+apiPatch\s*\(/g },
  { name: "await apiDelete", re: /\bawait\s+apiDelete\s*\(/g },
  { name: "await apiPostOrError", re: /\bawait\s+apiPostOrError\s*\(/g },
  { name: "await apiPutOrError", re: /\bawait\s+apiPutOrError\s*\(/g },
];

function listTSFiles(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    const st = statSync(p);
    if (st.isDirectory()) {
      // Skip vendored / build dirs.
      if (name === "node_modules" || name === ".vitest-cache" || name === "actions") continue;
      listTSFiles(p, out);
    } else if (name.endsWith(".ts") && !name.endsWith(".test.ts") && !name.endsWith(".d.ts")) {
      out.push(p);
    }
  }
  return out;
}

describe("action framework — regression guard", () => {
  it("no new `void apiX(` or `void transport.send(` outside allowlist", () => {
    const violations: string[] = [];
    for (const file of listTSFiles(ROOT)) {
      const rel = relative(ROOT, file);
      const base = rel.split("/").pop() ?? rel;
      if (BACKGROUND_ALLOWLIST.has(base)) continue;
      // Skip api-client/transport — they're the underlying primitives.
      if (base === "api-client.ts" || base === "transport.ts") continue;
      const src = readFileSync(file, "utf8");
      for (const { name, re } of PATTERNS) {
        for (const m of src.matchAll(re)) {
          const lineIdx = src.slice(0, m.index).split("\n").length;
          violations.push(`${rel}:${String(lineIdx)}: ${name} (${m[0].trim()})`);
        }
      }
    }
    if (violations.length > 0) {
      const message = [
        "User-initiated mutations must go through the actions framework.",
        "See web/static-src/actions/README.md.",
        "If this is a legitimate background poll, add the file to BACKGROUND_ALLOWLIST in actions/lint.test.ts.",
        "",
        "Violations:",
        ...violations.map((v) => `  ${v}`),
      ].join("\n");
      throw new Error(message);
    }
    expect(violations).toEqual([]);
  });
});
