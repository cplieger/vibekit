// ---------------------------------------------------------------------------
// Session context: the ambient model the user is currently composing with, and
// the reasoning-effort level they last picked. Separated from app.ts so multiple
// modules can read/write without going through the orchestrator.
//
// Both are also synced to server settings so they follow the user across
// devices. There is no ambient agent — v3 roles are modes, set via the mode
// picker, not an ambient agent.
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
  /** Empty means the user has never picked a level, so a new chat has nothing to
   *  open with and falls through to the model's own default tier. */
  private lastEffortCache = "";
  /** The model lastEffort was picked under. The seed applies only to a chat
   *  running THAT model: a tier is a judgement about one model, so carrying it
   *  onto another overrode that model's own default (user report, 2026-08-31).
   *  Empty (a pre-pair install) means the seed never applies, which self-heals
   *  on the next pick. */
  private lastEffortModelCache = "";

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
    if (model === "" || this.lastEffortModelCache !== model) {
      return "";
    }
    return this.lastEffortCache;
  }
  setLastEffort(level: string, model: string): void {
    // Same redundant-write guard as setLastModel, for the same reason: the
    // settings_updated handler must not be able to patch a confirmed value back
    // and loop, and a repeat pick of the level already in force must not wake the
    // save indicator.
    if (this.lastEffortCache === level && this.lastEffortModelCache === model) {
      return;
    }
    this.lastEffortCache = level;
    this.lastEffortModelCache = model;
    void patchSettings({ last_effort: level, last_effort_model: model });
  }

  restoreLastEffort(level: string | undefined, model: string | undefined): void {
    if (level !== undefined) {
      this.lastEffortCache = level;
    }
    if (model !== undefined) {
      this.lastEffortModelCache = model;
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

/** The remembered effort level, when it was picked under `model`; "" otherwise.
 *  The model gate is what stops a tier chosen on one model overriding another
 *  model's default — the seed's two readers (this and the server's effortFor)
 *  apply the same scope or the pill lies about what the session runs. */
export function getLastEffortFor(model: string): string {
  return instance.getLastEffortFor(model);
}
export function setLastEffort(level: string, model: string): void {
  instance.setLastEffort(level, model);
}

/** Restore last_effort (+ the model it was picked under) from settings on
 *  startup, and from the settings_updated SSE. Cache-only, like
 *  restoreLastModel: setLastEffort would patch the server-confirmed value
 *  straight back and loop at debounce speed. */
export function restoreLastEffort(level: string | undefined, model: string | undefined): void {
  instance.restoreLastEffort(level, model);
}
