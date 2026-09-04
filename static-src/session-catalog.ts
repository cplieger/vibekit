// ---------------------------------------------------------------------------
// The workspace's mode, model and effort catalog: one reader, one feed.
//
// `GET /api/config-template` is that feed, and it is the ONLY one. The catalog
// used to travel per chat as `ChatHeader.available_modes` and
// `available_models` — 59 modes copied onto each of 29 chats, measured at
// 1,236,118 B of a 1.25 MiB `/api/chats` response (93.1%), fetched twice per
// boot — and a per-session effect re-populated the picker from whichever chat
// was active. Both are gone: the catalog is a workspace fact, so it is fetched
// once and held once (`roles.ts` for the modes, `effort.ts` for the tiers,
// `picker.ts` for the models).
//
// Its own module rather than three functions in the composition root. `app.ts`
// is wiring, and a catalog fetcher that maps a wire shape, seeds a context-size
// table and repaints two controls is a job with its own subject.
// ---------------------------------------------------------------------------

import type { ModelInfo, SessionEffortLevel, SessionMode, SessionModel } from "./types.js";
import { apiGet } from "./api-client.js";
import { setCatalogModes } from "./roles.js";
import { setCatalogEfforts } from "./effort.js";
import { refreshPickerIfVisible, setPickerModels } from "./picker.js";
import { refreshContextUI } from "./context-ui.js";
import { MODEL_CONTEXT_SIZES, contextSizeFor, getActive, parseContextSize } from "./store.js";

/** One catalog entry, mapped from the wire `SessionModel` to the picker's
 *  `ModelInfo`.
 *
 *  Fields are spread conditionally rather than assigned undefined — the client
 *  compiles under exactOptionalPropertyTypes. It stays a named function rather
 *  than an inline map because a field carried on the wire and dropped here is
 *  invisible until a control silently loses its input: that is how the model's
 *  default effort tier went missing while the server was sending it. */
function toModelInfo(m: SessionModel): ModelInfo {
  return {
    model_id: m.id,
    model_name: m.name,
    ...(m.description === undefined || m.description === "" ? {} : { description: m.description }),
    rate_multiplier: m.rate_multiplier ?? 1,
    ...(m.has_effort === undefined ? {} : { has_effort: m.has_effort }),
    ...(m.default_effort_level === undefined || m.default_effort_level === ""
      ? {}
      : { default_effort_level: m.default_effort_level }),
  };
}

/** Fetch the workspace catalog and seed every control that reads it.
 *
 *  `/api/config-template` is kiro-cli's session-less `_kiro/config/template`,
 *  and the server prefers a LIVE session's report over the template when one
 *  exists — so this one feed carries the authoritative catalog whether or not a
 *  bridge has spawned, which is what let the per-session feed go.
 *
 *  Fire-and-forget by convention: every control it seeds renders a usable empty
 *  state, so a failed fetch degrades the picker rather than blocking a boot. */
export async function fetchCatalog(): Promise<void> {
  const d = await apiGet<{
    modes: SessionMode[];
    models: SessionModel[];
    default_model?: string;
    effort_levels?: SessionEffortLevel[];
    effort_active?: string;
  }>("/api/config-template");
  if (d === null) {
    return;
  }
  // Pre-session effort vocabulary: a chat with no bridge has no session catalog,
  // so without this the effort control has neither its tier list nor the level
  // the next session would run at.
  setCatalogEfforts(d.effort_levels ?? [], d.effort_active ?? "");
  if (d.modes.length > 0) {
    setCatalogModes(d.modes);
  }
  const active = getActive();
  if (d.models.length > 0) {
    populatePickerModels(d.models.map(toModelInfo), active?.model ?? "");
  }
  // The context-size seed and the pill's tier name both need the catalog, and
  // this is the only feed that carries it.
  if (active !== undefined) {
    if (active.usage.context_size === 0 && active.model !== "") {
      active.usage.context_size = contextSizeFor(active.model);
    }
    refreshContextUI(active);
  }
}

/** Merge a model list into the picker cache + context-size table.
 *  `activeModel` moves the active highlight; pass "" when no session is active
 *  yet. */
function populatePickerModels(models: ModelInfo[], activeModel: string): void {
  for (const m of models) {
    if (m.description !== undefined && MODEL_CONTEXT_SIZES[m.model_id] === undefined) {
      const size = parseContextSize(m.description);
      if (size !== undefined) {
        MODEL_CONTEXT_SIZES[m.model_id] = size;
      }
    }
  }
  setPickerModels(models);
  refreshPickerIfVisible(activeModel === "" ? undefined : activeModel);
}
