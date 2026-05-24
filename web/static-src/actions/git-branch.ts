import { defineAction, ActionError } from "./index.js";
import { withTimeout, API_TIMEOUT_MS } from "../api-client.js";

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
      signal: withTimeout(signal, API_TIMEOUT_MS),
    });
    if (!r.ok) {
      let msg = `HTTP ${String(r.status)}`;
      try {
        const body = (await r.json()) as { error?: string };
        if (body.error) msg = body.error;
      } catch { /* use default msg */ }
      throw new ActionError(msg, { status: r.status });
    }
    const body = (await r.json()) as { error?: string };
    if (body.error !== undefined && body.error !== "") {
      throw new ActionError(body.error);
    }
  },
  error: "Branch checkout failed",
});
