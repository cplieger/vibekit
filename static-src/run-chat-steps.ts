// ---------------------------------------------------------------------------
// The PERSISTED half of a run's step transcript: one render lifecycle, two
// sources.
//
// `run-step-blocks.ts` is the LIVE half — a PARENTLESS run's `run_step` frames,
// which nothing persists. This module renders what is durable, and there are two
// durable sources rather than one:
//
//   the CHAT route   a chat-parented run's steps stamp their blocks with
//                    `wf:<workflowId>:<nodePath>` into the launching chat's file, so
//                    `run-step-slice.ts` projects them out of the message store.
//                    Available only while this client holds that chat's window.
//   the KAS route    `run-step-transcript.ts` reads the step's own session back out
//                    of KAS on demand. Available whatever this client holds, and
//                    whichever route launched the run.
//
// Both produce a `RunStepSlice`, so both render through here — which is what makes
// the two launch routes read alike in the run tab rather than merely similar.
//
// It must be reached by a LAZY `import()` from `run-view.ts`, exactly as its
// sibling is. Hard requirement rather than tidiness: it calls
// `messages-blocks.ts`, whose graph runs through the editor openers and the
// navigator into `chat.ts` — the entire transcript stack — which is precisely what
// that lazy import exists to keep out of a run tab's static graph.
//
// What it owns is the RENDER LIFECYCLE per node path: the shape watermark that
// decides update-versus-rebuild, the SOURCE watermark that decides the same thing
// when a path's content changes route, the seal latch, and the id pairing the four
// detached calls have to agree on. What it does NOT own is where a step's content
// comes from (each source's) or which host it goes in (the detail pane's).
// ---------------------------------------------------------------------------

import {
  buildDetachedBody,
  disposeDetachedBody,
  finalizeDetachedBody,
  updateDetachedBody,
} from "./messages-blocks.js";
import { blockShape, shapeExtends } from "./subagent-slice.js";
import { stepSubtaskID, type RunStepSlice } from "./run-step-slice.js";
import type { Message } from "./types.js";

/** Where a step's slice came from.
 *
 *  TWO fields rather than one "source key", and the split is load-bearing: the
 *  render key and the CHAT ID are different things and `messages-blocks.ts` reads
 *  the second for real. It keys tool-call signals by `(chatID, toolCallID)` and
 *  builds a delegate's page link as `/chat/<chatID>/subagent/<id>`, so feeding it a
 *  synthetic key would produce a link to a chat that does not exist — and a step's
 *  own session CAN hold a delegate (`run-step-transcript.ts` deliberately keeps a
 *  uuid subtask id). The KAS arm therefore carries no chat, which is what makes
 *  that link correctly absent: `messages-blocks.ts` withholds it on an empty id. */
export type RunStepSource =
  { readonly kind: "chat"; readonly chatID: string } | { readonly kind: "kas" };

/** One step's slice plus the route that produced it. */
export interface RunStepPaint {
  readonly slice: RunStepSlice;
  readonly source: RunStepSource;
}

/** Where a step's blocks go. The exec page's detail pane answers this; declared
 *  here because it is this module's requirement, not the pane's. */
export type StepHostFor = (nodePath: string) => HTMLElement;

/** A run's projected step transcript. One per run tab. */
export interface RunChatStepStream {
  /** Paint every step the map holds. Idempotent and safe on every repaint. */
  apply(paints: ReadonlyMap<string, RunStepPaint>): void;
  /** Drop every render this stream holds. Called when the tab retargets. */
  dispose(): void;
}

interface StepRender {
  host: HTMLElement;
  /** The synthetic message id this step's render is keyed under. */
  renderKey: string;
  /** The `agent_subtask_id` the four detached calls pair that key with. */
  stepID: string;
  /** The block shape already mounted, for the update-versus-rebuild decision. */
  shape: readonly string[];
  /** Whether the settled body has been sealed. */
  sealed: boolean;
}

/** The render key's source component: the launching chat's id for the chat route,
 *  the literal "kas" for the on-demand read.
 *
 *  A chat id cannot be "kas" (`ids.ValidChatID` is `c-<hex>`), so the two routes
 *  can never collide on one key — which is what makes a source FLIP detectable
 *  rather than silent. */
function sourceKey(source: RunStepSource): string {
  return source.kind === "kas" ? "kas" : source.chatID;
}

/** The chat id `messages-blocks.ts` should key its own state by, or "" when there
 *  is no chat behind these blocks. */
function sourceChatID(source: RunStepSource): string {
  return source.kind === "kas" ? "" : source.chatID;
}

/** The synthetic message id a step's detached render is keyed under.
 *
 *  Distinct from the REAL message id, and that is load-bearing three times.
 *  `messages-blocks.ts` holds ONE render map keyed by message id and the
 *  transcript is already holding an entry for those messages, so a shared key
 *  would make whichever surface mounted second clobber the other's render state
 *  and then dispose the wrong one. A step's blocks can span two assistant
 *  messages (a mid-turn model switch splits a turn), which this renders as ONE
 *  transcript. And the SOURCE is in the key, so the same step read by the other
 *  route is a different render — see the flip rule in `paint`. */
function runStepRenderID(source: RunStepSource, nodePath: string): string {
  return `runstep:${sourceKey(source)}:${nodePath}`;
}

export function createRunChatStepStream(
  hostFor: StepHostFor,
  workflowID: string,
): RunChatStepStream {
  const steps = new Map<string, StepRender>();

  function recordFor(nodePath: string, source: RunStepSource): StepRender {
    let rec = steps.get(nodePath);
    if (rec === undefined) {
      rec = {
        host: hostFor(nodePath),
        renderKey: runStepRenderID(source, nodePath),
        stepID: stepSubtaskID(workflowID, nodePath),
        shape: [],
        sealed: false,
      };
      steps.set(nodePath, rec);
    }
    return rec;
  }

  /** The synthetic message the dispatcher renders. Its id is the render key, so
   *  the build, the update, the seal and the dispose cannot disagree about it. */
  function syntheticMessage(rec: StepRender, slice: RunStepSlice): Message {
    return {
      id: rec.renderKey,
      role: "assistant",
      ts: 0,
      content: "",
      blocks: slice.blocks,
      tool_calls: slice.toolCalls,
    };
  }

  function paint(nodePath: string, entry: RunStepPaint): void {
    const { slice, source } = entry;
    const rec = recordFor(nodePath, source);
    const shape = blockShape(slice.blocks);
    // A SOURCE FLIP is a rebuild, always. The chat's window can be evicted while
    // the tab is open, which empties the slice and hands this path to the KAS read
    // — and the reverse happens when the chat is reopened. Without this the record
    // would keep its OLD render key while every later call used the new one, so the
    // previous render would stay registered in `messages-blocks.ts` under a key
    // nothing ever disposes, holding that render's store subscriptions alive under
    // the next one.
    const wantKey = runStepRenderID(source, nodePath);
    const flipped = rec.renderKey !== wantKey;
    if (flipped) {
      disposeDetachedBody(rec.renderKey, rec.stepID);
      rec.renderKey = wantKey;
      rec.shape = [];
    }
    const message = syntheticMessage(rec, slice);
    // The dispatcher's incremental update appends past a watermark, so it is
    // correct only while the prefix it already mounted is unchanged. Growth at the
    // tail keeps it; a rewind, a refetch or a turn restructured underneath does
    // not, and those are the only things that can shorten or reorder this list.
    // Comparing the prefix is what tells the two apart without throwing away the
    // reader's place on every streamed chunk.
    if (!flipped && shapeExtends(rec.shape, shape) && rec.shape.length > 0) {
      updateDetachedBody(rec.host, message, sourceChatID(source), rec.stepID, slice.live);
    } else {
      disposeDetachedBody(rec.renderKey, rec.stepID);
      rec.host.replaceChildren();
      rec.sealed = false;
      buildDetachedBody(rec.host, message, sourceChatID(source), rec.stepID, slice.live);
    }
    rec.shape = shape;

    // Settled: flush the markdown streams and collapse the reasoning traces, so a
    // finished step does not sit under a caret. Guarded, because every later
    // repaint of a finished run lands here too.
    //
    // Read off the SLICE rather than taken as a parameter, so liveness is per
    // entry: a KAS-sourced read is a settled answer while a chat-sourced one is as
    // live as the chat, and a page can hold both across different paths.
    if (!slice.live && !rec.sealed) {
      rec.sealed = true;
      finalizeDetachedBody(rec.renderKey, rec.stepID);
    }
  }

  return {
    apply(paints) {
      for (const [nodePath, entry] of paints) {
        paint(nodePath, entry);
      }
    },
    dispose() {
      for (const rec of steps.values()) {
        disposeDetachedBody(rec.renderKey, rec.stepID);
      }
      steps.clear();
    },
  };
}
