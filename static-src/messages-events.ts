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
  // A turn that ended without a proper completion event. Two paths reach it,
  // and NEITHER is a server restart: a prompt RPC that failed with a started
  // assistant buffer (AbandonInFlightTurn), and an empty-turn recovery that
  // recreated the session (recoverEmptyTurn). The label used to name a restart
  // and a `.partial` sidecar recovery; that sidecar is deleted (see
  // hub/bridge_coord.go) and nothing in the tree detects a restart, so the
  // string was a claim about a mechanism that no longer exists.
  //
  // NO labelFn, and THIS IS THE OWNERSHIP RULE, stated here and at
  // `turnFailureText` (turns.ts) because it is the only thing keeping the two
  // surfaces from both speaking: THE CARD-LEVEL `.turn-notice` OWNS THE PROSE
  // ACCOUNT of why a turn did not end cleanly. A body divider marks the BOUNDARY
  // and names its KIND; it never repeats that prose.
  //
  // The notice wins because it is the surface present in BOTH fold states —
  // `syncTurnFace` early-returns for an unfolded card, which is why a face-only
  // reason was unreachable on an open turn, and a broken turn is precisely the
  // turn that never auto-folds. The divider is inside `.turn-body`, so a folded
  // card hides it.
  //
  // Nothing is lost by dropping the labelFn: `turnFailureText`'s first source
  // still reads THIS row's `content`, so the server's own sentence still reaches
  // the reader — once, on the durable surface, instead of twice about 50px apart
  // (measured on live chat `c-a7f83c9…` turn 1).
  //
  // Still red "failed"-styled: a turn cut short reads as a short but complete
  // answer without a visible boundary.
  interrupted: {
    kind: "boundary",
    boundary: "failed",
    icon: "\u26a0",
    defaultLabel: "Turn interrupted",
  },
  cancelled: { kind: "skip" },
  // The outcome marker is a CARRIER, not a divider. It exists because a turn that
  // emitted nothing has no assistant message to stamp `turn_outcome` on, so the
  // record needs a row to hold it — and the turn card already renders that outcome
  // as its own tint, glyph and label. A second visible line saying the same thing
  // would be the one case where an empty turn is louder than a full one.
  //
  // A `skip` is still a MESSAGE: it opens the turn, so the transcript shows a
  // headerless card with the failure on its footer rather than nothing at all.
  turn_outcome: { kind: "skip" },
  // A message a workflow STEP sent into the chat that launched its run, replayed
  // off the durable copy KAS keeps. It used to come back as a USER bubble, so the
  // transcript claimed the reader had typed the step's own question.
  //
  // The content IS the message, so `labelFn` renders it rather than a fixed
  // sentence with the text dropped — this is the only durable copy of a question
  // the interaction dock holds in memory, and a divider reading "A step sent a
  // message" would lose it a second way. A boundary rather than a bubble because
  // the author is neither side of the conversation: a step is work this chat
  // dispatched, and `switched` is the neutral face the other
  // this-happened-to-your-session markers already use.
  step_notice: {
    kind: "boundary",
    boundary: "switched",
    icon: "\u{1F4AC}",
    defaultLabel: "A workflow step sent a message",
    labelFn: (c) => (c === "" ? "A workflow step sent a message" : `Step: ${c}`),
  },
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
