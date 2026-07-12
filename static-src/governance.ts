// ---------------------------------------------------------------------------
// Organization / account governance policy (v3 _kiro/governance/state).
//
// KAS pushes an account/workspace feature-flag policy on every session; vibekit
// caches the latest server-side and serves a snapshot at GET /api/governance so
// a fresh page load can read it with no chat open, then keeps it live via the
// governance_state SSE. This module is the single client owner of that state:
//
//   - initGovernance()        fetch the snapshot on load + subscribe to the SSE
//   - currentGovernance()     the latest known state (or null before first read)
//   - featureDisabled(key)    true only when KNOWN and that feature is off
//   - onGovernanceChange(fn)  subscribe (fires immediately if already known)
//
// It also renders the read-only "Organization policy" disclosure in
// Settings → General (its own surface, like account-usage.ts owns the footer).
// Consumers that gate their own affordances: mcp-ui.ts (MCP availability) and
// code-refs.ts (the licensed-code attribution chip). Everything here is
// read-only — the flags are org-controlled, never user-settable.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { onSSE } from "./bus.js";
import { apiGetTyped } from "./api-client.js";
import { decodeGovernanceStatePayload } from "./wire/decoders.gen.js";
import type { GovernanceStatePayload, GovernanceFeatures } from "./types.js";

let current: GovernanceStatePayload | null = null;
const listeners = new Set<(g: GovernanceStatePayload) => void>();

/** The latest known governance state, or null before the first read. */
export function currentGovernance(): GovernanceStatePayload | null {
  return current;
}

/** True only when governance is KNOWN and the named feature is off. Unknown
 *  governance → false: never imply a feature is disabled before the server has
 *  told us the real policy (the all-false zero value is "unknown", not "off"). */
export function featureDisabled(key: keyof GovernanceFeatures): boolean {
  return current !== null && current.known && !current.features[key];
}

/** Subscribe to governance changes. Fires immediately with the current state
 *  when one is already known, then again on every update. No unsubscribe: the
 *  consumers (settings panels, mcp-ui) live for the app's lifetime. */
export function onGovernanceChange(fn: (g: GovernanceStatePayload) => void): void {
  listeners.add(fn);
  if (current !== null) {
    fn(current);
  }
}

function set(g: GovernanceStatePayload): void {
  current = g;
  renderOrgPolicy(g);
  for (const fn of listeners) {
    fn(g);
  }
}

/** Fetch the snapshot on load + subscribe to live updates. Call once from
 *  app.ts init. The snapshot may be Known=false (no bridge has started yet);
 *  the SSE fills it in once any session pushes the policy. */
export function initGovernance(): void {
  onSSE("governance_state", (_chatID, p) => {
    set(p);
  });
  void apiGetTyped<GovernanceStatePayload>("/api/governance", decodeGovernanceStatePayload).then(
    (g) => {
      // Only adopt a KNOWN snapshot; a cold Known=false read carries no real
      // policy, so leave `current` null (featureDisabled stays permissive).
      if (g?.known === true) {
        set(g);
      }
    },
  );
}

// --- Settings → General: read-only "Organization policy" disclosure ---

/** Rows shown in the disclosure. mcp_enabled is deliberately omitted — it has
 *  its own dedicated affordance in Settings → Tools. Privacy-relevant flags
 *  are grouped first, then capability flags that lack a dedicated surface. */
const POLICY_ROWS: readonly { key: keyof GovernanceFeatures; label: string }[] = [
  { key: "prompt_logging", label: "Prompt logging" },
  { key: "usage_analytics", label: "Usage analytics" },
  { key: "content_collection", label: "Content collection" },
  { key: "web_tools_enabled", label: "Built-in web tools" },
  { key: "autonomous_agents", label: "Autonomous agents" },
  { key: "code_reference_tracker", label: "Code-reference tracking" },
];

/** Render (or hide) the read-only org-policy disclosure. Hidden until the real
 *  policy is known so the panel never shows an all-"Off" placeholder. */
function renderOrgPolicy(g: GovernanceStatePayload): void {
  const section = document.getElementById("general-governance-section");
  if (section === null) {
    return;
  }
  if (!g.known) {
    section.hidden = true;
    return;
  }
  section.hidden = false;

  const grid = el("dl", { className: "about-grid governance-grid" });
  grid.appendChild(el("dt", {}, "Account"));
  grid.appendChild(el("dd", {}, g.is_enterprise === true ? "Enterprise (managed)" : "Individual"));
  for (const row of POLICY_ROWS) {
    grid.appendChild(el("dt", {}, row.label));
    grid.appendChild(policyValue(g.features[row.key]));
  }

  const children: HTMLElement[] = [
    el("h3", { className: "section-title" }, "Organization policy"),
    el(
      "p",
      { className: "section-hint" },
      "Controlled by your organization or account \u2014 shown here for transparency and not changeable from Vibekit.",
    ),
    grid,
  ];
  const reason = (g.disabled_reason ?? "").trim();
  if (reason !== "") {
    children.push(el("p", { className: "governance-reason" }, reason));
  }
  section.replaceChildren(...children);
}

/** A single On/Off value cell with a status class for styling. */
function policyValue(on: boolean): HTMLElement {
  return el("dd", { className: on ? "governance-on" : "governance-off" }, on ? "On" : "Off");
}
