import { apiAction } from "./index.js";

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
  request: ({ pattern, mode, priority }) => ({
    method: "POST",
    path: "/api/permissions/commands",
    body: { pattern, mode, priority },
  }),
  optimistic: ({ pattern, mode, priority, rules, setRules }) => {
    const idx = rules.findIndex((e) => e.pattern === pattern);
    const previousRule = idx >= 0 ? rules[idx] : undefined;
    const pending: CommandRule = { pattern, mode, priority, created_at: Date.now() };
    const next = [...rules];
    if (idx >= 0) next[idx] = pending; else next.push(pending);
    setRules(next);
    return { pattern, previousRule };
  },
  rollback: ({ rules, setRules }, op) => {
    if (op !== undefined && op !== null && typeof op === "object" && "pattern" in op) {
      const { pattern, previousRule } = op as { pattern: string; previousRule?: CommandRule };
      if (previousRule) {
        // restore the old rule
        setRules([...rules.filter((e) => e.pattern !== pattern), previousRule]);
      } else {
        // was a fresh add: just remove
        setRules(rules.filter((e) => e.pattern !== pattern));
      }
    }
  },
  error: "Couldn't add rule",
});

export const removeRuleAction = apiAction<RemoveRuleArgs, void>({
  name: "permissions.remove_rule",
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
    if (op !== undefined && op !== null && typeof op === "object" && "previousRule" in op) {
      const { previousRule, atIndex } = op as { previousRule: CommandRule | undefined; atIndex: number };
      if (previousRule === undefined) return;
      const next = [...rules];
      next.splice(Math.min(atIndex >= 0 ? atIndex : rules.length, rules.length), 0, previousRule);
      setRules(next);
    }
  },
  error: "Couldn't remove rule",
});
