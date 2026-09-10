// The workspace's mode, model and effort catalog: ONE reader of `GET /api/config-template`,
// because the endpoint is a utility-bridge RPC and a second reader seeding the same
// surfaces costs another subprocess round trip per boot and per gap. `model-catalog.ts`
// owns the freshness policy and the single in-flight slot that makes a second caller free.

import type { ModelInfo, SessionModel } from "./types.js";
import { apiGetTyped } from "./api-client.js";
import { decodeConfigTemplateResponse } from "./wire/decoders.gen.js";
import type { ConfigTemplateResponse } from "./wire/types.gen.js";
import { CATALOG_REQUEST_TIMEOUT_MS, refreshCatalog } from "./model-catalog.js";
import { setCatalogModes } from "./roles.js";
import { setCatalogEfforts } from "./effort.js";
import { refreshPickerIfVisible, setCatalogPhase, setPickerModels } from "./picker.js";
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
    ...(m.rate_multiplier === undefined ? {} : { rate_multiplier: m.rate_multiplier }),
    ...(m.has_effort === undefined ? {} : { has_effort: m.has_effort }),
    ...(m.default_effort_level === undefined || m.default_effort_level === ""
      ? {}
      : { default_effort_level: m.default_effort_level }),
  };
}

/** Fetch the workspace catalog and seed every control that reads it.
 *
 *  The server prefers a LIVE session's report over the template, so this one feed is
 *  authoritative whether or not a bridge has spawned. `reset` RESTARTS a retry loop
 *  already running; every other caller declines, so a second call on one gap is free. */
export function fetchCatalog(opts: { readonly reset?: boolean } = {}): Promise<void> {
  return refreshCatalog<ConfigTemplateResponse>(
    {
      // Through the GENERATED decoder: an inline `apiGet<{modes: …}>` is a CLAIM
      // rather than a check, so a server answering `{}` or `modes: null` produced a
      // TypeError inside the boot path.
      read: (signal) =>
        apiGetTyped(
          "/api/config-template",
          decodeConfigTemplateResponse,
          signal,
          CATALOG_REQUEST_TIMEOUT_MS,
        ),
      // Only a USABLE answer reaches here: an `unavailable` template emits an empty
      // effort list by construction, so a login-triggered fetch that degraded used to
      // replace the tiers a successful boot fetch had landed.
      apply: (d) => {
        // ONE rule over all three: an EMPTY list is the absence of a vocabulary rather
        // than a value, so it never replaces one an earlier answer landed. Per list
        // because each arrives empty on its own, a merely COLD cache included.
        if (d.effort_levels.length > 0) {
          // A chat with no bridge has no session catalog, so without this the effort
          // control has neither its tier list nor the level the next session would run at.
          setCatalogEfforts(d.effort_levels, d.effort_active ?? "");
        }
        if (d.modes.length > 0) {
          setCatalogModes(d.modes);
        }
        if (d.models.length > 0) {
          // The active chat's model moves the picker's highlight; "" leaves it where it is.
          populatePickerModels(d.models.map(toModelInfo), getActive()?.model ?? "");
        }
        // Re-read the session: the highlight above may have repainted the picker, and a
        // stale reference is how the pill and the picker desynced before.
        const active = getActive();
        if (active !== undefined) {
          // The context-size table is filled from the model DESCRIPTIONS just landed, so
          // this is the first moment a chat whose window nothing stated can learn it.
          if (active.usage.context_size === 0 && active.model !== "") {
            active.usage.context_size = contextSizeFor(active.model);
          }
          // The model pill names the chat's reasoning tier from the catalog's own
          // capability gate and default rung. Nothing else repaints it on this path.
          refreshContextUI(active);
        }
      },
      setPhase: setCatalogPhase,
    },
    opts,
  );
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
