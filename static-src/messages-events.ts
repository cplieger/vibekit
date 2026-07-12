// ---------------------------------------------------------------------------
// Event/boundary rendering: build event messages (boundary dividers, system
// messages).
//
// v3 EventKinds are boundary dividers (model_switched, compacted,
// compaction_failed, infra_safety_blocked, interrupted). `cancelled` is the
// one invisible marker (an expected user action needs no badge). The v2
// crew / inbox / agent_switched kinds are gone.
// ---------------------------------------------------------------------------

import type { Message, EventKind } from "./types.js";
import { el } from "@cplieger/reactive";

// ---------------------------------------------------------------------------
// Event render strategy (exhaustive over EventKind via satisfies)
// ---------------------------------------------------------------------------

type BoundaryKind = "switched" | "compacted" | "failed" | "blocked";

type EventRenderStrategy =
  | {
      kind: "boundary";
      boundary: BoundaryKind;
      icon: string;
      defaultLabel: string;
      labelFn?: (content: string) => string;
    }
  | { kind: "skip" };

/** Exhaustive render strategy map — every EventKind must have an entry.
 *  Adding a new EventKind to the generated union produces a compile error
 *  until this map is updated. */
export const EVENT_RENDER_MAP: Readonly<Record<EventKind, EventRenderStrategy>> = {
  model_switched: {
    kind: "boundary",
    boundary: "switched",
    icon: "\u21bb",
    defaultLabel: "Context reset",
    labelFn: (c) => (c ? `Switched to ${c}` : "Context reset"),
  },
  compacted: {
    kind: "boundary",
    boundary: "compacted",
    icon: "\u273b",
    defaultLabel: "Conversation compacted",
  },
  compaction_failed: {
    kind: "boundary",
    boundary: "failed",
    icon: "\u26a0",
    defaultLabel: "Compaction failed",
    labelFn: (c) => (c ? `Compaction failed: ${c}` : "Compaction failed"),
  },
  // Kiro Infrastructure-Safety ENFORCE-mode refusal: KAS blocked an
  // infra-as-code write/shell tool call upstream (the tool never ran, nothing
  // was written). A permanent transcript record, distinct from the transient
  // status banner in handlers/safety.ts. content carries the violated safety
  // properties; the block is chat-scoped (KAS's toolId is a tool name, not a
  // tool_call id, so it can't pin a specific tool card).
  infra_safety_blocked: {
    kind: "boundary",
    boundary: "blocked",
    icon: "\u{1F6E1}",
    defaultLabel: "Infrastructure Safety blocked a change",
    labelFn: (c) =>
      c ? `Infrastructure Safety blocked: ${c}` : "Infrastructure Safety blocked a change",
  },
  // A turn cut short by a server restart, recovered from its .partial file.
  // Without a visible boundary the recovered (often mid-sentence) turn reads
  // as a short but complete answer — so render a red "failed"-styled divider.
  // Reuses the existing .boundary-failed styling (no new CSS).
  interrupted: {
    kind: "boundary",
    boundary: "failed",
    icon: "\u26a0",
    defaultLabel: "Interrupted by server restart",
  },
  cancelled: { kind: "skip" },
} satisfies Record<EventKind, EventRenderStrategy>;

// ---------------------------------------------------------------------------
// Build / update
// ---------------------------------------------------------------------------

export function buildEvent(m: Message): HTMLElement | null {
  if (m.event_kind !== undefined) {
    const strategy = EVENT_RENDER_MAP[m.event_kind];
    if (strategy.kind === "boundary") {
      const content = m.content ?? "";
      const label = strategy.labelFn ? strategy.labelFn(content) : strategy.defaultLabel;
      const divider = buildBoundaryDivider(strategy.boundary, strategy.icon, label);
      // Compaction carries the conversation summary in the event content.
      // Surface it as a collapsible disclosure below the marker (reusing the
      // reasoning-block styling) instead of dropping it, matching the IDE's
      // "Conversation summary" affordance.
      if (m.event_kind === "compacted" && content !== "") {
        return wrapWithSummary(divider, content);
      }
      return divider;
    }
    // "skip" — cancelled produces no visible element
  }
  return null;
}

export function updateEvent(_el: HTMLElement, _m: Message): void {
  // All event types are immutable from the global reconcile's POV.
}

// ---------------------------------------------------------------------------
// Internal
// ---------------------------------------------------------------------------

function buildBoundaryDivider(kind: BoundaryKind, icon: string, label: string): HTMLElement {
  const node = el("div", { className: `boundary boundary-${kind}` });
  node.appendChild(el("span", { className: "boundary-icon" }, icon));
  node.appendChild(el("span", { className: "boundary-label" }, label));
  return node;
}

/** Stack a compaction boundary above a collapsible "Conversation summary"
 *  disclosure. Reuses the reasoning-block/summary/body classes so no new CSS
 *  is needed; the wrapper is a plain block container. */
function wrapWithSummary(divider: HTMLElement, summary: string): HTMLElement {
  const wrap = el("div", { className: "boundary-with-summary" });
  const details = el("details", { className: "reasoning-block compaction-summary" });
  details.appendChild(el("summary", { className: "reasoning-summary" }, "Conversation summary"));
  details.appendChild(el("blockquote", { className: "reasoning-body" }, summary));
  wrap.append(divider, details);
  return wrap;
}

export function buildSystemFallback(m: Message): HTMLElement {
  return el("div", { className: "message system" }, m.content ?? m.event_kind ?? "");
}
