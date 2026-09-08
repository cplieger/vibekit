// ---------------------------------------------------------------------------
// Reasoning-effort vocabulary: which tiers exist and which one is live. A leaf,
// because two surfaces read it and have to agree — the model card's tier row and
// the model pill. The DEFAULT is never a local table: it arrives per model on the
// catalog as `ModelInfo.default_effort_level`.
// ---------------------------------------------------------------------------

import type { ModelInfo, Session, SessionEffortLevel } from "./types.js";

/** Canonical effort levels with display labels: the FALLBACK vocabulary and the
 *  label table, never the authority. The authority is the `effortLevel` config
 *  option's own choices, per session (`Session.effort_levels`) or pre-session (the
 *  template cached below); the wire carries no per-model tier list. */
const EFFORT_LEVELS = [
  { id: "low", label: "low" },
  { id: "medium", label: "medium" },
  { id: "high", label: "high" },
  { id: "xhigh", label: "x-high" },
  { id: "max", label: "max" },
] as const;

/** The pre-session vocabulary from GET /api/config-template, written once at boot.
 *  A chat with no bridge has no session catalog, so this is its only evidence. */
let catalogEfforts: readonly SessionEffortLevel[] = [];
let catalogEffortActive = "";

export function setCatalogEfforts(levels: readonly SessionEffortLevel[], active: string): void {
  catalogEfforts = levels;
  catalogEffortActive = active;
}

/** The catalog's own name, else the house table (so `xhigh` stays "x-high"), else
 *  the id verbatim — hiding a tier the model offers is worse than an unstyled name. */
export function effortLabel(level: SessionEffortLevel): string {
  if (level.name !== undefined && level.name !== "") {
    return level.name;
  }
  return EFFORT_LEVELS.find((l) => l.id === level.id)?.label ?? level.id;
}

/** The default vocabulary, for a control with no catalog behind it yet. */
function fallbackEffortLevels(): SessionEffortLevel[] {
  return EFFORT_LEVELS.map((l) => ({ id: l.id, name: l.label }));
}

/** The current model's OWN default tier, or "" when the catalog does not say. Read
 *  off the catalog, never tabulated here: the value is a property of the model. */
function modelDefaultEffort(session: Session | undefined, models: readonly ModelInfo[]): string {
  return models.find((m) => m.model_id === session?.model)?.default_effort_level ?? "";
}

/** The tiers to render and the tier that is live, for a chat.
 *
 *  Levels: the session's catalog, else the pre-session template's, else the
 *  canonical five. Live tier, highest first: the chat's own choice, the level the
 *  session reports running at, the level last picked anywhere (`seed`), then the
 *  model's default. `effortPillLabel` and `BridgeCoordinator.effortFor` resolve the
 *  same order, so diverging here makes the pill lie about the session. The choice
 *  and the seed are reconciled against `levels`; the model default already is. */
export function effortVocabulary(
  session: Session | undefined,
  models: readonly ModelInfo[],
  seed: string,
): { levels: readonly SessionEffortLevel[]; active: string } {
  const fromSession = session?.effort_levels ?? [];
  const levels =
    fromSession.length > 0
      ? fromSession
      : catalogEfforts.length > 0
        ? catalogEfforts
        : fallbackEffortLevels();
  const candidates: readonly string[] = [
    ifOffered(session?.effort ?? "", levels),
    session?.effort_active ?? "",
    ifOffered(seed, levels),
    modelDefaultEffort(session, models),
    // The pre-session template's own active level is the service's answer too.
    catalogEffortActive,
  ];
  const active = candidates.find((level) => level !== "") ?? "";
  return { levels, active };
}

/** `level` when the current model offers it, else "" — the reconciliation both a
 *  chosen and a remembered level go through. */
function ifOffered(level: string, levels: readonly SessionEffortLevel[]): string {
  return level !== "" && levels.some((l) => l.id === level) ? level : "";
}

/** Whether two tier lists are the same sequence — the rebuild test. */
export function sameLevels(
  a: readonly SessionEffortLevel[],
  b: readonly SessionEffortLevel[],
): boolean {
  return a.length === b.length && a.every((l, i) => l.id === b[i]?.id && l.name === b[i].name);
}

/** Whether the CURRENT model advertises reasoning effort (`_meta.kiro.hasEffort`,
 *  plumbed onto the catalog entries). When no entry carries it the server has not
 *  plumbed it at all, and the answer is true: hiding a working control on a missing
 *  field is worse than showing one that does nothing. */
export function modelHasEffort(models: readonly ModelInfo[], modelID: string): boolean {
  let plumbed = false;
  let current = false;
  for (const m of models) {
    if (m.has_effort !== undefined) {
      plumbed = true;
      if (m.model_id === modelID) {
        current = m.has_effort;
      }
    }
  }
  return plumbed ? current : true;
}

/** The tier to name on the model pill, a READOUT of the level in force rather than
 *  a marker for an exception. Empty in two cases: the model advertises no effort, so
 *  there is no tier, or nothing resolved at all, so naming one would invent it.
 *
 *  Resolves through `effortVocabulary`, so the pill and the card's mark cannot
 *  disagree about what the session runs at. */
export function effortPillLabel(
  session: Session | undefined,
  models: readonly ModelInfo[],
  seed: string,
): string {
  if (!modelHasEffort(models, session?.model ?? "")) {
    return "";
  }
  const { levels, active } = effortVocabulary(session, models, seed);
  if (active === "") {
    return "";
  }
  return effortLabel(levels.find((l) => l.id === active) ?? { id: active });
}
