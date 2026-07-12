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
  switch_failed: { surface: "send-error", level: "error", dismissible: false },
  bridge_start_failed: { surface: "send-error", level: "error", dismissible: false },
  prompt_failed: { surface: "send-error", level: "error", dismissible: false },
};
