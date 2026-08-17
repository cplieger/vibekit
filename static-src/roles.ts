// ---------------------------------------------------------------------------
// Mode catalog: maps a session mode id to a label, description, and icon.
// Shared by the prompt-bar mode pill (role-picker.ts) and the chat-tab icon
// (chat.ts openChatTab / tabs.ts).
//
// On kiro-cli v3 (KAS) every role is a *mode* in the session's
// availableModes — the bundled workflow modes AND every workspace custom
// agent (.kiro/agents/*), switched via session/set_mode. The wire tags each
// with _meta.kiro.source ("bundled" | "workspace"); the picker groups
// bundled modes above custom agents.
//
// Icons: each bundled workflow mode gets a distinct glyph; every custom
// agent (and bundled non-workflow agents like semantic_reviewer) shares one
// common hexagon. Matching is by mode id, and the v2 agent ids
// (kiro_default / kiro_planner) resolve too so the legacy engine's tab
// icons still work when KIRO_AGENT_ENGINE=v2 is pinned:
//   vibe / "" / kiro_default -> wand (Default)
//   spec                     -> checklist (Spec)
//   quick-spec               -> file-check (Quick Spec)
//   bug-fix                  -> bug (Bug Fix)
//   plan / kiro_planner      -> notes (Plan)
//   autonomous               -> bot (Autonomous)
//   anything else            -> hexagon (custom agent)
// ---------------------------------------------------------------------------

import {
  ICON_TAB_CHAT,
  ICON_TAB_PLAN,
  ICON_TAB_SPEC,
  ICON_TAB_AGENT,
  ICON_TAB_QUICK_SPEC,
  ICON_TAB_BUG,
  ICON_TAB_AUTONOMOUS,
  ICON_SUBAGENT_INTROSPECT,
  ICON_SUBAGENT_GATHERER,
  ICON_SUBAGENT_TASK,
  ICON_SUBAGENT_CREATOR,
} from "./icons.js";
import type { SessionMode } from "./types.js";

/** The engine-default mode id. kiro-cli v3's session/new starts here; a
 *  chat with an empty current_mode_id is in this mode. Labelled "Default". */
const DEFAULT_MODE_ID = "vibe";

/** The bundled v3 workflow modes, in canonical order. Seeds the picker for
 *  an empty chat that has no live session yet (availableModes arrives only
 *  with session/new). Once the session reports availableModes that list is
 *  authoritative — it carries the same bundled modes PLUS workspace custom
 *  agents. ids/names/descriptions mirror kiro-cli v3's session/new
 *  modes.availableModes. */
const BUILTIN_MODES: readonly SessionMode[] = [
  { id: "vibe", name: "Default", description: "General coding assistance", source: "bundled" },
  { id: "spec", name: "Spec", description: "Structured feature development", source: "bundled" },
  {
    id: "quick-spec",
    name: "Quick Spec",
    description: "Fast spec workflow: clarify, then auto-generate requirements, design, and tasks",
    source: "bundled",
  },
  {
    id: "bug-fix",
    name: "Bug Fix",
    description: "Structured bug-fix workflow: investigate, diagnose, and resolve bugs",
    source: "bundled",
  },
  {
    id: "plan",
    name: "Plan",
    description:
      "Plan-only mode that helps break ideas down into an implementation plan without making any changes",
    source: "bundled",
  },
  {
    id: "autonomous",
    name: "Autonomous",
    description: "Autonomous agent execution",
    source: "bundled",
  },
];

/** Server-fetched pre-session mode catalog (kiro-cli 2.14
 *  /api/config-template): the bundled modes + bundled agents + the user's
 *  global ~/.kiro/agents, with real names/descriptions/source tags.
 *  Replaces BUILTIN_MODES as the picker's base once fetched; BUILTIN_MODES
 *  stays the offline/pre-fetch fallback. Workspace agents are NOT in the
 *  template (it is built session-less with no workspace paths) — the role
 *  picker merges those from /api/workspace/kiro-config. */
let catalogModes: readonly SessionMode[] | null = null;

export function setCatalogModes(modes: readonly SessionMode[]): void {
  catalogModes = modes;
}

/** The pre-session mode base: the fetched catalog when available, else the
 *  static bundled list. */
export function catalogBaseModes(): readonly SessionMode[] {
  return catalogModes ?? BUILTIN_MODES;
}

/** One offered mode plus what the pre-session merge had to DECIDE about it.
 *
 *  `shadowed` is that decision, made visible. It is not a field on SessionMode
 *  because SessionMode is a codegen'd wire type and this is a client-side
 *  resolution the wire never carries: the live session's availableModes arrives
 *  already resolved by KAS, so a shadow only exists in the pre-session window. */
export interface PickerMode {
  mode: SessionMode;
  /** The SOURCE of the catalog entry this workspace agent shadows (`global` or
   *  `bundled`), when it shadows one. Absent when nothing is shadowed. */
  shadowed?: string;
}

/** The description a workspace agent carries before a session can supply its
 *  own. Exported so a test can assert the merge's output shape without pinning
 *  the sentence. */
export const WORKSPACE_AGENT_DESC = "Custom agent from your workspace .kiro/agents/ folder.";

/** Merge the pre-session catalog with the workspace agents, MODELLING the
 *  collision instead of deduping it away.
 *
 *  The rule is KAS's own last-write-wins: when a workspace agent and a catalog
 *  entry (a bundled mode, or the user's global ~/.kiro/agents) share an id, the
 *  WORKSPACE definition is what a session loads. So the workspace row is the one
 *  offered, and it says what it shadows.
 *
 *  That is the opposite of what this merge used to do. It filtered the workspace
 *  agent OUT whenever the catalog held its id, so the surviving row was the
 *  GLOBAL entry while the comment three lines above claimed the workspace
 *  definition was what a session would load — the picker did not merely hide one
 *  of the two, it showed the wrong one, and the user had no way to tell.
 *
 *  Both halves matter: dropping the shadowed catalog row keeps one row per id
 *  (two rows carrying the same id would offer a choice `session/set_mode` cannot
 *  express, since both would send the same mode id), and marking the survivor is
 *  what tells the user which definition a run will use. */
export function mergeCatalogAndWorkspace(
  base: readonly SessionMode[],
  workspaceAgents: readonly string[],
): PickerMode[] {
  const workspace = new Set(workspaceAgents);
  const out: PickerMode[] = [];
  for (const m of base) {
    if (workspace.has(m.id)) {
      continue; // shadowed by the workspace definition appended below
    }
    out.push({ mode: m });
  }
  for (const name of workspaceAgents) {
    const shadows = base.find((m) => m.id === name);
    out.push({
      mode: {
        id: name,
        name,
        description: WORKSPACE_AGENT_DESC,
        source: "workspace",
      },
      ...(shadows !== undefined && { shadowed: shadows.source ?? "bundled" }),
    });
  }
  return out;
}

/** Human-facing label for a mode's scope. The wire's `source` values are
 *  `bundled` | `global` | `workspace`; a bundled mode needs no label (it is the
 *  top group and every row in it is bundled), which is why this answers "" for
 *  it rather than "bundled". */
export function scopeLabel(source: string | undefined): string {
  switch (source) {
    case "workspace":
      return "workspace";
    case "global":
      return "global";
    default:
      return "";
  }
}

/** Normalize an empty / legacy mode id to the canonical default. Maps the
 *  v2 default-agent ids onto the default so mixed-engine state resolves to
 *  one highlighted entry in the picker. */
export function normalizeModeID(id: string): string {
  if (id === "") {
    return DEFAULT_MODE_ID;
  }
  return id;
}

/** Icon (SVG string) for a mode/role, keyed by id. Each bundled workflow
 *  mode gets a distinct glyph; every other entry (workspace custom agents,
 *  bundled non-workflow agents) shares the hexagon. */
export function iconForMode(id: string): string {
  switch (id) {
    case "":
    case "vibe":
      return ICON_TAB_CHAT;
    case "spec":
      return ICON_TAB_SPEC;
    case "quick-spec":
      return ICON_TAB_QUICK_SPEC;
    case "bug-fix":
      return ICON_TAB_BUG;
    case "plan":
      return ICON_TAB_PLAN;
    case "autonomous":
      return ICON_TAB_AUTONOMOUS;
    default:
      return ICON_TAB_AGENT;
  }
}

/** Icon (SVG string) for a subagent, keyed by the invoke_sub_agent tool's
 *  input name (raw id, e.g. "introspect", "context-gatherer"). Mirrors
 *  iconForMode's convention on the SubagentBlock header: each pre-built
 *  kiro-cli subagent gets a distinct glyph, custom/unknown subagents share
 *  the agent hexagon. Subagents are NOT modes — the bundled set here
 *  (kiro-cli 2.13: general-task-execution, context-gatherer,
 *  custom-agent-creator, introspect) never appears in availableModes, so
 *  the two lookups stay separate. */
export function iconForSubagent(name: string): string {
  switch (name) {
    case "introspect":
      return ICON_SUBAGENT_INTROSPECT;
    case "context-gatherer":
      return ICON_SUBAGENT_GATHERER;
    case "general-task-execution":
      return ICON_SUBAGENT_TASK;
    case "custom-agent-creator":
      return ICON_SUBAGENT_CREATOR;
    default:
      return ICON_TAB_AGENT;
  }
}

/** Display form of a mode's name, for names that are really identifiers.
 *
 *  The six bundled workflow modes carry hand-written names (`bug-fix` is
 *  "Bug Fix"), so they never reach this. Everything else arrives named by
 *  whoever declared it, and two of those sources hand over a raw id: KAS names
 *  its non-workflow agents after themselves (`semantic_reviewer`), and a
 *  workspace agent is named by its front matter, which is commonly the file's
 *  own snake_case stem. Rendering those verbatim put an underscore in the role
 *  list beside eleven properly-spaced neighbours.
 *
 *  Applied by SHAPE rather than by a list of known ids, so an agent added later
 *  — by KAS or by the user — is covered without an edit here.
 *
 *  A name is treated as an identifier only when it has no whitespace and no
 *  capital: both are marks of a human having written it, and neither survives
 *  the transformation intact, so respecting them is what keeps this from
 *  mangling a deliberate name. "iOS review" and "Bug Fix" pass through
 *  untouched; `semantic_reviewer` becomes "Semantic Reviewer". */
export function displayModeName(name: string): string {
  if (name === "" || /\s/.test(name) || /[A-Z]/.test(name)) {
    return name;
  }
  return name
    .split(/[-_.]+/)
    .filter((word) => word !== "")
    .map((word) => word.replace(/^./, (first) => first.toUpperCase()))
    .join(" ");
}

/** Human-facing label for a mode id. Prefers the live session's mode name
 *  (custom agents carry their own), falls back to the bundled catalog, then
 *  to the raw id. */
export function labelForMode(id: string, modes?: readonly SessionMode[]): string {
  const key = normalizeModeID(id);
  const found = modes?.find((m) => m.id === key) ?? BUILTIN_MODES.find((m) => m.id === key);
  return displayModeName(found?.name ?? id);
}
