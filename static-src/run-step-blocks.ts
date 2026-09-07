// ---------------------------------------------------------------------------
// A parentless run's step transcript: the client half of the `run_step` event.
//
// This is what the run tab was missing. A run launched from the Workflows tab or
// by the scheduler has no chat, so its steps' content arrives on the run bridge
// rather than a conversation's — and that bridge dropped it, which left the tab
// showing a step's captured output once it finished and nothing at all while it
// worked. The server now projects those frames as `run_step` events
// (internal/translate/workflow_step_content.go); this module renders them.
//
// It renders the SAME THREE THINGS a transcript does, with the same primitives:
// prose through the streaming markdown bubble, reasoning through the collapsible
// trace, a tool call through the real tool card. That is deliberate rather than
// convenient — a reader who has learned what a tool card means in a chat must not
// have to learn a second vocabulary for the identical tool inside a run.
//
// What it is NOT: a message store. There are no message ids here, no turns, no
// persistence and no reconcile. A step's content belongs to a turn vibekit never
// prompted and therefore never finalizes, so it cannot survive a reload — the
// captured output on the step's row is the durable half, and this is the live
// half. `reset()` is called when the tab points at another run, which is the whole
// of its lifecycle.
//
// ONE HOST PER STEP, supplied by the caller. The DETAIL PANE owns those hosts
// (`exec-view/detail.ts` `bodyFor(nodePath)`, handed to this module by
// `run-view.ts`), so this module never decides where a step goes — it only decides
// what a step's body contains. The transcript's run card hosts nothing: its step
// rows are doors into this page.
// ---------------------------------------------------------------------------

import { buildAssistantBubble, type AssistantBubble } from "./fundamentals/text-bubble.js";
import { buildReasoning, type ReasoningView } from "./fundamentals/reasoning.js";
import { buildToolCard, expandToolDetails } from "./tool-card.js";
import { toolCardOptsFor } from "./tool-card-opts.js";
import type { RunStepPayload, ToolCall } from "./types.js";

/** The trailing block of a step's body, so a run of same-kind deltas extends one
 *  element instead of opening a new one per frame.
 *
 *  Exactly the rule the server's buffer applies to a chat (`AppendTextDelta`
 *  extends the trailing block only when the kind matches), applied on this side
 *  because a run has no buffer. A tool call between two text deltas closes the
 *  first bubble, which is what keeps the order on screen the order the step
 *  actually did things in. */
type Tail =
  | { kind: "text"; view: AssistantBubble }
  | { kind: "thinking"; view: ReasoningView }
  | { kind: "none" };

interface StepStream {
  host: HTMLElement;
  tail: Tail;
  /** The cards this step has opened, so an update REPLACES its card rather than
   *  appending a second one. A `run_step` tool frame carries the whole folded
   *  call on every update by design, so the card is rebuilt from it. */
  tools: Map<string, HTMLElement>;
}

/** Where a step's blocks go. The run card answers this; declared here because it
 *  is this module's requirement, not the card's. */
export type StepHost = (nodePath: string) => HTMLElement;

/** A run's live step transcript. One per run tab. */
export interface RunStepStream {
  /** Apply one `run_step` frame. */
  apply(payload: RunStepPayload): void;
  /** Seal every open block: no more content is coming. Called when the run
   *  reaches a terminal state, so a bubble stops showing its caret and a
   *  reasoning trace collapses. */
  seal(): void;
}

export function createRunStepStream(hostFor: StepHost): RunStepStream {
  const steps = new Map<string, StepStream>();

  function stepFor(nodePath: string): StepStream {
    let s = steps.get(nodePath);
    if (s === undefined) {
      s = { host: hostFor(nodePath), tail: { kind: "none" }, tools: new Map() };
      steps.set(nodePath, s);
    }
    return s;
  }

  function appendText(s: StepStream, delta: string): void {
    if (s.tail.kind === "text") {
      s.tail.view.append(delta);
      return;
    }
    sealTail(s);
    const view = buildAssistantBubble("", true);
    s.host.appendChild(view.root);
    view.append(delta);
    s.tail = { kind: "text", view };
  }

  function appendThinking(s: StepStream, delta: string): void {
    if (s.tail.kind === "thinking") {
      s.tail.view.append(delta);
      return;
    }
    sealTail(s);
    const view = buildReasoning("", true);
    s.host.appendChild(view.root);
    view.append(delta);
    s.tail = { kind: "thinking", view };
  }

  function applyTool(s: StepStream, tc: ToolCall): void {
    // Rebuilt rather than patched in place, and that follows from the wire shape
    // rather than being a shortcut: the server folds every update into the call
    // and sends the whole thing, so the newest frame is the complete truth and a
    // rebuild cannot drift from it. The transcript patches instead because its
    // cards are driven by a per-call signal in the message store, machinery a run
    // has no reason to grow.
    //
    // The one thing a rebuild would lose is an open details region, so it is
    // carried across: a reader watching a command's output must not have it fold
    // shut under them every time a chunk lands.
    const previous = s.tools.get(tc.id);
    const wasOpen =
      previous?.querySelector<HTMLElement>(".tool-disclosure")?.getAttribute("aria-expanded") ===
      "true";
    const card = buildToolCard(toolCardOptsFor(tc, true));
    if (previous === undefined) {
      // A tool call ends the trailing text run, so the next delta opens its own
      // bubble below the card instead of extending the paragraph above it.
      sealTail(s);
      s.host.appendChild(card);
    } else {
      previous.replaceWith(card);
    }
    if (wasOpen) {
      // After the mount, because the controller is wired during the build and
      // opening animates a height the element has to be laid out to have.
      expandToolDetails(card);
    }
    s.tools.set(tc.id, card);
  }

  function sealTail(s: StepStream): void {
    if (s.tail.kind === "text") {
      // `end`, not `finishNow`: the reveal cursor is still catching up to text
      // that has already arrived, and cutting it short would jump the last
      // paragraph onto the screen instead of finishing the stream it is in.
      s.tail.view.end();
    } else if (s.tail.kind === "thinking") {
      s.tail.view.seal();
    }
    s.tail = { kind: "none" };
  }

  return {
    apply(payload: RunStepPayload): void {
      if (payload.node_path === "") {
        return;
      }
      const s = stepFor(payload.node_path);
      switch (payload.kind) {
        case "text":
          if (payload.delta !== undefined && payload.delta !== "") {
            appendText(s, payload.delta);
          }
          return;
        case "thinking":
          if (payload.delta !== undefined && payload.delta !== "") {
            appendThinking(s, payload.delta);
          }
          return;
        case "tool":
          if (payload.tool_call !== undefined) {
            applyTool(s, payload.tool_call);
          }
          return;
        default:
          // An unrecognised kind is an upstream addition, not a fault here. The
          // server would have to grow one, and a client older than that server
          // renders nothing rather than guessing.
          return;
      }
    },
    seal(): void {
      for (const s of steps.values()) {
        sealTail(s);
      }
    },
  };
}
