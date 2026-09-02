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
// Surface: one transient toast per non-idle status. `idle` is the all-clear, so it
// raises nothing — there is no notice to retire, because a toast expires on its
// own. This is Kiro's infra guardrail, called out as distinct from vibekit's own
// Supervised write-gate. There is no authoring UI: safety properties are
// formalized out-of-band (a remote endpoint), never created by the client.
// ---------------------------------------------------------------------------

import { onSSE } from "../bus.js";
import { showToast, type ToastLevel } from "../toast.js";
import type { SafetyStatusPayload } from "../wire/types.gen.js";

const MAX_PROPS_SHOWN = 3;

// Latest formalized safety-property descriptions per chat, from
// safety_properties. Used as context on a status toast when the status itself
// carries no blocked_properties list.
const activeProps = new Map<string, string[]>();

/** Map a gate status to a toast level. Two-way because `ToastLevel` has no
 *  warning: `blocked` and `error` mean a write was or would be stopped, or the
 *  check itself broke; everything else is progress with nothing stopped. */
function levelFor(status: string): ToastLevel {
  return status === "blocked" || status === "error" ? "error" : "info";
}

/** Human-readable toast text for a gate status, prefixed so it reads as
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
  // idle is the all-clear, and it has nothing to say.
  if (p.status === "idle") {
    return;
  }
  showToast(withConstraints(headlineFor(p), p, chatID), levelFor(p.status));
});
