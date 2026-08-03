// ---------------------------------------------------------------------------
// Workflow-run SSE handlers.
//
// Three events, and all three mean the same thing to this client: something about
// a run changed, go and read it. That is the whole contract — the payloads are
// deliberately too thin to reconstruct a run from, because a client that
// accumulated them would garble it. `run_start` re-fires on every resume (three
// frames were measured for one run), and `node_complete` carries neither
// `iteration` nor `branchId`, so two passes of one loop are indistinguishable on
// the wire. `_kiro/workflow/inspect` is the truth; these events only say when to
// ask it.
//
// Two surfaces react, and neither is created by this handler:
//
//   - an open run review, which re-reads the run it is showing (a direct call:
//     run-view.ts is a leaf over api-client + tabs)
//   - the history list, reached over the BUS. Two reasons, and the second is the
//     load-bearing one: they are UI affordances that should not know about each
//     other, and importing history.ts from here drags chat.ts in behind it —
//     which put real network calls into every test that touches this handler.
//
// A run's own transcript needs nothing here: a step's content arrives on the
// launching chat's connection as ordinary blocks, attributed to the step, through
// the same handlers that render every other block.
// ---------------------------------------------------------------------------

import { onSSE, emitBus, BUS_RUNS_CHANGED } from "../bus.js";
import { refreshRunView } from "../run-view.js";

// The two ends of a run change the LIST as well as the run: one adds a row, the
// other settles its outcome. Everything between them only changes the run.
onSSE("run_started", (_chatID, p) => {
  refreshRunView(p.workflow_id);
  emitBus(BUS_RUNS_CHANGED);
});

onSSE("run_finished", (_chatID, p) => {
  refreshRunView(p.workflow_id);
  emitBus(BUS_RUNS_CHANGED);
});

onSSE("run_progress", (_chatID, p) => {
  refreshRunView(p.workflow_id);
});
