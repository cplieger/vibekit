import { defineAction, ActionError } from "./index.js";

interface CheckoutArgs {
  repo: string;
  branch: string;
  create: boolean;
}

export const checkoutBranch = defineAction<CheckoutArgs, void>({
  name: "git.checkout_branch",
  run: async (args, signal) => {
    const r = await fetch("/api/git/checkout", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(args),
      signal,
    });
    if (!r.ok) {
      throw new ActionError(`HTTP ${String(r.status)}`, { status: r.status });
    }
    const body = (await r.json()) as { error?: string };
    if (body.error !== undefined && body.error !== "") {
      throw new ActionError(body.error);
    }
  },
  error: "Branch checkout failed",
});
