// ---------------------------------------------------------------------------
// The tool-kind NOUN vocabulary: one owner, read by the tool group's mixed
// summary and by the turn footer's info panel.
//
// A LEAF on purpose — it imports the `ToolKind` type and nothing else. The
// footer is a `fundamentals/` primitive, so reading this vocabulary out of
// `tool-group.ts` would pull a disclosure controller, `scroll.ts` and a
// ui-primitives disclosure into it; a second table in the footer would be a
// second statement of one vocabulary.
//
// TOTAL over `ToolKind`, which is what removes the `?? "call"` fallback its
// predecessor needed. That fallback was also a `Record<string, string>` indexed
// by a caller's string, so `kindNoun("constructor", 1)` answered
// `Object`'s own name off the prototype chain; a total table over the union has
// no miss to fall through and no prototype to reach.
// ---------------------------------------------------------------------------

import type { ToolKind } from "./types.js";

/** Singular and plural per kind, spelled out rather than derived.
 *
 *  Both forms are written because the derivation its predecessor used —
 *  append `s`, or `es` after `x`/`h` — is a rule about English that has to be
 *  re-read at every call site to know what a kind says. Eleven of these are
 *  byte-identical to what that derivation produced, which is what keeps the
 *  group summaries unchanged.
 *
 *  The five the old table LACKED (`shell`, `hook`, `browser`, `command`,
 *  `other`) all answered "call" before, so a mixed group holding four shell
 *  commands read "4 calls". Four of them take the noun `TOOL_KIND_LABELS`
 *  already uses for the same kind, so the two surfaces agree; `other` keeps
 *  "call", which is the honest word for a kind whose name is the absence of
 *  one. */
const KIND_NOUNS: Readonly<Record<ToolKind, { one: string; many: string }>> = {
  read: { one: "read", many: "reads" },
  edit: { one: "edit", many: "edits" },
  write: { one: "write", many: "writes" },
  delete: { one: "delete", many: "deletes" },
  move: { one: "move", many: "moves" },
  search: { one: "search", many: "searches" },
  execute: { one: "command", many: "commands" },
  shell: { one: "shell command", many: "shell commands" },
  hook: { one: "hook", many: "hooks" },
  fetch: { one: "fetch", many: "fetches" },
  think: { one: "thinking step", many: "thinking steps" },
  switch_mode: { one: "mode switch", many: "mode switches" },
  mcp: { one: "integration call", many: "integration calls" },
  browser: { one: "page", many: "pages" },
  command: { one: "command", many: "commands" },
  other: { one: "call", many: "calls" },
};

/** What to call `count` calls of this kind. */
export function kindNoun(kind: ToolKind, count: number): string {
  const noun = KIND_NOUNS[kind];
  return count === 1 ? noun.one : noun.many;
}
