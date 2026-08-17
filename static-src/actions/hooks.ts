// Hook actions: enable/disable a workspace hook. ONE action, and there used to
// be two.
//
// `hooks.run` is DELETED with Run-now. It posted to a route that made the server
// run `sh -c` on a command a hook file specifies, and the whole path (route,
// handler, the `_kiro/hooks/executeHook` responder) went with it. Hooks get what
// every other `.kiro` document gets — open, delete — plus this toggle.
//
// The hook list is server-canonical (it lives in the .kiro/hooks/*.json files,
// read via KAS on the utility bridge — see internal/hub/hooks.go) and is
// refetched after every mutation (the server also broadcasts hooks_changed),
// so this action carries no optimistic state. Its reader is the configuration
// browser's Hooks tab (docs.ts).
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
