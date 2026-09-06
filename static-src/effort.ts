// ---------------------------------------------------------------------------
// Reasoning-effort vocabulary: which tiers exist, which one is live, and whether
// the live one is a departure from what the current model would have done.
//
// A leaf module because TWO surfaces read this and they have to agree. The model
// card's tier row (model-switcher.ts) marks the live tier; the model pill
// (context-ui.ts -> status.ts) names it when the user departed from the model's
// default. A second copy of the resolution order is a second thing that can be
// wrong, and the failure mode is a pill claiming a level the session does not run
// at.
//
// THE DEFAULT IS NEVER A LOCAL TABLE. It arrives per model on the catalog
// (`ModelInfo.default_effort_level`, from KAS `_meta.kiro.defaultEffortLevel`),
// which is the same field kiro-cli's own TUI resolves to label a tier
// `[default]`. So a model whose default moves upstream, or a model that ships
// after this build, needs no client release. The table below is display labels
// and a fallback vocabulary only; it says nothing about which tier is default.
//
// A catalog with no default for the current model is the ORDINARY state of a chat
// with no bridge, not an edge case: the pre-session feed runs on a lazily-spawned
// utility bridge, degrades to empty lists on any failure, may answer with no models
// at all because KAS resolves its model list asynchronously, and nothing retries.
// That is why the pill's departure test asks WHO decided the level rather than only
// comparing it to a default — see EffortSource.
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

/** WHO decided the live tier.
 *
 *  It exists because the pill's departure test cannot be answered from the level
 *  alone: a tier the USER decided (this chat's own pick, or the remembered pick
 *  for this model) is worth naming even with no default to compare against, while
 *  one the SERVICE resolved is what everyone gets anyway and naming it would turn
 *  the pill into a permanent readout. */
export type EffortSource = "chosen" | "reported" | "seeded" | "default" | "none";

/** The tiers to render, the tier that is live, and who decided it, for a chat.
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
 *  A candidate TABLE rather than a chain of ternaries, because the order is the
 *  contract: two other readers resolve the same seed under the same per-model gate
 *  (`nonDefaultEffortLabel` here, `BridgeCoordinator.effortFor` server-side), and a
 *  pill that resolves differently from the session it describes is the failure
 *  this module exists to prevent.
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
): { levels: readonly SessionEffortLevel[]; active: string; source: EffortSource } {
  const fromSession = session?.effort_levels ?? [];
  const levels =
    fromSession.length > 0
      ? fromSession
      : catalogEfforts.length > 0
        ? catalogEfforts
        : fallbackEffortLevels();
  const candidates: readonly (readonly [string, EffortSource])[] = [
    [ifOffered(session?.effort ?? "", levels), "chosen"],
    [session?.effort_active ?? "", "reported"],
    [ifOffered(seed, levels), "seeded"],
    [modelDefaultEffort(session, models), "default"],
    // The pre-session template's own active level is the service's answer too,
    // so it shares the `default` source.
    [catalogEffortActive, "default"],
  ];
  const unresolved: readonly [string, EffortSource] = ["", "none"];
  const [active, source] = candidates.find(([level]) => level !== "") ?? unresolved;
  return { levels, active, source };
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
 *  It names the tier when the level was DECIDED — this chat's own pick, or the
 *  remembered pick for this model — and otherwise only when a known default proves
 *  the level is a departure. Empty in four cases, each for its own reason:
 *
 *  - The model advertises no effort, so there is no tier.
 *  - No level resolved at all, so naming one would invent it.
 *  - The live level IS the model's own default, which is the point of the control:
 *    the pill stays quiet until the user departs from what the model would have
 *    done anyway.
 *  - Or nothing knows the default AND the level is one the SERVICE resolved. Those
 *    are what everyone gets anyway, so naming one with nothing to compare against
 *    would put a permanent readout on every model rather than the exception this
 *    pill exists to mark.
 *
 *  A DECIDED level is named in that last case rather than withheld, and it is not
 *  a guess: `ifOffered` has already reconciled it against the current model's own
 *  tier list, and the server resolves the same seed into `StartOpts.Effort` under
 *  the same per-model gate, so the pill states what the session will run at. The
 *  withholding used to cover both, which is why a click on a tier showed nothing
 *  until a bridge existed: no bridge means no per-model catalog, so no default is
 *  known, and that is the ordinary state of a new chat.
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
  const { levels, active, source } = effortVocabulary(session, models, seed);
  const dflt = modelDefaultEffort(session, models);
  const decided = source === "chosen" || source === "seeded";
  // `active === dflt` needs no non-empty guard on dflt: active is already
  // non-empty here, so the two can only match on a real default.
  if (active === "" || active === dflt || (dflt === "" && !decided)) {
    return "";
  }
  return effortLabel(levels.find((l) => l.id === active) ?? { id: active });
}
