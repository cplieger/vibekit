// Hook-management actions: enable/disable and run a workspace hook.
//
// The hook list is server-canonical (it lives in the .kiro/hooks/*.json files,
// read via KAS on the utility bridge — see internal/hub/hooks.go) and is
// refetched after every mutation (the server also broadcasts hooks_changed),
// so these actions carry no optimistic state. See hooks.ts.
// ---------------------------------------------------------------------------

import { apiAction } from "./index.js";

/** Base path for the hooks API — single source of truth. */
const HOOKS_API = "/api/hooks";

// --- hooks.set_enabled ---

interface SetEnabledArgs {
  id: string;
  enabled: boolean;
}

/** POST /api/hooks/{id}/enabled {enabled} — flip a hook's enabled flag
 *  (persisted to its .kiro/hooks/*.json file). Deduped per hook id so a rapid
 *  double-toggle collapses. */
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for an action with no result
export const setHookEnabled = apiAction<SetEnabledArgs, void>({
  name: "hooks.set_enabled",
  dedupe: (args) => `hooks.set_enabled:${args.id}`,
  request: ({ id, enabled }) => ({
    method: "POST",
    path: `${HOOKS_API}/${encodeURIComponent(id)}/enabled`,
    body: { enabled },
  }),
  error: "Couldn't update hook",
});

// --- hooks.run ---

interface RunArgs {
  id: string;
}

/** hookRunResult mirrors internal/hub/hooks.go hookTriggerResponse. */
export interface HookRunResult {
  output: string;
  exit_code: number;
  ran: boolean;
}

/** POST /api/hooks/{id}/trigger — run a runCommand hook now and return its
 *  captured output. No auto-retry: re-running a command that may already have
 *  executed is unsafe. `error: false` — the row surfaces the failure inline. */
export const runHook = apiAction<RunArgs, HookRunResult>({
  name: "hooks.run",
  dedupe: (args) => `hooks.run:${args.id}`,
  request: ({ id }) => ({
    method: "POST",
    path: `${HOOKS_API}/${encodeURIComponent(id)}/trigger`,
  }),
  error: false,
});
