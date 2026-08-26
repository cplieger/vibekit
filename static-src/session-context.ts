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

  getLastEffort(): string {
    return this.lastEffortCache;
  }
  setLastEffort(level: string): void {
    // Same redundant-write guard as setLastModel, for the same reason: the
    // settings_updated handler must not be able to patch a confirmed value back
    // and loop, and a repeat pick of the level already in force must not wake the
    // save indicator.
    if (this.lastEffortCache === level) {
      return;
    }
    this.lastEffortCache = level;
    void patchSettings({ last_effort: level });
  }

  restoreLastEffort(level: string | undefined): void {
    if (level !== undefined) {
      this.lastEffortCache = level;
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

export function getLastEffort(): string {
  return instance.getLastEffort();
}
export function setLastEffort(level: string): void {
  instance.setLastEffort(level);
}

/** Restore last_effort from settings on startup, and from the settings_updated
 *  SSE. Cache-only, like restoreLastModel: setLastEffort would patch the
 *  server-confirmed value straight back and loop at debounce speed. */
export function restoreLastEffort(level: string | undefined): void {
  instance.restoreLastEffort(level);
}
