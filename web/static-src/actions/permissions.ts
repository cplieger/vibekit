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
  retryable: "network",
  request: ({ pattern, mode, priority }) => ({
    method: "POST",
    path: "/api/permissions/commands",
    body: { pattern, mode, priority },
  }),
  optimistic: ({ pattern, mode, priority, rules, setRules }) => {
    const prev = [...rules];
    const pending: CommandRule = { pattern, mode, priority, created_at: Date.now() };
    const idx = rules.findIndex((e) => e.pattern === pattern);
    const next = [...rules];
    if (idx >= 0) next[idx] = pending; else next.push(pending);
    setRules(next);
    return prev;
  },
  rollback: (_args, op) => {
    if (op !== undefined) {
      const prev = op as CommandRule[];
      _args.setRules(prev);
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
    const prev = [...rules];
    setRules(rules.filter((e) => e.pattern !== pattern));
    return prev;
  },
  rollback: (_args, op) => {
    if (op !== undefined) {
      const prev = op as CommandRule[];
      _args.setRules(prev);
    }
  },
  error: "Couldn't remove rule",
});
