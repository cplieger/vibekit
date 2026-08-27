// ---------------------------------------------------------------------------
// The WORKFLOW adapter: KAS's `inspect` reply folded into the exec view's model.
//
// The whole workflow-specific half of the `/run/{id}` page lives here, so the panes
// under `exec-view/` name no workflow concept and a subagent tab is a second
// adapter rather than a second page.
//
// It is also where two things that were on the wire and unread finally get read:
//
//   `state.inputs`  — what the run was ASKED to do. Zero readers before this, on
//                     any surface. `recipes.ts` renders a recipe's DECLARED input
//                     names for the launch form; the VALUES a run actually
//                     received were displayed nowhere in the app.
//   `nodePlan`      — zero readers before this. Passed through verbatim by
//                     `GET /api/runs/{id}` and decoded by nothing. Its only
//                     content the state tree lacks is a repeat's `maxIterations`,
//                     `onMaxIterations` and `stopCondition`, so a loop's bound and
//                     its exit condition had never been on screen.
//
// And the structure itself. The state tree's CONTAINERS — `sequence`, `repeat`,
// `parallel`, `watch` — were dropped by the leaf-flattening the page and the
// transcript card both do, so a run's control flow was invisible: that there IS a
// loop, that two steps ran concurrently, where a watch sits. Those are the two
// things a single column cannot express and the two a run always has.
//
// `completionSignal`, `effortLevel`, `sessionId` and `watchTerminal` also had zero
// render sites and become facts on the detail pane here.
// ---------------------------------------------------------------------------

import { truncate } from "./strings.js";
import type { RunNode, RunState } from "./run-store.js";
import type { RunAsks } from "./fundamentals/run-card.js";
import { stateOf, withAsk, inFlight, type ExecState } from "./exec-view/status.js";
import type { ExecFact, ExecKind, ExecNode, ExecRun } from "./exec-view/model.js";

/** The per-node facts `nodePlan` carries that the state tree does not.
 *
 *  Keyed by node id rather than by path, because the PLAN is the definition: it
 *  describes a node once, while the state tree describes every execution of it. A
 *  repeat's two iterations share the plan entry that bounds them, which is exactly
 *  the join wanted here. */
interface PlanEntry {
  maxIterations?: number;
  onMaxIterations?: string;
  stopCondition?: string;
  /** A parallel's join policy: `all` / `allSettled` / `any`. */
  join?: string;
  /** A watch node's handler and what it waits for. */
  watch?: string;
}

/** Walk the raw plan and index every node id it names.
 *
 *  Structural rather than schema-driven, and defensively so: this is a foreign
 *  shape whose members grow between kiro-cli releases, and the reply is useful
 *  whether or not this walk understood all of it. Every field is read through a
 *  guard, an unrecognised container still has its children visited, and a plan that
 *  is not an array at all yields an empty index instead of throwing on a page whose
 *  other half renders fine.
 */
export function indexPlan(plan: unknown): Map<string, PlanEntry> {
  const out = new Map<string, PlanEntry>();
  const str = (v: unknown): string | undefined =>
    typeof v === "string" && v !== "" ? v : undefined;
  const num = (v: unknown): number | undefined =>
    typeof v === "number" && Number.isFinite(v) ? v : undefined;

  const walk = (raw: unknown): void => {
    if (Array.isArray(raw)) {
      for (const item of raw) {
        walk(item);
      }
      return;
    }
    if (raw === null || typeof raw !== "object") {
      return;
    }
    const o = raw as Record<string, unknown>;
    const id = str(o["nodeId"]);
    if (id !== undefined) {
      const entry: PlanEntry = {};
      const max = num(o["maxIterations"]);
      if (max !== undefined) {
        entry.maxIterations = max;
      }
      for (const [key, field] of [
        ["onMaxIterations", "onMaxIterations"],
        ["stopCondition", "stopCondition"],
        ["join", "join"],
      ] as const) {
        const v = str(o[key]);
        if (v !== undefined) {
          entry[field] = v;
        }
      }
      // `stopWhen` is the other spelling of a stop condition; the engine rejects a
      // node declaring both, so taking either into one field cannot lose one. The
      // guard rather than `??=` because the field is optional under
      // exactOptionalPropertyTypes, which refuses `undefined` as a value.
      if (entry.stopCondition === undefined) {
        const when = str(o["stopWhen"]);
        if (when !== undefined) {
          entry.stopCondition = when;
        }
      }
      const handler = str(o["watchHandler"]) ?? str(o["handler"]);
      const until = str(o["until"]) ?? str(o["waitFor"]);
      if (handler !== undefined || until !== undefined) {
        entry.watch = [handler, until].filter((v) => v !== undefined).join(" \u2192 ");
      }
      if (Object.values(entry).some((v) => v !== undefined)) {
        out.set(id, entry);
      }
    }
    // Every container spelling the engine uses, plus a generic `children` so a
    // node type added upstream still has its descendants indexed.
    for (const key of ["steps", "branches", "children", "body", "nodes"]) {
      walk(o[key]);
    }
  };
  walk(plan);
  return out;
}

/** A container's state, derived from its children.
 *
 *  KAS does stamp a status on a container, and it is taken when it says something
 *  terminal — but a container reads `running` for as long as anything inside it is
 *  open, which tells a reader nothing they cannot see. What is worth surfacing is
 *  the WORST outcome beneath it, so a collapsed group still says a step inside it
 *  failed. Precedence follows what a reader must act on. */
function rollUp(own: ExecState, kids: readonly ExecNode[]): ExecState {
  if (kids.length === 0) {
    return own;
  }
  const states = new Set(kids.map((k) => k.state));
  for (const s of ["input", "fail", "warn", "running", "waiting"] as const) {
    if (states.has(s)) {
      return s;
    }
  }
  if (states.has("ok")) {
    return states.has("pending") ? "running" : "ok";
  }
  return own;
}

/** The kind, mapped rather than passed through, so an upstream addition lands on
 *  `group` (which the CSS has a rule for) instead of on nothing. */
function kindOf(type: string): ExecKind {
  switch (type) {
    case "step":
    case "sequence":
    case "repeat":
    case "parallel":
    case "watch":
      return type;
    default:
      return "group";
  }
}

/** The identity facts for a step, in the order they answer questions: who ran it,
 *  on what, how it ended, and what it took to get there. */
function stepFacts(node: RunNode, plan: PlanEntry | undefined): ExecFact[] {
  const facts: ExecFact[] = [];
  const add = (label: string, value: string | undefined, mono = false): void => {
    if (value !== undefined && value !== "") {
      facts.push(mono ? { label, value, mono } : { label, value });
    }
  };
  add("Agent", node.agentName);
  // `auto` is not a model, it is the absence of a choice, and naming it as one
  // implies a pin that never happened.
  add("Model", node.modelId === "auto" ? undefined : node.modelId);
  add("Effort", node.effortLevel);
  // KAS's own statement of WHY the node ended, which is different information from
  // its status: a step can complete having asked for input.
  add("Signal", node.completionSignal);
  if (node.continuationAttempts !== undefined && node.continuationAttempts > 0) {
    add("Retries", String(node.continuationAttempts));
  }
  add("Pass", node.iteration === undefined ? undefined : String(node.iteration + 1));
  add("Branch", node.branchId);
  if (node.watchTerminal === true) {
    add("Watch", "reached a terminal state");
  }
  add("Max passes", plan?.maxIterations === undefined ? undefined : String(plan.maxIterations));
  add("At the cap", plan?.onMaxIterations);
  add("Stops when", plan?.stopCondition, true);
  add("Join", plan?.join);
  add("Waits for", plan?.watch, true);
  // Last, and monospace: it is the handle for the step's own session rather than
  // something a reader acts on, so it sits below the facts that describe the work.
  add("Session", node.sessionId, true);
  return facts;
}

/** The one-line summary under a row's label. */
function subtitleOf(node: RunNode, kind: ExecKind, plan: PlanEntry | undefined): string {
  const bits: string[] = [];
  if (kind === "repeat") {
    bits.push(
      plan?.maxIterations === undefined ? "repeats" : `up to ${String(plan.maxIterations)} passes`,
    );
    if (plan?.stopCondition !== undefined) {
      bits.push(`until ${truncate(plan.stopCondition, 60)}`);
    }
  } else if (kind === "parallel") {
    bits.push(plan?.join === undefined ? "in parallel" : `in parallel, join ${plan.join}`);
  } else if (kind === "watch") {
    bits.push(plan?.watch === undefined ? "polls" : `polls ${truncate(plan.watch, 60)}`);
  } else if (kind === "step") {
    if (node.agentName !== undefined && node.agentName !== "") {
      bits.push(node.agentName);
    }
    if (node.modelId !== undefined && node.modelId !== "" && node.modelId !== "auto") {
      bits.push(node.modelId);
    }
    if (node.iteration !== undefined) {
      bits.push(`pass ${String(node.iteration + 1)}`);
    }
  }
  return bits.join(" \u00b7 ");
}

/** Fold one state node and its subtree. */
function toNode(
  node: RunNode,
  trail: readonly string[],
  plans: Map<string, PlanEntry>,
  asks: RunAsks,
): ExecNode {
  const path = [...trail, node.nodeId];
  const plan = plans.get(node.nodeId);
  const kind = kindOf(node.type);
  const children = (node.children ?? []).map((k) => toNode(k, path, plans, asks));
  const own = withAsk(stateOf(node.status), asks.nodes.has(node.nodeId));
  const out: ExecNode = {
    path: path.join("/"),
    label: node.nodeId,
    kind,
    state: children.length === 0 ? own : rollUp(own, children),
    children,
  };
  if (node.startedAt !== undefined) {
    out.start = node.startedAt;
  }
  if (node.endedAt !== undefined) {
    out.end = node.endedAt;
  }
  const sub = subtitleOf(node, kind, plan);
  if (sub !== "") {
    out.subtitle = sub;
  }
  const facts = stepFacts(node, plan);
  if (facts.length > 0) {
    out.facts = facts;
  }
  if (node.failureReason !== undefined && node.failureReason !== "") {
    out.failure = node.failureReason;
  }
  if (node.capturedOutput !== undefined) {
    out.output = node.capturedOutput;
  }
  if (node.artifacts !== undefined && Object.keys(node.artifacts).length > 0) {
    out.artifacts = node.artifacts;
  }
  // A LEAF can host a transcript; a container cannot, and saying so is what lets
  // the detail pane distinguish "nothing streams here" from "nothing has arrived".
  if (children.length === 0) {
    out.transcript = true;
  }
  return out;
}

/** The run's alert, and the five things that put it in front of a person.
 *
 *  Order is by what the reader can DO: an unanswered ask is the one state a click
 *  resolves right now, a deliberate stop needs nothing, a pause may need an action,
 *  a transient-error park is informational, and a failure needs reading. Only one
 *  shows — a run has one reason it is not moving. Ahead of the run's own status
 *  deliberately: the run still reads `running` while a step's ask blocks it, so the
 *  status would report nothing wrong. */
function alertOf(state: RunState, asks: RunAsks, nodes: readonly ExecNode[]): ExecRun["alert"] {
  if (asks.count > 0) {
    const head =
      asks.label === ""
        ? "Waiting for your answer"
        : `Waiting for your answer: ${truncate(asks.label, 160)}`;
    return {
      kind: "input",
      text: asks.count > 1 ? `${head} (${String(asks.count)} asks waiting)` : head,
    };
  }
  if (state.stopInitiator === "user") {
    const why =
      state.stopReason === undefined || state.stopReason === "" ? "" : `: ${state.stopReason}`;
    return {
      kind: "stopped",
      text: (state.status === "completed" ? "Marked complete by you" : "Stopped by you") + why,
    };
  }
  if (state.status === "paused") {
    const bits = [
      state.pauseReason === undefined || state.pauseReason === ""
        ? "Waiting"
        : `Waiting: ${state.pauseReason}`,
    ];
    const code = state.pauseDetail?.code;
    if (code !== undefined && code !== "") {
      bits.push(`after a transient error (${code})`);
    }
    return { kind: "paused", text: bits.join(" \u00b7 ") };
  }
  if (state.status === "failed") {
    const failed = nodes
      .flatMap(function all(n: ExecNode): ExecNode[] {
        return [n, ...n.children.flatMap(all)];
      })
      .find((n) => n.state === "fail" && n.failure !== undefined);
    return {
      kind: "failed",
      text:
        failed === undefined
          ? "The run failed"
          : `${failed.label} failed: ${truncate(failed.failure ?? "", 200)}`,
    };
  }
  return undefined;
}

/** Fold KAS's `inspect` reply into the exec view's model.
 *
 *  `plan` is the raw `nodePlan`; `asks` is the dock's answer about which node is
 *  blocked on a person, which no source's status can carry. */
export function runToExec(
  workflowID: string,
  state: RunState,
  plan: unknown,
  asks: RunAsks,
): ExecRun {
  const plans = indexPlan(plan);
  // The root is a container KAS names after the workflow itself, so its children
  // are the run's real top level. Kept as a root only when it carries siblings
  // worth showing — otherwise it would be one group wrapping everything, which is
  // an indent for no information.
  const root = state.root;
  const nodes =
    root === undefined
      ? []
      : root.type === "sequence" && (root.children?.length ?? 0) > 0
        ? (root.children ?? []).map((k) => toNode(k, [root.nodeId], plans, asks))
        : [toNode(root, [], plans, asks)];

  const outputs = new Map<string, string>();
  for (const [k, v] of Object.entries(state.capturedOutputs ?? {})) {
    outputs.set(k, v);
  }
  // Artifacts win a key collision, being the value a step CHOSE to publish.
  for (const [k, v] of Object.entries(state.artifacts ?? {})) {
    outputs.set(k, v);
  }

  const runState = stateOf(state.status);
  const out: ExecRun = {
    id: workflowID,
    label: state.runLabel ?? state.workflowName ?? "Workflow run",
    state: asks.count > 0 ? "input" : runState,
    nodes,
    live: inFlight(runState) || asks.count > 0,
  };
  if (state.inputs !== undefined && Object.keys(state.inputs).length > 0) {
    out.inputs = state.inputs;
  }
  if (outputs.size > 0) {
    out.outputs = Object.fromEntries(outputs);
  }
  const alert = alertOf(state, asks, nodes);
  if (alert !== undefined) {
    out.alert = alert;
  }
  return out;
}
