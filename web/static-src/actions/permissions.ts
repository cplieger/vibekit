import { apiAction } from "./index.js";
import { asOp } from "./op.js";

export interface CommandRule {
  pattern: string;
  mode: "allow" | "deny";
  priority: number;
  created_at: number;
}

export interface AddRuleArgs {
  pattern: string;
  mode: "allow" | "deny";
  priority: number;
  /** Mutable reference to the controller's rules array. */
  rules: CommandRule[];
  /** Callback to update the controller's rules and re-render. */
  setRules: (rules: CommandRule[]) => void;
}

export interface RemoveRuleArgs {
  pattern: string;
  rules: CommandRule[];
  setRules: (rules: CommandRule[]) => void;
}

// Both addRule and removeRule are pure-optimistic: they update local state
// immediately without server confirmation (no loadRules() after dispatch).
// Any mismatch between local and server state is corrected on the next page
// load when loadRules() is called during initShellPolicy().

export const addRuleAction = apiAction<AddRuleArgs, unknown>({
  name: "permissions.add_rule",
  retryable: "network",
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
    if (idx >= 0) next[idx] = pending; else next.push(pending);
    setRules(next);
    return { pattern, previousRule };
  },
  rollback: ({ rules, setRules }, op) => {
    const o = asOp<{ pattern: string; previousRule?: CommandRule }>(op);
    if (o === undefined) return;
    if (o.previousRule) {
      setRules([...rules]);
    } else {
      setRules(rules.filter((e) => e.pattern !== o.pattern));
    }
  },
  error: "Couldn't add rule",
});

export const removeRuleAction = apiAction<RemoveRuleArgs, void>({
  name: "permissions.remove_rule",
  retryable: "network",
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
  rollback: ({ rules, setRules }, op) => {
    const o = asOp<{ previousRule: CommandRule | undefined }>(op);
    if (o === undefined || o.previousRule === undefined) return;
    setRules([...rules]);
  },
  error: "Couldn't remove rule",
});
