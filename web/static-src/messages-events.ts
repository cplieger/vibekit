// ---------------------------------------------------------------------------
// Event/boundary rendering: build event messages (boundary dividers,
// system messages, inbox, crew cards).
//
// Extracted from messages.ts — the "Events" section (lines 1244-1362).
// ---------------------------------------------------------------------------

import type { Message, EventKind, Crew } from "./types.js";
import { ensureCrewSig, clearCrewSig } from "./store-signals.js";
import { effect } from "./signals.js";
import {
  updateCrew as updateCrewInternal,
  buildCrewCardForReplay,
} from "./crew-card.js";

// ---------------------------------------------------------------------------
// Event render strategy (exhaustive over EventKind via satisfies)
// ---------------------------------------------------------------------------

export type BoundaryKind = "switched" | "compacted" | "failed" | "agent";

type EventRenderStrategy =
  | {
      kind: "boundary";
      boundary: BoundaryKind;
      icon: string;
      defaultLabel: string;
      labelFn?: (content: string) => string;
    }
  | { kind: "inline"; render: (m: Message) => HTMLElement | null }
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
  agent_switched: {
    kind: "boundary",
    boundary: "agent",
    icon: "\u2192",
    defaultLabel: "Agent switched",
    labelFn: (c) => c || "Agent switched",
  },
  interrupted: { kind: "skip" },
  cancelled: { kind: "skip" },
  inbox: {
    kind: "inline",
    render: (m) => {
      const el = document.createElement("div");
      el.className = "message system inbox-message";
      el.textContent = m.content ?? "Subagent message";
      return el;
    },
  },
  crew: {
    kind: "inline",
    render: () => null, // handled specially via buildEvent's crew branch
  },
} satisfies Record<EventKind, EventRenderStrategy>;

/** Legacy compat export — the old Partial<Record<EventKind, ...>> shape.
 *  Consumers that only need boundary metadata can use this. */
export const EVENT_BOUNDARY_META: Readonly<
  Partial<
    Record<
      EventKind,
      {
        readonly boundary: BoundaryKind;
        readonly icon: string;
        readonly defaultLabel: string;
        readonly labelFn?: (content: string) => string;
      }
    >
  >
> = Object.fromEntries(
  Object.entries(EVENT_RENDER_MAP)
    .filter(([, v]) => v.kind === "boundary")
    .map(([k, v]) => [k, v as Extract<EventRenderStrategy, { kind: "boundary" }>]),
) as Readonly<Partial<Record<EventKind, { readonly boundary: BoundaryKind; readonly icon: string; readonly defaultLabel: string; readonly labelFn?: (content: string) => string }>>>;

// ---------------------------------------------------------------------------
// Crew effect tracking (owned here, disposed by messages.ts on unmount)
// ---------------------------------------------------------------------------

const crewEffects = new Map<string, () => void>();

export function disposeCrewEffect(id: string): void {
  const fn = crewEffects.get(id);
  if (fn !== undefined) {
    fn();
    crewEffects.delete(id);
  }
  clearCrewSig(id);
}

export function disposeAllCrewEffects(): void {
  for (const [id, fn] of crewEffects) {
    fn();
    clearCrewSig(id);
  }
  crewEffects.clear();
}

// ---------------------------------------------------------------------------
// Build / update
// ---------------------------------------------------------------------------

export function buildEvent(m: Message): HTMLElement | null {
  if (m.event_kind !== undefined) {
    const strategy = EVENT_RENDER_MAP[m.event_kind];
    if (strategy.kind === "boundary") {
      const content = m.content ?? "";
      const label = strategy.labelFn ? strategy.labelFn(content) : strategy.defaultLabel;
      return buildBoundaryDivider(strategy.boundary, label);
    }
    if (strategy.kind === "inline") {
      if (m.event_kind === "crew" && m.crew !== undefined) {
        return buildCrewEvent(m.id, m.crew);
      }
      return strategy.render(m);
    }
    // "skip" — interrupted/cancelled produce no visible element
  }
  return null;
}

export function updateEvent(_el: HTMLElement, _m: Message): void {
  // All event types are immutable from the global reconcile's POV.
  // Crew snapshots flow through the per-crew signal directly.
}

// ---------------------------------------------------------------------------
// Internal
// ---------------------------------------------------------------------------

function buildCrewEvent(msgId: string, crew: Crew): HTMLElement {
  const el = buildCrewCardForReplay(msgId, crew);
  const sig = ensureCrewSig(msgId, crew);
  let lastApplied = crew;
  const cleanup = effect(() => {
    const next = sig.value;
    if (next === lastApplied) return;
    updateCrewInternal(msgId, next, () => { /* no-op: el already mounted */ });
    lastApplied = next;
  });
  crewEffects.set(msgId, cleanup);
  return el;
}

function buildBoundaryDivider(kind: BoundaryKind, label: string): HTMLDivElement {
  const el = document.createElement("div");
  el.className = `boundary boundary-${kind}`;
  let icon = "";
  for (const entry of Object.values(EVENT_RENDER_MAP)) {
    if (entry.kind === "boundary" && entry.boundary === kind) {
      icon = entry.icon;
      break;
    }
  }
  const iconSpan = document.createElement("span");
  iconSpan.className = "boundary-icon";
  iconSpan.textContent = icon;
  el.appendChild(iconSpan);
  const labelSpan = document.createElement("span");
  labelSpan.className = "boundary-label";
  labelSpan.textContent = label;
  el.appendChild(labelSpan);
  return el;
}

export function buildSystemFallback(m: Message): HTMLElement {
  const el = document.createElement("div");
  el.className = "message system";
  el.textContent = m.content ?? m.event_kind ?? "";
  return el;
}
