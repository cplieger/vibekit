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

export const addRuleAction = apiAction<AddRuleArgs, unknown>({
  name: "permissions.add_rule",
  request: ({ pattern, mode, priority }) => ({
    method: "POST",
    path: "/api/permissions/commands",
    body: { pattern, mode, priority },
  }),
  optimistic: ({ pattern, mode, priority, rules, setRules }) => {
    const pending: CommandRule = { pattern, mode, priority, created_at: Date.now() };
    const idx = rules.findIndex((e) => e.pattern === pattern);
    const next = [...rules];
    if (idx >= 0) next[idx] = pending; else next.push(pending);
    setRules(next);
    return { pattern };
  },
  rollback: ({ rules, setRules }, op) => {
    if (op !== undefined && op !== null && typeof op === "object" && "pattern" in op) {
      const { pattern } = op as { pattern: string };
      setRules(rules.filter((e) => e.pattern !== pattern));
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
    const removed = idx >= 0 ? rules[idx] : undefined;
    setRules(rules.filter((e) => e.pattern !== pattern));
    return { removed, idx };
  },
  rollback: ({ rules, setRules }, op) => {
    if (op !== undefined && op !== null && typeof op === "object" && "removed" in op) {
      const { removed, idx } = op as { removed: CommandRule | undefined; idx: number };
      if (removed === undefined) return;
      const next = [...rules];
      next.splice(idx, 0, removed);
      setRules(next);
    }
  },
  error: "Couldn't remove rule",
});
