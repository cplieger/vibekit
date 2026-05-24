import { apiAction } from "./index.js";

interface AddRuleArgs {
  pattern: string;
  mode: "allow" | "deny";
  priority: number;
}

export const addRuleAction = apiAction<AddRuleArgs, unknown>({
  name: "permissions.add_rule",
  request: ({ pattern, mode, priority }) => ({
    method: "POST",
    path: "/api/permissions/commands",
    body: { pattern, mode, priority },
  }),
  error: "Couldn't add rule",
});

export const removeRuleAction = apiAction<string, void>({
  name: "permissions.remove_rule",
  request: (pattern) => ({
    method: "DELETE",
    path: `/api/permissions/commands?pattern=${encodeURIComponent(pattern)}`,
  }),
  error: "Couldn't remove rule",
});
