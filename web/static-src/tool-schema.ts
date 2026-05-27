// ---------------------------------------------------------------------------
// Tool schema: a single table of everything the client needs to know about
// each tool kind/name. Replaces the stringly typed `kind === "edit" || "write"`
// checks and the handful of input-key probes (`path`, `targetFile`,
// `sourcePath`) that were scattered across the rendering code.
//
// Adding support for a new kiro-cli tool is a one-line entry here; the
// rendering pipeline picks it up automatically. Unknown kinds fall through
// to the generic profile.
// ---------------------------------------------------------------------------

import type { ToolKind, ToolStatus } from "./types.js";
export type { ToolKind };

/** Display tier for tool cards in the main transcript.
 *  - simple:  1-line card, no expandable body (file reads, edits, deletes, moves)
 *  - medium:  2-line card with subtitle row (searches, web fetches, thinks)
 *  - complex: scrollable output box (shell commands, MCP tools with output) */
export type ToolTier = "simple" | "medium" | "complex";

/** Map from tool kind to display tier. Record<ToolKind, ToolTier> enforces
 *  exhaustiveness at the type level — adding a new ToolKind without a tier
 *  entry is a compile error. */
const TOOL_TIERS: Readonly<Record<ToolKind, ToolTier>> = {
  read: "simple",
  edit: "simple",
  write: "simple",
  delete: "simple",
  move: "simple",
  search: "medium",
  fetch: "medium",
  think: "medium",
  switch_mode: "medium",
  other: "medium",
  execute: "complex",
  shell: "complex",
  command: "complex",
  browser: "medium",
  mcp: "complex",
  hook: "simple",
};

/** Classify a tool kind into a display tier. */
export function toolTier(kind: ToolKind): ToolTier {
  return TOOL_TIERS[kind];
}

interface ToolProfile {
  /** Normalized kind used for summaries, icons, and grouping. */
  kind: ToolKind;
  /** Whether this tool produces a diff-able (old, new) pair. */
  writesFile: boolean;
}

export interface ToolRenderInfo {
  kind: ToolKind;
  writesFile: boolean;
  /** Normalized workspace-relative path of the affected file, or "". */
  filePath: string;
  /** Basename derived from filePath, or "". */
  fileBasename: string;
  /** Source pair for a diff render, or null when not applicable. */
  diffSources: { oldText: string; newText: string } | null;
  /** MCP server and tool names, extracted from the mangled title if
   *  this is an MCP tool call (kind === "mcp"). null otherwise. Used
   *  for legible rendering of what would otherwise be "mcp__github__
   *  create_issue". */
  mcp: { server: string; tool: string } | null;
}

/** Lookup profile by tool title (kiro-cli names). Unknown titles resolve
 *  through the kind field the server already provides. */
const TITLE_PROFILES: Readonly<Record<string, ToolProfile>> = {
  // File reads
  readFile: { kind: "read", writesFile: false },
  readCode: { kind: "read", writesFile: false },
  readMultipleFiles: { kind: "read", writesFile: false },
  listDirectory: { kind: "read", writesFile: false },

  // File writes
  fsWrite: { kind: "write", writesFile: true },
  fsAppend: { kind: "write", writesFile: true },
  strReplace: { kind: "edit", writesFile: true },
  FileEdit: { kind: "edit", writesFile: true },
  FileWrite: { kind: "write", writesFile: true },

  // File lifecycle
  deleteFile: { kind: "delete", writesFile: false },
  smartRelocate: { kind: "move", writesFile: false },
  semanticRename: { kind: "edit", writesFile: false },

  // Discovery
  fileSearch: { kind: "search", writesFile: false },
  grepSearch: { kind: "search", writesFile: false },

  // Shell / web / reasoning
  executePwsh: { kind: "execute", writesFile: false },
  webFetch: { kind: "fetch", writesFile: false },
  remote_web_search: { kind: "fetch", writesFile: false },
};

/** Fallback profile keyed on the ACP-provided kind string. Covers tools
 *  the client doesn't explicitly know. */
const KIND_FALLBACK: Readonly<Record<string, ToolProfile>> = {
  read: { kind: "read", writesFile: false },
  edit: { kind: "edit", writesFile: true },
  write: { kind: "write", writesFile: true },
  delete: { kind: "delete", writesFile: false },
  move: { kind: "move", writesFile: false },
  search: { kind: "search", writesFile: false },
  execute: { kind: "execute", writesFile: false },
  command: { kind: "command", writesFile: false },
  browser: { kind: "browser", writesFile: false },
  fetch: { kind: "fetch", writesFile: false },
  think: { kind: "think", writesFile: false },
  switch_mode: { kind: "switch_mode", writesFile: false },
};

const OTHER: ToolProfile = { kind: "other", writesFile: false };

/** Set of tool kinds that mutate the workspace (create, modify, or remove
 *  files). Used by the git badge and any other "repo dirty" indicator. */
const REPO_MUTATING_KINDS: ReadonlySet<string> = new Set(["edit", "write", "delete", "move"]);

/** Reports whether a tool kind mutates the workspace (single source of truth
 *  for the "modifies repo" concept). */
export function isRepoMutatingKind(kind: string): boolean {
  return REPO_MUTATING_KINDS.has(kind);
}

/** Reports whether a tool call is still running (pending or in progress). */
export function isToolActive(s: ToolStatus): boolean {
  return s === "pending" || s === "in_progress";
}

/** Reports whether a tool call has settled (completed or failed). */
export function isToolDone(s: ToolStatus): boolean {
  return s === "completed" || s === "failed";
}

/** Resolve the profile for (title, kind) pair. Title wins when it matches
 *  a known entry; otherwise we fall back to the ACP kind. MCP-prefixed
 *  titles (mcp__<server>__<tool> or mcp:<server>:<tool>) resolve to the
 *  synthetic "mcp" kind so renderers can special-case them. */
export function profileFor(title: string, kind: string): ToolProfile {
  if (mcpToolInfo(title) !== null) {
    return { kind: "mcp", writesFile: false };
  }
  return TITLE_PROFILES[title] ?? KIND_FALLBACK[kind] ?? OTHER;
}

// --- MCP name parsing ---
//
// Ecosystem conventions for MCP tool names vary slightly:
//   Claude Code / Anthropic SDK: `mcp__<server>__<tool>`  (double underscore)
//   Zed editor:                  `mcp:<server>:<tool>`    (colon)
//   kiro-cli:                    not publicly documented, but almost
//                                certainly one of the above since kiro
//                                follows the ACP/MCP conventions
//
// We accept either form. The parser is strict about structure (exactly
// two separators) so a user-authored built-in tool happening to contain
// a double underscore won't be misidentified.

const MCP_UNDERSCORE_RE = /^mcp__([A-Za-z0-9][A-Za-z0-9_.-]*)__([A-Za-z0-9][A-Za-z0-9_.-]*)$/;
const MCP_COLON_RE = /^mcp:([A-Za-z0-9][A-Za-z0-9_.-]*):([A-Za-z0-9][A-Za-z0-9_.-]*)$/;

/** Extract the server and tool names from an MCP-prefixed tool title.
 *  Returns null for non-MCP titles. Title is passed verbatim from the
 *  wire (no "Running: " prefix — that's stripped at call site). */
export function mcpToolInfo(title: string): { server: string; tool: string } | null {
  const u = MCP_UNDERSCORE_RE.exec(title);
  if (u !== null) {
    return { server: u[1]!, tool: u[2]! };
  }
  const c = MCP_COLON_RE.exec(title);
  if (c !== null) {
    return { server: c[1]!, tool: c[2]! };
  }
  return null;
}

/** Format an MCP tool for display in a card/summary. Underscores become
 *  spaces so `create_issue` reads as `create issue`. The server name
 *  stays verbatim — users choose their own server names and we don't
 *  want to butcher them. */
export function formatMCPToolName(tool: string): string {
  return tool.replace(/_/g, " ");
}

// --- Input-shape extraction ---

const PATH_KEYS: readonly string[] = [
  "path",
  "targetFile",
  "sourcePath",
  "file",
  "destinationPath",
];

function pickFilePath(input: Record<string, unknown> | undefined): string {
  if (input === undefined) {
    return "";
  }
  for (const k of PATH_KEYS) {
    const v = input[k];
    if (typeof v === "string" && v !== "") {
      return v;
    }
  }
  return "";
}

function pickDiffSources(
  input: Record<string, unknown> | undefined,
): { oldText: string; newText: string } | null {
  if (input === undefined) {
    return null;
  }
  const os = input["oldStr"];
  const ns = input["newStr"];
  if (typeof os === "string" && typeof ns === "string") {
    return { oldText: os, newText: ns };
  }
  // fsWrite / fsAppend use `text` for the full new content; prior content
  // isn't on the wire. Render as pure-add — still useful for new files and
  // informative for overwrites.
  const t = input["text"];
  if (typeof t === "string") {
    return { oldText: "", newText: t };
  }
  return null;
}

/** Build the combined rendering info for a tool call. Single entry point
 *  used by every rendering path so they all agree. */
export function renderInfoFor(
  title: string,
  kind: string,
  input: Record<string, unknown> | undefined,
): ToolRenderInfo {
  const profile = profileFor(title, kind);
  const filePath = pickFilePath(input);
  const fileBasename = filePath !== "" ? (filePath.split("/").pop() ?? filePath) : "";
  const diffSources = profile.writesFile ? pickDiffSources(input) : null;
  const mcp = profile.kind === "mcp" ? mcpToolInfo(title) : null;
  return {
    kind: profile.kind,
    writesFile: profile.writesFile,
    filePath,
    fileBasename,
    diffSources,
    mcp,
  };
}
