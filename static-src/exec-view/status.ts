// ---------------------------------------------------------------------------
// ONE status vocabulary for every surface that reports delegated work.
//
// `vibekit-ui.md` names this as the shape to converge on and names the live defect
// it prevents: the todo BLOCK renders `☐ ◐ ☑` while the task PILL renders
// `○ ⏳ ✅` for the same states — two icon sets a reader learns separately, and
// two feeds that can disagree with nothing detecting it. The run surfaces were on
// the same road: the transcript card carried a private `STEP_BADGE`/`STEP_WORD`
// pair and the `/run/{id}` page had a third set of dot colours of its own. With a
// subagent tab coming, a fourth was the default outcome.
//
// So it lives here, it is named for STATE rather than for runs, and every surface
// reads it: the exec view's tree, its timeline and its detail pane, plus the
// transcript's run card.
//
// Three rules it encodes, each from the design system rather than invented here:
//
//   SHAPE CARRIES STATE, tint never alone. Every state has a character or a ring,
//   so WCAG 1.4.1 holds by construction instead of per site.
//
//   MOTION MEANS IN FLIGHT. `running` spins; `waiting` is the SAME ring held
//   still, because a paused node that spins claims progress where nothing moves.
//   That correction was already made once, for the tab dot.
//
//   A QUIET DEFAULT. Only states that want something from a reader take emphasis;
//   twenty green rows are noise.
//
// Two members have no wire status behind them, both deliberate. `input` is a node
// whose ask sits unanswered in the dock, which no source's status can say — KAS
// blocks the asking step's turn and leaves the run `running`. `pending` is a node
// the execution has not reached, which a TREE can show and a leaf list cannot.
// ---------------------------------------------------------------------------

/** A node's state, as every exec surface reports it. */
export type ExecState =
  "pending" | "running" | "waiting" | "input" | "ok" | "fail" | "warn" | "skipped";

/** The statuses `stateOf` recognises. KAS's `NodeStateSchema` is the first source
 *  to speak them; a second maps its own words onto these in its adapter.
 *
 *  `stateOf` takes a bare `string` rather than this type on purpose: the value comes
 *  off a foreign wire, so a member added upstream must fall through to `pending`
 *  instead of failing a decode. The type is the CONTRACT an adapter writes against;
 *  the function is the tolerant reader. */
export type WireStatus =
  "pending" | "running" | "paused" | "completed" | "failed" | "aborted" | "skipped";

/** The character a state paints. Empty for the three that have none, and CSS gives
 *  each of those a RING instead — hollow for `pending`, still for `waiting`, spinning
 *  for `running`. One primitive, three meanings, on the app's motion axis: not
 *  started, open with nothing moving, working.
 *
 *  `pending` used to be a dimmed row with no glyph at all, which made fade the only
 *  channel for it and measured 3.5:1 against the pane. Every state carries a shape
 *  now, so WCAG 1.4.1 holds across the whole vocabulary rather than six of its
 *  eight. */
export const STATE_BADGE: Readonly<Record<ExecState, string>> = {
  pending: "",
  running: "",
  waiting: "",
  input: "?",
  ok: "\u2713",
  fail: "\u2717",
  warn: "\u26A0",
  skipped: "\u2013",
};

/** The word an accessible name uses. Not the wire enum: "aborted" and "failed"
 *  are one thing to a listener, and "pending" reads better as "not started". */
export const STATE_WORD: Readonly<Record<ExecState, string>> = {
  pending: "not started",
  running: "running",
  waiting: "waiting",
  input: "waiting for your answer",
  ok: "succeeded",
  fail: "failed",
  warn: "stopped",
  skipped: "skipped",
};

/** Whether a state is still in flight, so a caller keeps a clock going without
 *  re-deriving the set. `input` counts: the node's turn is open, it is merely
 *  blocked on a person. */
export function inFlight(state: ExecState): boolean {
  return state === "running" || state === "waiting" || state === "input";
}

/** Fold a wire status onto the vocabulary.
 *
 *  `skipped` stays its own state rather than folding onto success: a branch that
 *  never ran did not succeed, and a check against it would claim work that never
 *  happened. `aborted` becomes `warn` rather than `fail`, because a stop is not a
 *  fault and a cancel wants different ink from a crash. */
export function stateOf(status: string | undefined): ExecState {
  switch (status) {
    case "completed":
      return "ok";
    case "failed":
      return "fail";
    case "aborted":
      return "warn";
    case "paused":
      return "waiting";
    case "running":
      return "running";
    case "skipped":
      return "skipped";
    default:
      return "pending";
  }
}

/** Reclassify an in-flight node whose ask is unanswered.
 *
 *  Applied only while the node is OTHERWISE in flight, and that guard resolves the
 *  one ambiguity in the join: on the workflow wire `node_id` is a node ID rather
 *  than a node PATH, so a repeat's iterations share it and a finished pass would
 *  light up beside the live one. */
export function withAsk(state: ExecState, asked: boolean): ExecState {
  return asked && (state === "running" || state === "waiting") ? "input" : state;
}
