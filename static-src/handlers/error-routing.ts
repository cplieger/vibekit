// ---------------------------------------------------------------------------
// Error classification table: maps server ErrorCode to UI surface.
// ---------------------------------------------------------------------------

import type { BannerLevel } from "../types.js";
import type { SettingsTab } from "../router.js";
import type { ErrorCode } from "../wire/types.gen.js";

export interface ErrorRoute {
  surface: "banner" | "send-error";
  level: BannerLevel;
  dismissible: boolean;
  /** Optional in-app jump for a banner: the Settings tab plus the id of the
   *  control the message is really about. Data rather than a callback so the
   *  routing table stays a table — the handler turns it into the banner's
   *  action. Only meaningful on `surface: "banner"`. */
  setting?: { tab: SettingsTab; control: string; label: string };
}

export const ERROR_ROUTES: Readonly<Partial<Record<ErrorCode, ErrorRoute>>> = {
  agent_not_found: { surface: "banner", level: "error", dismissible: true },
  // The payload names a `.kiro/agents` path, so the message is about authored
  // configuration; Custom instructions is the panel that owns it, and the global
  // instructions box is the control a reader lands on to check their setup.
  agent_config_error: {
    surface: "banner",
    level: "error",
    dismissible: false,
    setting: { tab: "instructions", control: "steering-input", label: "Open custom instructions" },
  },
  rate_limit: { surface: "banner", level: "warning", dismissible: true },
  compaction_failed: { surface: "banner", level: "error", dismissible: true },
  // The chat is running, just not in the mode that was asked for, and the fix is
  // one click on the mode pill. A banner says so without blocking the composer;
  // dismissible because the session is usable as it stands.
  mode_not_applied: { surface: "banner", level: "warning", dismissible: true },
  switch_failed: { surface: "send-error", level: "error", dismissible: false },
  // The pick was refused before it reached the wire, so the send that carried it
  // is what failed. Same surface as switch_failed, which is the other half of
  // choosing a model.
  model_not_served: { surface: "send-error", level: "error", dismissible: false },
  bridge_start_failed: { surface: "send-error", level: "error", dismissible: false },
  prompt_failed: { surface: "send-error", level: "error", dismissible: false },
  // Empty-turn recovery failed to respawn the session or to resend the prompt.
  // Routed explicitly rather than left to the unknown-code fallthrough, which
  // reaches setLastError with no level and no banner: this is the one error whose
  // meaning is "the automatic repair did not work", so the user has to see it.
  recovery_failed: { surface: "send-error", level: "error", dismissible: false },
};
