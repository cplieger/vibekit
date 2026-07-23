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

/** Human-facing label for a mode id. Prefers the live session's mode name
 *  (custom agents carry their own), falls back to the bundled catalog, then
 *  to the raw id. */
export function labelForMode(id: string, modes?: readonly SessionMode[]): string {
  const key = normalizeModeID(id);
  const found = modes?.find((m) => m.id === key) ?? BUILTIN_MODES.find((m) => m.id === key);
  return found?.name ?? id;
}
