// One owner for the `wf:` prefix the server stamps on a WORKFLOW STEP's blocks.
// A leaf because `handlers/messages.ts` reads it too, and importing
// `messages-blocks.ts` there would drag the render graph into an SSE handler.

/** The prefix the server stamps on a WORKFLOW STEP's subtask id
 *  (`internal/translate/wire.go` ACPWorkflowMeta.SubtaskID:
 *  `"wf:" + workflowId + ":" + nodePath`). */
export const STEP_PREFIX = "wf:";

/** A step subtask id, split into the two containers it names. */
export interface StepSubtask {
  workflowID: string;
  nodePath: string;
}

/** Whether this subtask id names a workflow STEP rather than a subagent.
 *
 *  The PREFIX only, deliberately: a caller asking "is this the chat's own work"
 *  must answer yes for a malformed step id too, where `parseStepSubtask` returns
 *  null so the renderer can fall back to a delegate box rather than lose the
 *  block. The two questions differ, so they are two functions — a caller asking
 *  instead whether the TRANSCRIPT draws the block wants `parseStepSubtask`, since
 *  the delegate-box fallback is a real destination. */
export function isStepSubtask(subtask: string): boolean {
  return subtask.startsWith(STEP_PREFIX);
}

/** Parse `wf:<workflowId>:<a/b/c>`, or null for a subagent's uuid.
 *
 *  One `indexOf` rather than a `split`, because a node path may not contain a
 *  colon but nothing here should depend on that: taking the FIRST colon after the
 *  prefix makes the workflow id unambiguous and hands everything after it to the
 *  path, whatever it contains. A malformed id (no second colon, or an empty half)
 *  returns null and falls through to the subagent branch, which renders it as a
 *  delegate box rather than losing the block. */
export function parseStepSubtask(subtask: string): StepSubtask | null {
  if (!isStepSubtask(subtask)) {
    return null;
  }
  const rest = subtask.slice(STEP_PREFIX.length);
  const sep = rest.indexOf(":");
  if (sep <= 0 || sep === rest.length - 1) {
    return null;
  }
  return { workflowID: rest.slice(0, sep), nodePath: rest.slice(sep + 1) };
}
