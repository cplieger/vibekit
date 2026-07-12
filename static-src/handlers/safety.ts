// ---------------------------------------------------------------------------
// Infrastructure-Safety status handler (v3/KAS _kiro/safety/*).
//
// DEFENSIVE / forward-looking, like handlers/open-external-url.ts. KAS's
// Infrastructure-Safety gate evaluates infrastructure-as-code tool calls
// (Terraform / CloudFormation / CDK / Docker / k8s / …) against remotely
// "formalized" safety properties and streams its state as safety_status /
// safety_properties SSE. But KAS only installs the gate — and so only emits
// these — when vibekit's infrastructureSafety capability AND an AWS governance
// flag (infraSafetyMonitor|infraSafetyEnforce) are both on; that flag is off by
// default on individual/Builder-ID accounts, so on a normal account this handler
// never fires. It exists so the state surfaces IF an enterprise account has the
// gate enabled.
//
// Surface: a single transient status banner (banner-stack), shown only while the
// gate is active and cleared on idle / turn end — nothing when idle/empty. This
// is Kiro's infra guardrail, called out as distinct from vibekit's own
// Supervised write-gate. There is no authoring UI: safety properties are
// formalized out-of-band (a remote endpoint), never created by the client.
// ---------------------------------------------------------------------------

import { onSSE } from "../bus.js";
import { showBanner, clearBannerCodes } from "../banner-stack.js";
import type { BannerLevel } from "../types.js";
import type { SafetyStatusPayload } from "../wire/types.gen.js";

const CODE = "safety_status";
const MAX_PROPS_SHOWN = 3;

// Latest formalized safety-property descriptions per chat, from
// safety_properties. Used as context on a status banner when the status itself
// carries no blocked_properties list.
const activeProps = new Map<string, string[]>();

/** Map a gate status to a banner severity. blocked → error (a write was or
 *  would be stopped); evaluating/error → warning; formalizing → info. */
function levelFor(status: string): BannerLevel {
  if (status === "blocked") {
    return "error";
  }
  if (status === "evaluating" || status === "error") {
    return "warning";
  }
  return "info";
}

/** Human-readable banner text for a gate status, prefixed so it reads as
 *  Kiro's Infrastructure Safety (distinct from vibekit's Supervised gate). */
function headlineFor(p: SafetyStatusPayload): string {
  switch (p.status) {
    case "blocked":
      return "Infrastructure Safety blocked a change";
    case "evaluating":
      return p.detail !== undefined && p.detail !== ""
        ? `Infrastructure Safety: ${p.detail}`
        : "Infrastructure Safety is reviewing a change";
    case "formalizing":
      return "Infrastructure Safety is analyzing a change\u2026";
    case "error":
      return "Infrastructure Safety check failed";
    default:
      return "Infrastructure Safety active";
  }
}

/** Append a short constraint list to the headline. Prefers the status's own
 *  blocked_properties; falls back to the chat's last formalized properties. */
function withConstraints(headline: string, p: SafetyStatusPayload, chatID: string): string {
  const blocked = p.blocked_properties ?? [];
  const list = blocked.length > 0 ? blocked : (activeProps.get(chatID) ?? []);
  if (list.length === 0) {
    return headline;
  }
  const shown = list.slice(0, MAX_PROPS_SHOWN).join("; ");
  const more = list.length > MAX_PROPS_SHOWN ? ` (+${list.length - MAX_PROPS_SHOWN} more)` : "";
  return `${headline} \u2014 ${shown}${more}`;
}

onSSE("safety_properties", (chatID, p) => {
  if (chatID === "") {
    return;
  }
  const descs = p.properties.map((x) => x.description).filter((d) => d !== "");
  if (descs.length === 0) {
    activeProps.delete(chatID);
    return;
  }
  activeProps.set(chatID, descs);
});

onSSE("safety_status", (chatID, p) => {
  if (chatID === "") {
    return;
  }
  // idle is the all-clear: drop any active-gate banner.
  if (p.status === "idle") {
    clearBannerCodes(chatID, [CODE]);
    return;
  }
  const message = withConstraints(headlineFor(p), p, chatID);
  showBanner(chatID, CODE, message, levelFor(p.status), true);
});

// A blocked/evaluating state can arrive without a trailing idle (the turn may
// end first); clear the transient banner on turn end so it can't wedge.
onSSE("turn_ended", (chatID) => {
  clearBannerCodes(chatID, [CODE]);
});
