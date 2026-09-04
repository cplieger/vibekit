// ---------------------------------------------------------------------------
// The run-control vocabulary: which verbs exist, what each button says, and how
// a verb name off the wire is narrowed into one.
//
// WHICH VERBS A RUN OFFERS IS NO LONGER DECIDED HERE. It used to be a
// status→verbs table twinned with the server's own, and the pair could agree
// perfectly while still drawing a button whose only possible outcome was a
// refusal: the rule has a third input — whether anything still hosts the run —
// that neither copy could see. Worse, the caller gating this table asked whether
// the run was parentless and answered from an SSE-fed cache that is empty after
// any reload, so a chat-parented run read as parentless and got the wrong row.
// `GET /api/runs/{id}/controls` answers all three inputs at once now; this
// module renders what it is handed.
//
// Its own leaf module rather than a const inside run-view.ts, for the reason
// several other pure rules in this codebase live apart from their renderers
// (turns.ts, mcp-content.ts, url-safety.ts): run-view.ts imports tabs.ts, which
// reads `location` at module scope, so a test of these rules through that module
// would drag the whole app graph and a partially-staged DOM in behind it.
// ---------------------------------------------------------------------------

/** The run-control verbs, in the order a row presents them. */
export type RunVerb = "pause" | "resume" | "cancel" | "retry";

/** Button text per verb. */
export const CONTROL_LABEL: Record<RunVerb, string> = {
  pause: "Pause",
  resume: "Resume",
  cancel: "Cancel",
  retry: "Retry failed steps",
};

/** Narrow one verb name off the wire, or `undefined` for a word this client has
 *  no button for.
 *
 *  The label table IS the boundary: rendering is total over `RunVerb`, so a verb
 *  the server grows before this client is taught it must be DROPPED rather than
 *  reaching `CONTROL_LABEL` and producing a blank button. Driven off that table's
 *  own keys rather than a second list, so the two cannot disagree. */
export function asRunVerb(name: string): RunVerb | undefined {
  return Object.hasOwn(CONTROL_LABEL, name) ? (name as RunVerb) : undefined;
}

/** The verbs a control answer offers, narrowed and in the server's row order.
 *
 *  Order is the server's because the row order is part of the answer — a
 *  destructive verb last, a re-drive first — and re-sorting here would be this
 *  module deciding a thing it was just relieved of. */
export function offeredVerbs(verbs: readonly string[]): RunVerb[] {
  const out: RunVerb[] = [];
  for (const name of verbs) {
    const verb = asRunVerb(name);
    if (verb !== undefined) {
      out.push(verb);
    }
  }
  return out;
}

/** The refusal sentences to show where the buttons would have been, in verb
 *  order so the list does not reshuffle between fetches.
 *
 *  A refusal for a verb this client cannot name is still SHOWN: the sentence is
 *  the server's own prose and it explains the run, not the button — dropping it
 *  would leave a reader with an empty row and no reason, which is the state this
 *  whole answer exists to end. */
export function refusalSentences(refused: Readonly<Record<string, string>> | undefined): string[] {
  if (refused === undefined) {
    return [];
  }
  return Object.keys(refused)
    .sort((a, b) => verbRank(a) - verbRank(b) || a.localeCompare(b))
    .map((verb) => refused[verb] ?? "")
    .filter((text) => text !== "");
}

/** A verb's position in the row order, and a large number for one this client
 *  does not know — so an unrecognised verb's sentence sorts last rather than
 *  jumping the queue. */
function verbRank(name: string): number {
  const order: readonly RunVerb[] = ["retry", "resume", "pause", "cancel"];
  const i = order.indexOf(name as RunVerb);
  return i < 0 ? order.length : i;
}

/** The sentence a completed retry is reported with, and the channel it belongs
 *  in.
 *
 *  A retry that reset ZERO nodes is a first-class outcome upstream and a no-op
 *  from the reader's seat, so it is not a success: reporting it as one is what
 *  made "I pressed Retry and nothing happened" the design's expected appearance.
 *  Pure and exported so both sentences are testable without a toast stack. */
export function retryOutcomeNotice(count: number): { level: "success" | "error"; text: string } {
  if (count <= 0) {
    return {
      level: "error",
      text: "Nothing to retry: the workflow engine reset no step of this run.",
    };
  }
  return {
    level: "success",
    text: `Retrying ${String(count)} ${count === 1 ? "step" : "steps"}`,
  };
}
