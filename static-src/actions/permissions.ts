// Actions for command permission rules: add, remove, reorder.
// ---------------------------------------------------------------------------

import { apiAction, retryNetwork, RETRY_STANDARD } from "./index.js";
import type { PolicyExplainResult } from "../types.js";

export interface CommandRule {
  pattern: string;
  mode: "allow" | "deny";
  priority: number;
  created_at: number;
}

// --- Native Cedar policy rules (v3 / KAS) -----------------------------------
// These write the user/workspace permissions.yaml, which KAS hot-reloads. No
// optimistic update: the native view is a server projection, refetched after
// a successful edit (and on the permissions_changed SSE), so it can never
// drift from what KAS actually enforces.

/** Add or remove a native policy rule. op="add" defaults an empty effect to
 *  "ask" server-side (conservative); op="remove" needs the exact rule, and
 *  removing a deny needs confirm=true (widens access). */
export interface NativeRuleArgs {
  op: "add" | "remove";
  scope: "user" | "workspace";
  capability: string;
  /** allow | deny | ask. Empty on add → server defaults to ask. */
  effect: string;
  match?: string[];
  exclude?: string[];
  confirm?: boolean;
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

interface AddRuleArgs {
  pattern: string;
  mode: "allow" | "deny";
  priority: number;
  /** Mutable reference to the controller's rules array (captured at
   *  dispatch time; may be stale by rollback time if other paths
   *  mutated it). Prefer `getCurrentRules` for fresh state. */
  rules: CommandRule[];
  /** Callback to update the controller's rules and re-render. */
  setRules: (rules: CommandRule[]) => void;
  /** Live getter for the current rules array. Used by rollback to
   *  splice changes into the latest state without clobbering
   *  unrelated concurrent mutations (e.g. loadRules() refreshing
   *  outside the scope queue). */
  getCurrentRules: () => CommandRule[];
}

interface RemoveRuleArgs {
  pattern: string;
  rules: CommandRule[];
  setRules: (rules: CommandRule[]) => void;
  /** Live getter for the current rules array. */
  getCurrentRules: () => CommandRule[];
}

// Both addRule and removeRule are pure-optimistic: they update local state
// immediately without server confirmation (no loadRules() after dispatch).
// Any mismatch between local and server state is corrected on the next page
// load when loadRules() is called during initShellPolicy().

export const addRule = apiAction<
  AddRuleArgs,
  unknown,
  { pattern: string; previousRule: CommandRule | undefined }
>({
  name: "permissions.add_rule",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  idempotencyKey: true,
  scope: "permissions",
  request: ({ pattern, mode, priority }) => ({
    method: "POST",
    path: "/api/permissions/commands",
    body: { pattern, mode, priority },
  }),
  optimistic: ({ pattern, mode, priority, rules, setRules }) => {
    // rules captured by value here; rollback uses the snapshot to restore pre-optimistic state.
    const idx = rules.findIndex((e) => e.pattern === pattern);
    const previousRule = idx >= 0 ? rules[idx] : undefined;
    const pending: CommandRule = { pattern, mode, priority, created_at: Date.now() };
    const next = [...rules];
    if (idx >= 0) {
      next[idx] = pending;
    } else {
      next.push(pending);
    }
    setRules(next);
    return { pattern, previousRule };
  },
  rollback: ({ getCurrentRules, setRules }, op) => {
    if (op === undefined) {
      return;
    }
    // Read current state at rollback time, not the dispatch-time
    // snapshot. If concurrent mutations (e.g. loadRules) replaced
    // the rules array, we splice into the latest state instead of
    // clobbering it.
    const current = getCurrentRules();
    if (op.previousRule !== undefined) {
      // Restore the pre-optimistic version of THIS rule (overwrite
      // our optimistic insert with the previous version). If the
      // pattern is no longer in current state (e.g. an external
      // delete happened between optimistic and rollback), fall back
      // to appending so the previousRule isn't lost.
      const prev = op.previousRule;
      const found = current.some((r) => r.pattern === op.pattern);
      if (found) {
        setRules(current.map((r) => (r.pattern === op.pattern ? prev : r)));
      } else {
        setRules([...current, prev]);
      }
    } else {
      // No previous rule — remove our optimistic insert.
      setRules(current.filter((e) => e.pattern !== op.pattern));
    }
  },
  error: "Couldn't add rule",
});

export const removeRule = apiAction<
  RemoveRuleArgs,
  // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args/result
  void,
  { previousRule: CommandRule | undefined; atIndex: number }
>({
  name: "permissions.remove_rule",
  // Not retryable: a timed-out DELETE may have succeeded server-side;
  // retrying would hit 404 and trigger a misleading rollback.
  scope: "permissions",
  request: ({ pattern }) => ({
    method: "DELETE",
    path: `/api/permissions/commands?pattern=${encodeURIComponent(pattern)}`,
  }),
  optimistic: ({ pattern, rules, setRules }) => {
    const idx = rules.findIndex((e) => e.pattern === pattern);
    const previousRule = idx >= 0 ? rules[idx] : undefined;
    setRules(rules.filter((e) => e.pattern !== pattern));
    return { previousRule, atIndex: idx };
  },
  rollback: ({ getCurrentRules, setRules }, op) => {
    if (op?.previousRule === undefined) {
      return;
    }
    // Splice previousRule back into the current rules array at its
    // original position. Without atIndex the rule would jump to the
    // end of the list on rollback (cosmetic glitch); using atIndex
    // preserves the user's display order.
    const prev = op.previousRule;
    const current = getCurrentRules();
    if (current.some((r) => r.pattern === prev.pattern)) {
      // Already there (e.g. loadRules re-fetched it); no-op.
      return;
    }
    const next = [...current];
    next.splice(Math.min(op.atIndex, next.length), 0, prev);
    setRules(next);
  },
  error: "Couldn't remove rule",
});
