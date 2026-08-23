// Lint test: catches regressions to the action framework policy.
//
// Policy: user-initiated mutations must go through the actions
// framework (defineAction / apiAction / transportAction). Background
// reads, cleanup, and infrastructure stay silent.
//
// What this test asserts:
//   - No new write-shaped calls outside the explicit allowlist:
//       * `void apiPost(`, `void apiDelete(` (fire-and-forget mutations)
//       * `await apiPost(`, `await apiDelete(`, `await apiPutOrError(`
//         (callers bypassing the framework's lifecycle wrapper)
//       * `void transport.send(`, `await transport.send(`
//       * `void transportSend(`, `await transportSend(` (aliased imports)
//   - GET reads (`apiGet` / `apiGetTyped`) are NOT lint-checked because
//     they're inherently safe (read-only). Background polls and inline
//     reads use them freely.
//   - The allowlist consists of:
//       * actions/*.ts (the framework + per-area action files)
//       * api-client.ts and transport.ts (the underlying transports)
//       * test files (*.test.ts)
//       * specific files with documented background-poll / cleanup
//         exceptions (BACKGROUND_ALLOWLIST below)
//
// If you're adding a new user-initiated mutation, declare an action
// in actions/<area>.ts and dispatch it. See actions/index.ts.
// If you're adding a legitimate background poll or cleanup that
// must remain silent, add the file to the BACKGROUND_ALLOWLIST
// below with a one-line comment explaining why.
// ---------------------------------------------------------------------------

import { describe, it, expect } from "vitest";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative } from "node:path";

const ROOT = join(import.meta.dirname, "..");

/** Files where a write-shaped call (`void/await apiPost/apiDelete`,
 *  `await apiPutOrError`, `void/await transport.send`) is permitted
 *  because they're documented background paths or cleanup.
 *
 *  NOTE: Matching is by basename only (the last path segment). This
 *  means entries must be unique filenames across the source tree. If
 *  two files share a basename and only one should be allowlisted,
 *  switch to relative-path matching for that entry. */
const BACKGROUND_ALLOWLIST = new Set<string>([
  // Background fan-out for revalidation; partial failure is expected.
  "forge-auth.ts", // await apiPost in revalidateInBackground (probe per forge)

  // OAuth poll loop — runs inside a polling timer that surfaces its own
  // status/error UI; not user-initiated mutations through actions.
  "forge-auth-oauth.ts",

  // Modal dialogs surface errors inline rather than via toast.
  "modals.ts",

  // Inline dialog mutation: error surfaces in the dialog status line,
  // not via toast. Intentionally excluded from the action framework.
  "git-prs-tab.ts", // await apiPost for AI PR-description generation (inline dialog; PR creation is now the git.create_pr action)
]);

/** Regex for forbidden patterns. Each match is a regression candidate.
 *  Matches both `void apiX(` (fire-and-forget mutation) and bare
 *  `await apiX(` outside of action files (caller bypassing the
 *  framework). The lint runs against non-test, non-action source
 *  files. */
const PATTERNS: { name: string; re: RegExp }[] = [
  { name: "void apiPost", re: /\bvoid\s+apiPost\s*[<(]/g },
  { name: "void apiDelete", re: /\bvoid\s+apiDelete\s*[<(]/g },
  { name: "void transport.send", re: /\bvoid\s+transport\.send\s*\(/g },
  { name: "await transport.send", re: /\bawait\s+transport\.send\s*\(/g },
  { name: "void transportSend", re: /\bvoid\s+transportSend\s*\(/g },
  { name: "await transportSend", re: /\bawait\s+transportSend\s*\(/g },
  { name: "await apiPost", re: /\bawait\s+apiPost\s*[<(]/g },
  { name: "await apiDelete", re: /\bawait\s+apiDelete\s*[<(]/g },
  { name: "await apiPutOrError", re: /\bawait\s+apiPutOrError\s*[<(]/g },
];

function listTSFiles(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    const st = statSync(p);
    if (st.isDirectory()) {
      // Skip vendored / build dirs.
      if (name === "node_modules" || name === ".vitest-cache" || name === "actions") {
        continue;
      }
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
      if (BACKGROUND_ALLOWLIST.has(base)) {
        continue;
      }
      // Skip api-client/transport — they're the underlying primitives.
      if (base === "api-client.ts" || base === "transport.ts") {
        continue;
      }
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
        "See web/static-src/actions/index.ts.",
        "If this is a legitimate background poll, add the file to BACKGROUND_ALLOWLIST in actions/lint.node.test.ts.",
        "",
        "Violations:",
        ...violations.map((v) => `  ${v}`),
      ].join("\n");
      throw new Error(message);
    }
    expect(violations).toEqual([]);
  });
});
