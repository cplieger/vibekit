// ---------------------------------------------------------------------------
// Session context: the ambient model the user is currently composing with.
// Separated from app.ts so multiple modules can read/write without going
// through the orchestrator.
//
// last_model is also synced to server settings so it follows the user across
// devices. There is no ambient agent — v3 roles are modes, set via the mode
// picker, not an ambient agent.
// ---------------------------------------------------------------------------

import { patchSettings } from "./persist.js";

class SessionContextController {
  private currentModel = "auto";
  private lastModelCache = "auto";

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
