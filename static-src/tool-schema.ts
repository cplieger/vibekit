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

import type { ToolDenial, ToolDisclosed, ToolKind, ToolStatus } from "./types.js";
export type { ToolKind };

/** What a tool card reveals when you open it — its "depth 1".
 *
 *  This REPLACED a three-value display tier (simple / medium / complex), and the
 *  replacement is the point rather than a rename. The tier decided three
 *  unrelated things at once from one axis: whether the card got a disclosure
 *  toggle at all (`simple` got none, so an edit could not be expanded), whether
 *  its output was an always-visible unwindowed box (`complex`), and it said
 *  nothing about WHAT to show. Depth is per KIND because the answer is
 *  per kind: a diff for an edit, a windowed output for a command, nothing at all
 *  for a read.
 *
 *  - `none`     claim-only. There is no second level; the card has no toggle.
 *  - `diff`     the change itself, unified and windowed to whole hunks.
 *  - `output`   first and last N lines with a truncation marker; complete at depth 2.
 *  - `search`   match count per file and the first matching lines.
 *  - `move`     `from -> to`, two facts the claim line cannot carry.
 *  - `fetch`    resolved URL, response status, the head of the body.
 *  - `mcp`      server badge, formatted input, output.
 *  - `todo`     the checklist, next item marked.
 *  - `generic`  the raw input/output block, for kinds with nothing better. */
export type ToolDepth1 =
  "none" | "diff" | "output" | "search" | "move" | "fetch" | "mcp" | "todo" | "generic";

/** Depth-1 content per kind. Record<ToolKind, ToolDepth1> enforces
 *  exhaustiveness at the type level — a new ToolKind without an entry is a
 *  compile error, which is what keeps a new tool from silently landing in a
 *  generic card. */
const TOOL_DEPTH1: Readonly<Record<ToolKind, ToolDepth1>> = {
  // Claim-only, and measured: `read` is 20.2% of 33,156 real tool calls, median
  // 1 per turn but p99 75 and max 190. A card each is exactly where a transcript
  // collapses, so reads carry their fact on the claim line and group (tool-group).
  read: "none",
  // Also claim-only, for the opposite reason: `delete_file` takes ONE targetFile,
  // so a "path list" would be a one-item list restating the claim.
  delete: "none",
  hook: "none",
  think: "none",
  switch_mode: "none",

  edit: "diff",
  write: "diff",
  move: "move",
  search: "search",
  fetch: "fetch",
  mcp: "mcp",

  execute: "output",
  shell: "output",
  command: "output",

  browser: "generic",
  other: "generic",
};

/** What a tool card reveals on expand. */
export function toolDepth1(kind: ToolKind): ToolDepth1 {
  return TOOL_DEPTH1[kind];
}

/** Whether a kind has anything to reveal. A card with no depth 1 gets no
 *  disclosure toggle — a control that opens an empty region is worse than no
 *  control. */
export function hasDepth1(kind: ToolKind): boolean {
  return TOOL_DEPTH1[kind] !== "none";
}

/** What a `read` tool actually read, phrased from the tool FAMILY.
 *
 *  `kind: "read"` covers seven action types and four of them do not read files:
 *  `get_process_output` and `list_processes` read processes, `open_folders` reads
 *  folders, `get_diagnostics` reads diagnostics. "Read 3 files" is false for all
 *  four, and the card's own rule is that a claim must be specific and true. The
 *  mapping keys on the tool NAME (both the live snake_case set and the legacy
 *  camelCase aliases persisted sessions carry) and falls back to files, which is
 *  what the other three read. */
const READ_SUBJECTS: Readonly<Record<string, string>> = {
  list_processes: "processes",
  listProcesses: "processes",
  get_process_output: "processes",
  getProcessOutput: "processes",
  open_folders: "folders",
  openFolders: "folders",
  listDirectory: "folders",
  get_diagnostics: "diagnostics",
  getDiagnostics: "diagnostics",
};

/** The plural noun a `read` claim should use for this tool name. */
export function readSubject(toolName: string): string {
  return READ_SUBJECTS[toolName] ?? "files";
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
  /** The skill or steering document this call loaded into context, or null.
   *  Set only on a `disclose_context` call, and it is the only signal that a
   *  skill's body actually reached the model, so the card names it instead of
   *  showing a generic tool row. */
  disclosed: ToolDisclosed | null;
  /** The policy verdict that refused this call, or null. Present makes the card
   *  read as a refusal rather than a failure: the two want opposite reactions,
   *  edit the rule or debug the tool. */
  denial: ToolDenial | null;
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
  // shell/hook aren't emitted by v3 (execute covers shell; hooks arrive as
  // kind:"other"), but persisted PRE-v3 chats carry them — keep the mappings
  // so a legacy tool card renders in its proper tier instead of falling to OTHER.
  shell: { kind: "shell", writesFile: false },
  hook: { kind: "hook", writesFile: false },
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
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
    return { server: u[1]!, tool: u[2]! };
  }
  const c = MCP_COLON_RE.exec(title);
  if (c !== null) {
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
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
  meta?: { disclosed?: ToolDisclosed | undefined; denial?: ToolDenial | undefined },
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
    disclosed: meta?.disclosed ?? null,
    denial: meta?.denial ?? null,
  };
}

/** The claim line for a disclose_context call. The agent activating a skill is
 *  the moment its body enters the prompt, so the card says which document rather
 *  than naming the tool that fetched it. */
export function disclosedClaim(d: ToolDisclosed): string {
  const kindWord = d.type === "steering" ? "steering" : "skill";
  return `Loaded ${kindWord}: ${d.display_name}`;
}

/** Display titles of KAS-internal bookkeeping announced as tool calls — the
 *  session-boot cloud-config fetch is the one member. The server drops these
 *  frames at translate keyed on `_meta.kiro.toolId` (the machine name), so this
 *  list exists only for transcripts persisted BEFORE that suppression, whose
 *  fragments carry a card stuck at in_progress forever. The persisted ToolCall
 *  has no tool id, so the title — a KAS constant, not model text — is the only
 *  key legacy data offers. */
const INTERNAL_TOOL_TITLES: ReadonlySet<string> = new Set(["Fetching your cloud config"]);

/** Whether a persisted tool call is internal engine bookkeeping the transcript
 *  never renders. See INTERNAL_TOOL_TITLES. */
export function isInternalToolTitle(title: string): boolean {
  return INTERNAL_TOOL_TITLES.has(title);
}
