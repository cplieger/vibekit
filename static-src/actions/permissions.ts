// Actions for the native (Cedar) permission policy — the sole tool-call
// authorization surface on v3.
// ---------------------------------------------------------------------------

import { apiAction, retryNetwork, RETRY_STANDARD } from "./index.js";
import type { PolicyExplainResult } from "../types.js";

// --- Native Cedar policy rules (v3 / KAS) -----------------------------------
// These write the user/workspace permissions.yaml, which KAS hot-reloads. No
// optimistic update: the native view is a server projection, refetched after
// a successful edit (and on the permissions_changed SSE), so it can never
// drift from what KAS actually enforces.

/** Add, remove, or update a native policy rule. op="add" defaults an empty
 *  effect to "ask" server-side (conservative); op="remove" and op="update"
 *  need the exact existing rule. Removing a deny — like any update that
 *  widens access (deny→ask, deny→allow, ask→allow) — needs confirm=true.
 *  op="update" changes the rule's effect to new_effect in one atomic file
 *  write. guard_resource (add+allow only) pre-flights the write against
 *  the live policy: when an explicit ask rule already covers that resource
 *  the allow would be silently shadowed (ask > allow), so the server
 *  refuses with 409 instead of persisting a rule that changes nothing. */
export interface NativeRuleArgs {
  op: "add" | "remove" | "update";
  scope: "user" | "workspace";
  capability: string;
  /** allow | deny | ask. Empty on add → server defaults to ask. */
  effect: string;
  /** Target effect for op="update". */
  new_effect?: string;
  match?: string[];
  exclude?: string[];
  confirm?: boolean;
  guard_resource?: string;
}

export const editNativeRule = apiAction<NativeRuleArgs, { ok?: boolean; error?: string }>({
  name: "permissions.edit_native_rule",
  // Both ops are idempotent server-side (add no-ops if identical rule exists;
  // remove no-ops if absent), so a retried timeout is safe.
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  idempotencyKey: true,
  scope: "permissions",
  request: (a) => ({ method: "POST", path: "/api/permissions/rules", body: a }),
  error: "Couldn't update policy rule",
});

/** Simulate the policy decision for a capability/resource. Pure — KAS
 *  evaluateSingleResource raises no consent prompt (verified live), so this
 *  is safe to call as a UI pre-flight. Errors are shown inline, not toasted. */
export const explainPolicy = apiAction<
  { capability?: string; tool_id?: string; resource?: string },
  PolicyExplainResult
>({
  name: "permissions.explain",
  scope: "permissions",
  request: (a) => ({ method: "POST", path: "/api/permissions/explain", body: a }),
  error: false,
});
