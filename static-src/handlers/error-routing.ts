// ---------------------------------------------------------------------------
// Error classification table: maps server ErrorCode to UI surface.
// ---------------------------------------------------------------------------

import type { BannerLevel } from "../types.js";
import type { ErrorCode } from "../wire/types.gen.js";

export interface ErrorRoute {
  surface: "banner" | "send-error";
  level: BannerLevel;
  dismissible: boolean;
}

export const ERROR_ROUTES: Readonly<Partial<Record<ErrorCode, ErrorRoute>>> = {
  agent_not_found: { surface: "banner", level: "error", dismissible: true },
  agent_config_error: { surface: "banner", level: "error", dismissible: false },
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
