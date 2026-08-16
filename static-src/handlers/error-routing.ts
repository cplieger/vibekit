// ---------------------------------------------------------------------------
// Error classification table: maps server ErrorCode to UI surface.
// ---------------------------------------------------------------------------

import type { BannerLevel } from "../types.js";
import type { SettingsTab } from "../router.js";
import type { ErrorCode } from "../wire/types.gen.js";

/** The in-app jump a banner offers, as DATA rather than a callback so the
 *  routing table stays a table — the handler turns it into the banner's action.
 *  Discriminated because the two kinds go to different places: `setting` names a
 *  Settings control the message is really about, `sign-in` opens the login modal,
 *  which is not in Settings at all. Only meaningful on `surface: "banner"`. */
export type ErrorAction =
  | { kind: "setting"; tab: SettingsTab; control: string; label: string }
  | { kind: "sign-in"; label: string };

export interface ErrorRoute {
  surface: "banner" | "send-error";
  level: BannerLevel;
  dismissible: boolean;
  action?: ErrorAction;
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
    action: {
      kind: "setting",
      tab: "instructions",
      control: "steering-input",
      label: "Open custom instructions",
    },
  },
  rate_limit: { surface: "banner", level: "warning", dismissible: true },
  compaction_failed: { surface: "banner", level: "error", dismissible: true },
  // The chat is running, just not in the mode that was asked for, and the fix is
  // one click on the mode pill. A banner says so without blocking the composer;
  // dismissible because the session is usable as it stands.
  mode_not_applied: { surface: "banner", level: "warning", dismissible: true },
  // kiro-cli could not vend a KAS access token, so the agent runtime is running
  // unauthenticated: the session opened and every service-backed surface behind
  // it will fail. A banner rather than a send-error because it is not this send
  // that is broken, and not dismissible because nothing else on screen says the
  // runtime is signed out. The CTA is the login modal: the only action that fixes
  // it is signing in, and there is no Settings control for that.
  auth_token_unavailable: {
    surface: "banner",
    level: "error",
    dismissible: false,
    action: { kind: "sign-in", label: "Sign in" },
  },
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
