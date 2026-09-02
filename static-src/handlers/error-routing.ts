// ---------------------------------------------------------------------------
// Error classification table: maps server ErrorCode to UI surface.
// ---------------------------------------------------------------------------

import type { BannerLevel } from "../types.js";
import type { SettingsTab } from "../router.js";
import type { ErrorCode } from "../wire/types.gen.js";

/** The in-app jump a routed error offers, as DATA rather than a callback so the
 *  routing table stays a table — the handler maps it onto the toast's one action
 *  slot. Discriminated because the two kinds go to different places: `setting`
 *  names a Settings control the message is really about, `sign-in` opens the login
 *  modal, which is not in Settings at all. */
export type ErrorAction =
  | { kind: "setting"; tab: SettingsTab; control: string; label: string }
  | { kind: "sign-in"; label: string };

export interface ErrorRoute {
  /** Where this error is reported.
   *
   *  - `toast`: bottom-right, paired with the turn's own transcript divider (the
   *    server writes the same reason there). Raised for EVERY chat, and named with
   *    that chat when its tab is not the one on screen.
   *  - `agent-down`: the send button's alert face. Reserved for "there is no
   *    agent to send to", never for a failed attempt — see send-state.ts.
   */
  surface: "toast" | "agent-down";
  /** Declared for the wire code and read by neither surface: a toast has three
   *  levels and no dismiss contract. */
  level: BannerLevel;
  dismissible: boolean;
  action?: ErrorAction;
}

export const ERROR_ROUTES: Readonly<Partial<Record<ErrorCode, ErrorRoute>>> = {
  agent_not_found: { surface: "toast", level: "error", dismissible: true },
  // The payload names a `.kiro/agents` path, so the message is about authored
  // configuration; Custom instructions is the panel that owns it, and the global
  // instructions box is the control a reader lands on to check their setup.
  //
  // Sticky rather than the 12s an action reachable elsewhere gets (toast.ts): a
  // typo in a `.kiro/agents` file blocks the chat and nothing else on screen says
  // so, so this notice must not expire unread.
  agent_config_error: {
    surface: "toast",
    level: "error",
    dismissible: false,
    action: {
      kind: "setting",
      tab: "instructions",
      control: "steering-input",
      label: "Open custom instructions",
    },
  },
  rate_limit: { surface: "toast", level: "warning", dismissible: true },
  compaction_failed: { surface: "toast", level: "error", dismissible: true },
  // The chat is running, just not in the mode that was asked for, and the fix is
  // one click on the mode pill, so this reports without blocking the composer.
  mode_not_applied: { surface: "toast", level: "warning", dismissible: true },
  // kiro-cli could not vend a KAS access token, so the agent runtime is running
  // unauthenticated: the session opened and every service-backed surface behind
  // it will fail. Sticky, because nothing else on screen says the runtime is
  // signed out. The CTA is the login modal: the only action that fixes it is
  // signing in, and there is no Settings control for that.
  auth_token_unavailable: {
    surface: "toast",
    level: "error",
    dismissible: false,
    action: { kind: "sign-in", label: "Sign in" },
  },
  // The four failed-attempt codes. Each one ends the turn and each one leaves a
  // promptable chat behind, which is precisely why none of them reaches the send
  // button: the composer's next Send is the retry.
  //
  // `prompt_failed` is the throttle / 5xx / capacity family and the reason this
  // whole routing changed. `recovery_failed` is empty-turn recovery giving up,
  // routed explicitly rather than left to the unknown-code fallthrough because it
  // is the one error whose meaning is "the automatic repair did not work".
  // `switch_failed` and `model_not_served` are the two halves of choosing a model,
  // refused before the wire and on it.
  prompt_failed: { surface: "toast", level: "error", dismissible: false },
  recovery_failed: { surface: "toast", level: "error", dismissible: false },
  switch_failed: { surface: "toast", level: "error", dismissible: false },
  model_not_served: { surface: "toast", level: "error", dismissible: false },
  // The ONE code that earns the send button's alert face: kiro-cli could not be
  // spawned, so this chat has no ACP connection behind it. Every other failure
  // here happened to a live agent.
  bridge_start_failed: { surface: "agent-down", level: "error", dismissible: false },
};
