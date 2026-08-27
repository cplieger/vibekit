// ---------------------------------------------------------------------------
// Reasoning-effort vocabulary: which tiers exist, which one is live, and whether
// the live one is a departure from what the current model would have done.
//
// A leaf module because TWO surfaces read this and they have to agree. The model
// card's tier row (model-switcher.ts) marks the live tier; the model pill
// (context-ui.ts -> status.ts) names it when it is not the model's default. A
// second copy of the resolution order is a second thing that can be wrong, and
// the failure mode is a pill claiming a level the session does not run at.
//
// THE DEFAULT IS NEVER A LOCAL TABLE. It arrives per model on the catalog
// (`ModelInfo.default_effort_level`, from KAS `_meta.kiro.defaultEffortLevel`),
// which is the same field kiro-cli's own TUI resolves to label a tier
// `[default]`. So a model whose default moves upstream, or a model that ships
// after this build, needs no client release. The table below is display labels
// and a fallback vocabulary only; it says nothing about which tier is default.
// ---------------------------------------------------------------------------

import type { ModelInfo, Session, SessionEffortLevel } from "./types.js";

/** Canonical effort levels with display labels. The FALLBACK vocabulary and the
 *  label table, not the authority.
 *
 *  The authority is the `effortLevel` config option's own choices, which arrive
 *  per session (`Session.effort_levels`) and pre-session (the config template,
 *  cached below). Read off kiro-cli 2.18.0: its TUI builds the same picker from
 *  that option and refuses the command when the list is empty, and there is NO
 *  per-model tier list on the wire — the model choice carries only
 *  `defaultEffortLevel`. This list therefore renders when no catalog has landed
 *  yet, and the id→label map names an id whose catalog entry has no name. */
const EFFORT_LEVELS = [
  { id: "low", label: "low" },
  { id: "medium", label: "medium" },
  { id: "high", label: "high" },
  { id: "xhigh", label: "x-high" },
  { id: "max", label: "max" },
] as const;

/** The pre-session effort vocabulary from GET /api/config-template: the tiers a
 *  fresh session would offer and the level it would run at. A chat with no bridge
 *  has no session catalog, and this is the only evidence available then. Written
 *  once at boot by app.ts. */
let catalogEfforts: readonly SessionEffortLevel[] = [];
let catalogEffortActive = "";

export function setCatalogEfforts(levels: readonly SessionEffortLevel[], active: string): void {
  catalogEfforts = levels;
  catalogEffortActive = active;
}

/** The label for an effort tier: the catalog's own name when it has one, else the
 *  house table (so `xhigh` stays "x-high"), else the id verbatim — hiding a tier
 *  the model offers is worse than an unstyled name. */
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

/** The current model's OWN default tier, or "" when the catalog does not say.
 *  The one definition of "default" in the client; both the live-tier fallback
 *  chain and the pill's departure test read it here. */
function modelDefaultEffort(session: Session | undefined, models: readonly ModelInfo[]): string {
  return models.find((m) => m.model_id === session?.model)?.default_effort_level ?? "";
}

/** The tiers to render and the tier that is live, for a chat.
 *
 *  Levels: the session's own catalog, else the pre-session template's, else the
 *  canonical five (nothing has landed yet — a control with no tiers would be a
 *  worse answer than a stale one).
 *
 *  Live tier, highest first: the chat's own choice, so a click marks instantly
 *  through the optimistic store write; then the level the session reports
 *  running at; then the level the user last picked anywhere (`last_effort`),
 *  which is what a NEW chat opens on and what the server independently resolves
 *  into StartOpts.Effort, so the two agree about a session before it exists; then
 *  the current model's own default, which is all that is left when nobody has
 *  ever picked.
 *
 *  The chat's choice and the seed are both reconciled against `levels`; the model
 *  default is not, being in its own list by construction. A tier list is per
 *  model, so a chosen or remembered `max` on a model that stops at `high` is a
 *  level the service rejects, and marking it would claim the session runs at a
 *  tier it cannot reach. Falling through to what the session REPORTS is the honest
 *  answer, and the choice stays on the record for a model that offers it again.
 *
 *  `seed` is passed in rather than read here so this module stays a pure leaf:
 *  its two callers already hold the remembered pick. */
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
  const modelDefault = modelDefaultEffort(session, models);
  const chosen = ifOffered(session?.effort ?? "", levels);
  const seeded = ifOffered(seed, levels);
  const active =
    chosen !== ""
      ? chosen
      : (session?.effort_active ?? "") !== ""
        ? (session?.effort_active ?? "")
        : seeded !== ""
          ? seeded
          : modelDefault !== ""
            ? modelDefault
            : catalogEffortActive;
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

/** Model-aware effort gating. KAS advertises a per-model effort capability in
 *  the config catalog (config_option_update `_meta.kiro.hasEffort`). When the
 *  server plumbs it onto the catalog entries, gate on the CURRENT model's
 *  capability; when it isn't plumbed at all (no entry carries it), fall back to
 *  true so a working control is never silently hidden. */
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

/** The tier to name on the model pill, or "" when the pill has nothing to say.
 *
 *  Empty in four cases, each for its own reason. The model advertises no effort,
 *  so there is no tier. No level resolved at all, so naming one would invent it.
 *  The live level IS the model's own default, which is the point of the control:
 *  the pill stays quiet until the user departs from what the model would have
 *  done anyway. Or the catalog carries no default for this model, in which case a
 *  departure is not knowable — and a suffix on every model would be a permanent
 *  readout rather than the exception it exists to mark.
 *
 *  It reads the live tier through the same resolution order the card's mark uses,
 *  so the two surfaces can never disagree about what the session runs at. */
export function nonDefaultEffortLabel(
  session: Session | undefined,
  models: readonly ModelInfo[],
  seed: string,
): string {
  if (!modelHasEffort(models, session?.model ?? "")) {
    return "";
  }
  const { levels, active } = effortVocabulary(session, models, seed);
  const dflt = modelDefaultEffort(session, models);
  if (active === "" || dflt === "" || active === dflt) {
    return "";
  }
  return effortLabel(levels.find((l) => l.id === active) ?? { id: active });
}
