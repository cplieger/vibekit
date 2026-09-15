// ---------------------------------------------------------------------------
// Session context: the ambient model the user is currently composing with, and
// the reasoning-effort level they last picked. Separated from app.ts so multiple
// modules can read/write without going through the orchestrator.
//
// Both live in server settings so they follow the user across devices, but only
// the model is WRITTEN here: the effort seed is written by the server, inside the
// set_effort command that justifies it, and this side only adopts it. There is no
// ambient agent — v3 roles are modes, set via the mode picker.
//
// The two are ambient MEMORY, not state: the model a chat runs on and the tier it
// runs at both live on the chat record. These answer the different question a NEW
// chat asks, which is what to open with. Effort had no answer to it at all until
// 2026-08, so every new chat silently reopened at the current model's default
// tier however many times the user had chosen otherwise.
// ---------------------------------------------------------------------------

import { patchSettings } from "./persist.js";

class SessionContextController {
  private currentModel = "auto";
  private lastModelCache = "auto";
  /** The level last picked under each model. A model with no entry has nothing
   *  for a new chat to open with and falls through to that model's own default
   *  tier. PER MODEL because a tier is a judgement about one model's
   *  speed/quality trade, so one level for the whole app retracted every other
   *  model's remembered pick the moment a tier was chosen anywhere (user report,
   *  2026-08-31). */
  private lastEffortByModel: Record<string, string> = {};

  getCurrentModel(): string {
    return this.currentModel;
  }
  setCurrentModel(id: string): void {
    this.currentModel = id;
  }

  getLastModel(): string {
    return this.lastModelCache;
  }
  setLastModel(id: string): void {
    // Guard against redundant writes: an earlier bug had the SSE
    // settings_updated handler calling setLastModel with the
    // server-confirmed value, which patched it straight back, which
    // triggered another settings_updated, which… looped at debounce
    // speed forever. The handler now uses restoreLastModel for that
    // path, but a no-op guard here prevents any other caller from
    // re-introducing the loop. It also avoids waking the save
    // indicator for writes that don't change anything.
    if (this.lastModelCache === id) {
      return;
    }
    this.lastModelCache = id;
    void patchSettings({ last_model: id });
  }

  restoreLastModel(id: string | undefined): void {
    if (id !== undefined) {
      this.lastModelCache = id;
    }
  }

  getLastEffortFor(model: string): string {
    // A model id is arbitrary text, so an inherited member (`constructor`) must
    // answer like an absent one rather than like an entry.
    if (model === "" || !Object.hasOwn(this.lastEffortByModel, model)) {
      return "";
    }
    return this.lastEffortByModel[model] ?? "";
  }
  restoreLastEffort(byModel: Record<string, string> | undefined): void {
    if (byModel !== undefined) {
      this.lastEffortByModel = { ...byModel };
    }
  }
}

const instance = new SessionContextController();

export function getCurrentModel(): string {
  return instance.getCurrentModel();
}
export function setCurrentModel(id: string): void {
  instance.setCurrentModel(id);
}

export function getLastModel(): string {
  return instance.getLastModel();
}
export function setLastModel(id: string): void {
  instance.setLastModel(id);
}

/** Restore last_model from settings on startup. */
export function restoreLastModel(id: string | undefined): void {
  instance.restoreLastModel(id);
}

/** The level remembered for `model`; "" when that model has none. The per-model
 *  lookup is what stops a tier chosen on one model overriding another model's
 *  default — the seed's two readers (this and the server's effortFor) apply the
 *  same scope or the pill lies about what the session runs. */
export function getLastEffortFor(model: string): string {
  return instance.getLastEffortFor(model);
}

/** Adopt last_effort_by_model from settings on startup and from the
 *  settings_updated SSE. Cache-only: the SEED is written by the server, inside
 *  the set_effort command that justifies it, so this side never patches. */
export function restoreLastEffort(byModel: Record<string, string> | undefined): void {
  instance.restoreLastEffort(byModel);
}
