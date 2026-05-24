import { defineAction, ActionError } from "./index.js";
import { withTimeout, API_TIMEOUT_MS } from "../api-client.js";

interface CheckoutArgs {
  repo: string;
  branch: string;
  create: boolean;
  /** The branch chip element to optimistically update. */
  anchorEl?: HTMLElement;
}

export const checkoutBranch = defineAction<CheckoutArgs, void>({
  name: "git.checkout_branch",
  run: async (args, signal) => {
    const r = await fetch("/api/git/checkout", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ repo: args.repo, branch: args.branch, create: args.create }),
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
    // Handle empty 200 body (no Content-Length guarantee) — use text()
    // first to avoid SyntaxError from json() on empty response.
    const text = await r.text();
    if (text === "") return;
    const body = JSON.parse(text) as { error?: string };
    if (body.error !== undefined && body.error !== "") {
      throw new ActionError(body.error);
    }
  },
  optimistic: (args) => {
    if (!args.anchorEl) return undefined;
    const prev = args.anchorEl.textContent ?? "";
    args.anchorEl.textContent = args.branch;
    return prev;
  },
  rollback: (args, op) => {
    if (args.anchorEl && typeof op === "string") {
      args.anchorEl.textContent = op;
    }
  },
  retryable: "network",
  error: "Branch checkout failed",
});
