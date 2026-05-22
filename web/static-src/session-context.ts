// ---------------------------------------------------------------------------
// Session context: the ambient agent + model the user is currently composing
// with. Separated from app.ts so multiple modules can read/write without
// going through the orchestrator.
//
// last_model is also synced to server settings so it follows the user across
// devices; current_agent is ephemeral (plan button sets it for the next
// createSession then reverts).
// ---------------------------------------------------------------------------

import { patchSettings } from "./persist.js";

class SessionContextController {
  private currentAgent = "";
  private currentModel = "auto";
  private lastModelCache = "auto";

  getCurrentAgent(): string { return this.currentAgent; }
  getCurrentModel(): string { return this.currentModel; }
  setCurrentModel(id: string): void { this.currentModel = id; }

  getLastModel(): string { return this.lastModelCache; }
  setLastModel(id: string): void {
    this.lastModelCache = id;
    void patchSettings({ last_model: id });
  }

  restoreLastModel(id: string | undefined): void {
    if (id !== undefined) this.lastModelCache = id;
  }

  withAgent(agent: string, fn: () => void): void {
    const prev = this.currentAgent;
    this.currentAgent = agent;
    try { fn(); }
    finally { this.currentAgent = prev; }
  }
}

const instance = new SessionContextController();

export function getCurrentAgent(): string { return instance.getCurrentAgent(); }
export function getCurrentModel(): string { return instance.getCurrentModel(); }
export function setCurrentModel(id: string): void { instance.setCurrentModel(id); }

export function getLastModel(): string { return instance.getLastModel(); }
export function setLastModel(id: string): void { instance.setLastModel(id); }

/** Restore last_model from settings on startup. */
export function restoreLastModel(id: string | undefined): void { instance.restoreLastModel(id); }

/** Run fn with currentAgent temporarily set; restored when fn returns. */
export function withAgent(agent: string, fn: () => void): void { instance.withAgent(agent, fn); }
