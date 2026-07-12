// ---------------------------------------------------------------------------
// Account/subscription usage in the sidebar status-dot popup.
//
// This is ACCOUNT-level usage (plan, credits, quota) from the KAS
// _kiro/account/getUsage request — distinct from the per-chat context ring
// (which reads the session usage_update notification). It is surfaced ONLY
// here, in the status footer popup, per the product decision.
//
// Fetched LAZILY when the status popup opens (wired via makeExpandable's
// onExpand in app.ts) with a short client-side throttle on top of the
// server's own cache, since account usage changes slowly and the upstream
// call may be rate-limited. Failure degrades gracefully to "Usage
// unavailable" (the server also serves a last-known snapshot when it can).
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { apiGetTyped } from "./api-client.js";
import { decodeAccountUsage } from "./wire/decoders.gen.js";
import type { AccountUsage, AccountUsageBreakdown } from "./types.js";

const CLIENT_TTL_MS = 30_000;
let lastFetch = 0;
let inflight = false;

/** Fetch + render account usage. Throttled to CLIENT_TTL_MS unless forced.
 *  Safe to call on every popup open; the throttle + server cache keep it
 *  cheap. */
export function loadAccountUsage(force = false): void {
  const now = Date.now();
  if (inflight || (!force && now - lastFetch < CLIENT_TTL_MS)) {
    return;
  }
  inflight = true;
  lastFetch = now;
  void apiGetTyped<AccountUsage>("/api/account/usage", decodeAccountUsage)
    .then((u) => {
      renderAccountUsage(u);
    })
    .finally(() => {
      inflight = false;
    });
}

/** Pick the credit breakdown (or the first line) for the compact summary. */
function primaryBreakdown(u: AccountUsage): AccountUsageBreakdown | undefined {
  const list = u.breakdowns;
  return list.find((b) => b.resource_type === "CREDIT") ?? list[0];
}

function fmtAmount(n: number): string {
  return n.toLocaleString(undefined, { maximumFractionDigits: 0 });
}

function unitLabel(b: AccountUsageBreakdown): string {
  // Credits render as "cr"; anything else uses its display name lowercased.
  if (b.resource_type === "CREDIT") {
    return "cr";
  }
  return (b.display_name ?? "").toLowerCase();
}

function renderAccountUsage(u: AccountUsage | null): void {
  const box = $.stAccount;
  const planEl = $.acctPlan;
  const meterEl = $.acctMeter;
  box.hidden = false;

  if (u === null) {
    planEl.textContent = "Usage unavailable";
    meterEl.textContent = "";
    meterEl.removeAttribute("title");
    return;
  }

  const plan =
    u.plan_name !== undefined && u.plan_name !== "" ? u.plan_name : (u.note ?? "Account");
  planEl.textContent = u.stale === true ? `${plan} (cached)` : plan;

  const b = primaryBreakdown(u);
  if (b === undefined) {
    // No usage line (e.g. admin-managed plan). The plan label already
    // carries the note, so leave the meter empty.
    meterEl.textContent = "";
    meterEl.removeAttribute("title");
    return;
  }

  const used = fmtAmount(b.used);
  const unit = unitLabel(b);
  if (b.has_limit === true) {
    const limit = fmtAmount(b.limit);
    meterEl.textContent = `${used} / ${limit} ${unit} (${String(b.percentage)}%)`;
  } else {
    meterEl.textContent = `${used} ${unit}`;
  }
  if (u.billing_cycle_reset !== undefined && u.billing_cycle_reset !== "") {
    meterEl.title = `Resets ${u.billing_cycle_reset}`;
  } else {
    meterEl.removeAttribute("title");
  }
}
